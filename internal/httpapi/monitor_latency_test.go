package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestAdminMonitorCanToggleLatencyMeasurement(t *testing.T) {
	ctx := context.Background()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "monitor-latency.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	record, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{
			InstallID: uuid.NewString(),
			Identity:  model.AgentIdentity{Hostname: "shared-node", OS: "linux", Arch: "amd64"},
			Metadata:  model.EnrollmentMetadata{Name: "Shared node"},
			Config:    model.DefaultAgentConfig(),
		},
		NodeID: uuid.NewString(), TokenID: "shared-token", TokenHash: "shared-hash", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	latencyTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "Shared node", Type: model.ProbeTCP, Target: "127.0.0.1:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: model.ProbePurposeLatency,
		RunOn: model.NodeRoleProbe, TargetNodeID: record.Node.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := &API{store: repository, agentStreams: make(map[string]map[chan struct{}]struct{})}
	revoked, unregister := api.registerAgentStream(record.Node.ID)
	defer unregister()

	enableRequest := requestWithNodeAndAdmin(t, http.MethodPut, record.Node.ID, `{"enabled":true}`, admin)
	enableResponse := httptest.NewRecorder()
	api.handleAdminSetNodeLatency(enableResponse, enableRequest)
	if enableResponse.Code != http.StatusOK {
		t.Fatalf("enable returned HTTP %d: %s", enableResponse.Code, enableResponse.Body.String())
	}
	select {
	case <-revoked:
	default:
		t.Fatal("enabling latency did not refresh the active Agent stream")
	}
	updated, err := repository.GetNode(ctx, record.Node.ID)
	if err != nil || !updated.LatencyEnabled || !updated.CanMeasureLatency() {
		t.Fatalf("monitor latency capability was not persisted: %#v %v", updated, err)
	}
	tasks, err := repository.ListProbeTasks(ctx, record.Node.ID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != latencyTask.ID {
		t.Fatalf("enabled monitor did not receive latency tasks: %#v %v", tasks, err)
	}
	measurementNodes, err := repository.ListLatencyNodes(ctx, true)
	if err != nil || len(measurementNodes) != 1 || measurementNodes[0].ID != record.Node.ID {
		t.Fatalf("enabled monitor was absent from latency inventory: %#v %v", measurementNodes, err)
	}
	overview, err := api.latencyOverview(ctx, true)
	if err != nil || len(overview.ProbeNodes) != 1 || overview.ProbeNodes[0].Role != model.NodeRoleMonitor {
		t.Fatalf("public latency overview omitted the shared monitor: %#v %v", overview.ProbeNodes, err)
	}

	disableRequest := requestWithNodeAndAdmin(t, http.MethodDelete, record.Node.ID, "", admin)
	disableResponse := httptest.NewRecorder()
	api.handleAdminDeleteLatencyNode(disableResponse, disableRequest)
	if disableResponse.Code != http.StatusNoContent {
		t.Fatalf("disable returned HTTP %d: %s", disableResponse.Code, disableResponse.Body.String())
	}
	updated, err = repository.GetNode(ctx, record.Node.ID)
	if err != nil || updated.LatencyEnabled || updated.Role != model.NodeRoleMonitor {
		t.Fatalf("disabling latency removed or changed the monitor: %#v %v", updated, err)
	}
	tasks, err = repository.ListProbeTasks(ctx, record.Node.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("disabled monitor retained latency tasks: %#v %v", tasks, err)
	}
}

func requestWithNodeAndAdmin(t *testing.T, method, nodeID, body string, admin store.Admin) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", nodeID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, adminContextKey{}, admin)
	return request.WithContext(ctx)
}

type countingLatencyStore struct {
	store.Store
	latestCalls atomic.Int64
	delayMS     atomic.Int64
}

func (s *countingLatencyStore) LatestLatencyResults(ctx context.Context, since time.Time) ([]model.LatencyResult, error) {
	s.latestCalls.Add(1)
	if delay := s.delayMS.Load(); delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	return s.Store.LatestLatencyResults(ctx, since)
}

