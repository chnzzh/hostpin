package alerting

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type memoryRepository struct {
	rules  []model.AlertRule
	nodes  []model.Node
	probes []model.Node
	events []model.AlertEvent
}

func (m *memoryRepository) ListAlertRules(context.Context) ([]model.AlertRule, error) {
	return append([]model.AlertRule(nil), m.rules...), nil
}
func (m *memoryRepository) ListNodes(context.Context, bool) ([]model.Node, error) {
	return append([]model.Node(nil), m.nodes...), nil
}
func (m *memoryRepository) ListLatencyNodes(context.Context, bool) ([]model.Node, error) {
	return append([]model.Node(nil), m.probes...), nil
}
func (m *memoryRepository) SaveAlertEvent(_ context.Context, event model.AlertEvent, _ *int64) error {
	m.events = append(m.events, event)
	return nil
}
func (m *memoryRepository) EnqueueNotificationDeliveries(context.Context, string, time.Time) error {
	return nil
}

type emptyLatest struct{}

func (emptyLatest) Latest(string) (model.MetricSample, bool) { return model.MetricSample{}, false }

func TestRuleComparisonRecoveryAndScope(t *testing.T) {
	rule := model.AlertRule{Operator: ">", Threshold: 90, RecoveryThreshold: 80}
	if !compare(91, rule.Operator, rule.Threshold) || compare(90, rule.Operator, rule.Threshold) {
		t.Fatal("strict comparison is incorrect")
	}
	if !recovered(79, rule) || recovered(81, rule) {
		t.Fatal("recovery hysteresis is incorrect")
	}
	node := model.Node{ID: "n1", Group: "edge"}
	if !matchesScope(model.AlertScope{Groups: []string{"edge"}}, node) || matchesScope(model.AlertScope{Excluded: []string{"n1"}}, node) {
		t.Fatal("alert scope matching is incorrect")
	}
}

func TestMetricValues(t *testing.T) {
	sample := model.MetricSample{MemoryTotal: 1000, MemoryUsed: 925}
	value, ok := metricValue("memory", &sample, nil)
	if !ok || value != 92.5 {
		t.Fatalf("unexpected memory value %v", value)
	}
	value, ok = metricValue("probe_loss", nil, &model.ProbeResult{LossPercent: 33.333})
	if !ok || value != 33.333 {
		t.Fatalf("unexpected probe loss value %v", value)
	}
	maximum := ^uint64(0)
	value, ok = metricValue("traffic_sum", &model.MetricSample{MonthlyRxBytes: maximum, MonthlyTxBytes: maximum}, nil)
	if !ok || value <= float64(maximum) {
		t.Fatalf("traffic sum wrapped before comparison: %v", value)
	}
}

func TestProbeNodeAlertLinkUsesLatencyAdministration(t *testing.T) {
	node := model.Node{ID: "probe-1", Role: model.NodeRoleProbe}
	if link := nodeAlertLink("https://monitor.example", node); link != "https://monitor.example/admin/latency" {
		t.Fatalf("unexpected Probe Node alert link %q", link)
	}
}

