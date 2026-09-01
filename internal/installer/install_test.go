package installer

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/agentconfig"
	"github.com/chnzzh/hostpin/internal/enrollment"
	"github.com/chnzzh/hostpin/internal/model"
)

func TestPlainHTTPAuthorizationRequiresExplicitConfirmation(t *testing.T) {
	publicEndpoint := "http://198.51.100.20:8080"
	if err := authorizePlainHTTP(publicEndpoint, false, nil); err == nil || !strings.Contains(err.Error(), "public plain HTTP") {
		t.Fatalf("public HTTP without confirmation returned %v", err)
	}
	if err := authorizePlainHTTP(publicEndpoint, true, nil); err != nil {
		t.Fatalf("explicit public HTTP authorization was rejected: %v", err)
	}

	yesPrompt := &prompt{reader: bufio.NewReader(strings.NewReader("yes\n")), output: io.Discard}
	if err := authorizePlainHTTP(publicEndpoint, false, yesPrompt); err != nil {
		t.Fatalf("interactive public HTTP confirmation was rejected: %v", err)
	}
	noPrompt := &prompt{reader: bufio.NewReader(strings.NewReader("no\n")), output: io.Discard}
	if err := authorizePlainHTTP(publicEndpoint, false, noPrompt); err == nil {
		t.Fatal("interactive public HTTP rejection was ignored")
	}

	if err := authorizePlainHTTP("http://10.0.0.4:8080", false, nil); err == nil || strings.Contains(err.Error(), "public") {
		t.Fatalf("private HTTP policy returned %v", err)
	}
	if err := authorizePlainHTTP("https://monitor.example.test", false, nil); err != nil {
		t.Fatalf("HTTPS unexpectedly required confirmation: %v", err)
	}
}

func TestNonInteractiveMetadata(t *testing.T) {
	t.Setenv("HOSTPIN_NODE_NAME", "edge-test")
	t.Setenv("HOSTPIN_NODE_GROUP", "qa")
	t.Setenv("HOSTPIN_NODE_REGION", "Singapore")
	t.Setenv("HOSTPIN_NODE_TAGS", "edge, qa,edge")
	metadata, cfg, err := readMetadata(nil, true, false, model.AgentIdentity{Hostname: "fallback"}, agentconfig.Config{}, model.NodeRoleMonitor)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "edge-test" || metadata.Group != "qa" || metadata.Region != "Singapore" || len(metadata.Tags) != 3 {
		t.Fatalf("non-interactive metadata was not collected: %#v", metadata)
	}
	if cfg.CollectIntervalSeconds != 3 || cfg.PersistIntervalSeconds != 60 || cfg.EnableGPU || cfg.AutoUpdate {
		t.Fatalf("unexpected default Agent configuration: %#v", cfg)
	}
}

func TestNonInteractiveProbeNodeMetadata(t *testing.T) {
	t.Setenv("HOSTPIN_NODE_NAME", "home-router")
	t.Setenv("HOSTPIN_NODE_REGION", "Home ISP")
	t.Setenv("HOSTPIN_PROBE_PUBLIC", "false")
	metadata, cfg, err := readMetadata(nil, true, false, model.AgentIdentity{Hostname: "router"}, agentconfig.Config{}, model.NodeRoleProbe)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "home-router" || metadata.Region != "Home ISP" || !metadata.Hidden {
		t.Fatalf("probe-node metadata was not collected: %#v", metadata)
	}
	if cfg.CollectIntervalSeconds != 5 || cfg.ProbeConcurrency != 4 || cfg.EnableGPU {
		t.Fatalf("unexpected Probe Node configuration: %#v", cfg)
	}
}

func TestPINValidation(t *testing.T) {
	if _, err := validatePIN("246810"); err != nil {
		t.Fatalf("six-character PIN rejected: %v", err)
	}
	for _, value := range []string{"short", string(make([]byte, 65))} {
		if _, err := validatePIN(value); err == nil {
			t.Fatalf("invalid PIN length %d was accepted", len(value))
		}
	}
}

func TestTrafficLimitGiBParsing(t *testing.T) {
	tests := map[string]int64{
		"0":    0,
		"1":    1_073_741_824,
		"1.5":  1_610_612_736,
		"1000": 1_073_741_824_000,
	}
	for value, expected := range tests {
		actual, err := parseTrafficLimitGiB(value)
		if err != nil || actual != expected {
			t.Fatalf("parseTrafficLimitGiB(%q)=%d, %v; want %d", value, actual, err, expected)
		}
	}
	for _, value := range []string{"-1", "NaN", "Inf", "not-a-number", "999999999999999999999999"} {
		if _, err := parseTrafficLimitGiB(value); err == nil {
			t.Fatalf("invalid GiB traffic limit %q was accepted", value)
		}
	}
}

func TestEnrollmentNetworkContextStartsWhenRequestBegins(t *testing.T) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), enrollmentNetworkTimeout)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("enrollment network context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < enrollmentNetworkTimeout-time.Second || remaining > enrollmentNetworkTimeout {
		t.Fatalf("network deadline=%v after request start, want about %v (started %v ago)", remaining, enrollmentNetworkTimeout, time.Since(started))
	}
}

func TestEnrollmentRetryClassification(t *testing.T) {
	if shouldRetryEnrollment(&enrollment.ResponseError{Status: http.StatusUnauthorized}) {
		t.Fatal("unauthorized response was classified as retryable")
	}
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway} {
		if !shouldRetryEnrollment(&enrollment.ResponseError{Status: status}) {
			t.Fatalf("HTTP %d was not classified as retryable", status)
		}
	}
	if !shouldRetryEnrollment(errors.New("connection reset")) {
		t.Fatal("network failure was not classified as retryable")
	}
}
