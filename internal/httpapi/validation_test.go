package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
)

func TestNodeAndAgentConfigurationValidation(t *testing.T) {
	node := model.Node{
		Name: "edge", CountryCode: "SG", Tags: []string{"edge"}, Currency: "USD",
		BillingCycleDays: 30, TrafficLimitType: "sum", TrafficResetDay: 1,
	}
	if err := validateNodeUpdate(node); err != nil {
		t.Fatalf("valid node rejected: %v", err)
	}
	invalid := node
	invalid.CountryCode = "Singapore"
	if validateNodeUpdate(invalid) == nil {
		t.Fatal("invalid country code was accepted")
	}
	invalid = node
	invalid.TrafficResetDay = 32
	if validateNodeUpdate(invalid) == nil {
		t.Fatal("invalid traffic reset day was accepted")
	}

	cfg := model.DefaultAgentConfig()
	if err := validateAgentConfig(cfg, false); err != nil {
		t.Fatalf("default Agent configuration rejected: %v", err)
	}
	cfg.PersistIntervalSeconds = 1
	if validateAgentConfig(cfg, false) == nil {
		t.Fatal("persistence interval below live interval was accepted")
	}
}

func TestTrafficAdjustmentUsesSignedDifference(t *testing.T) {
	if got := trafficAdjustment(1_500, 1_000); got != 500 {
		t.Fatalf("positive correction=%d, want 500", got)
	}
	if got := trafficAdjustment(250, 1_000); got != -750 {
		t.Fatalf("negative correction=%d, want -750", got)
	}
	if got := trafficAdjustment(maxTrafficCorrectionBytes, 0); got != int64(maxTrafficCorrectionBytes) {
		t.Fatalf("maximum correction=%d", got)
	}
}

func TestRecoveryCodesAreOneTime(t *testing.T) {
	codes, hashes, err := recoveryCodes(2)
	if err != nil {
		t.Fatal(err)
	}
	admin := store.Admin{RecoveryHashes: hashes}
	if !consumeRecoveryCode(&admin, codes[0]) || consumeRecoveryCode(&admin, codes[0]) || len(admin.RecoveryHashes) != 1 {
		t.Fatal("recovery code was not consumed exactly once")
	}
}

