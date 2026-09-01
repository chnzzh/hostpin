package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/agentconfig"
	"github.com/chnzzh/hostpin/internal/model"
)

func TestHTTPFallbackReportsAndAppliesConfiguration(t *testing.T) {
	var reports atomic.Int32
	serverConfig := model.DefaultAgentConfig()
	serverConfig.CollectIntervalSeconds = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/reports" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-agent-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var report model.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.Sample.CollectedAt.IsZero() {
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		reports.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.AgentAck{Accepted: true, ServerTime: time.Now().UTC(), Config: serverConfig})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "agent.json")
	cfg := agentconfig.Config{
		Endpoint: server.URL, NodeID: "node", InstallID: "install", Token: "test-agent-token",
		Agent: model.DefaultAgentConfig(), Metadata: agentconfig.LocalMetadata{Name: "fallback"},
	}
	if err := agentconfig.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(configPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.runHTTPFallback(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatalf("HTTP fallback failed: %v", err)
	}
	if reports.Load() == 0 {
		t.Fatal("HTTP fallback did not send a report")
	}
	if runtime.config.Agent.CollectIntervalSeconds != 1 {
		t.Fatal("server Agent configuration was not applied")
	}
}

func TestProbeNodeHTTPFallbackUsesOutboundHeartbeatWithoutHostMetrics(t *testing.T) {
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agent/reports" {
			http.NotFound(w, r)
			return
		}
		var report model.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		if report.Sample != nil {
			http.Error(w, "probe-only nodes must not send host metrics", http.StatusBadRequest)
			return
		}
		reports.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.AgentAck{
			Accepted: true, ServerTime: time.Now().UTC(), Config: model.DefaultAgentConfig(),
		})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "probe-agent.json")
	cfg := agentconfig.Config{
		Endpoint: server.URL, NodeID: "probe-node", Role: model.NodeRoleProbe,
		InstallID: "probe-install", Token: "test-agent-token",
		Agent: model.DefaultAgentConfig(), Metadata: agentconfig.LocalMetadata{Name: "private-router"},
	}
	if err := agentconfig.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(configPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.collector != nil {
		t.Fatal("probe-only runtime allocated the host metric collector")
	}
	if err := runtime.reportHTTP(context.Background()); err != nil {
		t.Fatalf("outbound probe heartbeat failed: %v", err)
	}
	if reports.Load() != 1 {
		t.Fatalf("got %d outbound reports, want 1", reports.Load())
	}
}

func TestAgentURLSchemes(t *testing.T) {
	if value, err := agentURL("https://monitor.example/", "/api/v1/agent/stream", true); err != nil || value != "wss://monitor.example/api/v1/agent/stream" {
		t.Fatalf("unexpected secure stream URL %q: %v", value, err)
	}
	if _, err := agentURL("file:///tmp/hostpin", "/stream", true); err == nil {
		t.Fatal("non-HTTP endpoint was accepted")
	}
}
