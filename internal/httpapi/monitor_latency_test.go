package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
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