func TestLatencyOverviewCacheCoalescingAndInvalidation(t *testing.T) {
	ctx := context.Background()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "latency-cache.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	enroll := func(name string, role model.NodeRole, hidden bool, tokenID string) model.Node {
		record, enrollErr := repository.EnrollNode(ctx, store.EnrollParams{
			Request: model.EnrollmentRequest{
				InstallID: uuid.NewString(), Role: role,
				Identity: model.AgentIdentity{Hostname: name, OS: "linux", Arch: "amd64"},
				Metadata: model.EnrollmentMetadata{Name: name, Hidden: hidden},
				Config:   model.DefaultAgentConfig(),
			},
			NodeID: uuid.NewString(), TokenID: tokenID, TokenHash: "hash-" + tokenID, Now: now,
		})
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		return record.Node
	}
	target := enroll("target-node", model.NodeRoleMonitor, false, "tok-target")
	visibleProbe := enroll("visible-probe", model.NodeRoleProbe, false, "tok-vprobe")
	_ = enroll("hidden-probe", model.NodeRoleProbe, true, "tok-hprobe")

	latencyTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "target-route", Type: model.ProbeTCP, Target: "127.0.0.1:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: model.ProbePurposeLatency,
		RunOn: model.NodeRoleProbe, TargetNodeID: target.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: visibleProbe.ID, TaskID: latencyTask.ID, CollectedAt: now, ReceivedAt: now,
		Success: false, LatencyMS: -1, LossPercent: 100, Error: "dial secret-endpoint",
	}); err != nil {
		t.Fatal(err)
	}

	counted := &countingLatencyStore{Store: repository}
	counted.delayMS.Store(40)
	api := &API{store: counted, agentStreams: make(map[string]map[chan struct{}]struct{})}

	const callers = 8
	var wg sync.WaitGroup
	overviews := make([]model.LatencyOverview, callers)
	errs := make([]error, callers)
	wg.Add(callers)
	for i := range callers {
		go func(idx int) {
			defer wg.Done()
			overviews[idx], errs[idx] = api.latencyOverview(ctx, false)
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d failed: %v", i, errs[i])
		}
		if len(overviews[i].ProbeNodes) != 1 || len(overviews[i].Latest) != 1 || overviews[i].Latest[0].Error != "probe failed" {
			t.Fatalf("caller %d got unexpected public overview: %#v", i, overviews[i])
		}
	}
	if got := counted.latestCalls.Load(); got != 1 {
		t.Fatalf("concurrent public latency calls executed %d DB reads, want 1", got)
	}

	counted.delayMS.Store(0)
	cachedOverview, err := api.latencyOverview(ctx, false)
	if err != nil || len(cachedOverview.Latest) != 1 {
		t.Fatalf("cached public overview failed: %#v %v", cachedOverview, err)
	}
	if got := counted.latestCalls.Load(); got != 1 {
		t.Fatalf("cached public latency call hit DB again: %d calls", got)
	}

	authOverview, err := api.latencyOverview(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := counted.latestCalls.Load(); got != 2 {
		t.Fatalf("authenticated latency call should use separate cache entry, got %d DB calls", got)
	}
	if len(authOverview.ProbeNodes) != 2 || len(authOverview.Latest) != 1 || authOverview.Latest[0].Error != "dial secret-endpoint" {
		t.Fatalf("authenticated overview lost hidden probe or unredacted error: %#v", authOverview)
	}

	updatedAt := now.Add(time.Second)
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: visibleProbe.ID, TaskID: latencyTask.ID, CollectedAt: updatedAt, ReceivedAt: updatedAt,
		Success: true, LatencyMS: 12.5, LossPercent: 0,
	}); err != nil {
		t.Fatal(err)
	}
	api.acceptProbe(visibleProbe, model.ProbeResult{TaskID: latencyTask.ID, Success: true, LatencyMS: 12.5}, updatedAt)

	freshOverview, err := api.latencyOverview(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := counted.latestCalls.Load(); got != 3 {
		t.Fatalf("expected cache invalidation on acceptProbe, got %d DB calls", got)
	}
	if len(freshOverview.Latest) != 1 || !freshOverview.Latest[0].Success || freshOverview.Latest[0].LatencyMS != 12.5 {
		t.Fatalf("fresh overview after acceptProbe did not reflect latest measurement: %#v", freshOverview.Latest)
	}
}

