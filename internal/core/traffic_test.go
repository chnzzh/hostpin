package core

import (
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestTrafficTrackerHandlesCounterAndPeriodReset(t *testing.T) {
	tracker := NewTrafficTracker()
	node := model.Node{ID: "node-1", TrafficResetDay: 1}
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	first := tracker.Apply(node, model.MetricSample{BootID: "a", NetRxBytes: 1000, NetTxBytes: 500}, now)
	if first.MonthlyRxBytes != 0 || first.MonthlyTxBytes != 0 {
		t.Fatal("first counters should establish a baseline")
	}
	second := tracker.Apply(node, model.MetricSample{BootID: "a", NetRxBytes: 1400, NetTxBytes: 700}, now.Add(time.Minute))
	if second.MonthlyRxBytes != 400 || second.MonthlyTxBytes != 200 {
		t.Fatalf("unexpected monthly deltas: %d/%d", second.MonthlyRxBytes, second.MonthlyTxBytes)
	}
	reboot := tracker.Apply(node, model.MetricSample{BootID: "b", NetRxBytes: 10, NetTxBytes: 20}, now.Add(2*time.Minute))
	if reboot.MonthlyRxBytes != 400 || reboot.MonthlyTxBytes != 200 {
		t.Fatal("boot reset changed accumulated traffic")
	}
	afterReboot := tracker.Apply(node, model.MetricSample{BootID: "b", NetRxBytes: 60, NetTxBytes: 45}, now.Add(3*time.Minute))
	if afterReboot.MonthlyRxBytes != 450 || afterReboot.MonthlyTxBytes != 225 {
		t.Fatal("post-boot deltas were not accumulated")
	}
	nextMonth := tracker.Apply(node, model.MetricSample{BootID: "b", NetRxBytes: 100, NetTxBytes: 100}, time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC))
	if nextMonth.MonthlyRxBytes != 0 || nextMonth.MonthlyTxBytes != 0 {
		t.Fatalf("monthly reset baseline was incorrect: %d/%d", nextMonth.MonthlyRxBytes, nextMonth.MonthlyTxBytes)
	}
	afterReset := tracker.Apply(node, model.MetricSample{BootID: "b", NetRxBytes: 130, NetTxBytes: 140}, time.Date(2026, 9, 1, 0, 0, 4, 0, time.UTC))
	if afterReset.MonthlyRxBytes != 30 || afterReset.MonthlyTxBytes != 40 {
		t.Fatalf("new-period deltas were incorrect: %d/%d", afterReset.MonthlyRxBytes, afterReset.MonthlyTxBytes)
	}
}

func TestTrafficTrackerRestoresPersistedStateAndHandlesCounterRollback(t *testing.T) {
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	tracker := NewTrafficTracker()
	tracker.Load(map[string]model.MetricSample{
		"node-1": {
			BootID: "boot-a", NetRxBytes: 10_000, NetTxBytes: 5_000,
			MonthlyRxBytes: 4_000, MonthlyTxBytes: 2_000, ReceivedAt: now,
		},
	}, map[string]model.Node{"node-1": {ID: "node-1", TrafficResetDay: 1}})
	node := model.Node{ID: "node-1", TrafficResetDay: 1}
	restored := tracker.Apply(node, model.MetricSample{BootID: "boot-a", NetRxBytes: 10_400, NetTxBytes: 5_250}, now.Add(time.Minute))
	if restored.MonthlyRxBytes != 4_400 || restored.MonthlyTxBytes != 2_250 {
		t.Fatalf("persisted traffic was not restored: %d/%d", restored.MonthlyRxBytes, restored.MonthlyTxBytes)
	}
	rollback := tracker.Apply(node, model.MetricSample{BootID: "boot-a", NetRxBytes: 50, NetTxBytes: 25}, now.Add(2*time.Minute))
	if rollback.MonthlyRxBytes != 4_400 || rollback.MonthlyTxBytes != 2_250 {
		t.Fatalf("counter rollback changed usage: %d/%d", rollback.MonthlyRxBytes, rollback.MonthlyTxBytes)
	}
	afterRollback := tracker.Apply(node, model.MetricSample{BootID: "boot-a", NetRxBytes: 80, NetTxBytes: 45}, now.Add(3*time.Minute))
	if afterRollback.MonthlyRxBytes != 4_430 || afterRollback.MonthlyTxBytes != 2_270 {
		t.Fatalf("counter rollback did not establish a new baseline: %d/%d", afterRollback.MonthlyRxBytes, afterRollback.MonthlyTxBytes)
	}
}

