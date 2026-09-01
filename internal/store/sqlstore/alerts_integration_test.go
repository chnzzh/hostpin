package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
)

func assertSystemAlertRoundTrip(t *testing.T, repository store.Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	event := model.AlertEvent{
		ID: uuid.NewString(), Type: "security.enrollment", Status: model.AlertFiring,
		Severity: "critical", OccurredAt: now,
		Node:   model.PublicNode{ID: "system", Name: "Hostpin control plane"},
		Metric: "failed_enrollments", Value: 100, Threshold: 100,
		Message: "Enrollment paused after abnormal PIN failures", Link: "https://monitor.example.test/admin/audit",
	}
	if err := repository.SaveAlertEvent(ctx, event, nil); err != nil {
		t.Fatalf("save system-level alert event: %v", err)
	}
	channel, err := repository.SaveNotificationChannel(ctx, model.NotificationChannel{
		Name: "system alert sink", Type: "webhook", Enabled: true,
	}, "encrypted-test-config")
	if err != nil {
		t.Fatalf("save notification channel: %v", err)
	}
	if channel.ID == 0 {
		t.Fatal("notification channel ID was not assigned")
	}
	if err := repository.EnqueueNotificationDeliveries(ctx, event.ID, now); err != nil {
		t.Fatalf("enqueue system alert notification: %v", err)
	}

	events, err := repository.ListAlertEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != event.ID || events[0].Node.ID != "system" || events[0].Node.Name != "Hostpin control plane" || events[0].Link != event.Link {
		t.Fatalf("system alert snapshot did not round-trip: %#v", events)
	}
	deliveries, err := repository.DueNotificationDeliveries(ctx, now.Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Event.ID != event.ID || deliveries[0].Event.Node.Name != "Hostpin control plane" || deliveries[0].Event.Link != event.Link {
		t.Fatalf("system alert notification did not retain its subject: %#v", deliveries)
	}
}
