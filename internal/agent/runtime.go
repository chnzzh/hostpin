package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/agentconfig"
	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/collector"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/probe"
	"github.com/chnzzh/hostpin/internal/updater"
	"github.com/coder/websocket"
)

var ErrUnauthorized = errors.New("agent credential was rejected")

type Runtime struct {
	configPath string
	config     agentconfig.Config
	identity   model.AgentIdentity
	collector  *collector.Collector
	probes     *probe.Scheduler
	http       *http.Client
	logger     *slog.Logger
	clockSkew  time.Duration
	lastFull   time.Time
	pending    []model.ProbeResult
}

func New(configPath string, logger *slog.Logger) (*Runtime, error) {
	cfg, err := agentconfig.Load(configPath)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		MaxIdleConns:      4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second,
	}
	identityCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg.Role = model.NormalizeNodeRole(cfg.Role)
	scheduler := probe.NewScheduler()
	scheduler.SetConcurrency(cfg.Agent.ProbeConcurrency)
	runtime := &Runtime{
		configPath: configPath, config: cfg, identity: collector.Identity(identityCtx, buildinfo.Version),
		probes: scheduler,
		http:   &http.Client{Transport: transport, Timeout: 20 * time.Second}, logger: logger,
	}
	if cfg.Role == model.NodeRoleMonitor {
		runtime.collector = collector.New(cfg.Agent)
	}
	return runtime, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updated := make(chan updater.Result, 1)
	if r.config.Agent.AutoUpdate {
		go r.watchUpdates(runCtx, cancel, updated)
	}
	backoff := time.Second
	for {
		if err := runCtx.Err(); err != nil {
			select {
			case result := <-updated:
				r.logger.Info("signed Agent update installed; service restart requested", "version", result.Version, "rollback", result.Backup)
			default:
			}
			return nil
		}
		connectedFor, err := r.runStream(runCtx)
		if runCtx.Err() != nil {
			select {
			case result := <-updated:
				r.logger.Info("signed Agent update installed; service restart requested", "version", result.Version, "rollback", result.Backup)
			default:
			}
			return nil
		}
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		if err != nil {
			r.logger.Warn("websocket unavailable; using HTTP fallback", "error", err, "retry_in", backoff)
		}
		if connectedFor > 30*time.Second {
			backoff = time.Second
		}
		jitter := time.Duration(rand.IntN(500)) * time.Millisecond
		if fallbackErr := r.runHTTPFallback(runCtx, backoff+jitter); fallbackErr != nil && !errors.Is(fallbackErr, context.Canceled) {
			if errors.Is(fallbackErr, ErrUnauthorized) {
				return fallbackErr
			}
			r.logger.Warn("HTTP fallback report failed", "error", fallbackErr)
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (r *Runtime) runHTTPFallback(ctx context.Context, duration time.Duration) error {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	interval := time.NewTicker(r.collectInterval())
	defer interval.Stop()
	var lastErr error
	for {
		lastErr = r.reportHTTP(ctx)
		if errors.Is(lastErr, ErrUnauthorized) || errors.Is(lastErr, context.Canceled) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return lastErr
		case <-interval.C:
		}
	}
}

func (r *Runtime) watchUpdates(ctx context.Context, cancel context.CancelFunc, applied chan<- updater.Result) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Minute)
		result, err := updater.CheckAndApply(checkCtx, buildinfo.Version)
		checkCancel()
		if err != nil {
			r.logger.Warn("signed automatic update check failed", "error", err)
		} else if result.Updated {
			applied <- result
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) runStream(ctx context.Context) (time.Duration, error) {
	started := time.Now()
	streamURL, err := agentURL(r.config.Endpoint, "/api/v1/agent/stream", true)
	if err != nil {
		return 0, err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + r.config.Token}, "User-Agent": []string{"Hostpin-Agent/" + buildinfo.Version}}
	connection, response, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{
		HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return 0, ErrUnauthorized
		}
		return 0, err
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	connection.SetReadLimit(2 << 20)

	ack, err := readAck(ctx, connection)
	if err != nil {
		return time.Since(started), err
	}
	r.applyAck(ack, true)
	hello := model.AgentFrame{Type: "hello", Hello: &model.AgentHello{
		Type: "hello", ProtocolVersion: model.ProtocolVersion, InstallID: r.config.InstallID,
		Version: buildinfo.Version, ConfigVersion: r.config.Agent.ConfigVersion, Identity: r.identity,
	}}
	if err := writeFrame(ctx, connection, hello); err != nil {
		return time.Since(started), err
	}
	if ack, err = readAck(ctx, connection); err != nil {
		return time.Since(started), err
	}
	r.applyAck(ack, false)

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return time.Since(started), nil
		case <-timer.C:
		}
		r.probes.Tick(ctx, time.Now())
		frame := model.AgentFrame{Type: "heartbeat"}
		if r.config.Role == model.NodeRoleMonitor {
			sample, collectErr := r.collect(ctx)
			if collectErr != nil {
				r.logger.Debug("metric collection was partial", "error", collectErr)
			}
			frame = model.AgentFrame{Type: "sample", Sample: &sample}
		}
		if err := writeFrame(ctx, connection, frame); err != nil {
			return time.Since(started), err
		}
		ack, err := readAck(ctx, connection)
		if err != nil {
			return time.Since(started), err
		}
		r.applyAck(ack, false)
		r.collectProbeResults(128)
		for len(r.pending) > 0 {
			result := r.pending[0]
			if err := writeFrame(ctx, connection, model.AgentFrame{Type: "probe_result", ProbeResult: &result}); err != nil {
				return time.Since(started), err
			}
			ack, err = readAck(ctx, connection)
			if err != nil {
				return time.Since(started), err
			}
			r.applyAck(ack, false)
			r.pending = r.pending[1:]
		}
		timer.Reset(r.collectInterval())
	}
}

