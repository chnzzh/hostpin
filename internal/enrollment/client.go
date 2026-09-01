package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type ResponseError struct {
	Status  int
	Code    string
	Message string
}

func (e *ResponseError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("enrollment failed with HTTP %d", e.Status)
	}
	return fmt.Sprintf("enrollment failed (%s): %s", e.Code, e.Message)
}

func NormalizeEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("endpoint must be an absolute http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("endpoint must not contain credentials, a query, or a fragment")
	}
	return parsed.String(), nil
}

func IsSafePlainHTTP(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" {
		return parsed != nil && parsed.Scheme == "https"
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && (address.IsLoopback() || address.IsPrivate())
}

func Enroll(ctx context.Context, endpoint string, request model.EnrollmentRequest) (model.EnrollmentResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return model.EnrollmentResponse{}, err
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/api/v1/enrollments", bytes.NewReader(payload))
	if err != nil {
		return model.EnrollmentResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", "Hostpin-Agent-Installer/1")
	response, err := client.Do(httpRequest)
	if err != nil {
		return model.EnrollmentResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope model.Envelope[json.RawMessage]
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope)
		responseErr := &ResponseError{Status: response.StatusCode}
		if envelope.Error != nil {
			responseErr.Code, responseErr.Message = envelope.Error.Code, envelope.Error.Message
		}
		return model.EnrollmentResponse{}, responseErr
	}
	var result model.EnrollmentResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		return model.EnrollmentResponse{}, err
	}
	if result.NodeID == "" || result.ProtocolVersion != model.ProtocolVersion {
		return model.EnrollmentResponse{}, errors.New("server returned an incompatible enrollment response")
	}
	return result, nil
}