func TestTrafficPeriodStartClampsShortMonths(t *testing.T) {
	for _, test := range []struct {
		now      time.Time
		resetDay int
		want     time.Time
	}{
		{time.Date(2027, 2, 28, 12, 0, 0, 0, time.UTC), 31, time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)},
		{time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC), 31, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 31, time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC), 15, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)},
		{time.Date(2027, 1, 14, 12, 0, 0, 0, time.UTC), 15, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), 0, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if got := TrafficPeriodStart(test.now, test.resetDay); !got.Equal(test.want) {
			t.Errorf("TrafficPeriodStart(%s, %d)=%s, want %s", test.now, test.resetDay, got, test.want)
		}
	}
}

func TestTrafficCorrectionIsReversibleAndPeriodScoped(t *testing.T) {
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	period := TrafficPeriodStart(now, 1)
	node := model.Node{
		ID: "node-1", TrafficResetDay: 1, TrafficRXCorrection: -400,
		TrafficTXCorrection: 700, TrafficCorrectionPeriodStart: &period,
	}
	tracker := NewTrafficTracker()
	tracker.Load(map[string]model.MetricSample{
		"node-1": {
			BootID: "boot-a", NetRxBytes: 10_000, NetTxBytes: 5_000,
			MonthlyRxBytes: 1_000, MonthlyTxBytes: 500, ReceivedAt: now,
		},
	}, map[string]model.Node{"node-1": node})

	raw := tracker.Apply(node, model.MetricSample{
		BootID: "boot-a", NetRxBytes: 10_100, NetTxBytes: 5_050,
		ReceivedAt: now.Add(time.Minute),
	}, now.Add(time.Minute))
	if raw.MonthlyRxBytes != 1_100 || raw.MonthlyTxBytes != 550 {
		t.Fatalf("raw totals changed by correction: %d/%d", raw.MonthlyRxBytes, raw.MonthlyTxBytes)
	}
	corrected := tracker.Correct(node.ID, raw)
	if corrected.MonthlyRxBytes != 700 || corrected.MonthlyTxBytes != 1_250 {
		t.Fatalf("unexpected corrected totals: %d/%d", corrected.MonthlyRxBytes, corrected.MonthlyTxBytes)
	}
	restored := tracker.Uncorrect(node.ID, corrected)
	if restored.MonthlyRxBytes != raw.MonthlyRxBytes || restored.MonthlyTxBytes != raw.MonthlyTxBytes {
		t.Fatalf("correction was not reversible: %#v", restored)
	}

	nextPeriod := raw
	nextPeriod.ReceivedAt = time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC)
	nextPeriod.MonthlyRxBytes, nextPeriod.MonthlyTxBytes = 20, 10
	if got := tracker.Correct(node.ID, nextPeriod); got.MonthlyRxBytes != 20 || got.MonthlyTxBytes != 10 {
		t.Fatalf("old correction leaked into the next period: %#v", got)
	}
}

func TestTrafficTrackerSaturatesInsteadOfWrapping(t *testing.T) {
	maximum := ^uint64(0)
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	tracker := NewTrafficTracker()
	tracker.Load(map[string]model.MetricSample{
		"node-1": {BootID: "a", NetRxBytes: 100, NetTxBytes: 100, MonthlyRxBytes: maximum - 5, MonthlyTxBytes: maximum - 2, ReceivedAt: now},
	}, map[string]model.Node{"node-1": {ID: "node-1", TrafficResetDay: 1}})
	result := tracker.Apply(model.Node{ID: "node-1", TrafficResetDay: 1}, model.MetricSample{BootID: "a", NetRxBytes: 110, NetTxBytes: 110}, now.Add(time.Second))
	if result.MonthlyRxBytes != maximum || result.MonthlyTxBytes != maximum {
		t.Fatalf("traffic totals wrapped: %d/%d", result.MonthlyRxBytes, result.MonthlyTxBytes)
	}
}

func TestHubUnsubscribeIsSafeDuringPublish(t *testing.T) {
	hub := NewHub()
	for index := 0; index < 200; index++ {
		_, unsubscribe := hub.Subscribe(nil)
		done := make(chan struct{})
		go func() {
			hub.Publish(model.MetricSample{NodeID: "node", ReceivedAt: time.Now()}, time.Minute)
			close(done)
		}()
		unsubscribe()
		<-done
	}
}