func (r *Runtime) reportHTTP(ctx context.Context) error {
	r.probes.Tick(ctx, time.Now())
	var sample *model.MetricSample
	if r.config.Role == model.NodeRoleMonitor {
		collected, _ := r.collect(ctx)
		sample = &collected
	}
	r.collectProbeResults(128)
	probeCount := len(r.pending)
	report := model.AgentReport{Identity: r.identity, Sample: sample, ProbeResults: append([]model.ProbeResult(nil), r.pending...)}
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	reportURL, err := agentURL(r.config.Endpoint, "/api/v1/agent/reports", false)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, reportURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.config.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Hostpin-Agent/"+buildinfo.Version)
	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var ack model.AgentAck
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&ack); err != nil {
		return err
	}
	r.applyAck(ack, true)
	r.pending = r.pending[probeCount:]
	return nil
}

func (r *Runtime) collect(ctx context.Context) (model.MetricSample, error) {
	persistInterval := time.Duration(max(r.config.Agent.PersistIntervalSeconds, 1)) * time.Second
	full := r.lastFull.IsZero() || time.Since(r.lastFull) >= persistInterval
	sample, err := r.collector.Collect(ctx, full)
	if full {
		r.lastFull = time.Now()
	}
	sample.ClockOffsetMS = r.clockSkew.Milliseconds()
	return sample, err
}

func (r *Runtime) applyAck(ack model.AgentAck, syncTasks bool) {
	if !ack.ServerTime.IsZero() {
		r.clockSkew = ack.ServerTime.Sub(time.Now().UTC())
	}
	if ack.Config.CollectIntervalSeconds > 0 && ack.Config.ConfigVersion >= r.config.Agent.ConfigVersion {
		changed := ack.Config.ConfigVersion != r.config.Agent.ConfigVersion
		r.config.Agent = ack.Config
		if r.collector != nil {
			r.collector.UpdateConfig(ack.Config)
		}
		r.probes.SetConcurrency(ack.Config.ProbeConcurrency)
		if changed {
			if err := agentconfig.Save(r.configPath, r.config); err != nil {
				r.logger.Warn("could not persist dynamic agent configuration", "error", err)
			}
		}
	}
	if syncTasks || ack.Tasks != nil {
		r.probes.Sync(ack.Tasks)
	}
}

func (r *Runtime) collectInterval() time.Duration {
	return time.Duration(max(r.config.Agent.CollectIntervalSeconds, 1)) * time.Second
}

func (r *Runtime) collectProbeResults(limit int) {
	for len(r.pending) < limit {
		select {
		case result := <-r.probes.Results():
			r.pending = append(r.pending, result)
		default:
			return
		}
	}
}

func agentURL(endpoint, path string, websocketURL bool) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(endpoint, "/") + path)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid Hostpin endpoint")
	}
	if websocketURL {
		switch parsed.Scheme {
		case "https":
			parsed.Scheme = "wss"
		case "http":
			parsed.Scheme = "ws"
		default:
			return "", errors.New("invalid Hostpin endpoint scheme")
		}
	}
	return parsed.String(), nil
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
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, payload, err := connection.Read(readCtx)
	if err != nil {
		return model.AgentAck{}, err
	}
	var ack model.AgentAck
	if err := json.Unmarshal(payload, &ack); err != nil {
		return model.AgentAck{}, err
	}
	if !ack.Accepted && ack.Error != "" {
		return ack, errors.New(ack.Error)
	}
	return ack, nil
}
