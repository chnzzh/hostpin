package core

import (
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type trafficState struct {
	bootID      string
	lastRX      uint64
	lastTX      uint64
	monthlyRX   uint64
	monthlyTX   uint64
	periodStart time.Time
}

type trafficPolicy struct {
	resetDay     int
	rxCorrection int64
	txCorrection int64
	periodStart  *time.Time
}

type TrafficTracker struct {
	mu       sync.Mutex
	states   map[string]trafficState
	policies map[string]trafficPolicy
}

func NewTrafficTracker() *TrafficTracker {
	return &TrafficTracker{states: make(map[string]trafficState), policies: make(map[string]trafficPolicy)}
}

func (t *TrafficTracker) Load(samples map[string]model.MetricSample, nodes map[string]model.Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for nodeID, sample := range samples {
		node := nodes[nodeID]
		t.policies[nodeID] = policyFromNode(node)
		t.states[nodeID] = trafficState{
			bootID: sample.BootID, lastRX: sample.NetRxBytes, lastTX: sample.NetTxBytes,
			monthlyRX: sample.MonthlyRxBytes, monthlyTX: sample.MonthlyTxBytes,
			periodStart: TrafficPeriodStart(sample.ReceivedAt, node.TrafficResetDay),
		}
	}
}

func (t *TrafficTracker) Apply(node model.Node, sample model.MetricSample, now time.Time) model.MetricSample {
	t.mu.Lock()
	defer t.mu.Unlock()
	policy, configured := t.policies[node.ID]
	if !configured {
		policy = policyFromNode(node)
		t.policies[node.ID] = policy
	}
	period := TrafficPeriodStart(now, policy.resetDay)
	state, exists := t.states[node.ID]
	if !exists {
		state = trafficState{bootID: sample.BootID, lastRX: sample.NetRxBytes, lastTX: sample.NetTxBytes, periodStart: period}
		sample.MonthlyRxBytes, sample.MonthlyTxBytes = 0, 0
		t.states[node.ID] = state
		return sample
	}
	if !state.periodStart.Equal(period) {
		// A counter delta that crosses the reset boundary cannot be split
		// accurately between billing periods. Start from the first counter seen
		// in the new period instead of over-counting it as current-period usage.
		state = trafficState{
			bootID: sample.BootID, lastRX: sample.NetRxBytes, lastTX: sample.NetTxBytes,
			periodStart: period,
		}
		sample.MonthlyRxBytes, sample.MonthlyTxBytes = 0, 0
		t.states[node.ID] = state
		return sample
	}
	if state.bootID == sample.BootID && sample.NetRxBytes >= state.lastRX {
		state.monthlyRX = saturatingAdd(state.monthlyRX, sample.NetRxBytes-state.lastRX)
	}
	if state.bootID == sample.BootID && sample.NetTxBytes >= state.lastTX {
		state.monthlyTX = saturatingAdd(state.monthlyTX, sample.NetTxBytes-state.lastTX)
	}
	state.bootID, state.lastRX, state.lastTX = sample.BootID, sample.NetRxBytes, sample.NetTxBytes
	sample.MonthlyRxBytes, sample.MonthlyTxBytes = state.monthlyRX, state.monthlyTX
	t.states[node.ID] = state
	return sample
}

// ConfigureNode updates the current reset and correction policy without
// disturbing the raw monotonic-counter state.
func (t *TrafficTracker) ConfigureNode(node model.Node) {
	t.mu.Lock()
	t.policies[node.ID] = policyFromNode(node)
	t.mu.Unlock()
}

// Correct returns a presentation copy of a raw sample. The tracker keeps raw
// period totals internally so changing or clearing a correction never changes
// the Agent counters or durable metric history.
func (t *TrafficTracker) Correct(nodeID string, sample model.MetricSample) model.MetricSample {
	t.mu.Lock()
	policy, ok := t.policies[nodeID]
	t.mu.Unlock()
	if !ok || !correctionApplies(policy, metricTime(sample)) {
		return sample
	}
	sample.MonthlyRxBytes = addSigned(sample.MonthlyRxBytes, policy.rxCorrection)
	sample.MonthlyTxBytes = addSigned(sample.MonthlyTxBytes, policy.txCorrection)
	return sample
}

// Uncorrect reverses the active presentation adjustment. Corrections are
// created from a non-negative target total, so the operation is exact for the
// current and all subsequent samples in that billing period.
func (t *TrafficTracker) Uncorrect(nodeID string, sample model.MetricSample) model.MetricSample {
	t.mu.Lock()
	policy, ok := t.policies[nodeID]
	t.mu.Unlock()
	if !ok || !correctionApplies(policy, metricTime(sample)) {
		return sample
	}
	sample.MonthlyRxBytes = subtractSigned(sample.MonthlyRxBytes, policy.rxCorrection)
	sample.MonthlyTxBytes = subtractSigned(sample.MonthlyTxBytes, policy.txCorrection)
	return sample
}

func policyFromNode(node model.Node) trafficPolicy {
	policy := trafficPolicy{
		resetDay: node.TrafficResetDay, rxCorrection: node.TrafficRXCorrection,
		txCorrection: node.TrafficTXCorrection,
	}
	if node.TrafficCorrectionPeriodStart != nil {
		period := node.TrafficCorrectionPeriodStart.UTC()
		policy.periodStart = &period
	}
	return policy
}

func correctionApplies(policy trafficPolicy, at time.Time) bool {
	return policy.periodStart != nil && policy.periodStart.Equal(TrafficPeriodStart(at, policy.resetDay))
}

func metricTime(sample model.MetricSample) time.Time {
	if !sample.ReceivedAt.IsZero() {
		return sample.ReceivedAt
	}
	return sample.CollectedAt
}

func addSigned(value uint64, adjustment int64) uint64 {
	if adjustment >= 0 {
		return saturatingAdd(value, uint64(adjustment))
	}
	magnitude := uint64(-(adjustment + 1)) + 1
	if magnitude >= value {
		return 0
	}
	return value - magnitude
}

func subtractSigned(value uint64, adjustment int64) uint64 {
	if adjustment >= 0 {
		magnitude := uint64(adjustment)
		if magnitude >= value {
			return 0
		}
		return value - magnitude
	}
	magnitude := uint64(-(adjustment + 1)) + 1
	return saturatingAdd(value, magnitude)
}

func saturatingAdd(left, right uint64) uint64 {
	maximum := ^uint64(0)
	if right > maximum-left {
		return maximum
	}
	return left + right
}

func (t *TrafficTracker) Delete(nodeID string) {
	t.mu.Lock()
	delete(t.states, nodeID)
	delete(t.policies, nodeID)
	t.mu.Unlock()
}

func TrafficPeriodStart(now time.Time, resetDay int) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	if resetDay < 1 || resetDay > 31 {
		resetDay = 1
	}
	thisMonthDay := min(resetDay, daysInMonth(now.Year(), now.Month()))
	start := time.Date(now.Year(), now.Month(), thisMonthDay, 0, 0, 0, 0, time.UTC)
	if now.Before(start) {
		year, month := now.Year(), now.Month()-1
		if month < time.January {
			year, month = year-1, time.December
		}
		day := min(resetDay, daysInMonth(year, month))
		start = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	}
	return start
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
