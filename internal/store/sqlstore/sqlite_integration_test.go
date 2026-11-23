package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
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

func TestSQLiteReadWritePoolSplitMemoryModeAndIndexes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pool-split.db")
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	if !repository.sqliteFile || repository.readDB == nil || repository.readDB == repository.db {
		t.Fatalf("expected separate reader pool for file-backed SQLite: sqliteFile=%v samePool=%v", repository.sqliteFile, repository.readDB == repository.db)
	}
	if got := repository.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := repository.readDB.Stats().MaxOpenConnections; got != 6 {
		t.Fatalf("reader MaxOpenConnections = %d, want 6", got)
	}
	if _, err := repository.readDB.ExecContext(ctx, `CREATE TABLE should_fail (id INTEGER)`); err == nil {
		t.Fatal("expected readDB to reject write statements")
	}

	indexRows, err := repository.readDB.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'probe_results'`)
	if err != nil {
		t.Fatal(err)
	}
	indexes := make(map[string]bool)
	for indexRows.Next() {
		var name string
		if err := indexRows.Scan(&name); err != nil {
			indexRows.Close()
			t.Fatal(err)
		}
		indexes[name] = true
	}
	indexRows.Close()
	if indexes["idx_probe_results_lookup"] {
		t.Fatal("redundant idx_probe_results_lookup was not dropped by migration 010")
	}
	if !indexes["idx_probe_results_ts"] || !indexes["idx_probe_results_summary"] {
		t.Fatalf("expected probe_results performance indexes, got %#v", indexes)
	}

	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := repository.SetupComplete(ctx); err != nil {
					errCh <- err
					return
				}
				if _, err := repository.SiteSettings(ctx); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for readErr := range errCh {
		t.Fatalf("concurrent reader pool query failed: %v", readErr)
	}

	memRepo, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer memRepo.Close()
	if memRepo.sqliteFile || memRepo.readDB != memRepo.db {
		t.Fatalf("expected in-memory SQLite to share single pool: sqliteFile=%v samePool=%v", memRepo.sqliteFile, memRepo.readDB == memRepo.db)
	}
	if err := memRepo.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatalf("in-memory initialize failed: %v", err)
	}
	complete, err := memRepo.SetupComplete(ctx)
	if err != nil || !complete {
		t.Fatalf("in-memory SetupComplete = %v, %v", complete, err)
	}
}

func TestSQLiteBatchPersistenceRollupAndRetention(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "batch-rollup-retention.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	enroll := func(role model.NodeRole, name, tokenID string) model.Node {
		t.Helper()
		record, err := repository.EnrollNode(ctx, store.EnrollParams{
			Request: model.EnrollmentRequest{
				InstallID: uuid.NewString(), Role: role,
				Identity: model.AgentIdentity{Hostname: name, OS: "linux", Arch: "amd64"},
				Metadata: model.EnrollmentMetadata{Name: name}, Config: model.DefaultAgentConfig(),
			},
			NodeID: uuid.NewString(), TokenID: tokenID, TokenHash: "hash-" + tokenID, Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return record.Node
	}

	targetA := enroll(model.NodeRoleMonitor, "target-a", "tok-a")
	targetB := enroll(model.NodeRoleMonitor, "target-b", "tok-b")
	probe1 := enroll(model.NodeRoleProbe, "probe-1", "tok-p1")
	probe2 := enroll(model.NodeRoleProbe, "probe-2", "tok-p2")

	if err := repository.SaveMetricsBatch(ctx, nil); err != nil {
		t.Fatalf("empty SaveMetricsBatch failed: %v", err)
	}
	if err := repository.SaveProbeResultsBatch(ctx, nil); err != nil {
		t.Fatalf("empty SaveProbeResultsBatch failed: %v", err)
	}

	batchSamples := []model.MetricSample{
		{NodeID: targetA.ID, Sequence: 1, ReceivedAt: now, CollectedAt: now, CPU: 10, Disks: []model.DiskMetric{{Mountpoint: "/", Total: 100, Used: 40}}, Message: "initial"},
		{NodeID: targetA.ID, Sequence: 2, ReceivedAt: now, CollectedAt: now, CPU: 20, Disks: []model.DiskMetric{{Mountpoint: "/", Total: 100, Used: 50}}, Message: "updated"},
		{NodeID: targetB.ID, Sequence: 1, ReceivedAt: now.Add(time.Minute), CollectedAt: now.Add(time.Minute), CPU: 30},
	}
	if err := repository.SaveMetricsBatch(ctx, batchSamples); err != nil {
		t.Fatalf("SaveMetricsBatch failed: %v", err)
	}
	latestA, err := repository.LatestMetric(ctx, targetA.ID)
	if err != nil || latestA.Sequence != 2 || latestA.CPU != 20 {
		t.Fatalf("duplicate key upsert in SaveMetricsBatch failed: %#v %v", latestA, err)
	}

	mixedSamples := []model.MetricSample{
		{NodeID: targetA.ID, Sequence: 3, ReceivedAt: now.Add(2 * time.Minute), CollectedAt: now.Add(2 * time.Minute), CPU: 40},
		{NodeID: "missing-node-id", Sequence: 1, ReceivedAt: now.Add(2 * time.Minute), CollectedAt: now.Add(2 * time.Minute), CPU: 99},
		{NodeID: targetB.ID, Sequence: 2, ReceivedAt: now.Add(2 * time.Minute), CollectedAt: now.Add(2 * time.Minute), CPU: 50},
	}
	if err := repository.SaveMetricsBatch(ctx, mixedSamples); err == nil {
		t.Fatal("expected SaveMetricsBatch to return foreign key error for missing node")
	}
	latestA, err = repository.LatestMetric(ctx, targetA.ID)
	if err != nil || latestA.Sequence != 3 || latestA.CPU != 40 {
		t.Fatalf("valid sample before bad FK item was lost: %#v %v", latestA, err)
	}
	latestB, err := repository.LatestMetric(ctx, targetB.ID)
	if err != nil || latestB.Sequence != 2 || latestB.CPU != 50 {
		t.Fatalf("valid sample after bad FK item was lost: %#v %v", latestB, err)
	}

	taskA, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "lat-a", Type: model.ProbeICMP, Target: "203.0.113.1",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency",
		RunOn: model.NodeRoleProbe, TargetNodeID: targetA.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "lat-b", Type: model.ProbeICMP, Target: "203.0.113.2",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency",
		RunOn: model.NodeRoleProbe, TargetNodeID: targetB.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	probeBatch := []model.ProbeResult{
		{NodeID: probe2.ID, TaskID: taskB.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 12},
		{NodeID: probe1.ID, TaskID: taskA.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 5},
		{NodeID: probe1.ID, TaskID: taskA.ID, ReceivedAt: now.Add(time.Minute), CollectedAt: now.Add(time.Minute), Success: true, LatencyMS: 7},
		{NodeID: "missing-probe-node", TaskID: taskA.ID, ReceivedAt: now, CollectedAt: now, Success: false, LatencyMS: -1},
		{NodeID: probe2.ID, TaskID: taskA.ID, ReceivedAt: now.Add(30 * time.Second), CollectedAt: now.Add(30 * time.Second), Success: true, LatencyMS: 9},
	}
	if err := repository.SaveProbeResultsBatch(ctx, probeBatch); err == nil {
		t.Fatal("expected SaveProbeResultsBatch to return foreign key error for missing node")
	}

	latestLatency, err := repository.LatestLatencyResults(ctx, now.Add(-time.Minute))
	if err != nil || len(latestLatency) != 3 {
		t.Fatalf("LatestLatencyResults = %#v, err=%v", latestLatency, err)
	}
	for i := 1; i < len(latestLatency); i++ {
		prev, curr := latestLatency[i-1], latestLatency[i]
		if prev.TargetNodeID > curr.TargetNodeID || (prev.TargetNodeID == curr.TargetNodeID && prev.ProbeNodeID > curr.ProbeNodeID) {
			t.Fatalf("LatestLatencyResults not sorted by (TargetNodeID, ProbeNodeID): %#v", latestLatency)
		}
	}
	for _, r := range latestLatency {
		if r.ProbeNodeID == probe1.ID && r.TargetNodeID == targetA.ID && r.LatencyMS != 7 {
			t.Fatalf("expected latest latency 7ms for (probe1, targetA), got %#v", r)
		}
	}

	// Test initial and incremental Rollup (ensuring straddling 5m bucket stays complete).
	firstRollupAt := now.Add(3 * time.Minute)
	if err := repository.Rollup(ctx, firstRollupAt); err != nil {
		t.Fatalf("initial Rollup failed: %v", err)
	}
	if !repository.lastRollup5m.Equal(firstRollupAt) || !repository.lastRollup1h.Equal(firstRollupAt) {
		t.Fatalf("lastRollup timestamps not updated: 5m=%v 1h=%v", repository.lastRollup5m, repository.lastRollup1h)
	}
	if err := repository.SaveMetric(ctx, model.MetricSample{
		NodeID: targetA.ID, Sequence: 4, ReceivedAt: now.Add(4 * time.Minute), CollectedAt: now.Add(4 * time.Minute), CPU: 60,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveMetric(ctx, model.MetricSample{
		NodeID: targetA.ID, Sequence: 5, ReceivedAt: now.Add(6 * time.Minute), CollectedAt: now.Add(6 * time.Minute), CPU: 80,
	}); err != nil {
		t.Fatal(err)
	}
	secondRollupAt := now.Add(8 * time.Minute)
	if err := repository.Rollup(ctx, secondRollupAt); err != nil {
		t.Fatalf("incremental Rollup failed: %v", err)
	}
	var bucketCount int
	var avgCPU float64
	if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*), AVG(cpu) FROM metrics_5m WHERE node_id = ?`, targetA.ID).Scan(&bucketCount, &avgCPU); err != nil {
		t.Fatal(err)
	}
	// Bucket 1 [10:00..10:05) has CPU 20, 40, 60 -> avg 40; Bucket 2 [10:05..10:10) has CPU 80 -> overall avg 60 across 2 buckets.
	if bucketCount != 2 || math.Abs(avgCPU-60) > 0.001 {
		t.Fatalf("incremental 5m rollup buckets=%d avgCPU=%v, want 2 and 60", bucketCount, avgCPU)
	}

	// Test multi-batch deletion loop and end-to-end ApplyRetention.
	expiredBase := now.Add(-10 * 24 * time.Hour)
	expiredBatch := make([]model.MetricSample, 11)
	for i := range expiredBatch {
		at := expiredBase.Add(time.Duration(i) * time.Second)
		expiredBatch[i] = model.MetricSample{NodeID: targetA.ID, Sequence: uint64(100 + i), ReceivedAt: at, CollectedAt: at, CPU: 1}
	}
	if err := repository.SaveMetricsBatch(ctx, expiredBatch); err != nil {
		t.Fatal(err)
	}
	if err := repository.deleteExpiredRows(ctx, "metrics_raw", now.Add(-24*time.Hour), 4); err != nil {
		t.Fatalf("batched deleteExpiredRows failed: %v", err)
	}
	var remainingExpired int
	if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics_raw WHERE ts < ?`, millis(now.Add(-24*time.Hour))).Scan(&remainingExpired); err != nil || remainingExpired != 0 {
		t.Fatalf("remaining expired metrics_raw = %d, err=%v", remainingExpired, err)
	}

	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: probe1.ID, TaskID: taskA.ID, ReceivedAt: now.Add(-100 * 24 * time.Hour), CollectedAt: now.Add(-100 * 24 * time.Hour), Success: true, LatencyMS: 11,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyRetention(ctx, model.DefaultSiteSettings(), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("ApplyRetention failed: %v", err)
	}
	var oldProbes int
	if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_results WHERE ts < ?`, millis(now.Add(-90*24*time.Hour))).Scan(&oldProbes); err != nil || oldProbes != 0 {
		t.Fatalf("expired probe_results were not cleaned up: %d %v", oldProbes, err)
	}
}

