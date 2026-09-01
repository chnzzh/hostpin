package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
)

func TestSQLiteAlertSnapshotMigrationPreservesDeliveries(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "upgrade.db")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := store.Migrations("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 4 {
			break
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(migration.SQL, "-- hostpin:split") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				transaction.Rollback()
				t.Fatalf("apply pre-v4 migration %s: %v", migration.Name, err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, migration.Version, time.Now().UnixMilli()); err != nil {
			transaction.Rollback()
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `INSERT INTO nodes(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)`, "legacy-node", "Legacy node", now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO notification_channels(id, name, type, config_enc, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, 1, "legacy channel", "webhook", "encrypted", 1, now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO alert_events(id, node_id, event_type, status, severity, message, occurred_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, "legacy-event", "legacy-node", "cpu", "firing", "warning", "legacy event", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO notification_deliveries(id, event_id, channel_id, status, next_attempt_at, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, "legacy-delivery", "legacy-event", 1, "pending", now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	events, err := repository.ListAlertEvents(ctx, 10)
	if err != nil || len(events) != 1 || events[0].ID != "legacy-event" || events[0].Node.Name != "Legacy node" {
		t.Fatalf("legacy event was not preserved: %#v %v", events, err)
	}
	deliveries, err := repository.DueNotificationDeliveries(ctx, now.Add(time.Second), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != "legacy-delivery" || deliveries[0].Event.Node.Name != "Legacy node" {
		t.Fatalf("legacy notification delivery was not preserved: %#v %v", deliveries, err)
	}
	systemEvent := model.AlertEvent{
		ID: "system-event", Type: "security.enrollment", Status: model.AlertFiring,
		Severity: "critical", OccurredAt: now, Node: model.PublicNode{ID: "system", Name: "Hostpin control plane"},
		Message: "enrollment paused",
	}
	if err := repository.SaveAlertEvent(ctx, systemEvent, nil); err != nil {
		t.Fatalf("post-upgrade system event failed: %v", err)
	}
	var violations int
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations after migration: %d %v", violations, err)
	}
}

func TestSQLiteMonitorLatencyMigrationBackfillsProbeNodes(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "monitor-latency-upgrade.db")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := store.Migrations("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 9 {
			break
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(migration.SQL, "-- hostpin:split") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				transaction.Rollback()
				t.Fatalf("apply pre-v9 migration %s: %v", migration.Name, err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, migration.Version, time.Now().UnixMilli()); err != nil {
			transaction.Rollback()
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := database.ExecContext(ctx, `INSERT INTO nodes(id, role, name, created_at, updated_at) VALUES(?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		"legacy-monitor", model.NodeRoleMonitor, "Legacy monitor", now, now,
		"legacy-probe", model.NodeRoleProbe, "Legacy probe", now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	monitor, err := repository.GetNode(ctx, "legacy-monitor")
	if err != nil || monitor.LatencyEnabled {
		t.Fatalf("legacy monitor was unexpectedly enabled: %#v %v", monitor, err)
	}
	probeNode, err := repository.GetNode(ctx, "legacy-probe")
	if err != nil || !probeNode.LatencyEnabled || !probeNode.CanMeasureLatency() {
		t.Fatalf("legacy Probe Node was not backfilled: %#v %v", probeNode, err)
	}
}

func TestSQLiteEnrollmentMetricsAndAccess(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "hostpin.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "test", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin-hash", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	assertSystemAlertRoundTrip(t, repository, now)
	rules, err := repository.ListAlertRules(ctx)
	if err != nil || len(rules) < 4 {
		t.Fatalf("default alert rules missing: %v (%d)", err, len(rules))
	}
	token, tokenID, tokenHash, _ := security.NewAgentToken()
	request := model.EnrollmentRequest{
		InstallID: uuid.NewString(), Token: token,
		Identity: model.AgentIdentity{Hostname: "edge-1", OS: "linux", Arch: "amd64"},
		Metadata: model.EnrollmentMetadata{Name: "Edge One", Tags: []string{"edge"}}, Config: model.DefaultAgentConfig(),
	}
	params := store.EnrollParams{Request: request, NodeID: uuid.NewString(), TokenID: tokenID, TokenHash: tokenHash, SourceIP: "192.0.2.1", Now: now}
	enrolled, err := repository.EnrollNode(ctx, params)
	if err != nil || !enrolled.Created {
		t.Fatalf("enrollment failed: %v", err)
	}
	if enrolled.Node.SourceIP != params.SourceIP || enrolled.Node.LocationManual {
		t.Fatalf("location source metadata did not round-trip: %#v", enrolled.Node)
	}
	correctionPeriod, correctionUpdated := now, now.Add(time.Minute)
	if err := repository.UpdateTrafficCorrection(ctx, enrolled.Node.ID, -250, 500, &correctionPeriod, correctionUpdated); err != nil {
		t.Fatalf("traffic correction update failed: %v", err)
	}
	correctedNode, err := repository.GetNode(ctx, enrolled.Node.ID)
	if err != nil || correctedNode.TrafficRXCorrection != -250 || correctedNode.TrafficTXCorrection != 500 ||
		correctedNode.TrafficCorrectionPeriodStart == nil || !correctedNode.TrafficCorrectionPeriodStart.Equal(correctionPeriod) ||
		correctedNode.TrafficCorrectionUpdatedAt == nil || !correctedNode.TrafficCorrectionUpdatedAt.Equal(correctionUpdated) {
		t.Fatalf("traffic correction did not round-trip: %#v %v", correctedNode, err)
	}
	retry, err := repository.EnrollNode(ctx, params)
	if err != nil || retry.Created || retry.Node.ID != enrolled.Node.ID {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	_, _, otherHash, _ := security.NewAgentToken()
	params.TokenHash = otherHash
	if _, err := repository.EnrollNode(ctx, params); !errors.Is(err, store.ErrInstallConflict) {
		t.Fatalf("credential takeover was not rejected: %v", err)
	}
	if _, _, err := repository.AuthenticateAgent(ctx, tokenID, tokenHash); err != nil {
		t.Fatalf("agent authentication failed: %v", err)
	}
	cfg := enrolled.Config
	cfg.EnableGPU = true
	if err := repository.SaveAgentConfig(ctx, enrolled.Node.ID, cfg); err != nil {
		t.Fatalf("Agent configuration update failed: %v", err)
	}
	updatedConfig, err := repository.AgentConfig(ctx, enrolled.Node.ID)
	if err != nil || !updatedConfig.EnableGPU || updatedConfig.ConfigVersion != enrolled.Config.ConfigVersion+1 {
		t.Fatalf("Agent configuration version did not advance: %v %#v", err, updatedConfig)
	}
	for index := 0; index < 12; index++ {
		sample := model.MetricSample{
			NodeID: enrolled.Node.ID, Sequence: uint64(index + 1),
			ReceivedAt: now.Add(time.Duration(index) * time.Minute), CollectedAt: now.Add(time.Duration(index) * time.Minute),
			CPU: float64(index), MemoryTotal: 1000, MemoryUsed: uint64(index * 10),
			NetRxBytes: 10_000 + uint64(index*100), NetTxBytes: 5_000 + uint64(index*50),
			MonthlyRxBytes: uint64(index * 100), MonthlyTxBytes: uint64(index * 50),
		}
		if err := repository.SaveMetric(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	history, err := repository.History(ctx, store.HistoryQuery{NodeID: enrolled.Node.ID, Start: now.Add(-time.Minute), End: now.Add(time.Hour), MaxPoints: 5})
	if err != nil || len(history) != 5 || history[len(history)-1].Sequence != 12 || history[len(history)-1].MonthlyRxBytes != 1_100 || history[len(history)-1].MonthlyTxBytes != 550 {
		t.Fatalf("history query/downsample failed: %v %#v", err, history)
	}
	if err := repository.Rollup(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("traffic rollup failed: %v", err)
	}
	for _, table := range []string{"metrics_5m", "metrics_1h"} {
		var monthlyRX, monthlyTX int64
		if err := repository.db.QueryRowContext(ctx, `SELECT MAX(monthly_rx_bytes), MAX(monthly_tx_bytes) FROM `+table+` WHERE node_id = ?`, enrolled.Node.ID).Scan(&monthlyRX, &monthlyTX); err != nil || monthlyRX != 1_100 || monthlyTX != 550 {
			t.Fatalf("%s traffic rollup=%d/%d: %v", table, monthlyRX, monthlyTX, err)
		}
	}
	probe, err := repository.SaveProbeTask(ctx, model.ProbeTask{Name: "edge health", Type: model.ProbeTCP, Target: "127.0.0.1:443", IntervalSeconds: 30, TimeoutSeconds: 3, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if err := repository.SaveProbeResult(ctx, model.ProbeResult{NodeID: enrolled.Node.ID, TaskID: probe.ID, ReceivedAt: now.Add(time.Duration(index) * time.Minute), CollectedAt: now.Add(time.Duration(index) * time.Minute), Success: true, LatencyMS: float64(index)}); err != nil {
			t.Fatal(err)
		}
	}
	probeHistory, err := repository.ProbeHistory(ctx, enrolled.Node.ID, probe.ID, now.Add(-time.Minute), now.Add(time.Hour), 2)
	if err != nil || len(probeHistory) != 2 || probeHistory[0].LatencyMS != 2 || probeHistory[1].LatencyMS != 3 {
		t.Fatalf("probe history did not retain the latest ordered points: %v %#v", err, probeHistory)
	}
	shareToken, shareHash, _ := security.NewShareToken()
	link := model.ShareLink{ID: uuid.NewString(), NodeIDs: []string{enrolled.Node.ID}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := repository.CreateShareLink(ctx, store.ShareLinkRecord{Link: link, TokenHash: shareHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveShareLink(ctx, security.HashToken(shareToken), now.Add(time.Minute)); err != nil {
		t.Fatalf("share link did not resolve: %v", err)
	}
	if _, err := repository.ResolveShareLink(ctx, security.HashToken(shareToken), now.Add(2*time.Hour)); !errors.Is(err, store.ErrUnauthorized) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired share link was accepted: %v", err)
	}
	apiToken, apiTokenID, apiHash, _ := security.NewAPIKeyToken()
	key := model.APIKey{ID: uuid.NewString(), Name: "test", Scopes: []string{"admin"}, CreatedAt: now}
	if err := repository.CreateAPIKey(ctx, store.APIKeyRecord{Key: key, AdminID: admin.ID, TokenID: apiTokenID, TokenHash: apiHash}); err != nil {
		t.Fatal(err)
	}
	parsedID, parsedHash, _ := security.ParseAPIKeyToken(apiToken)
	if _, _, err := repository.AuthenticateAPIKey(ctx, parsedID, parsedHash, now); err != nil {
		t.Fatalf("API key authentication failed: %v", err)
	}
}

func TestSQLiteLatencyProbeRolesDispatchAndHistory(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "latency.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "test", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin-hash", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}

	enroll := func(role model.NodeRole, name string) (store.EnrollmentRecord, store.EnrollParams) {
		t.Helper()
		_, tokenID, tokenHash, tokenErr := security.NewAgentToken()
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		params := store.EnrollParams{
			Request: model.EnrollmentRequest{
				InstallID: uuid.NewString(), Role: role,
				Identity: model.AgentIdentity{Hostname: name, OS: "linux", Arch: "arm64"},
				Metadata: model.EnrollmentMetadata{Name: name}, Config: model.DefaultAgentConfig(),
			},
			NodeID: uuid.NewString(), TokenID: tokenID, TokenHash: tokenHash,
			SourceIP: "192.168.8.1", Now: now,
		}
		record, enrollErr := repository.EnrollNode(ctx, params)
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		return record, params
	}
	monitor, _ := enroll(model.NodeRoleMonitor, "target-server")
	probeNode, probeParams := enroll(model.NodeRoleProbe, "home-router")
	if monitor.Node.Role != model.NodeRoleMonitor || monitor.Node.LatencyEnabled ||
		probeNode.Node.Role != model.NodeRoleProbe || !probeNode.Node.LatencyEnabled {
		t.Fatalf("roles did not round-trip: monitor=%q probe=%q", monitor.Node.Role, probeNode.Node.Role)
	}
	monitors, err := repository.ListNodes(ctx, true)
	if err != nil || len(monitors) != 1 || monitors[0].ID != monitor.Node.ID {
		t.Fatalf("monitor inventory was not role-scoped: %#v %v", monitors, err)
	}
	probes, err := repository.ListLatencyNodes(ctx, true)
	if err != nil || len(probes) != 1 || probes[0].ID != probeNode.Node.ID {
		t.Fatalf("Probe Node inventory was not role-scoped: %#v %v", probes, err)
	}
	probeParams.Request.Role = model.NodeRoleMonitor
	if _, err := repository.EnrollNode(ctx, probeParams); !errors.Is(err, store.ErrInstallConflict) {
		t.Fatalf("an existing Probe Node identity changed roles: %v", err)
	}

	custom, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "service check", Type: model.ProbeTCP, Target: "127.0.0.1:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "custom",
		RunOn: model.NodeRoleMonitor, Samples: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	latency, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "target-server", Type: model.ProbeICMP, Target: "203.0.113.40",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency",
		RunOn: model.NodeRoleProbe, TargetNodeID: monitor.Node.ID,
		Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	monitorTasks, err := repository.ListProbeTasks(ctx, monitor.Node.ID)
	if err != nil || len(monitorTasks) != 1 || monitorTasks[0].ID != custom.ID {
		t.Fatalf("monitor received the wrong tasks: %#v %v", monitorTasks, err)
	}
	monitor.Node.LatencyEnabled = true
	if err := repository.UpdateNode(ctx, monitor.Node); err != nil {
		t.Fatalf("enabling monitor latency failed: %v", err)
	}
	measurementNodes, err := repository.ListLatencyNodes(ctx, true)
	if err != nil || len(measurementNodes) != 2 {
		t.Fatalf("latency inventory omitted the enabled monitor: %#v %v", measurementNodes, err)
	}
	monitorTasks, err = repository.ListProbeTasks(ctx, monitor.Node.ID)
	if err != nil || len(monitorTasks) != 2 || monitorTasks[0].ID != custom.ID || monitorTasks[1].ID != latency.ID {
		t.Fatalf("latency-enabled monitor received the wrong tasks: %#v %v", monitorTasks, err)
	}
	probeTasks, err := repository.ListProbeTasks(ctx, probeNode.Node.ID)
	if err != nil || len(probeTasks) != 1 || probeTasks[0].ID != latency.ID || probeTasks[0].Samples != 3 {
		t.Fatalf("Probe Node received the wrong tasks: %#v %v", probeTasks, err)
	}
	if _, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "duplicate", Type: model.ProbeTCP, Target: "203.0.113.40:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency",
		RunOn: model.NodeRoleProbe, TargetNodeID: monitor.Node.ID, Samples: 3, Enabled: true,
	}); err == nil {
		t.Fatal("duplicate latency target was accepted")
	}

	for index, loss := range []float64{33.333, 0} {
		at := now.Add(time.Duration(index) * time.Minute)
		if err := repository.SaveProbeResult(ctx, model.ProbeResult{
			NodeID: probeNode.Node.ID, TaskID: latency.ID, CollectedAt: at,
			ReceivedAt: at, Success: true, LatencyMS: 17.5 + float64(index), LossPercent: loss,
		}); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := repository.LatestLatencyResults(ctx, now.Add(-time.Minute))
	if err != nil || len(latest) != 1 || latest[0].ProbeNodeID != probeNode.Node.ID || latest[0].TargetNodeID != monitor.Node.ID || latest[0].LatencyMS != 18.5 {
		t.Fatalf("latest latency result was incorrect: %#v %v", latest, err)
	}
	history, err := repository.LatencyHistory(ctx, probeNode.Node.ID, monitor.Node.ID, now.Add(-time.Minute), now.Add(time.Hour), 100)
	if err != nil || len(history) != 2 || history[0].LossPercent != 33.333 || history[1].LossPercent != 0 {
		t.Fatalf("latency history was incorrect: %#v %v", history, err)
	}
	failedAt := now.Add(2 * time.Minute)
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: probeNode.Node.ID, TaskID: latency.ID, CollectedAt: failedAt,
		ReceivedAt: failedAt, Success: false, LatencyMS: -1, LossPercent: 100,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := repository.LatencyWindowSummary(ctx, probeNode.Node.ID, monitor.Node.ID, now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil || summary.SampleCount != 3 || summary.SuccessCount != 2 || summary.AverageLatencyMS != 18 || math.Abs(summary.AverageLossPercent-44.44433333333333) > 0.000001 {
		t.Fatalf("latency window summary was incorrect: %#v %v", summary, err)
	}
	monitorResultAt := now.Add(3 * time.Minute)
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: monitor.Node.ID, TaskID: latency.ID, CollectedAt: monitorResultAt,
		ReceivedAt: monitorResultAt, Success: true, LatencyMS: 0.25, LossPercent: 0,
	}); err != nil {
		t.Fatalf("latency-enabled monitor result failed: %v", err)
	}
	monitor.Node.LatencyEnabled = false
	if err := repository.UpdateNode(ctx, monitor.Node); err != nil {
		t.Fatalf("disabling monitor latency failed: %v", err)
	}
	measurementNodes, err = repository.ListLatencyNodes(ctx, true)
	if err != nil || len(measurementNodes) != 1 || measurementNodes[0].ID != probeNode.Node.ID {
		t.Fatalf("disabled monitor remained in latency inventory: %#v %v", measurementNodes, err)
	}
	monitorTasks, err = repository.ListProbeTasks(ctx, monitor.Node.ID)
	if err != nil || len(monitorTasks) != 1 || monitorTasks[0].ID != custom.ID {
		t.Fatalf("disabled monitor retained latency tasks: %#v %v", monitorTasks, err)
	}
	monitorHistory, err := repository.LatencyHistory(ctx, monitor.Node.ID, monitor.Node.ID, now, now.Add(time.Hour), 100)
	if err != nil || len(monitorHistory) != 1 || monitorHistory[0].LatencyMS != 0.25 {
		t.Fatalf("disabling monitor latency removed its history: %#v %v", monitorHistory, err)
	}
	if err := repository.DeleteNode(ctx, monitor.Node.ID); err != nil {
		t.Fatal(err)
	}
	allTasks, err := repository.ListAllProbeTasks(ctx)
	if err != nil || len(allTasks) != 1 || allTasks[0].ID != custom.ID {
		t.Fatalf("deleting the target did not cascade its latency task: %#v %v", allTasks, err)
	}
}