func TestClientIPTrustsOnlyConfiguredProxyPeers(t *testing.T) {
	api := &API{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	untrusted := httptest.NewRequest("GET", "http://example.test", nil)
	untrusted.RemoteAddr = "192.0.2.10:443"
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := api.clientIP(untrusted); got != "192.0.2.10" {
		t.Fatalf("untrusted proxy spoofed source IP as %q", got)
	}
	trusted := httptest.NewRequest("GET", "http://example.test", nil)
	trusted.RemoteAddr = "10.0.0.2:443"
	trusted.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.3")
	if got := api.clientIP(trusted); got != "198.51.100.7" {
		t.Fatalf("trusted proxy chain resolved source IP as %q", got)
	}
}

func TestAgentStreamRevocation(t *testing.T) {
	api := &API{agentStreams: make(map[string]map[chan struct{}]struct{})}
	revoked, unregister := api.registerAgentStream("node-1")
	api.revokeAgentStreams("node-1")
	select {
	case <-revoked:
	default:
		t.Fatal("active Agent stream was not revoked")
	}
	unregister()
}

func TestProbePresenceIntervalTracksOfflineThreshold(t *testing.T) {
	if got := probePresenceInterval(90 * time.Second); got != 15*time.Second {
		t.Fatalf("default presence interval = %v, want 15s", got)
	}
	if got := probePresenceInterval(10 * time.Second); got != 5*time.Second {
		t.Fatalf("short-threshold presence interval = %v, want 5s", got)
	}
	if got := probePresenceInterval(time.Second); got != time.Second {
		t.Fatalf("minimum presence interval = %v, want 1s", got)
	}
}

func TestEnrollmentAndProbeValidation(t *testing.T) {
	token, _, _, err := security.NewAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	request := model.EnrollmentRequest{
		PIN: "246810", InstallID: uuid.NewString(), Token: token,
		Identity: model.AgentIdentity{Hostname: "edge", OS: "linux", Arch: "arm64", IPv4: "192.0.2.8"},
		Metadata: model.EnrollmentMetadata{Name: "edge", CountryCode: "SG", Currency: "USD", TrafficLimitType: "sum", TrafficResetDay: 1},
		Config:   model.DefaultAgentConfig(),
	}
	if err := validateEnrollment(request); err != nil {
		t.Fatalf("valid enrollment rejected: %v", err)
	}
	request.Identity.IPv4 = "not-an-address"
	if validateEnrollment(request) == nil {
		t.Fatal("invalid identity address was accepted")
	}
	request.Identity.IPv4 = "192.0.2.8"
	request.Role = model.NodeRole("shell")
	if validateEnrollment(request) == nil {
		t.Fatal("unsupported Agent role was accepted")
	}

	validProbe := model.ProbeTask{Name: "health", Type: model.ProbeHTTP, Target: "https://example.test/health", IntervalSeconds: 30, TimeoutSeconds: 5, ExpectedStatus: 200}
	if err := validateProbeTask(validProbe); err != nil {
		t.Fatalf("valid probe rejected: %v", err)
	}
	validProbe.Type, validProbe.Target = model.ProbeTCP, "missing-port"
	if validateProbeTask(validProbe) == nil {
		t.Fatal("malformed TCP target was accepted")
	}
}

func TestLatencyDownsamplingAndTaskAuthorization(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	input := make([]model.LatencyResult, 5)
	for index := range input {
		input[index].ReceivedAt = start.Add(time.Duration(index) * time.Minute)
	}
	output := downsampleLatency(input, 3)
	if len(output) != 3 || output[0].ReceivedAt != input[0].ReceivedAt || output[len(output)-1].ReceivedAt != input[len(input)-1].ReceivedAt {
		t.Fatalf("latency downsampling lost a boundary: %#v", output)
	}
	if one := downsampleLatency(input, 1); len(one) != 1 || one[0].ReceivedAt != input[len(input)-1].ReceivedAt {
		t.Fatalf("single-point latency downsampling did not retain the latest sample: %#v", one)
	}
	tasks := []model.ProbeTask{{ID: 1, Enabled: true}, {ID: 2, Enabled: false}}
	if !probeTaskAllowed(tasks, 1) || probeTaskAllowed(tasks, 2) || probeTaskAllowed(tasks, 3) {
		t.Fatal("Agent probe result authorization ignored task assignment or enabled state")
	}
}

func TestSMTPConfigurationRejectsHeaderInjection(t *testing.T) {
	valid := map[string]any{
		"host": "smtp.example.com", "from": "Hostpin <alerts@example.com>",
		"to": "ops@example.com, owner@example.com",
	}
	if err := validateChannelConfig("smtp", valid); err != nil {
		t.Fatalf("valid SMTP configuration rejected: %v", err)
	}
	valid["to"] = "ops@example.com\r\nBcc: victim@example.com"
	if validateChannelConfig("smtp", valid) == nil {
		t.Fatal("SMTP recipient header injection was accepted")
	}
}

func TestWebhookConfigurationRejectsURLCredentials(t *testing.T) {
	if validateChannelConfig("webhook", map[string]any{"url": "https://hooks.example.com/event"}) != nil {
		t.Fatal("valid webhook URL was rejected")
	}
	if validateChannelConfig("webhook", map[string]any{"url": "https://user:secret@hooks.example.com/event"}) == nil {
		t.Fatal("webhook URL credentials were accepted")
	}
}

func TestPositiveRouteIDValidation(t *testing.T) {
	for _, valid := range []string{"1", "42", "9223372036854775807"} {
		if id, err := parsePositiveID(valid); err != nil || id <= 0 {
			t.Fatalf("valid route id %q was rejected: id=%d err=%v", valid, id, err)
		}
	}
	for _, invalid := range []string{"", "0", "-1", "abc", "1.5", "9223372036854775808"} {
		if id, err := parsePositiveID(invalid); err == nil || id != 0 {
			t.Fatalf("invalid route id %q was accepted: id=%d err=%v", invalid, id, err)
		}
	}
}