func TestDeleteNodeAndProbeTaskBatchedCleanup(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "delete-perf.db")
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	// 1. Verify migration 011 indexes exist.
	checkIndex := func(tbl, idx string) {
		t.Helper()
		var count int
		if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name = ?`, tbl, idx).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected index %s on %s, count=%d err=%v", idx, tbl, count, err)
		}
	}
	checkIndex("probe_results", "idx_probe_results_task_ts")
	checkIndex("alert_events", "idx_alert_events_node")

	// Missing node / task return store.ErrNotFound.
	if err := repository.DeleteNode(ctx, "missing-node"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteNode(missing) = %v, want ErrNotFound", err)
	}
	if err := repository.DeleteProbeTask(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteProbeTask(missing) = %v, want ErrNotFound", err)
	}

	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	targetRes, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Token: "hpa_del_target.secret", Role: model.NodeRoleMonitor,
			Metadata: model.EnrollmentMetadata{Name: "target-node"}, Config: model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "del_target", TokenHash: "hash1", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	keeperRes, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Token: "hpa_del_keeper.secret", Role: model.NodeRoleMonitor,
			Metadata: model.EnrollmentMetadata{Name: "keeper-node"}, Config: model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "del_keeper", TokenHash: "hash2", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	probeRes, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(), Token: "hpa_del_probe.secret", Role: model.NodeRoleProbe,
			Metadata: model.EnrollmentMetadata{Name: "probe-node"}, Config: model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "del_probe", TokenHash: "hash3", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	latencyTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "target-latency", Type: model.ProbeICMP, Target: "1.1.1.1",
		IntervalSeconds: 30, TimeoutSeconds: 5, Purpose: model.ProbePurposeLatency,
		RunOn: model.NodeRoleProbe, TargetNodeID: targetRes.Node.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	keeperTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "keeper-latency", Type: model.ProbeICMP, Target: "8.8.8.8",
		IntervalSeconds: 30, TimeoutSeconds: 5, Purpose: model.ProbePurposeLatency,
		RunOn: model.NodeRoleProbe, TargetNodeID: keeperRes.Node.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	customTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "custom-probe", Type: model.ProbeTCP, Target: "1.1.1.1:443",
		IntervalSeconds: 60, TimeoutSeconds: 5, Purpose: model.ProbePurposeCustom,
		RunOn: model.NodeRoleMonitor, Samples: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Test deleteRowsBatched across multiple batches while preserving other nodes/tasks.
	batchProbes := make([]model.ProbeResult, 0, 12)
	for i := 0; i < 11; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		batchProbes = append(batchProbes, model.ProbeResult{
			NodeID: targetRes.Node.ID, TaskID: customTask.ID, ReceivedAt: at, CollectedAt: at, Success: true, LatencyMS: 10,
		})
	}
	batchProbes = append(batchProbes, model.ProbeResult{
		NodeID: keeperRes.Node.ID, TaskID: customTask.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 12,
	})
	if err := repository.SaveProbeResultsBatch(ctx, batchProbes); err != nil {
		t.Fatal(err)
	}
	deletedBatch, err := repository.deleteRowsBatched(ctx, "probe_results", "node_id", targetRes.Node.ID, 4)
	if err != nil || deletedBatch != 11 {
		t.Fatalf("deleteRowsBatched deleted=%d err=%v, want 11 nil", deletedBatch, err)
	}
	var keeperCustomCount int
	if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_results WHERE task_id = ? AND node_id = ?`, customTask.ID, keeperRes.Node.ID).Scan(&keeperCustomCount); err != nil || keeperCustomCount != 1 {
		t.Fatalf("keeper custom probe result was affected: count=%d err=%v", keeperCustomCount, err)
	}

	// 3. Test DeleteProbeTask removes task and its probe_results while keeping other tasks.
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: targetRes.Node.ID, TaskID: customTask.ID, ReceivedAt: now.Add(time.Minute), CollectedAt: now.Add(time.Minute), Success: true, LatencyMS: 15,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteProbeTask(ctx, customTask.ID); err != nil {
		t.Fatalf("DeleteProbeTask failed: %v", err)
	}
	var remainingCustom int
	if err := repository.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_results WHERE task_id = ?`, customTask.ID).Scan(&remainingCustom); err != nil || remainingCustom != 0 {
		t.Fatalf("expected 0 probe_results after DeleteProbeTask, got %d err=%v", remainingCustom, err)
	}

	// 4. Populate targetNode metrics (raw, 5m, 1h), alert_events, own probe_results, AND probe_results recorded by probeNode for targetNode's latencyTask.
	if err := repository.SaveMetricsBatch(ctx, []model.MetricSample{
		{NodeID: targetRes.Node.ID, Sequence: 1, ReceivedAt: now, CollectedAt: now, CPU: 25},
		{NodeID: keeperRes.Node.ID, Sequence: 1, ReceivedAt: now, CollectedAt: now, CPU: 35},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Rollup(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveProbeResultsBatch(ctx, []model.ProbeResult{
		{NodeID: probeRes.Node.ID, TaskID: latencyTask.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 5},
		{NodeID: probeRes.Node.ID, TaskID: latencyTask.ID, ReceivedAt: now.Add(30 * time.Second), CollectedAt: now.Add(30 * time.Second), Success: true, LatencyMS: 6},
		{NodeID: targetRes.Node.ID, TaskID: keeperTask.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 8},
		{NodeID: probeRes.Node.ID, TaskID: keeperTask.ID, ReceivedAt: now, CollectedAt: now, Success: true, LatencyMS: 9},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveAlertEvent(ctx, model.AlertEvent{
		ID: uuid.NewString(), Type: "cpu", Status: model.AlertFiring, Severity: "warning",
		OccurredAt: now, Node: targetRes.Node.Public(), Metric: "cpu", Value: 90, Threshold: 80, Message: "high cpu",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveAlertEvent(ctx, model.AlertEvent{
		ID: uuid.NewString(), Type: "cpu", Status: model.AlertFiring, Severity: "warning",
		OccurredAt: now, Node: keeperRes.Node.Public(), Metric: "cpu", Value: 91, Threshold: 80, Message: "high cpu",
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := repository.DeleteNode(ctx, targetRes.Node.ID); err != nil {
		t.Fatalf("DeleteNode failed: %v", err)
	}

	assertZero := func(query string, args ...any) {
		t.Helper()
		var count int
		if err := repository.readDB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expected 0 rows for %q (%v), got %d err=%v", query, args, count, err)
		}
	}
	assertOne := func(query string, args ...any) {
		t.Helper()
		var count int
		if err := repository.readDB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected 1 row for %q (%v), got %d err=%v", query, args, count, err)
		}
	}

	assertZero(`SELECT COUNT(*) FROM nodes WHERE id = ?`, targetRes.Node.ID)
	assertZero(`SELECT COUNT(*) FROM probe_tasks WHERE id = ?`, latencyTask.ID)
	assertZero(`SELECT COUNT(*) FROM probe_results WHERE task_id = ?`, latencyTask.ID)
	assertZero(`SELECT COUNT(*) FROM probe_results WHERE node_id = ?`, targetRes.Node.ID)
	assertZero(`SELECT COUNT(*) FROM metrics_raw WHERE node_id = ?`, targetRes.Node.ID)
	assertZero(`SELECT COUNT(*) FROM metrics_5m WHERE node_id = ?`, targetRes.Node.ID)
	assertZero(`SELECT COUNT(*) FROM metrics_1h WHERE node_id = ?`, targetRes.Node.ID)
	assertZero(`SELECT COUNT(*) FROM alert_events WHERE node_id = ?`, targetRes.Node.ID)

	// Unrelated keeper node, latency task, metrics, alert event, and probe results must remain untouched.
	assertOne(`SELECT COUNT(*) FROM nodes WHERE id = ?`, keeperRes.Node.ID)
	assertOne(`SELECT COUNT(*) FROM probe_tasks WHERE id = ?`, keeperTask.ID)
	assertOne(`SELECT COUNT(*) FROM probe_results WHERE node_id = ? AND task_id = ?`, probeRes.Node.ID, keeperTask.ID)
	assertOne(`SELECT COUNT(*) FROM metrics_raw WHERE node_id = ?`, keeperRes.Node.ID)
	assertOne(`SELECT COUNT(*) FROM metrics_5m WHERE node_id = ?`, keeperRes.Node.ID)
	assertOne(`SELECT COUNT(*) FROM metrics_1h WHERE node_id = ?`, keeperRes.Node.ID)
	assertOne(`SELECT COUNT(*) FROM alert_events WHERE node_id = ?`, keeperRes.Node.ID)
}
