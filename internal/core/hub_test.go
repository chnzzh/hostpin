package core

import (
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestHubDistinguishesAllFromNoAllowedNodes(t *testing.T) {
	hub := NewHub()
	now := time.Now().UTC()
	hub.Load(map[string]model.MetricSample{
		"visible": {NodeID: "visible", ReceivedAt: now},
		"hidden":  {NodeID: "hidden", ReceivedAt: now},
	})
	if got := len(hub.Snapshot(nil)); got != 2 {
		t.Fatalf("nil allow-list returned %d samples, want all 2", got)
	}
	if got := len(hub.Snapshot([]string{})); got != 0 {
		t.Fatalf("empty allow-list returned %d samples, want none", got)
	}
	if snapshot := hub.Snapshot([]string{"visible"}); len(snapshot) != 1 || snapshot["visible"].NodeID != "visible" {
		t.Fatalf("explicit allow-list returned %#v", snapshot)
	}

	updates, unsubscribe := hub.Subscribe([]string{})
	defer unsubscribe()
	hub.Publish(model.MetricSample{NodeID: "hidden", ReceivedAt: now.Add(time.Second)}, time.Minute)
	select {
	case update := <-updates:
		t.Fatalf("empty allow-list leaked update for %s", update.NodeID)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHubResetPersistenceMakesNextSampleEligible(t *testing.T) {
	hub := NewHub()
	now := time.Now().UTC()
	if !hub.Publish(model.MetricSample{NodeID: "node-1", ReceivedAt: now}, time.Minute) {
		t.Fatal("first sample was not eligible for persistence")
	}
	if hub.Publish(model.MetricSample{NodeID: "node-1", ReceivedAt: now.Add(time.Second)}, time.Minute) {
		t.Fatal("sample inside persistence interval was unexpectedly eligible")
	}

	hub.ResetPersistence()
	if !hub.Publish(model.MetricSample{NodeID: "node-1", ReceivedAt: now.Add(2 * time.Second)}, time.Minute) {
		t.Fatal("first sample after persistence reset was not eligible")
	}
}

func TestHubReplaceLatestBroadcastsWithoutChangingPersistenceSchedule(t *testing.T) {
	hub := NewHub()
	now := time.Now().UTC()
	if !hub.Publish(model.MetricSample{NodeID: "node-1", ReceivedAt: now, MonthlyRxBytes: 100}, time.Minute) {
		t.Fatal("first sample was not eligible for persistence")
	}
	updates, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()
	hub.ReplaceLatest(model.MetricSample{NodeID: "node-1", ReceivedAt: now, MonthlyRxBytes: 500})
	select {
	case update := <-updates:
		if update.Sample == nil || update.Sample.MonthlyRxBytes != 500 {
			t.Fatalf("replacement broadcast was incorrect: %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement was not broadcast")
	}
	if latest, ok := hub.Latest("node-1"); !ok || latest.MonthlyRxBytes != 500 {
		t.Fatalf("replacement did not become latest: %#v", latest)
	}
	if hub.Publish(model.MetricSample{NodeID: "node-1", ReceivedAt: now.Add(30 * time.Second)}, time.Minute) {
		t.Fatal("replacement changed the existing persistence deadline")
	}
}
