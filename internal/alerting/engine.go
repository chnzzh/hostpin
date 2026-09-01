package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/google/uuid"
)

type latestProvider interface {
	Latest(string) (model.MetricSample, bool)
}

type repository interface {
	ListAlertRules(context.Context) ([]model.AlertRule, error)
	ListNodes(context.Context, bool) ([]model.Node, error)
	ListLatencyNodes(context.Context, bool) ([]model.Node, error)
	SaveAlertEvent(context.Context, model.AlertEvent, *int64) error
	EnqueueNotificationDeliveries(context.Context, string, time.Time) error
}

type stateKey struct {
	ruleID int64
	nodeID string
}

type ruleState struct {
	pendingSince time.Time
	firing       bool
	lastEvent    time.Time
}

type evaluation struct {
	node   model.Node
	sample *model.MetricSample
	probe  *model.ProbeResult
}

type Engine struct {
	store        repository
	hub          latestProvider
	publicURL    string
	offlineAfter time.Duration
	logger       *slog.Logger
	queue        chan evaluation
	reload       chan struct{}
	mu           sync.RWMutex
	rules        []model.AlertRule
	states       map[stateKey]*ruleState
	expiry       map[string]map[int]time.Time
}

func New(repository repository, hub latestProvider, publicURL string, offlineAfter time.Duration, logger *slog.Logger) *Engine {
	if offlineAfter <= 0 {
		offlineAfter = 90 * time.Second
	}
	return &Engine{
		store: repository, hub: hub, publicURL: strings.TrimRight(publicURL, "/"), logger: logger,
		offlineAfter: offlineAfter,
		queue:        make(chan evaluation, 2048), reload: make(chan struct{}, 1),
		states: make(map[stateKey]*ruleState), expiry: make(map[string]map[int]time.Time),
	}
}

func (e *Engine) Start(ctx context.Context) {
	e.loadRules(ctx)
	go e.run(ctx)
}

func (e *Engine) Reload() {
	select {
	case e.reload <- struct{}{}:
	default:
	}
}

func (e *Engine) EvaluateSample(node model.Node, sample model.MetricSample) {
	copy := sample
	e.enqueue(evaluation{node: node, sample: &copy})
}

func (e *Engine) EvaluateProbe(node model.Node, result model.ProbeResult) {
	copy := result
	e.enqueue(evaluation{node: node, probe: &copy})
}

func (e *Engine) enqueue(item evaluation) {
	select {
	case e.queue <- item:
	default:
		e.logger.Warn("alert evaluator queue is full", "node", item.node.ID)
	}
}

func (e *Engine) run(ctx context.Context) {
	sweep := time.NewTicker(15 * time.Second)
	refresh := time.NewTicker(time.Minute)
	defer sweep.Stop()
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-e.queue:
			e.evaluate(ctx, item, time.Now().UTC())
		case <-e.reload:
			e.loadRules(ctx)
		case <-refresh.C:
			e.loadRules(ctx)
		case now := <-sweep.C:
			e.sweepNodes(ctx, now.UTC())
		}
	}
}

func (e *Engine) loadRules(ctx context.Context) {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		e.logger.Error("load alert rules", "error", err)
		return
	}
	e.mu.Lock()
	e.rules = rules
	e.mu.Unlock()
}

func (e *Engine) evaluate(ctx context.Context, item evaluation, now time.Time) {
	e.mu.RLock()
	rules := append([]model.AlertRule(nil), e.rules...)
	e.mu.RUnlock()
	for _, rule := range rules {
		if !rule.Enabled || !matchesScope(rule.Scope, item.node) {
			continue
		}
		value, ok := metricValue(rule.Metric, item.sample, item.probe)
		if !ok {
			continue
		}
		e.evaluateRule(ctx, rule, item.node, value, now)
	}
}

func (e *Engine) evaluateRule(ctx context.Context, rule model.AlertRule, node model.Node, value float64, now time.Time) {
	key := stateKey{ruleID: rule.ID, nodeID: node.ID}
	state := e.states[key]
	if state == nil {
		state = &ruleState{}
		e.states[key] = state
	}
	condition := compare(value, rule.Operator, rule.Threshold)
	if condition {
		if state.firing {
			return
		}
		if state.pendingSince.IsZero() {
			state.pendingSince = now
		}
		if now.Sub(state.pendingSince) < time.Duration(max(rule.DurationSeconds, 0))*time.Second {
			return
		}
		if rule.CooldownSeconds > 0 && now.Sub(state.lastEvent) < time.Duration(rule.CooldownSeconds)*time.Second {
			return
		}
		message := fmt.Sprintf("%s: %s is %.2f (threshold %.2f)", node.Name, rule.Metric, value, rule.Threshold)
		if e.emit(ctx, &rule.ID, rule.Name, model.AlertFiring, rule.Severity, node, rule.Metric, value, rule.Threshold, message, now) {
			state.firing, state.lastEvent = true, now
		}
		return
	}
	state.pendingSince = time.Time{}
	if state.firing && recovered(value, rule) {
		message := fmt.Sprintf("%s recovered: %s is %.2f", node.Name, rule.Metric, value)
		if e.emit(ctx, &rule.ID, rule.Name, model.AlertResolved, rule.Severity, node, rule.Metric, value, rule.RecoveryThreshold, message, now) {
			state.firing, state.lastEvent = false, now
		}
	}
}

