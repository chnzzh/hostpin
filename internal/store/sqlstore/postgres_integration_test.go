package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresMigrationsAndMetricPartition(t *testing.T) {
	rawDSN := os.Getenv("HOSTPIN_TEST_POSTGRES")
	if rawDSN == "" {
		t.Skip("HOSTPIN_TEST_POSTGRES is not configured")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("HOSTPIN_TEST_POSTGRES must be a URL DSN")
	}
	schema := "hostpin_test_" + regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(uuid.NewString(), "")
	root, err := sql.Open("pgx", rawDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	ctx := context.Background()
	repository, err := Open(ctx, config.DatabaseConfig{Driver: "postgres", DSN: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "test", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	temporaryPIN := store.TemporaryEnrollmentPIN{
		ID: uuid.NewString(), PINHash: "pg-temporary-hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := repository.ReplaceTemporaryEnrollmentPIN(ctx, temporaryPIN, now); err != nil {
		t.Fatal(err)
	}
	assertSystemAlertRoundTrip(t, repository, now)
	nodeID := uuid.NewString()
	request := model.EnrollmentRequest{InstallID: uuid.NewString(), Identity: model.AgentIdentity{Hostname: "pg-node"}, Metadata: model.EnrollmentMetadata{Name: "PG node"}, Config: model.DefaultAgentConfig()}
	if _, err := repository.EnrollNode(ctx, store.EnrollParams{Request: request, NodeID: nodeID, TokenID: "token-id", TokenHash: "token-hash", TemporaryPINID: temporaryPIN.ID, Now: now}); err != nil {
		t.Fatal(err)
	}
	usedTemporaryPIN, err := repository.LatestTemporaryEnrollmentPIN(ctx)
	if err != nil || usedTemporaryPIN.UsedAt == nil || usedTemporaryPIN.ClaimedInstallID != request.InstallID {
		t.Fatalf("PostgreSQL temporary PIN claim failed: %#v %v", usedTemporaryPIN, err)
	}
	secondRequest := model.EnrollmentRequest{InstallID: uuid.NewString(), Metadata: model.EnrollmentMetadata{Name: "rejected"}, Config: model.DefaultAgentConfig()}
	if _, err := repository.EnrollNode(ctx, store.EnrollParams{Request: secondRequest, NodeID: uuid.NewString(), TokenID: "second-token", TokenHash: "second-hash", TemporaryPINID: temporaryPIN.ID, Now: now}); !errors.Is(err, store.ErrTemporaryPINUnavailable) {
		t.Fatalf("PostgreSQL reused temporary PIN was not rejected: %v", err)
	}
	correctionPeriod, correctionUpdated := now.Truncate(24*time.Hour), now.Add(time.Minute)
	if err := repository.UpdateTrafficCorrection(ctx, nodeID, 400, -200, &correctionPeriod, correctionUpdated); err != nil {
		t.Fatalf("PostgreSQL traffic correction update failed: %v", err)
	}
	correctedNode, err := repository.GetNode(ctx, nodeID)
	if err != nil || correctedNode.TrafficRXCorrection != 400 || correctedNode.TrafficTXCorrection != -200 ||
		correctedNode.TrafficCorrectionPeriodStart == nil || !correctedNode.TrafficCorrectionPeriodStart.Equal(correctionPeriod) {
		t.Fatalf("PostgreSQL traffic correction did not round-trip: %#v %v", correctedNode, err)
	}
	if err := repository.SaveMetric(ctx, model.MetricSample{
		NodeID: nodeID, ReceivedAt: now, CollectedAt: now, CPU: 25,
		NetRxBytes: 12_000, NetTxBytes: 8_000, MonthlyRxBytes: 2_500, MonthlyTxBytes: 1_250,
	}); err != nil {
		t.Fatal(err)
	}
	probeNodeID := uuid.NewString()
	probeRequest := model.EnrollmentRequest{
		InstallID: uuid.NewString(), Role: model.NodeRoleProbe,
		Identity: model.AgentIdentity{Hostname: "pg-router", OS: "linux", Arch: "amd64"},
		Metadata: model.EnrollmentMetadata{Name: "PG router"}, Config: model.DefaultAgentConfig(),
	}
	if _, err := repository.EnrollNode(ctx, store.EnrollParams{Request: probeRequest, NodeID: probeNodeID, TokenID: "probe-token-id", TokenHash: "probe-token-hash", Now: now}); err != nil {
		t.Fatal(err)
	}
	latencyTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "PG route", Type: model.ProbeTCP, Target: "127.0.0.1:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency",
		RunOn: model.NodeRoleProbe, TargetNodeID: nodeID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: probeNodeID, TaskID: latencyTask.ID, ReceivedAt: now, CollectedAt: now,
		Success: true, LatencyMS: 8.25, LossPercent: 33.333,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := repository.LatencyWindowSummary(ctx, probeNodeID, nodeID, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || summary.SampleCount != 1 || summary.SuccessCount != 1 || summary.AverageLatencyMS != 8.25 || summary.AverageLossPercent != 33.333 {
		t.Fatalf("PostgreSQL latency window summary was incorrect: %#v %v", summary, err)
	}
	probeTasks, err := repository.ListProbeTasks(ctx, probeNodeID)
	if err != nil || len(probeTasks) != 1 || probeTasks[0].Purpose != "latency" || probeTasks[0].Samples != 3 {
		t.Fatalf("PostgreSQL Probe Node dispatch failed: %#v %v", probeTasks, err)
	}
	monitorNode, err := repository.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	monitorNode.LatencyEnabled = true
	if err := repository.UpdateNode(ctx, monitorNode); err != nil {
		t.Fatalf("PostgreSQL monitor latency enable failed: %v", err)
	}
	monitorTasks, err := repository.ListProbeTasks(ctx, nodeID)
	foundLatencyTask := false
	for _, task := range monitorTasks {
		foundLatencyTask = foundLatencyTask || task.ID == latencyTask.ID
	}
	if err != nil || !foundLatencyTask {
		t.Fatalf("PostgreSQL latency-enabled monitor dispatch failed: %#v %v", monitorTasks, err)
	}
	measurementNodes, err := repository.ListLatencyNodes(ctx, true)
	if err != nil || len(measurementNodes) != 2 {
		t.Fatalf("PostgreSQL measurement inventory omitted the monitor: %#v %v", measurementNodes, err)
	}
	latencyLatest, err := repository.LatestLatencyResults(ctx, now.Add(-time.Minute))
	if err != nil || len(latencyLatest) != 1 || latencyLatest[0].TargetNodeID != nodeID || latencyLatest[0].LossPercent != 33.333 {
		t.Fatalf("PostgreSQL latency result failed: %#v %v", latencyLatest, err)
	}
	latest, err := repository.LatestMetric(ctx, nodeID)
	if err != nil || latest.CPU != 25 || latest.MonthlyRxBytes != 2_500 || latest.MonthlyTxBytes != 1_250 {
		t.Fatalf("partitioned metric roundtrip failed: %#v %v", latest, err)
	}
	if err := repository.Rollup(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("PostgreSQL traffic rollup failed: %v", err)
	}
	var rolledRX, rolledTX int64
	if err := root.QueryRow(`SELECT MAX(monthly_rx_bytes), MAX(monthly_tx_bytes) FROM `+schema+`.metrics_1h WHERE node_id=$1`, nodeID).Scan(&rolledRX, &rolledTX); err != nil || rolledRX != 2_500 || rolledTX != 1_250 {
		t.Fatalf("PostgreSQL traffic rollup=%d/%d: %v", rolledRX, rolledTX, err)
	}
	future := now.AddDate(0, 5, 0)
	if err := repository.EnsureMetricPartitions(ctx, future); err != nil {
		t.Fatalf("future partition maintenance failed: %v", err)
	}
	if err := repository.SaveMetric(ctx, model.MetricSample{NodeID: nodeID, ReceivedAt: future, CollectedAt: future, CPU: 26}); err != nil {
		t.Fatalf("metric write after five months of uptime failed: %v", err)
	}
}

func TestSQLiteToPostgresTransfer(t *testing.T) {
	rawDSN := os.Getenv("HOSTPIN_TEST_POSTGRES")
	if rawDSN == "" {
		t.Skip("HOSTPIN_TEST_POSTGRES is not configured")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("HOSTPIN_TEST_POSTGRES must be a URL DSN")
	}
	schema := "hostpin_transfer_" + regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(uuid.NewString(), "")
	root, err := sql.Open("pgx", rawDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "test", CreatedAt: now, UpdatedAt: now}
	if err := source.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	transferPIN := store.TemporaryEnrollmentPIN{
		ID: uuid.NewString(), PINHash: "transfer-temporary-hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := source.ReplaceTemporaryEnrollmentPIN(ctx, transferPIN, now); err != nil {
		t.Fatal(err)
	}
	assertSystemAlertRoundTrip(t, source, now)
	nodeID := uuid.NewString()
	request := model.EnrollmentRequest{
		InstallID: uuid.NewString(), Identity: model.AgentIdentity{Hostname: "migrated-node"},
		Metadata: model.EnrollmentMetadata{Name: "Migrated", CountryCode: "SG"}, Config: model.DefaultAgentConfig(),
	}
	if _, err := source.EnrollNode(ctx, store.EnrollParams{Request: request, NodeID: nodeID, TokenID: "transfer-token", TokenHash: "transfer-hash", SourceIP: "203.0.113.10", LocationManual: true, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := source.SaveMetric(ctx, model.MetricSample{
		NodeID: nodeID, ReceivedAt: now, CollectedAt: now, CPU: 42,
		NetRxBytes: 99_000, NetTxBytes: 77_000, MonthlyRxBytes: 12_345, MonthlyTxBytes: 6_789,
	}); err != nil {
		t.Fatal(err)
	}
	probeNodeID := uuid.NewString()
	probeRequest := model.EnrollmentRequest{
		InstallID: uuid.NewString(), Role: model.NodeRoleProbe,
		Identity: model.AgentIdentity{Hostname: "migrated-router"},
		Metadata: model.EnrollmentMetadata{Name: "Migrated router"}, Config: model.DefaultAgentConfig(),
	}
	if _, err := source.EnrollNode(ctx, store.EnrollParams{Request: probeRequest, NodeID: probeNodeID, TokenID: "transfer-probe-token", TokenHash: "transfer-probe-hash", Now: now}); err != nil {
		t.Fatal(err)
	}
	latencyTask, err := source.SaveProbeTask(ctx, model.ProbeTask{
		Name: "Migrated route", Type: model.ProbeICMP, Target: "203.0.113.10",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency", RunOn: model.NodeRoleProbe,
		TargetNodeID: nodeID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: probeNodeID, TaskID: latencyTask.ID, ReceivedAt: now, CollectedAt: now,
		Success: true, LatencyMS: 18.75, LossPercent: 25,
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := TransferSQLiteToPostgres(ctx, sourcePath, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows == 0 || len(report.Tables) != len(transferTables) {
		t.Fatalf("incomplete transfer report: %#v", report)
	}
	var transferredPINHash string
	if err := root.QueryRow(`SELECT pin_hash FROM `+schema+`.temporary_enrollment_pins WHERE id=$1`, transferPIN.ID).Scan(&transferredPINHash); err != nil || transferredPINHash != transferPIN.PINHash {
		t.Fatalf("temporary enrollment PIN was not transferred: %q %v", transferredPINHash, err)
	}
	var manual bool
	if err := root.QueryRow(`SELECT location_manual FROM `+schema+`.nodes WHERE id=$1`, nodeID).Scan(&manual); err != nil || !manual {
		t.Fatalf("boolean node metadata was not transferred: %v manual=%v", err, manual)
	}
	var metrics int
	if err := root.QueryRow(`SELECT COUNT(*) FROM `+schema+`.metrics_raw WHERE node_id=$1`, nodeID).Scan(&metrics); err != nil || metrics != 1 {
		t.Fatalf("metric transfer count=%d: %v", metrics, err)
	}
	var transferredRX, transferredTX int64
	if err := root.QueryRow(`SELECT monthly_rx_bytes, monthly_tx_bytes FROM `+schema+`.metrics_raw WHERE node_id=$1`, nodeID).Scan(&transferredRX, &transferredTX); err != nil || transferredRX != 12_345 || transferredTX != 6_789 {
		t.Fatalf("traffic totals were not transferred: %d/%d %v", transferredRX, transferredTX, err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("source SQLite database was removed: %v", err)
	}
	var role, purpose, runOn string
	var samples int
	if err := root.QueryRow(`SELECT role FROM `+schema+`.nodes WHERE id=$1`, probeNodeID).Scan(&role); err != nil || role != string(model.NodeRoleProbe) {
		t.Fatalf("Probe Node role was not transferred: role=%q err=%v", role, err)
	}
	if err := root.QueryRow(`SELECT purpose, run_on, samples FROM `+schema+`.probe_tasks WHERE id=$1`, latencyTask.ID).Scan(&purpose, &runOn, &samples); err != nil || purpose != "latency" || runOn != string(model.NodeRoleProbe) || samples != 3 {
		t.Fatalf("latency task was not transferred: purpose=%q run_on=%q samples=%d err=%v", purpose, runOn, samples, err)
	}
	var loss float64
	if err := root.QueryRow(`SELECT loss_percent FROM `+schema+`.probe_results WHERE node_id=$1 AND task_id=$2`, probeNodeID, latencyTask.ID).Scan(&loss); err != nil || loss != 25 {
		t.Fatalf("latency result was not transferred: loss=%v err=%v", loss, err)
	}
}
