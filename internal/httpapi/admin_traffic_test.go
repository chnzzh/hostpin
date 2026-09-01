package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/core"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestTrafficCorrectionUpdatesLiveViewAndKeepsDurableRawTotals(t *testing.T) {
	ctx := context.Background()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "traffic-correction.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	record, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Metadata: model.EnrollmentMetadata{Name: "traffic-node"},
			Config: model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "traffic-token", TokenHash: "hash", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := model.MetricSample{
		NodeID: record.Node.ID, BootID: "boot-a", NetRxBytes: 1_600, NetTxBytes: 800,
		MonthlyRxBytes: 600, MonthlyTxBytes: 300, CollectedAt: now, ReceivedAt: now,
	}
	if err := repository.SaveMetric(ctx, raw); err != nil {
		t.Fatal(err)
	}
	hub := core.NewHub()
	hub.Load(map[string]model.MetricSample{record.Node.ID: raw})
	tracker := core.NewTrafficTracker()
	tracker.Load(map[string]model.MetricSample{record.Node.ID: raw}, map[string]model.Node{record.Node.ID: record.Node})
	api := &API{store: repository, hub: hub, traffic: tracker}

	put := trafficRequest(t, http.MethodPut, record.Node.ID, []byte(`{"rx_bytes":900,"tx_bytes":450}`), admin)
	response := httptest.NewRecorder()
	api.handleAdminSaveTrafficCorrection(response, put)
	if response.Code != http.StatusOK {
		t.Fatalf("traffic correction failed: HTTP %d %s", response.Code, response.Body.String())
	}
	var payload model.Envelope[trafficCorrectionStatus]
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Data.Active || payload.Data.RawRXBytes != 600 || payload.Data.RXBytes != 900 || payload.Data.TXBytes != 450 {
		t.Fatalf("unexpected correction response: %#v", payload.Data)
	}
	latest, ok := hub.Latest(record.Node.ID)
	if !ok || latest.MonthlyRxBytes != 900 || latest.MonthlyTxBytes != 450 {
		t.Fatalf("live view was not corrected: %#v", latest)
	}
	durable, err := repository.LatestMetric(ctx, record.Node.ID)
	if err != nil || durable.MonthlyRxBytes != 600 || durable.MonthlyTxBytes != 300 {
		t.Fatalf("durable raw totals were modified: %#v %v", durable, err)
	}

	nextRaw := tracker.Apply(record.Node, model.MetricSample{
		NodeID: record.Node.ID, BootID: "boot-a", NetRxBytes: 1_700, NetTxBytes: 850,
		CollectedAt: now.Add(time.Second), ReceivedAt: now.Add(time.Second),
	}, now.Add(time.Second))
	nextDisplayed := tracker.Correct(record.Node.ID, nextRaw)
	if nextRaw.MonthlyRxBytes != 700 || nextRaw.MonthlyTxBytes != 350 || nextDisplayed.MonthlyRxBytes != 1_000 || nextDisplayed.MonthlyTxBytes != 500 {
		t.Fatalf("post-correction traffic did not continue accumulating: raw=%#v displayed=%#v", nextRaw, nextDisplayed)
	}
	hub.ReplaceLatest(nextDisplayed)

	clear := trafficRequest(t, http.MethodDelete, record.Node.ID, nil, admin)
	clearResponse := httptest.NewRecorder()
	api.handleAdminClearTrafficCorrection(clearResponse, clear)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clearing traffic correction failed: HTTP %d %s", clearResponse.Code, clearResponse.Body.String())
	}
	latest, ok = hub.Latest(record.Node.ID)
	if !ok || latest.MonthlyRxBytes != 700 || latest.MonthlyTxBytes != 350 {
		t.Fatalf("clearing did not restore live raw totals: %#v", latest)
	}
	updatedNode, err := repository.GetNode(ctx, record.Node.ID)
	if err != nil || updatedNode.TrafficCorrectionPeriodStart != nil || updatedNode.TrafficRXCorrection != 0 || updatedNode.TrafficTXCorrection != 0 {
		t.Fatalf("cleared correction remained in storage: %#v %v", updatedNode, err)
	}
}

func trafficRequest(t *testing.T, method, nodeID string, body []byte, admin store.Admin) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1/admin/nodes/"+nodeID+"/traffic-correction", bytes.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", nodeID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, adminContextKey{}, admin)
	return request.WithContext(ctx)
}