func TestAlertStateMachineDurationRecoveryAndCooldown(t *testing.T) {
	repository := &memoryRepository{}
	engine := New(repository, emptyLatest{}, "https://monitor.example", 90*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rule := model.AlertRule{
		ID: 1, Name: "CPU sustained", Metric: "cpu", Operator: ">", Threshold: 90,
		RecoveryThreshold: 80, DurationSeconds: 60, CooldownSeconds: 300,
		Severity: "critical", Enabled: true,
	}
	engine.rules = []model.AlertRule{rule}
	node := model.Node{ID: "node-1", Role: model.NodeRoleMonitor, Name: "edge"}
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	evaluate := func(at time.Time, cpu float64) {
		engine.evaluate(context.Background(), evaluation{node: node, sample: &model.MetricSample{CPU: cpu}}, at)
	}
	evaluate(start, 95)
	evaluate(start.Add(59*time.Second), 95)
	if len(repository.events) != 0 {
		t.Fatal("sustained rule fired before its duration")
	}
	evaluate(start.Add(60*time.Second), 95)
	evaluate(start.Add(61*time.Second), 96)
	if len(repository.events) != 1 || repository.events[0].Status != model.AlertFiring {
		t.Fatalf("firing transition was incorrect: %#v", repository.events)
	}
	evaluate(start.Add(62*time.Second), 70)
	if len(repository.events) != 2 || repository.events[1].Status != model.AlertResolved {
		t.Fatalf("recovery transition was incorrect: %#v", repository.events)
	}
	evaluate(start.Add(63*time.Second), 95)
	evaluate(start.Add(180*time.Second), 95)
	if len(repository.events) != 2 {
		t.Fatal("cooldown allowed an early repeat firing")
	}
	evaluate(start.Add(400*time.Second), 95)
	if len(repository.events) != 3 || repository.events[2].Status != model.AlertFiring {
		t.Fatalf("rule did not fire after cooldown: %#v", repository.events)
	}
}

func TestProbeLossAndProbeOfflineRules(t *testing.T) {
	lastSeen := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	repository := &memoryRepository{probes: []model.Node{{
		ID: "probe-1", Role: model.NodeRoleProbe, Name: "home-router", LastSeenAt: &lastSeen,
	}}}
	engine := New(repository, emptyLatest{}, "https://monitor.example", 90*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.rules = []model.AlertRule{{
		ID: 2, Name: "Packet loss", Metric: "probe_loss", Operator: ">=", Threshold: 50,
		RecoveryThreshold: 5, Severity: "warning", Enabled: true,
	}}
	engine.evaluate(context.Background(), evaluation{
		node: repository.probes[0], probe: &model.ProbeResult{LossPercent: 66.67},
	}, lastSeen.Add(time.Second))
	if len(repository.events) != 1 || repository.events[0].Metric != "probe_loss" {
		t.Fatalf("packet-loss rule did not fire: %#v", repository.events)
	}

	repository.events = nil
	engine.rules = []model.AlertRule{{
		ID: 3, Name: "Node offline", Metric: "online", Operator: "<", Threshold: 1,
		RecoveryThreshold: 1, Severity: "critical", Enabled: true,
	}}
	engine.sweepNodes(context.Background(), lastSeen.Add(2*time.Minute))
	if len(repository.events) != 1 || repository.events[0].Node.ID != "probe-1" || repository.events[0].Link != "https://monitor.example/admin/latency" {
		t.Fatalf("Probe Node offline event was incorrect: %#v", repository.events)
	}
}

func TestExpiryReminderScheduleSeverityAndDeduplication(t *testing.T) {
	repository := &memoryRepository{}
	engine := New(repository, emptyLatest{}, "https://monitor.example", 90*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	expires := now.Add(3 * 24 * time.Hour)
	node := model.Node{ID: "node-expiry", Name: "leased-edge", ExpiresAt: &expires}
	engine.evaluateExpiry(context.Background(), node, now)
	engine.evaluateExpiry(context.Background(), node, now.Add(time.Hour))
	if len(repository.events) != 1 || repository.events[0].Type != "expiry" || repository.events[0].Severity != "critical" || repository.events[0].Value != 3 {
		t.Fatalf("three-day expiry reminder was incorrect: %#v", repository.events)
	}

	warningExpiry := now.Add(14 * 24 * time.Hour)
	engine.evaluateExpiry(context.Background(), model.Node{ID: "warning-expiry", Name: "warning-edge", ExpiresAt: &warningExpiry}, now)
	if len(repository.events) != 2 || repository.events[1].Severity != "warning" || repository.events[1].Value != 14 {
		t.Fatalf("fourteen-day expiry reminder was incorrect: %#v", repository.events)
	}
	autoExpiry := now.Add(24 * time.Hour)
	engine.evaluateExpiry(context.Background(), model.Node{ID: "auto-renew", Name: "auto", ExpiresAt: &autoExpiry, AutoRenewal: true}, now)
	if len(repository.events) != 2 {
		t.Fatal("auto-renewing node emitted an expiry reminder")
	}
}
