package httpapi

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestKomariMetricDefinitionsUseOfficialNames(t *testing.T) {
	definitions := metricDefinitions()
	required := map[string]bool{
		"cpu.usage": false, "memory.used": false, "net.in.rate": false,
		"net.total.up": false, "ping.latency_ms": false,
	}
	for _, definition := range definitions {
		name, _ := definition["name"].(string)
		if _, ok := required[name]; ok {
			required[name] = true
		}
		if name == "" || definition["type"] == nil || definition["unit"] == nil {
			t.Fatalf("incomplete metric definition: %#v", definition)
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("official metric definition %q is missing", name)
		}
	}
}

func TestKomariMetricValueAliasesAndCounterReset(t *testing.T) {
	samples := []model.MetricSample{
		{CPU: 25, MemoryUsed: 100, NetTxBytes: 1000, NetRxBytes: 2000},
		{CPU: 50, MemoryUsed: 200, NetTxBytes: 1300, NetRxBytes: 2500},
		{CPU: 75, MemoryUsed: 300, NetTxBytes: 50, NetRxBytes: 80},
	}
	checks := []struct {
		index  int
		metric string
		want   float64
	}{
		{1, "cpu.usage", 50}, {1, "cpu", 50},
		{1, "memory.used", 200}, {1, "ram", 200},
		{1, "traffic.up", 300}, {1, "traffic.down", 500},
		{2, "traffic.up", 50}, {2, "traffic.down", 80},
	}
	for _, check := range checks {
		got, ok := komariMetricValue(samples, check.index, check.metric)
		if !ok || got != check.want {
			t.Errorf("metric %s at %d = (%v, %v), want (%v, true)", check.metric, check.index, got, ok, check.want)
		}
	}
}

func TestKomariPingStatMatchesRPC2Contract(t *testing.T) {
	now := time.Now().UTC()
	task := model.ProbeTask{ID: 7, Name: "edge", Type: model.ProbeHTTP, IntervalSeconds: 30}
	records := []model.ProbeResult{
		{TaskID: 7, Success: true, LatencyMS: 10, ReceivedAt: now.Add(-time.Minute)},
		{TaskID: 7, Success: false, LatencyMS: 0, ReceivedAt: now.Add(-30 * time.Second)},
		{TaskID: 7, Success: true, LatencyMS: 30, ReceivedAt: now},
	}
	stat := komariPingStat("node-1", 7, task, records)
	for _, key := range []string{"entity_id", "task_id", "total", "valid", "loss", "avg", "latest", "p50", "p99", "stddev", "p99_p50_ratio"} {
		if _, ok := stat[key]; !ok {
			t.Fatalf("Ping stat is missing %q: %#v", key, stat)
		}
	}
	if stat["entity_id"] != "node-1" || stat["task_id"] != "7" || stat["total"] != 3 || stat["valid"] != 2 {
		t.Fatalf("unexpected Ping identity/counts: %#v", stat)
	}
	if math.Abs(stat["loss"].(float64)-100.0/3.0) > 1e-9 || stat["avg"] != 20.0 || stat["latest"] != 30.0 {
		t.Fatalf("unexpected Ping statistics: %#v", stat)
	}
}

func TestKomariEmeraldRegionAndAssetCompatibility(t *testing.T) {
	if got := komariRegion("Tokyo", "jp"); got != "JP" {
		t.Fatalf("Komari region=%q, want JP country code", got)
	}
	if got := komariRegion("US", ""); got != "US" {
		t.Fatalf("country-code region=%q, want US", got)
	}
	if got := komariRegion("🇯🇵", ""); got != "JP" {
		t.Fatalf("emoji region=%q, want normalized JP country code", got)
	}
	if got := komariRegion("Private edge", ""); got != "Private edge" {
		t.Fatalf("free-form region changed to %q", got)
	}
	flag, ok := komariFlagSVG("JP.svg")
	if !ok || !strings.Contains(flag, `id="flag-icons-jp"`) || !strings.Contains(flag, `viewBox="0 0 640 480"`) || strings.Contains(flag, "🇯🇵") || strings.Contains(flag, "<text") {
		t.Fatalf("Japanese compatibility flag was invalid: ok=%v %s", ok, flag)
	}
	if _, ok := komariFlagSVG("Tokyo.svg"); ok {
		t.Fatal("free-form text was accepted as a country asset")
	}
	logo, contentType, ok := komariLogoAsset("os-debian.svg")
	if !ok || contentType != "image/svg+xml; charset=utf-8" || !strings.Contains(string(logo), `width="72" height="72"`) || !strings.Contains(string(logo), `fill="#A80030"`) {
		t.Fatalf("CF-Server-Monitor Debian logo was invalid: ok=%v type=%q %s", ok, contentType, logo)
	}
	alpine, contentType, ok := komariLogoAsset("os-alpine.webp")
	if !ok || contentType != "image/webp" || !strings.HasPrefix(string(alpine), "RIFF") {
		t.Fatalf("CF-Server-Monitor Alpine logo was invalid: ok=%v type=%q", ok, contentType)
	}
	if _, _, ok := komariLogoAsset("../../secret.svg"); ok {
		t.Fatal("path traversal was accepted as a logo asset")
	}
}

func TestKomariPingTasksIncludeCarrierButExcludeLatencyMatrix(t *testing.T) {
	tasks := []model.ProbeTask{
		{ID: 1, Name: "Custom", Purpose: model.ProbePurposeCustom, Samples: 1},
		{ID: 2, Name: "China Telecom", Purpose: model.ProbePurposeCarrierTelecom, Samples: 4},
		{ID: 3, Name: "Latency matrix", Purpose: model.ProbePurposeLatency, Samples: 3},
	}
	listed := komariTaskList(tasks)
	if len(listed) != 2 || listed[0]["id"] != int64(1) || listed[1]["id"] != int64(2) || listed[1]["samples"] != 4 {
		t.Fatalf("unexpected Komari Ping task list: %#v", listed)
	}
	partial := model.ProbeResult{Success: true, LatencyMS: 80, LossPercent: 25}
	if successful, lost := probeSampleBreakdown(partial, tasks[1]); successful != 3 || lost != 1 {
		t.Fatalf("25%% loss expanded to %d successful/%d lost, want 3/1", successful, lost)
	}
	failed := model.ProbeResult{Success: false, LatencyMS: -1, LossPercent: 100}
	if successful, lost := probeSampleBreakdown(failed, tasks[1]); successful != 0 || lost != 4 {
		t.Fatalf("failed four-sample probe expanded to %d/%d, want 0/4", successful, lost)
	}
}

func TestKomariPingStatUsesPartialPacketLoss(t *testing.T) {
	now := time.Now().UTC()
	records := []model.ProbeResult{
		{Success: true, LatencyMS: 10, LossPercent: 25, ReceivedAt: now.Add(-time.Minute)},
		{Success: true, LatencyMS: 20, LossPercent: 0, ReceivedAt: now},
	}
	stat := komariPingStat("node-1", 8, model.ProbeTask{ID: 8}, records)
	if stat["loss"] != 12.5 {
		t.Fatalf("partial packet loss summary=%v, want 12.5", stat["loss"])
	}
}