func (e *Engine) sweepNodes(ctx context.Context, now time.Time) {
	nodes, err := e.store.ListNodes(ctx, true)
	if err != nil {
		return
	}
	for _, node := range nodes {
		online := 0.0
		if sample, ok := e.hub.Latest(node.ID); ok && now.Sub(sample.ReceivedAt) <= e.offlineAfter {
			online = 1
		}
		sample := model.MetricSample{ReceivedAt: now, CPU: online}
		e.evaluate(ctx, evaluation{node: node, sample: &sample}, now)
		e.evaluateExpiry(ctx, node, now)
	}
	probeNodes, err := e.store.ListLatencyNodes(ctx, true)
	if err != nil {
		return
	}
	for _, node := range probeNodes {
		online := 0.0
		if node.LastSeenAt != nil && now.Sub(*node.LastSeenAt) <= e.offlineAfter {
			online = 1
		}
		sample := model.MetricSample{ReceivedAt: now, CPU: online}
		e.evaluate(ctx, evaluation{node: node, sample: &sample}, now)
	}
}

func (e *Engine) evaluateExpiry(ctx context.Context, node model.Node, now time.Time) {
	if node.ExpiresAt == nil || node.AutoRenewal {
		return
	}
	days := int(math.Ceil(node.ExpiresAt.Sub(now).Hours() / 24))
	if !slices.Contains([]int{30, 14, 7, 3, 1, 0}, days) {
		return
	}
	byDay := e.expiry[node.ID]
	if byDay == nil {
		byDay = make(map[int]time.Time)
		e.expiry[node.ID] = byDay
	}
	if previous := byDay[days]; !previous.IsZero() && now.Sub(previous) < 20*time.Hour {
		return
	}
	severity := "warning"
	if days <= 3 {
		severity = "critical"
	}
	message := fmt.Sprintf("%s expires in %d day(s)", node.Name, days)
	if e.emit(ctx, nil, "expiry", model.AlertFiring, severity, node, "expiry_days", float64(days), float64(days), message, now) {
		byDay[days] = now
	}
}

func (e *Engine) emit(ctx context.Context, ruleID *int64, eventType string, status model.AlertStatus, severity string, node model.Node, metric string, value, threshold float64, message string, now time.Time) bool {
	id, err := uuid.NewV7()
	if err != nil {
		return false
	}
	event := model.AlertEvent{
		ID: id.String(), Type: eventType, Status: status, Severity: severity,
		OccurredAt: now, Node: node.Public(), Metric: metric, Value: value,
		Threshold: threshold, Link: nodeAlertLink(e.publicURL, node), Message: message,
	}
	if err := e.store.SaveAlertEvent(ctx, event, ruleID); err != nil {
		e.logger.Error("save alert event", "error", err, "node", node.ID, "type", eventType)
		return false
	}
	if err := e.store.EnqueueNotificationDeliveries(ctx, event.ID, now); err != nil {
		e.logger.Error("queue alert notifications", "error", err, "event", event.ID)
	}
	return true
}

func nodeAlertLink(publicURL string, node model.Node) string {
	if node.Role == model.NodeRoleProbe {
		return publicURL + "/admin/latency"
	}
	return publicURL + "/nodes/" + node.ID
}

func matchesScope(scope model.AlertScope, node model.Node) bool {
	if slices.Contains(scope.Excluded, node.ID) {
		return false
	}
	if len(scope.NodeIDs) == 0 && len(scope.Groups) == 0 {
		return true
	}
	return slices.Contains(scope.NodeIDs, node.ID) || slices.Contains(scope.Groups, node.Group)
}

func metricValue(metric string, sample *model.MetricSample, result *model.ProbeResult) (float64, bool) {
	if result != nil {
		switch metric {
		case "probe_success":
			if result.Success {
				return 1, true
			}
			return 0, true
		case "probe_latency":
			return result.LatencyMS, true
		case "probe_loss":
			return result.LossPercent, true
		}
		return 0, false
	}
	if sample == nil {
		return 0, false
	}
	percentage := func(used, total uint64) float64 {
		if total == 0 {
			return 0
		}
		return float64(used) / float64(total) * 100
	}
	switch metric {
	case "online":
		if sample.NodeID == "" {
			return sample.CPU, true // Lifecycle sweep stores the online bit in this synthetic field.
		}
		return 0, false
	case "cpu":
		return sample.CPU, true
	case "memory":
		return percentage(sample.MemoryUsed, sample.MemoryTotal), true
	case "swap":
		return percentage(sample.SwapUsed, sample.SwapTotal), true
	case "load1":
		return sample.Load1, true
	case "load5":
		return sample.Load5, true
	case "load15":
		return sample.Load15, true
	case "disk":
		return percentage(sample.DiskUsed, sample.DiskTotal), true
	case "temperature":
		return sample.Temperature, true
	case "gpu":
		value := 0.0
		for _, gpu := range sample.GPUs {
			value = max(value, gpu.Utilization)
		}
		return value, len(sample.GPUs) > 0
	case "traffic_sum":
		// Convert each direction before addition so two valid uint64 counters
		// cannot wrap before they reach the alert comparison.
		return float64(sample.MonthlyRxBytes) + float64(sample.MonthlyTxBytes), true
	case "traffic_up":
		return float64(sample.MonthlyTxBytes), true
	case "traffic_down":
		return float64(sample.MonthlyRxBytes), true
	default:
		return 0, false
	}
}

func compare(value float64, operator string, threshold float64) bool {
	switch operator {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	default:
		return false
	}
}

func recovered(value float64, rule model.AlertRule) bool {
	switch rule.Operator {
	case ">", ">=":
		return value <= rule.RecoveryThreshold
	case "<", "<=":
		return value >= rule.RecoveryThreshold
	case "==":
		return value != rule.RecoveryThreshold
	default:
		return true
	}
}
