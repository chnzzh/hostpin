package httpapi

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/alerting"
	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/core"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/notification"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/chnzzh/hostpin/internal/theme"
	"github.com/google/uuid"
)

func TestRecordSettingControlsHistoryWithoutStoppingLiveState(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(dataDir, "recording.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	now := time.Now().UTC()
	settings := model.DefaultSiteSettings()
	settings.RecordEnabled = false
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin-hash", settings); err != nil {
		t.Fatal(err)
	}
	record, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(),
			Metadata:  model.EnrollmentMetadata{Name: "record-toggle"},
			Config:    model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "record-token", TokenHash: "hash", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "record probe", Type: model.ProbeTCP, Target: "127.0.0.1:80",
		IntervalSeconds: 60, TimeoutSeconds: 3, NodeIDs: []string{record.Node.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := core.NewHub()
	persister := core.NewPersister(repository, logger, 32)
	persistCtx, cancelPersist := context.WithCancel(context.Background())
	persister.Start(persistCtx)
	defer func() {
		cancelPersist()
		persister.Stop()
	}()
	box, err := security.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	themes, err := theme.New(repository, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.PublicURL = "http://example.test"
	api := New(
		cfg, repository, hub, persister, box,
		alerting.New(repository, hub, cfg.PublicURL, time.Minute, logger),
		notification.New(repository, box, cfg.PublicURL, logger), themes, nil, nil, logger,
	)

	identity := model.AgentIdentity{Version: "test", Hostname: "record-host"}
	if persisted := api.acceptSample(ctx, record.Node, record.Config, identity, model.MetricSample{Sequence: 1, CPU: 10}, "", now); persisted {
		t.Fatal("metric was reported as persisted while recording was disabled")
	}
	api.acceptProbe(record.Node, model.ProbeResult{TaskID: task.ID, Success: true, LatencyMS: 2}, now)
	if latest, ok := hub.Latest(record.Node.ID); !ok || latest.Sequence != 1 {
		t.Fatalf("live state stopped while recording was disabled: %#v, %v", latest, ok)
	}
	updatedNode, err := repository.GetNode(ctx, record.Node.ID)
	if err != nil || updatedNode.LastSeenAt == nil || updatedNode.Hostname != identity.Hostname {
		t.Fatalf("agent presence was not refreshed while recording was disabled: %#v, %v", updatedNode, err)
	}
	assertStoredHistoryCount(t, repository, record.Node.ID, task.ID, now, 0, 0)

	api.setRecordEnabled(true)
	secondAt := now.Add(time.Second)
	if persisted := api.acceptSample(ctx, record.Node, record.Config, identity, model.MetricSample{Sequence: 2, CPU: 20}, "", secondAt); !persisted {
		t.Fatal("first metric after re-enabling recording was not persisted immediately")
	}
	api.acceptProbe(record.Node, model.ProbeResult{TaskID: task.ID, Success: true, LatencyMS: 3}, secondAt)
	waitForStoredHistory(t, repository, record.Node.ID, task.ID, now, 1, 1)
}

func assertStoredHistoryCount(t *testing.T, repository store.Store, nodeID string, taskID int64, now time.Time, wantMetrics, wantProbes int) {
	t.Helper()
	metrics, err := repository.History(context.Background(), store.HistoryQuery{
		NodeID: nodeID, Start: now.Add(-time.Hour), End: now.Add(time.Hour), MaxPoints: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	probes, err := repository.ProbeHistory(context.Background(), nodeID, taskID, now.Add(-time.Hour), now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != wantMetrics || len(probes) != wantProbes {
		t.Fatalf("stored history counts are metrics=%d probes=%d, want %d and %d", len(metrics), len(probes), wantMetrics, wantProbes)
	}
}

func waitForStoredHistory(t *testing.T, repository store.Store, nodeID string, taskID int64, now time.Time, wantMetrics, wantProbes int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		metrics, metricErr := repository.History(context.Background(), store.HistoryQuery{
			NodeID: nodeID, Start: now.Add(-time.Hour), End: now.Add(time.Hour), MaxPoints: 100,
		})
		probes, probeErr := repository.ProbeHistory(context.Background(), nodeID, taskID, now.Add(-time.Hour), now.Add(time.Hour), 100)
		if metricErr == nil && probeErr == nil && len(metrics) == wantMetrics && len(probes) == wantProbes {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertStoredHistoryCount(t, repository, nodeID, taskID, now, wantMetrics, wantProbes)
}
