package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func testThemeArchive(t *testing.T, short string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := map[string]string{
		"komari-theme.json": `{"name":"` + short + `","short":"` + short + `","version":"1.0.0"}`,
		"dist/index.html":   `<html><body>` + short + `</body></html>`,
		"dist/asset.js":     `window.hostpinTheme = true`,
		"source.txt":        `not public`,
	}
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPublicAPIsDoNotLeakHiddenNodesOrPrivateFields(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(dataDir, "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	enroll := func(name string, hidden bool, tokenID string, roles ...model.NodeRole) model.Node {
		role := model.NodeRoleMonitor
		if len(roles) > 0 {
			role = roles[0]
		}
		record, err := repository.EnrollNode(ctx, store.EnrollParams{
			Request: model.EnrollmentRequest{InstallID: uuid.NewString(), Role: role, Identity: model.AgentIdentity{IPv4: "203.0.113.8"}, Metadata: model.EnrollmentMetadata{Name: name, Hidden: hidden, PrivateRemark: "secret-note"}, Config: model.DefaultAgentConfig()},
			NodeID:  uuid.NewString(), TokenID: tokenID, TokenHash: "hash-" + tokenID, Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return record.Node
	}
	visible := enroll("visible", false, "visible-token")
	hidden := enroll("hidden-secret", true, "hidden-token")
	visibleMeasurement := enroll("visible-measurement", false, "visible-measurement-token", model.NodeRoleProbe)
	hiddenMeasurement := enroll("hidden-measurement-secret", true, "hidden-measurement-token", model.NodeRoleProbe)
	probeTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{Name: "Public health", Type: model.ProbeHTTP, Target: "https://private-target.example/secret", IntervalSeconds: 60, TimeoutSeconds: 5, NodeIDs: []string{visible.ID}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{NodeID: visible.ID, TaskID: probeTask.ID, CollectedAt: now, ReceivedAt: now, Success: false, LatencyMS: 12, Value: "private-response-value", Error: "dial private-target.example"}); err != nil {
		t.Fatal(err)
	}
	hiddenTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{Name: "hidden-probe-secret", Type: model.ProbeTCP, Target: "hidden.internal:443", IntervalSeconds: 60, TimeoutSeconds: 5, NodeIDs: []string{hidden.ID}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{NodeID: hidden.ID, TaskID: hiddenTask.ID, CollectedAt: now, ReceivedAt: now, Success: true, LatencyMS: 3, Value: "hidden-probe-value"}); err != nil {
		t.Fatal(err)
	}
	latencyTask, err := repository.SaveProbeTask(ctx, model.ProbeTask{
		Name: "visible route", Type: model.ProbeTCP, Target: "private-latency-target.example:443",
		IntervalSeconds: 30, TimeoutSeconds: 2, Purpose: "latency", RunOn: model.NodeRoleProbe,
		TargetNodeID: visible.ID, Public: true, Samples: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, measurement := range []model.Node{visibleMeasurement, hiddenMeasurement} {
		if err := repository.SaveProbeResult(ctx, model.ProbeResult{
			NodeID: measurement.ID, TaskID: latencyTask.ID, CollectedAt: now, ReceivedAt: now,
			Success: false, LatencyMS: -1, LossPercent: 100, Error: "dial private-latency-target.example: network-secret",
		}); err != nil {
			t.Fatal(err)
		}
	}
	allProbeRecords, err := repository.ProbeHistory(ctx, "", 0, now.Add(-time.Minute), now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	komariRecords, err := (&API{store: repository}).filterVisibleProbeRecords(ctx, allProbeRecords, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range komariRecords {
		if record.TaskID == latencyTask.ID {
			t.Fatal("authenticated Komari probe history included Hostpin latency-matrix records")
		}
	}
	hub := core.NewHub()
	hub.Load(map[string]model.MetricSample{
		visible.ID: {NodeID: visible.ID, ReceivedAt: now, CollectedAt: now, CPU: 10},
		hidden.ID:  {NodeID: hidden.ID, ReceivedAt: now, CollectedAt: now, CPU: 99},
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	key := make([]byte, 32)
	box, _ := security.NewSecretBox(key)
	persister := core.NewPersister(repository, logger, 100)
	alerts := alerting.New(repository, hub, "http://example.test", 90*time.Second, logger)
	notifier := notification.New(repository, box, "http://example.test", logger)
	themes, _ := theme.New(repository, dataDir)
	if _, err := themes.Install(ctx, testThemeArchive(t, "ActiveTheme"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := themes.Install(ctx, testThemeArchive(t, "InactiveTheme"), "", ""); err != nil {
		t.Fatal(err)
	}
	settings, err := repository.SiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Theme = "ActiveTheme"
	if err := repository.SaveSiteSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.PublicURL = "http://example.test"
	apiServer := New(cfg, repository, hub, persister, box, alerts, notifier, themes, nil, nil, logger)
	server := httptest.NewServer(apiServer.Router())
	defer server.Close()
	allTasks, err := repository.ListAllProbeTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	carrierTasks := carrierProbeTasks(allTasks)
	if len(carrierTasks) != 3 {
		t.Fatalf("default carrier probes=%d, want 3: %#v", len(carrierTasks), carrierTasks)
	}
	for _, task := range carrierTasks {
		if task.Type != model.ProbeTCP || task.RunOn != model.NodeRoleMonitor || task.Samples != 4 || task.IntervalSeconds != 120 || !task.Enabled {
			t.Fatalf("unexpected default carrier probe: %#v", task)
		}
	}
	if err := repository.SaveProbeResult(ctx, model.ProbeResult{
		NodeID: visible.ID, TaskID: carrierTasks[0].ID, CollectedAt: now, ReceivedAt: now,
		Success: true, LatencyMS: 24.5, LossPercent: 0, Value: "private-carrier-address",
	}); err != nil {
		t.Fatal(err)
	}

	assertNoLeak := func(path string, body []byte) {
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, bytes.NewReader(body))
		if body != nil {
			request.Method = http.MethodPost
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		text := string(data)
		for _, secret := range []string{"hidden-secret", "hidden-probe-secret", "hidden-probe-value", "hidden-measurement-secret", "private-latency-target.example", "network-secret", "secret-note", "203.0.113.8", hidden.ID, hiddenMeasurement.ID} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s leaked %q: %s", path, secret, text)
			}
		}
	}
	assertNoLeak("/api/v1/public/nodes", nil)
	assertNoLeak("/api/rpc2", []byte(`{"jsonrpc":"2.0","id":1,"method":"public:getNodesInformation"}`))
	assertNoLeak("/api/task/ping", nil)
	assertNoLeak("/api/records/ping?hours=1", nil)
	assertNoLeak("/api/rpc2", []byte(`{"jsonrpc":"2.0","id":2,"method":"public:getPublicPingTasks"}`))
	assertNoLeak("/api/rpc2", []byte(`{"jsonrpc":"2.0","id":3,"method":"public:getPingRecords","params":{"hours":1}}`))
	assertNoLeak("/api/rpc2", []byte(`{"jsonrpc":"2.0","id":4,"method":"public:getPingMetricStats","params":{"hours":1}}`))
	assertNoLeak("/api/v1/public/latency", nil)

	response, err := http.Get(server.URL + "/api/v1/public/latency")
	if err != nil {
		t.Fatal(err)
	}
	latencyBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(latencyBody), "visible-measurement") || !strings.Contains(string(latencyBody), visible.ID) || !strings.Contains(string(latencyBody), "probe failed") {
		t.Fatalf("public latency matrix did not include the safe visible route: HTTP %d %s", response.StatusCode, latencyBody)
	}
	response, err = http.Get(server.URL + "/api/v1/public/latency/history?probe_node_id=" + visibleMeasurement.ID + "&target_node_id=" + visible.ID + "&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	latencyBody, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(latencyBody), "probe failed") || !strings.Contains(string(latencyBody), `"average_latency_ms":999`) || !strings.Contains(string(latencyBody), `"average_loss_percent":100`) || strings.Contains(string(latencyBody), "network-secret") {
		t.Fatalf("public latency history was not safely redacted: HTTP %d %s", response.StatusCode, latencyBody)
	}

	response, err = http.Get(server.URL + "/api/v1/public/history?node_id=" + hidden.ID + "&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden history returned HTTP %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/api/v1/public/probes?node_id=" + visible.ID + "&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	probeBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(probeBody), "private-target") || strings.Contains(string(probeBody), "private-response-value") || !strings.Contains(string(probeBody), "probe failed") {
		t.Fatalf("public probe history was not safely redacted: HTTP %d %s", response.StatusCode, probeBody)
	}
	response, err = http.Get(server.URL + "/api/v1/public/probes?node_id=" + hidden.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden probe history returned HTTP %d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/api/v1/public/probes?purpose=carrier&node_id=" + visible.ID + "&hours=1")
	if err != nil {
		t.Fatal(err)
	}
	carrierBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	carrierText := string(carrierBody)
	if response.StatusCode != http.StatusOK || !strings.Contains(carrierText, model.ProbePurposeCarrierTelecom) || !strings.Contains(carrierText, `"latency_ms":24.5`) {
		t.Fatalf("carrier history was not returned: HTTP %d %s", response.StatusCode, carrierBody)
	}
	for _, secret := range []string{"zstaticcdn.com", "private-carrier-address"} {
		if strings.Contains(carrierText, secret) {
			t.Fatalf("carrier history leaked %q: %s", secret, carrierBody)
		}
	}
	response, err = http.Get(server.URL + "/api/records/ping?hours=1")
	if err != nil {
		t.Fatal(err)
	}
	komariCarrierBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	komariCarrierText := string(komariCarrierBody)
	if response.StatusCode != http.StatusOK || !strings.Contains(komariCarrierText, "China Telecom") || strings.Count(komariCarrierText, `"value":24.5`) != 4 {
		t.Fatalf("Komari carrier records were not returned with sample semantics: HTTP %d %s", response.StatusCode, komariCarrierBody)
	}
	if strings.Contains(komariCarrierText, "zstaticcdn.com") || strings.Contains(komariCarrierText, "private-carrier-address") {
		t.Fatalf("Komari carrier records leaked a private target or value: %s", komariCarrierBody)
	}

	for _, asset := range []struct {
		path        string
		contentType string
		prefix      string
	}{
		{path: "/assets/flags/JP.svg", contentType: "image/svg+xml", prefix: "<svg"},
		{path: "/assets/logo/os-debian.svg", contentType: "image/svg+xml", prefix: "<svg"},
		{path: "/assets/logo/os-alpine.webp", contentType: "image/webp", prefix: "RIFF"},
	} {
		response, err = http.Get(server.URL + asset.path)
		if err != nil {
			t.Fatal(err)
		}
		assetBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), asset.contentType) || !strings.HasPrefix(string(assetBody), asset.prefix) {
			t.Errorf("GET %s did not return the compatibility image: HTTP %d type=%q", asset.path, response.StatusCode, response.Header.Get("Content-Type"))
		}
	}

	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/themes/ActiveTheme/dist/asset.js", want: http.StatusOK},
		{path: "/themes/ActiveTheme/source.txt", want: http.StatusNotFound},
		{path: "/themes/ActiveTheme/komari-theme.json", want: http.StatusNotFound},
		{path: "/themes/InactiveTheme/dist/asset.js", want: http.StatusNotFound},
	} {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Errorf("GET %s returned %d, want %d", test.path, response.StatusCode, test.want)
		}
	}

	response, err = http.Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(body), "ActiveTheme") {
		t.Error("the native login recovery route was replaced by an installed theme")
	}
	assetStart := strings.Index(string(body), `src="/assets/`)
	if assetStart < 0 {
		t.Fatal("native login index did not reference its embedded JavaScript")
	}
	assetStart += len(`src="`)
	assetEnd := strings.Index(string(body)[assetStart:], `"`)
	if assetEnd < 0 {
		t.Fatal("native login JavaScript URL was malformed")
	}
	response, err = http.Get(server.URL + string(body)[assetStart:assetStart+assetEnd])
	if err != nil {
		t.Fatal(err)
	}
	assetBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(response.Header.Get("Content-Type"), "javascript") || strings.Contains(string(assetBody), "ActiveTheme") {
		t.Fatal("native login JavaScript was intercepted by the active theme")
	}
}
