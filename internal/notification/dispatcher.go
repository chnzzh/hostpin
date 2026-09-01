package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
)

type repository interface {
	DueNotificationDeliveries(context.Context, time.Time, int) ([]model.NotificationDelivery, error)
	UpdateNotificationDelivery(context.Context, string, string, int, time.Time, string) error
}

type Dispatcher struct {
	store     repository
	secrets   *security.SecretBox
	publicURL string
	logger    *slog.Logger
	http      *http.Client
}

func New(repository repository, secrets *security.SecretBox, publicURL string, logger *slog.Logger) *Dispatcher {
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true, MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second,
	}
	return &Dispatcher{
		store: repository, secrets: secrets, publicURL: strings.TrimRight(publicURL, "/"),
		logger: logger, http: &http.Client{Transport: transport, Timeout: 20 * time.Second},
	}
}

func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		d.process(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.process(ctx)
			}
		}
	}()
}

func (d *Dispatcher) process(ctx context.Context) {
	deliveries, err := d.store.DueNotificationDeliveries(ctx, time.Now().UTC(), 25)
	if err != nil {
		d.logger.Error("load notification deliveries", "error", err)
		return
	}
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}
		if delivery.Event.Link == "" {
			delivery.Event.Link = d.publicURL + "/nodes/" + delivery.Event.Node.ID
		}
		err := d.Send(ctx, delivery.Channel, delivery.Event)
		attempts := delivery.Attempts + 1
		if err == nil {
			_ = d.store.UpdateNotificationDelivery(ctx, delivery.ID, "sent", attempts, time.Now().UTC(), "")
			continue
		}
		status := "retry"
		delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
		next := time.Now().UTC()
		if attempts <= len(delays) {
			next = next.Add(delays[attempts-1])
		} else {
			status = "failed"
		}
		message := err.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
		_ = d.store.UpdateNotificationDelivery(ctx, delivery.ID, status, attempts, next, message)
		d.logger.Warn("notification delivery failed", "channel", delivery.Channel.Name, "attempt", attempts, "error", err)
	}
}

func (d *Dispatcher) Send(ctx context.Context, channel model.NotificationChannel, event model.AlertEvent) error {
	plaintext, err := d.secrets.Open(channel.EncryptedConfig)
	if err != nil {
		return err
	}
	config := make(map[string]any)
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return errors.New("notification channel configuration is invalid")
	}
	switch channel.Type {
	case "webhook":
		return d.sendWebhook(ctx, config, event)
	case "telegram":
		return d.sendTelegram(ctx, config, event)
	case "bark":
		return d.sendBark(ctx, config, event)
	case "smtp":
		return sendSMTP(ctx, config, event)
	default:
		return fmt.Errorf("unsupported notification channel %q", channel.Type)
	}
}

func (d *Dispatcher) sendWebhook(ctx context.Context, config map[string]any, event model.AlertEvent) error {
	target := stringValue(config, "url")
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("webhook URL must be absolute http(s)")
	}
	payload, _ := json.Marshal(event)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Hostpin-Notifier/1")
	if secret := stringValue(config, "secret"); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(payload)
		request.Header.Set("X-Hostpin-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	if headers, ok := config["headers"].(map[string]any); ok {
		for key, raw := range headers {
			if value, ok := raw.(string); ok && strings.HasPrefix(strings.ToLower(key), "x-") {
				request.Header.Set(key, value)
			}
		}
	}
	return d.do(request)
}

func (d *Dispatcher) sendTelegram(ctx context.Context, config map[string]any, event model.AlertEvent) error {
	token, chatID := stringValue(config, "bot_token"), stringValue(config, "chat_id")
	if token == "" || chatID == "" || strings.ContainsAny(token, "/?#") {
		return errors.New("Telegram bot_token and chat_id are required")
	}
	base := strings.TrimRight(stringValue(config, "api_base"), "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	form := url.Values{"chat_id": {chatID}, "text": {formatEvent(event)}, "disable_web_page_preview": {"true"}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return d.do(request)
}

func (d *Dispatcher) sendBark(ctx context.Context, config map[string]any, event model.AlertEvent) error {
	key := stringValue(config, "device_key")
	if key == "" {
		return errors.New("Bark device_key is required")
	}
	endpoint := strings.TrimRight(stringValue(config, "endpoint"), "/")
	if endpoint == "" {
		endpoint = "https://api.day.app"
	}
	payload := map[string]any{
		"device_key": key, "title": "Hostpin · " + strings.ToUpper(string(event.Status)),
		"body": event.Message, "group": firstValue(stringValue(config, "group"), "Hostpin"),
		"url": event.Link,
	}
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/push", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return d.do(request)
}

func (d *Dispatcher) do(request *http.Request) error {
	response, err := d.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("remote returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func formatEvent(event model.AlertEvent) string {
	return fmt.Sprintf("Hostpin %s [%s]\n%s\nNode: %s\nTime: %s\n%s",
		strings.ToUpper(string(event.Status)), strings.ToUpper(event.Severity), event.Message,
		event.Node.Name, event.OccurredAt.UTC().Format(time.RFC3339), event.Link)
}

func stringValue(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return strings.TrimSpace(value)
}

func boolValue(config map[string]any, key string) bool {
	switch value := config[key].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(value)
		return parsed
	default:
		return false
	}
}

func intValue(config map[string]any, key string, fallback int) int {
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
