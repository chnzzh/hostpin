package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
)

type deliveryUpdate struct {
	id       string
	status   string
	attempts int
	next     time.Time
	error    string
}

type deliveryRepository struct {
	deliveries []model.NotificationDelivery
	updates    []deliveryUpdate
}

func (r *deliveryRepository) DueNotificationDeliveries(context.Context, time.Time, int) ([]model.NotificationDelivery, error) {
	return append([]model.NotificationDelivery(nil), r.deliveries...), nil
}

func (r *deliveryRepository) UpdateNotificationDelivery(_ context.Context, id, status string, attempts int, next time.Time, message string) error {
	r.updates = append(r.updates, deliveryUpdate{id: id, status: status, attempts: attempts, next: next, error: message})
	return nil
}

func TestWebhookSignature(t *testing.T) {
	secret := "test-webhook-secret"
	received := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			received <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, key := range []string{"event_id", "type", "status", "severity", "occurred_at", "node", "metric", "value", "threshold", "link"} {
			if _, exists := payload[key]; !exists {
				received <- io.ErrUnexpectedEOF
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		wanted := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(r.Header.Get("X-Hostpin-Signature")), []byte(wanted)) {
			received <- io.ErrUnexpectedEOF
		} else {
			received <- nil
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	box, _ := security.NewSecretBox(make([]byte, 32))
	config, _ := json.Marshal(map[string]any{"url": server.URL, "secret": secret})
	encrypted, _ := box.Seal(string(config))
	dispatcher := New(nil, box, server.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	event := model.AlertEvent{ID: "event", Type: "cpu", Status: model.AlertFiring, Severity: "warning", OccurredAt: time.Now(), Node: model.PublicNode{ID: "node", Name: "node"}, Message: "CPU high"}
	if err := dispatcher.Send(context.Background(), model.NotificationChannel{Type: "webhook", EncryptedConfig: encrypted}, event); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		t.Fatal("webhook signature did not match request body")
	}
}

func TestDurableDeliveryRetryScheduleAndTerminalState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	box, err := security.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	channel := func(target string) model.NotificationChannel {
		payload, _ := json.Marshal(map[string]any{"url": target})
		encrypted, sealErr := box.Seal(string(payload))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return model.NotificationChannel{Type: "webhook", Name: "test", EncryptedConfig: encrypted}
	}
	event := model.AlertEvent{ID: "event", Status: model.AlertFiring, Severity: "warning", Node: model.PublicNode{ID: "node", Name: "node"}}
	repository := &deliveryRepository{deliveries: []model.NotificationDelivery{
		{ID: "ok", Event: event, Channel: channel(server.URL + "/ok"), Attempts: 0},
		{ID: "retry-1", Event: event, Channel: channel(server.URL + "/fail"), Attempts: 0},
		{ID: "retry-5", Event: event, Channel: channel(server.URL + "/fail"), Attempts: 1},
		{ID: "retry-15", Event: event, Channel: channel(server.URL + "/fail"), Attempts: 2},
		{ID: "terminal", Event: event, Channel: channel(server.URL + "/fail"), Attempts: 3},
	}}
	dispatcher := New(repository, box, "https://monitor.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := time.Now().UTC()
	dispatcher.process(context.Background())
	finished := time.Now().UTC()

	if len(repository.updates) != len(repository.deliveries) {
		t.Fatalf("updated %d deliveries, want %d", len(repository.updates), len(repository.deliveries))
	}
	byID := make(map[string]deliveryUpdate, len(repository.updates))
	for _, update := range repository.updates {
		byID[update.id] = update
	}
	if update := byID["ok"]; update.status != "sent" || update.attempts != 1 || update.error != "" {
		t.Fatalf("successful delivery transition is incorrect: %#v", update)
	}
	for _, test := range []struct {
		id       string
		attempts int
		delay    time.Duration
	}{
		{"retry-1", 1, time.Minute},
		{"retry-5", 2, 5 * time.Minute},
		{"retry-15", 3, 15 * time.Minute},
	} {
		update := byID[test.id]
		if update.status != "retry" || update.attempts != test.attempts || update.error == "" {
			t.Fatalf("retry transition for %s is incorrect: %#v", test.id, update)
		}
		earliest, latest := started.Add(test.delay), finished.Add(test.delay)
		if update.next.Before(earliest) || update.next.After(latest) {
			t.Fatalf("retry %s scheduled at %s, want between %s and %s", test.id, update.next, earliest, latest)
		}
	}
	if update := byID["terminal"]; update.status != "failed" || update.attempts != 4 || update.error == "" {
		t.Fatalf("terminal delivery transition is incorrect: %#v", update)
	}
}

func TestTelegramAndBarkDeliveryContracts(t *testing.T) {
	type receivedRequest struct {
		path        string
		contentType string
		body        []byte
	}
	received := make(chan receivedRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- receivedRequest{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	box, err := security.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := New(nil, box, "https://monitor.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	event := model.AlertEvent{
		ID: "event", Status: model.AlertFiring, Severity: "critical", OccurredAt: time.Now().UTC(),
		Node: model.PublicNode{ID: "node", Name: "edge"}, Message: "CPU high", Link: "https://monitor.example/nodes/node",
	}
	channel := func(channelType string, config map[string]any) model.NotificationChannel {
		payload, _ := json.Marshal(config)
		encrypted, sealErr := box.Seal(string(payload))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return model.NotificationChannel{Type: channelType, EncryptedConfig: encrypted}
	}

	if err := dispatcher.Send(context.Background(), channel("telegram", map[string]any{
		"api_base": server.URL, "bot_token": "123456:test-token", "chat_id": "-1001",
	}), event); err != nil {
		t.Fatal(err)
	}
	telegram := <-received
	telegramForm, err := url.ParseQuery(string(telegram.body))
	if err != nil || telegram.path != "/bot123456:test-token/sendMessage" || telegramForm.Get("chat_id") != "-1001" || !strings.Contains(telegramForm.Get("text"), "CPU high") {
		t.Fatalf("Telegram request is incompatible: path=%q form=%v err=%v", telegram.path, telegramForm, err)
	}

	if err := dispatcher.Send(context.Background(), channel("bark", map[string]any{
		"endpoint": server.URL, "device_key": "device-key", "group": "Operations",
	}), event); err != nil {
		t.Fatal(err)
	}
	bark := <-received
	var barkPayload map[string]any
	if err := json.Unmarshal(bark.body, &barkPayload); err != nil || bark.path != "/push" || barkPayload["device_key"] != "device-key" || barkPayload["group"] != "Operations" || barkPayload["url"] != event.Link {
		t.Fatalf("Bark request is incompatible: path=%q payload=%v err=%v", bark.path, barkPayload, err)
	}
}
