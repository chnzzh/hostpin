// Command hostpin-load drives virtual Agents against an initialized test site.
// It is intentionally kept outside the production commands and reads the
// enrollment PIN only from HOSTPIN_LOAD_PIN.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type virtualAgent struct {
	nodeID    string
	installID string
	token     string
}

type loadReport struct {
	Status                 string  `json:"status"`
	Nodes                  int     `json:"nodes"`
	DurationSeconds        float64 `json:"duration_seconds"`
	Reports                uint64  `json:"reports"`
	VisibleLatencySeconds  float64 `json:"visible_latency_seconds"`
	LatestAPIP95Millis     float64 `json:"latest_api_p95_ms"`
	HistoryPoints          int     `json:"history_points"`
	TrafficRXBytes         uint64  `json:"traffic_rx_bytes"`
	TrafficTXBytes         uint64  `json:"traffic_tx_bytes"`
	PersistenceDegraded    bool    `json:"persistence_degraded"`
	DroppedPersistenceRows uint64  `json:"dropped_persistence_rows"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hostpin-load:", err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "http://127.0.0.1:18082", "initialized Hostpin test endpoint")
	nodeCount := flag.Int("nodes", 100, "number of virtual Agents")
	duration := flag.Duration("duration", 65*time.Second, "live reporting duration")
	interval := flag.Duration("interval", 3*time.Second, "report interval")
	enrollmentWorkers := flag.Int("enrollment-workers", 4, "parallel enrollment requests")
	maxP95 := flag.Duration("max-p95", 300*time.Millisecond, "maximum latest-state API p95")
	flag.Parse()
	base, err := normalizeEndpoint(*endpoint)
	if err != nil {
		return err
	}
	pin := strings.TrimSpace(os.Getenv("HOSTPIN_LOAD_PIN"))
	if len(pin) < 6 {
		return errors.New("HOSTPIN_LOAD_PIN must contain at least 6 characters")
	}
	if *nodeCount < 1 || *nodeCount > 5000 || *duration < time.Second || *interval < 100*time.Millisecond {
		return errors.New("nodes, duration, or interval is outside the test safety bounds")
	}
	workers := min(max(*enrollmentWorkers, 1), *nodeCount)
	transport := &http.Transport{MaxIdleConns: workers * 2, MaxIdleConnsPerHost: workers, IdleConnTimeout: time.Minute}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*nodeCount)*time.Second+*duration+3*time.Minute)
	defer cancel()
	agents, err := enrollAgents(ctx, client, base, pin, *nodeCount, workers)
	pin = ""
	if err != nil {
		return err
	}

	start := make(chan struct{})
	ready := make(chan error, len(agents))
	done := make(chan error, len(agents))
	var reports atomic.Uint64
	for index, agent := range agents {
		go runVirtualAgent(ctx, base, index, agent, *duration, *interval, start, ready, done, &reports)
	}
	for range agents {
		if err := <-ready; err != nil {
			return fmt.Errorf("connect virtual Agent: %w", err)
		}
	}
	started := time.Now()
	close(start)
	visibleLatency, err := waitUntilVisible(ctx, client, base, agents, 5*time.Second)
	if err != nil {
		return err
	}
	for range agents {
		if err := <-done; err != nil {
			return fmt.Errorf("report virtual Agent: %w", err)
		}
	}

	p95, err := latestP95(ctx, client, base, agents, 25)
	if err != nil {
		return err
	}
	if p95 > *maxP95 {
		return fmt.Errorf("latest-state API p95 %s exceeds %s", p95, *maxP95)
	}
	historyPoints, err := waitForHistory(ctx, client, base, agents[0].nodeID, map[bool]int{true: 2, false: 1}[*duration >= time.Minute], 20*time.Second)
	if err != nil {
		return err
	}
	trafficRX, trafficTX, err := latestTraffic(ctx, client, base, agents[0].nodeID)
	if err != nil {
		return err
	}
	health, err := readHealth(ctx, client, base)
	if err != nil {
		return err
	}
	if health.PersistenceDegraded || health.Dropped > 0 {
		return fmt.Errorf("persistence degraded=%v dropped=%d", health.PersistenceDegraded, health.Dropped)
	}
	report := loadReport{
		Status: "ok", Nodes: len(agents), DurationSeconds: time.Since(started).Seconds(), Reports: reports.Load(),
		VisibleLatencySeconds: visibleLatency.Seconds(), LatestAPIP95Millis: float64(p95.Microseconds()) / 1000,
		HistoryPoints: historyPoints, TrafficRXBytes: trafficRX, TrafficTXBytes: trafficTX,
		PersistenceDegraded: health.PersistenceDegraded, DroppedPersistenceRows: health.Dropped,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func latestTraffic(ctx context.Context, client *http.Client, base, nodeID string) (uint64, uint64, error) {
	nodes, _, err := readNodes(ctx, client, base)
	if err != nil {
		return 0, 0, err
	}
	for _, snapshot := range nodes {
		if snapshot.Node.ID != nodeID || snapshot.Metric == nil {
			continue
		}
		rx, tx := snapshot.Metric.MonthlyRxBytes, snapshot.Metric.MonthlyTxBytes
		if rx == 0 || tx == 0 {
			return 0, 0, fmt.Errorf("monthly traffic did not accumulate for %s: rx=%d tx=%d", nodeID, rx, tx)
		}
		if rx > snapshot.Metric.NetRxBytes || tx > snapshot.Metric.NetTxBytes {
			return 0, 0, fmt.Errorf("monthly traffic exceeds source counters for %s: monthly=%d/%d total=%d/%d", nodeID, rx, tx, snapshot.Metric.NetRxBytes, snapshot.Metric.NetTxBytes)
		}
		return rx, tx, nil
	}
	return 0, 0, fmt.Errorf("traffic sample for %s was not found", nodeID)
}

func normalizeEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return parsed.String(), nil
}

func enrollAgents(ctx context.Context, client *http.Client, base, pin string, count, workers int) ([]virtualAgent, error) {
	agents := make([]virtualAgent, count)
	jobs := make(chan int)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				agent, err := enrollAgent(ctx, client, base, pin, index)
				if err != nil {
					select {
					case errorsFound <- err:
					default:
					}
					continue
				}
				agents[index] = agent
			}
		}()
	}
	for index := range count {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	close(errorsFound)
	if err := <-errorsFound; err != nil {
		return nil, err
	}
	return agents, nil
}

func enrollAgent(ctx context.Context, client *http.Client, base, pin string, index int) (virtualAgent, error) {
	token, _, _, err := security.NewAgentToken()
	if err != nil {
		return virtualAgent{}, err
	}
	installID := uuid.NewString()
	payload := model.EnrollmentRequest{
		PIN: pin, InstallID: installID, Token: token,
		Identity: model.AgentIdentity{Version: "load-test", OS: "linux", Arch: "amd64", Hostname: fmt.Sprintf("load-%04d", index), CPUCores: 2},
		Metadata: model.EnrollmentMetadata{Name: fmt.Sprintf("load-%04d", index), Group: "capacity", Tags: []string{"load-test"}, Currency: "USD", TrafficLimitType: "sum", TrafficResetDay: 1},
		Config:   model.DefaultAgentConfig(),
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/enrollments", bytes.NewReader(body))
	if err != nil {
		return virtualAgent{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return virtualAgent{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return virtualAgent{}, fmt.Errorf("enrollment %d returned %s: %s", index, response.Status, strings.TrimSpace(string(message)))
	}
	var enrolled model.EnrollmentResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&enrolled); err != nil {
		return virtualAgent{}, err
	}
	return virtualAgent{nodeID: enrolled.NodeID, installID: installID, token: token}, nil
}

func runVirtualAgent(ctx context.Context, base string, index int, agent virtualAgent, duration, interval time.Duration, start <-chan struct{}, ready, done chan<- error, reports *atomic.Uint64) {
	streamURL := strings.Replace(base, "http://", "ws://", 1)
	streamURL = strings.Replace(streamURL, "https://", "wss://", 1) + "/api/v1/agent/stream"
	headers := http.Header{"Authorization": []string{"Bearer " + agent.token}}
	connection, response, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil {
			err = fmt.Errorf("%w (HTTP %d)", err, response.StatusCode)
		}
		ready <- err
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "capacity test complete")
	if _, err = readAck(ctx, connection); err == nil {
		hello := model.AgentFrame{Type: "hello", Hello: &model.AgentHello{
			Type: "hello", ProtocolVersion: model.ProtocolVersion, InstallID: agent.installID,
			Version: "load-test", ConfigVersion: 1,
			Identity: model.AgentIdentity{Version: "load-test", OS: "linux", Arch: "amd64", Hostname: fmt.Sprintf("load-%04d", index), CPUCores: 2},
		}}
		err = writeFrame(ctx, connection, hello)
	}
	if err == nil {
		_, err = readAck(ctx, connection)
	}
	ready <- err
	if err != nil {
		return
	}
	select {
	case <-ctx.Done():
		done <- ctx.Err()
		return
	case <-start:
	}
	jitter := time.NewTimer(time.Duration(index%1000) * time.Millisecond)
	select {
	case <-ctx.Done():
		jitter.Stop()
		done <- ctx.Err()
		return
	case <-jitter.C:
	}
	deadline := time.NewTimer(duration)
	ticker := time.NewTicker(interval)
	defer deadline.Stop()
	defer ticker.Stop()
	sequence := uint64(0)
	for {
		sequence++
		now := time.Now().UTC()
		sample := model.MetricSample{
			Sequence: sequence, CollectedAt: now, BootID: agent.installID,
			CPU: float64((index + int(sequence)) % 100), Load1: float64(index%20) / 10,
			MemoryTotal: 4 << 30, MemoryUsed: uint64(1<<30) + uint64(index)*1024,
			DiskTotal: 100 << 30, DiskUsed: 40 << 30,
			NetRxBPS: 1024, NetTxBPS: 512, NetRxBytes: 1_000_000 + sequence*3072, NetTxBytes: 500_000 + sequence*1536,
			UptimeSeconds: uint64(time.Hour.Seconds()) + sequence*uint64(interval.Seconds()),
		}
		if err := writeFrame(ctx, connection, model.AgentFrame{Type: "sample", Sample: &sample}); err != nil {
			done <- err
			return
		}
		if _, err := readAck(ctx, connection); err != nil {
			done <- err
			return
		}
		reports.Add(1)
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-deadline.C:
			done <- nil
			return
		case <-ticker.C:
		}
	}
}

func writeFrame(ctx context.Context, connection *websocket.Conn, frame model.AgentFrame) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return connection.Write(writeCtx, websocket.MessageText, payload)
}

func readAck(ctx context.Context, connection *websocket.Conn) (model.AgentAck, error) {
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, payload, err := connection.Read(readCtx)
	if err != nil {
		return model.AgentAck{}, err
	}
	var ack model.AgentAck
	if err := json.Unmarshal(payload, &ack); err != nil {
		return ack, err
	}
	if !ack.Accepted {
		return ack, errors.New(firstNonEmpty(ack.Error, "server rejected Agent frame"))
	}
	return ack, nil
}

func waitUntilVisible(ctx context.Context, client *http.Client, base string, agents []virtualAgent, timeout time.Duration) (time.Duration, error) {
	wanted := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		wanted[agent.nodeID] = struct{}{}
	}
	started := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		nodes, _, err := readNodes(ctx, client, base)
		if err == nil {
			visible := 0
			for _, node := range nodes {
				if _, ok := wanted[node.Node.ID]; ok && node.Node.Online && node.Metric != nil {
					visible++
				}
			}
			if visible == len(wanted) {
				return time.Since(started), nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, errors.New("not all virtual Agents became visible within 5 seconds")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func latestP95(ctx context.Context, client *http.Client, base string, agents []virtualAgent, requests int) (time.Duration, error) {
	durations := make([]time.Duration, 0, requests)
	for range requests {
		nodes, elapsed, err := readNodes(ctx, client, base)
		if err != nil {
			return 0, err
		}
		if len(nodes) < len(agents) {
			return 0, fmt.Errorf("latest-state API returned %d nodes, expected at least %d", len(nodes), len(agents))
		}
		durations = append(durations, elapsed)
	}
	slices.Sort(durations)
	index := max(0, int(math.Ceil(float64(len(durations))*0.95))-1)
	return durations[index], nil
}

func readNodes(ctx context.Context, client *http.Client, base string) ([]model.NodeSnapshot, time.Duration, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/public/nodes", nil)
	started := time.Now()
	response, err := client.Do(request)
	elapsed := time.Since(started)
	if err != nil {
		return nil, elapsed, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, elapsed, fmt.Errorf("latest-state API returned %s", response.Status)
	}
	var envelope model.Envelope[[]model.NodeSnapshot]
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&envelope); err != nil {
		return nil, elapsed, err
	}
	return envelope.Data, elapsed, nil
}

func waitForHistory(ctx context.Context, client *http.Client, base, nodeID string, minimum int, timeout time.Duration) (int, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/public/history?node_id="+url.QueryEscape(nodeID)+"&hours=1", nil)
		response, err := client.Do(request)
		if err == nil {
			var envelope model.Envelope[[]model.MetricSample]
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && len(envelope.Data) >= minimum {
				return len(envelope.Data), nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return 0, fmt.Errorf("history did not reach %d points", minimum)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type healthResponse struct {
	PersistenceDegraded bool   `json:"persistence_degraded"`
	Dropped             uint64 `json:"dropped"`
}

func readHealth(ctx context.Context, client *http.Client, base string) (healthResponse, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	response, err := client.Do(request)
	if err != nil {
		return healthResponse{}, err
	}
	defer response.Body.Close()
	var result healthResponse
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	return result, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
