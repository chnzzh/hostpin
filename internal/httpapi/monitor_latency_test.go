package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