func TestDualStackLatencyTargetsOverviewAndHistoryIsolation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(dataDir, "dualstack-api.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	apiToken, apiTokenID, apiTokenHash, err := security.NewAPIKeyToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAPIKey(ctx, store.APIKeyRecord{
		Key: model.APIKey{ID: uuid.NewString(), Name: "ci", Scopes: []string{"admin"}, CreatedAt: now},
		AdminID: admin.ID, TokenID: apiTokenID, TokenHash: apiTokenHash,
	}); err != nil {
		t.Fatal(err)
	}

	enrollNode := func(name string, role model.NodeRole, group string, weight int, tokenID string) model.Node {
		t.Helper()
		rec, enrollErr := repository.EnrollNode(ctx, store.EnrollParams{
			Request: model.EnrollmentRequest{
				InstallID: uuid.NewString(), Role: role,
				Identity: model.AgentIdentity{Hostname: name, OS: "linux", Arch: "amd64"},
				Metadata: model.EnrollmentMetadata{Name: name, Group: group},
				Config:   model.DefaultAgentConfig(),
			},
			NodeID: uuid.NewString(), TokenID: tokenID, TokenHash: "hash-" + tokenID, Now: now,
		})
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		if weight != 0 {
			rec.Node.Weight = weight
			if err := repository.UpdateNode(ctx, rec.Node); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Node
	}

	serverPrimary := enrollNode("alpha-server", model.NodeRoleMonitor, "ap", 10, "tok-srv1")
	serverSecondary := enrollNode("beta-server", model.NodeRoleMonitor, "ap", 5, "tok-srv2")
	probeNode := enrollNode("probe-tokyo", model.NodeRoleProbe, "probes", 0, "tok-probe1")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := core.NewHub()
	persister := core.NewPersister(repository, logger, 32)
	box, _ := security.NewSecretBox(make([]byte, 32))
	themes, _ := theme.New(repository, dataDir)
	cfg := config.Default()
	cfg.PublicURL = "http://example.test"
	apiServer := New(cfg, repository, hub, persister, box,
		alerting.New(repository, hub, cfg.PublicURL, time.Minute, logger),
		notification.New(repository, box, cfg.PublicURL, logger), themes, nil, nil, logger)
	ts := httptest.NewServer(apiServer.Router())
	defer ts.Close()

	doAdmin := func(method, path string, payload any) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiToken)
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}

	// 1. Create all 4 combinations (IPv4/IPv6 x ICMP/TCP) on serverPrimary, plus legacy omitted ip_version on serverSecondary.
	createTarget := func(payload map[string]any) model.ProbeTask {
		t.Helper()
		code, data := doAdmin(http.MethodPost, "/api/v1/admin/latency/targets", payload)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/v1/admin/latency/targets %v -> HTTP %d: %s", payload, code, data)
		}
		var env model.Envelope[model.ProbeTask]
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatal(err)
		}
		return env.Data
	}

	// Deliberately create in reverse order (6/tcp, 6/icmp, 4/tcp, 4/icmp) to test deterministic sorting!
	t6TCP := createTarget(map[string]any{
		"target_node_id": serverPrimary.ID, "type": "tcp", "target": "[2001:db8::1]:443",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	if t6TCP.IPVersion != 6 {
		t.Fatalf("expected inferred ip_version=6 for [2001:db8::1]:443, got %d", t6TCP.IPVersion)
	}
	t6ICMP := createTarget(map[string]any{
		"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "icmp", "target": "[2001:db8::1]",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	if t6ICMP.Target != "2001:db8::1" || t6ICMP.IPVersion != 6 {
		t.Fatalf("expected bracketed ICMP IPv6 to normalize to unbracketed 2001:db8::1: %#v", t6ICMP)
	}
	t4TCP := createTarget(map[string]any{
		"target_node_id": serverPrimary.ID, "ip_version": 4, "type": "tcp", "target": "203.0.113.10:443",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	t4ICMP := createTarget(map[string]any{
		"target_node_id": serverPrimary.ID, "type": "icmp", "target": "203.0.113.10",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	if t4ICMP.IPVersion != 4 {
		t.Fatalf("expected inferred ip_version=4 for 203.0.113.10, got %d", t4ICMP.IPVersion)
	}
	tSec4ICMP := createTarget(map[string]any{
		"target_node_id": serverSecondary.ID, "ip_version": 4, "type": "icmp", "target": "198.51.100.2",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})

	// 2. Reject duplicate (target_node_id, ip_version, type) with HTTP 409 latency_target_exists.
	dupCode, dupBody := doAdmin(http.MethodPost, "/api/v1/admin/latency/targets", map[string]any{
		"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "icmp", "target": "2001:db8::99",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	if dupCode != http.StatusConflict || !bytes.Contains(dupBody, []byte("latency_target_exists")) {
		t.Fatalf("expected 409 latency_target_exists, got HTTP %d: %s", dupCode, dupBody)
	}

	// Also reject PUT moving an existing target onto a colliding (target_node_id, ip_version, type).
	putDupCode, putDupBody := doAdmin(http.MethodPut, "/api/v1/admin/latency/targets/"+strconv.FormatInt(t4ICMP.ID, 10), map[string]any{
		"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "icmp", "target": "2001:db8::1",
		"interval_seconds": 30, "timeout_seconds": 3, "public": true, "samples": 3, "enabled": true,
	})
	if putDupCode != http.StatusConflict || !bytes.Contains(putDupBody, []byte("latency_target_exists")) {
		t.Fatalf("expected PUT conflict 409 latency_target_exists, got HTTP %d: %s", putDupCode, putDupBody)
	}

	// 3. Reject invalid ip_version, family mismatches, ICMP with port, and invalid TCP ports.
	for _, bad := range []map[string]any{
		{"target_node_id": serverPrimary.ID, "ip_version": 5, "type": "icmp", "target": "1.1.1.1", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 4, "type": "icmp", "target": "2001:db8::1", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "icmp", "target": "1.1.1.1", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 4, "type": "tcp", "target": "[2001:db8::1]:443", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "tcp", "target": "1.1.1.1:443", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 6, "type": "icmp", "target": "[2001:db8::1]:443", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 4, "type": "tcp", "target": "1.1.1.1:0", "interval_seconds": 30, "timeout_seconds": 3},
		{"target_node_id": serverPrimary.ID, "ip_version": 4, "type": "tcp", "target": "1.1.1.1:65536", "interval_seconds": 30, "timeout_seconds": 3},
	} {
		code, data := doAdmin(http.MethodPost, "/api/v1/admin/latency/targets", bad)
		if code != http.StatusBadRequest {
			t.Fatalf("invalid target %v returned HTTP %d: %s", bad, code, data)
		}
	}

	// 4. Record distinct measurements for each of the 4 tasks on serverPrimary.
	if err := repository.SaveProbeResultsBatch(ctx, []model.ProbeResult{
		{NodeID: probeNode.ID, TaskID: t4ICMP.ID, ReceivedAt: now.Add(-2 * time.Minute), CollectedAt: now.Add(-2 * time.Minute), Success: true, LatencyMS: 11},
		{NodeID: probeNode.ID, TaskID: t4TCP.ID, ReceivedAt: now.Add(-2 * time.Minute), CollectedAt: now.Add(-2 * time.Minute), Success: true, LatencyMS: 22},
		{NodeID: probeNode.ID, TaskID: t6ICMP.ID, ReceivedAt: now.Add(-2 * time.Minute), CollectedAt: now.Add(-2 * time.Minute), Success: true, LatencyMS: 33},
		{NodeID: probeNode.ID, TaskID: t6TCP.ID, ReceivedAt: now.Add(-2 * time.Minute), CollectedAt: now.Add(-2 * time.Minute), Success: true, LatencyMS: 44},
	}); err != nil {
		t.Fatal(err)
	}
	apiServer.invalidateLatencyCache()

	// 5. Verify GET /api/v1/public/latency deterministic target sorting and ip_version/type fields.
	pubResp, err := http.Get(ts.URL + "/api/v1/public/latency")
	if err != nil {
		t.Fatal(err)
	}
	pubBytes, _ := io.ReadAll(pubResp.Body)
	pubResp.Body.Close()
	if pubResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/public/latency HTTP %d: %s", pubResp.StatusCode, pubBytes)
	}
	var overviewEnv model.Envelope[model.LatencyOverview]
	if err := json.Unmarshal(pubBytes, &overviewEnv); err != nil {
		t.Fatal(err)
	}
	targets := overviewEnv.Data.Targets
	if len(targets) != 5 {
		t.Fatalf("expected 5 latency targets, got %d: %#v", len(targets), targets)
	}
	wantOrder := []int64{t4ICMP.ID, t4TCP.ID, t6ICMP.ID, t6TCP.ID, tSec4ICMP.ID}
	for i, wantID := range wantOrder {
		if targets[i].TaskID != wantID {
			t.Fatalf("targets[%d].TaskID = %d, want %d (full order: %#v)", i, targets[i].TaskID, wantID, targets)
		}
	}
	if len(overviewEnv.Data.Latest) != 4 {
		t.Fatalf("expected 4 latest results, got %#v", overviewEnv.Data.Latest)
	}
	for _, r := range overviewEnv.Data.Latest {
		if (r.IPVersion != 4 && r.IPVersion != 6) || (r.Type != model.ProbeICMP && r.Type != model.ProbeTCP) {
			t.Fatalf("latest result missing ip_version/type: %#v", r)
		}
	}

	// 6. Verify GET /api/v1/public/latency/history?task_id=... isolates each curve and summary.
	type historyResp struct {
		Data    []model.LatencyResult      `json:"data"`
		Summary model.LatencyWindowSummary `json:"summary"`
	}
	for _, check := range []struct {
		taskID      int64
		wantIP      int
		wantType    model.ProbeType
		wantLatency float64
	}{
		{t4ICMP.ID, 4, model.ProbeICMP, 11},
		{t4TCP.ID, 4, model.ProbeTCP, 22},
		{t6ICMP.ID, 6, model.ProbeICMP, 33},
		{t6TCP.ID, 6, model.ProbeTCP, 44},
	} {
		url := ts.URL + "/api/v1/public/latency/history?probe_node_id=" + probeNode.ID + "&task_id=" + strconv.FormatInt(check.taskID, 10) + "&hours=1"
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("history task_id=%d HTTP %d: %s", check.taskID, resp.StatusCode, raw)
		}
		var parsed historyResp
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		if len(parsed.Data) != 1 || parsed.Data[0].TaskID != check.taskID || parsed.Data[0].IPVersion != check.wantIP ||
			parsed.Data[0].Type != check.wantType || parsed.Data[0].LatencyMS != check.wantLatency ||
			parsed.Summary.SampleCount != 1 || parsed.Summary.AverageLatencyMS != check.wantLatency {
			t.Fatalf("history task_id=%d returned mixed or wrong series: %#v", check.taskID, parsed)
		}
	}

	// Mismatched target_node_id alongside task_id must be rejected.
	mismatchResp, err := http.Get(ts.URL + "/api/v1/public/latency/history?probe_node_id=" + probeNode.ID + "&task_id=" + strconv.FormatInt(t6ICMP.ID, 10) + "&target_node_id=" + serverSecondary.ID + "&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	mismatchResp.Body.Close()
	if mismatchResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched target_node_id with task_id returned HTTP %d, want 400", mismatchResp.StatusCode)
	}

	// Filter query without task_id (ip_version + type) narrows cleanly to the single matching task.
	filteredResp, err := http.Get(ts.URL + "/api/v1/public/latency/history?probe_node_id=" + probeNode.ID + "&target_node_id=" + serverPrimary.ID + "&ip_version=6&type=tcp&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	filteredRaw, _ := io.ReadAll(filteredResp.Body)
	filteredResp.Body.Close()
	var filteredParsed historyResp
	if err := json.Unmarshal(filteredRaw, &filteredParsed); err != nil {
		t.Fatal(err)
	}
	if filteredResp.StatusCode != http.StatusOK || len(filteredParsed.Data) != 1 || filteredParsed.Data[0].TaskID != t6TCP.ID || filteredParsed.Data[0].LatencyMS != 44 {
		t.Fatalf("filtered history without task_id returned %d %#v", filteredResp.StatusCode, filteredParsed)
	}
}
