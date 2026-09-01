package notification

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
	"unicode"

	"github.com/chnzzh/hostpin/internal/model"
)

func sendSMTP(ctx context.Context, config map[string]any, event model.AlertEvent) error {
	host := stringValue(config, "host")
	port := intValue(config, "port", 587)
	from := stringValue(config, "from")
	recipients := stringSlice(config["to"])
	if host == "" || from == "" || len(recipients) == 0 || port < 1 || port > 65535 {
		return errors.New("SMTP host, from, and at least one recipient are required")
	}
	fromAddress, err := parseSMTPAddress(from)
	if err != nil {
		return fmt.Errorf("invalid SMTP from address: %w", err)
	}
	recipientAddresses := make([]*mail.Address, 0, len(recipients))
	for _, recipient := range recipients {
		address, parseErr := parseSMTPAddress(recipient)
		if parseErr != nil {
			return fmt.Errorf("invalid SMTP recipient: %w", parseErr)
		}
		recipientAddresses = append(recipientAddresses, address)
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var connection net.Conn
	if boolValue(config, "implicit_tls") {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer connection.Close()
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !boolValue(config, "implicit_tls") {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		} else if boolValue(config, "require_tls") {
			return errors.New("SMTP server does not support STARTTLS")
		}
	}
	username, password := stringValue(config, "username"), stringValue(config, "password")
	if username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP server does not advertise authentication")
		}
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return err
	}
	for _, recipient := range recipientAddresses {
		if err := client.Rcpt(recipient.Address); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	buffer := bufio.NewWriter(writer)
	headerRecipients := make([]string, 0, len(recipientAddresses))
	for _, recipient := range recipientAddresses {
		headerRecipients = append(headerRecipients, recipient.String())
	}
	subject := mime.QEncoding.Encode("UTF-8", sanitizeHeaderValue(fmt.Sprintf("[Hostpin][%s] %s", strings.ToUpper(event.Severity), event.Node.Name)))
	_, err = fmt.Fprintf(buffer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		fromAddress.String(), strings.Join(headerRecipients, ", "), subject, formatEvent(event))
	if err == nil {
		err = buffer.Flush()
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return client.Quit()
}

func parseSMTPAddress(raw string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("address contains a line break")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(address.Address) == "" {
		return nil, errors.New("address is malformed")
	}
	return address, nil
}

func sanitizeHeaderValue(value string) string {
	return strings.TrimSpace(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value))
}

func stringSlice(raw any) []string {
	switch value := raw.(type) {
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	case []string:
		return value
	case string:
		return splitAddresses(value)
	default:
		return nil
	}
}

func splitAddresses(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })
	result := make([]string, 0, len(parts))
	for _, item := range parts {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
