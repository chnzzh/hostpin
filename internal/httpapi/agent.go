package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func (a *API) handleEnrollment(w http.ResponseWriter, r *http.Request) {
	complete, err := a.store.SetupComplete(r.Context())
	if err != nil || !complete {
		writeError(w, http.StatusPreconditionRequired, "setup_required", "Hostpin must be configured before agents can enroll")
		return
	}
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil || !settings.EnrollmentEnabled {
		writeError(w, http.StatusForbidden, "enrollment_disabled", "agent enrollment is disabled")
		return
	}
	ip := a.clientIP(r)
	if !a.enrollmentSourceAllowed(ip) {
		writeError(w, http.StatusForbidden, "source_not_allowed", "this network is not allowed to enroll agents")
		return
	}
	now := time.Now().UTC()
	if allowed, retryAfter := a.limiter.Allow(ip, now); !allowed {
		w.Header().Set("Retry-After", durationSeconds(retryAfter))
		writeError(w, http.StatusTooManyRequests, "enrollment_rate_limited", "too many failed enrollment attempts")
		return
	}
	var request model.EnrollmentRequest
	if !decodeJSON(w, r, &request, 256<<10) {
		return
	}
	if err := validateEnrollment(request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_enrollment", err.Error())
		return
	}
	request.Role = model.NormalizeNodeRole(request.Role)
	temporaryPINID, authorized := a.authorizeEnrollmentPIN(r.Context(), request.PIN, now)
	if !authorized {
		a.rejectEnrollmentPIN(w, r, request.InstallID, ip, now)
		return
	}
	tokenID, tokenHash, err := security.ParseAgentToken(request.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_token", "agent-generated credential is invalid")
		return
	}
	locationManual := strings.TrimSpace(request.Metadata.Region) != "" || request.Metadata.CountryCode != "" || request.Metadata.Latitude != nil || request.Metadata.Longitude != nil
	a.geoip.Configure(settings.GeoIPEnabled, settings.GeoIPProvider)
	if !locationManual {
		if location, lookupErr := a.geoip.Lookup(r.Context(), ip); lookupErr == nil {
			request.Metadata.CountryCode = location.CountryCode
			request.Metadata.Region = firstNonEmpty(request.Metadata.Region, location.Region, location.City)
			request.Metadata.Latitude = &location.Latitude
			request.Metadata.Longitude = &location.Longitude
		}
	}
	nodeUUID, err := uuid.NewV7()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create node identity")
		return
	}
	record, err := a.store.EnrollNode(r.Context(), store.EnrollParams{
		Request: request, NodeID: nodeUUID.String(), TokenID: tokenID,
		TokenHash: tokenHash, SourceIP: ip, LocationManual: locationManual,
		TemporaryPINID: temporaryPINID, Now: now,
	})
	if errors.Is(err, store.ErrTemporaryPINUnavailable) {
		a.rejectEnrollmentPIN(w, r, request.InstallID, ip, now)
		return
	}
	if errors.Is(err, store.ErrInstallConflict) {
		writeError(w, http.StatusConflict, "install_conflict", "this installation identity is already bound to another credential")
		return
	}
	if err != nil {
		a.logger.Error("enroll agent", "error", err, "install_id", request.InstallID)
		writeError(w, http.StatusInternalServerError, "enrollment_failed", "could not enroll agent")
		return
	}
	a.limiter.Success(ip)
	request.PIN, request.Token = "", ""
	_ = a.store.AppendAudit(r.Context(), "enrollment", "node.enroll", record.Node.ID, record.Node.Name+" from "+ip, now)
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[record.Created], model.EnrollmentResponse{
		NodeID: record.Node.ID, Role: record.Node.Role, ProtocolVersion: model.ProtocolVersion,
		StreamURL: publicBase(a.cfg) + "/api/v1/agent/stream",
		ReportURL: publicBase(a.cfg) + "/api/v1/agent/reports",
		Config:    record.Config, Created: record.Created,
	})
}

func (a *API) authorizeEnrollmentPIN(ctx context.Context, pin string, now time.Time) (string, bool) {
	if hash, err := a.store.GetSetting(ctx, "enrollment_pin_hash"); err == nil && security.VerifyHash(hash, pin) {
		return "", true
	}
	temporary, err := a.store.ActiveTemporaryEnrollmentPIN(ctx, now)
	if err == nil && security.VerifyHash(temporary.PINHash, pin) {
		return temporary.ID, true
	}
	return "", false
}

func (a *API) rejectEnrollmentPIN(w http.ResponseWriter, r *http.Request, installID, ip string, now time.Time) {
	paused := a.limiter.Failure(ip, now)
	detail := "invalid PIN from " + ip
	if paused {
		detail += "; global enrollment circuit breaker opened"
		a.recordEnrollmentCircuitBreaker(r.Context(), ip, now)
	}
	_ = a.store.AppendAudit(r.Context(), "anonymous", "enrollment.failed", installID, detail, now)
	time.Sleep(250 * time.Millisecond)
	writeError(w, http.StatusUnauthorized, "invalid_pin", "invalid enrollment PIN")
}

func (a *API) recordEnrollmentCircuitBreaker(ctx context.Context, sourceIP string, now time.Time) {
	eventID, err := uuid.NewV7()
	if err != nil {
		return
	}
	event := model.AlertEvent{
		ID: eventID.String(), Type: "security.enrollment", Status: model.AlertFiring, Severity: "critical",
		OccurredAt: now, Node: model.PublicNode{ID: "system", Name: "Hostpin control plane"},
		Metric: "failed_enrollments", Value: 100, Threshold: 100,
		Link:    publicBase(a.cfg) + "/admin/audit",
		Message: "Enrollment was temporarily paused after abnormal PIN failures; last source " + sourceIP,
	}
	if err := a.store.SaveAlertEvent(ctx, event, nil); err != nil {
		a.logger.Error("save enrollment security event", "error", err)
		return
	}
	if err := a.store.EnqueueNotificationDeliveries(ctx, event.ID, now); err != nil {
		a.logger.Error("queue enrollment security notification", "error", err)
	}
}

func validateEnrollment(request model.EnrollmentRequest) error {
	if len(request.PIN) < 6 || len(request.PIN) > 64 {
		return errors.New("PIN must contain 6 to 64 characters")
	}
	if _, err := uuid.Parse(request.InstallID); err != nil {
		return errors.New("install_id must be a UUID")
	}
	if _, _, err := security.ParseAgentToken(request.Token); err != nil {
		return errors.New("token must be a Hostpin agent token")
	}
	if request.Role != "" && request.Role != model.NodeRoleMonitor && request.Role != model.NodeRoleProbe {
		return errors.New("role must be monitor or probe")
	}
	if len(request.Metadata.Name) > 128 || len(request.Metadata.Group) > 128 ||
		len(request.Metadata.Region) > 128 || len(request.Metadata.PublicRemark) > 4096 ||
		len(request.Metadata.PrivateRemark) > 4096 || len(request.Metadata.Tags) > 64 {
		return errors.New("one or more metadata fields exceed their limits")
	}
	if request.Metadata.CountryCode != "" {
		country := strings.ToUpper(strings.TrimSpace(request.Metadata.CountryCode))
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return errors.New("country_code must contain two ASCII letters")
		}
	}
	for _, tag := range request.Metadata.Tags {
		if len(tag) == 0 || len(tag) > 64 {
			return errors.New("tags must contain 1 to 64 characters")
		}
	}
	if request.Metadata.Latitude != nil && (*request.Metadata.Latitude < -90 || *request.Metadata.Latitude > 90) {
		return errors.New("latitude is out of range")
	}
	if request.Metadata.Longitude != nil && (*request.Metadata.Longitude < -180 || *request.Metadata.Longitude > 180) {
		return errors.New("longitude is out of range")
	}
	if (request.Metadata.Latitude == nil) != (request.Metadata.Longitude == nil) {
		return errors.New("latitude and longitude must be supplied together")
	}
	if math.IsNaN(request.Metadata.Price) || math.IsInf(request.Metadata.Price, 0) || request.Metadata.Price < 0 || request.Metadata.Price > 1_000_000_000 {
		return errors.New("price must be a finite non-negative value")
	}
	if request.Metadata.BillingCycleDays < 0 || request.Metadata.BillingCycleDays > 3650 || request.Metadata.TrafficLimit < 0 || request.Metadata.TrafficResetDay < 0 || request.Metadata.TrafficResetDay > 31 {
		return errors.New("billing or traffic metadata is out of range")
	}
	if request.Metadata.TrafficLimitType != "" && !slices.Contains([]string{"sum", "max", "up", "down"}, request.Metadata.TrafficLimitType) {
		return errors.New("traffic_limit_type must be sum, max, up, or down")
	}
	if len(request.Metadata.Currency) > 8 {
		return errors.New("currency must not exceed 8 characters")
	}
	if len(request.Identity.Version) > 128 || len(request.Identity.OS) > 128 || len(request.Identity.Arch) > 64 ||
		len(request.Identity.Hostname) > 255 || len(request.Identity.CPUName) > 512 || len(request.Identity.Virtualization) > 128 ||
		len(request.Identity.KernelVersion) > 512 || request.Identity.CPUCores < 0 || request.Identity.CPUCores > 65536 {
		return errors.New("one or more identity fields exceed their limits")
	}
	if request.Identity.IPv4 != "" {
		address, err := netip.ParseAddr(request.Identity.IPv4)
		if err != nil || !address.Is4() {
			return errors.New("ipv4 must be a valid IPv4 address")
		}
	}
	if request.Identity.IPv6 != "" {
		address, err := netip.ParseAddr(request.Identity.IPv6)
		if err != nil || !address.Is6() {
			return errors.New("ipv6 must be a valid IPv6 address")
		}
	}
	if err := validateAgentConfig(request.Config, true); err != nil {
		return err
	}
	return nil
}

func (a *API) authenticateAgent(r *http.Request) (model.Node, model.AgentConfig, error) {
	header := r.Header.Get("Authorization")
	scheme, raw, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return model.Node{}, model.AgentConfig{}, store.ErrUnauthorized
	}
	tokenID, tokenHash, err := security.ParseAgentToken(strings.TrimSpace(raw))
	if err != nil {
		return model.Node{}, model.AgentConfig{}, store.ErrUnauthorized
	}
	return a.store.AuthenticateAgent(r.Context(), tokenID, tokenHash)
}

func (a *API) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	node, cfg, err := a.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_agent_token", "agent authentication failed")
		return
	}
	var report model.AgentReport
	if !decodeJSON(w, r, &report, 2<<20) {
		return
	}
	now := time.Now().UTC()
	persisted := false
	if node.Role == model.NodeRoleMonitor {
		if report.Sample == nil {
			writeError(w, http.StatusBadRequest, "sample_required", "monitoring nodes must include a metric sample")
			return
		}
		persisted = a.acceptSample(r.Context(), node, cfg, report.Identity, *report.Sample, a.clientIP(r), now)
	} else {
		_ = a.store.UpdateAgentSeen(r.Context(), node.ID, report.Identity, now, a.clientIP(r))
	}
	tasks, _ := a.store.ListProbeTasks(r.Context(), node.ID)
	for _, result := range report.ProbeResults {
		if probeTaskAllowed(tasks, result.TaskID) {
			a.acceptProbe(node, result, now)
		}
	}
	writeJSON(w, http.StatusAccepted, model.AgentAck{
		Type: "ack", Accepted: true, Persisted: persisted, ServerTime: now,
		NextReportMS: cfg.CollectIntervalSeconds * 1000, Config: cfg, Tasks: tasks,
	})
}

func (a *API) acceptSample(ctx context.Context, node model.Node, cfg model.AgentConfig, identity model.AgentIdentity, sample model.MetricSample, sourceIP string, now time.Time) bool {
	sample.NodeID, sample.ReceivedAt = node.ID, now
	if sample.CollectedAt.IsZero() {
		sample.CollectedAt = now
	}
	rawSample := a.traffic.Apply(node, sample, now)
	sample = a.traffic.Correct(node.ID, rawSample)
	persistEvery := time.Duration(max(cfg.PersistIntervalSeconds, 1)) * time.Second
	checkpoint := a.hub.Publish(sample, persistEvery)
	persist := checkpoint && a.recordEnabled.Load()
	if persist {
		a.persister.EnqueueMetric(rawSample)
	}
	if checkpoint && (identity.Version != "" || identity.Hostname != "") {
		if sourceIP != "" && sourceIP != node.SourceIP {
			a.refreshAutomaticLocation(ctx, &node, sourceIP, now)
		}
		_ = a.store.UpdateAgentSeen(ctx, node.ID, identity, now, sourceIP)
	}
	a.alerts.EvaluateSample(node, sample)
	return persist
}

func (a *API) acceptProbe(node model.Node, result model.ProbeResult, now time.Time) {
	result.NodeID, result.ReceivedAt = node.ID, now
	if a.recordEnabled.Load() {
		a.persister.EnqueueProbe(result)
	}
	a.alerts.EvaluateProbe(node, result)
}

func (a *API) refreshAutomaticLocation(ctx context.Context, node *model.Node, sourceIP string, now time.Time) {
	if node.LocationManual {
		return
	}
	settings, err := a.store.SiteSettings(ctx)
	if err != nil || !settings.GeoIPEnabled {
		return
	}
	a.geoip.Configure(settings.GeoIPEnabled, settings.GeoIPProvider)
	location, err := a.geoip.Lookup(ctx, sourceIP)
	if err != nil {
		return
	}
	node.CountryCode = location.CountryCode
	node.Region = firstNonEmpty(node.Region, location.Region, location.City)
	node.Latitude, node.Longitude = &location.Latitude, &location.Longitude
	if err := a.store.UpdateNode(ctx, *node); err == nil {
		_ = a.store.AppendAudit(ctx, "agent", "node.location.auto", node.ID, "public IP changed", now)
	}
}

func (a *API) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	node, cfg, err := a.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_agent_token", "agent authentication failed")
		return
	}
	tasks, _ := a.store.ListProbeTasks(r.Context(), node.ID)
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "tasks": tasks, "server_time": time.Now().UTC()})
}

func (a *API) handleAgentStream(w http.ResponseWriter, r *http.Request) {
	node, cfg, err := a.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_agent_token", "agent authentication failed")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	connection.SetReadLimit(2 << 20)
	revoked, unregister := a.registerAgentStream(node.ID)
	defer unregister()
	ctx, cancel := a.connectionContext(r.Context())
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
		case <-revoked:
			cancel()
		}
	}()
	if _, err := a.store.GetNode(ctx, node.ID); err != nil {
		return
	}
	tasks, _ := a.store.ListProbeTasks(ctx, node.ID)
	lastConfigRefresh := time.Now().UTC()
	if err := writeAgentAck(ctx, connection, model.AgentAck{
		Type: "hello", Accepted: true, ServerTime: time.Now().UTC(), Config: cfg,
		Tasks: tasks, NextReportMS: cfg.CollectIntervalSeconds * 1000,
	}); err != nil {
		return
	}
	var identity model.AgentIdentity
	lastSeenUpdate := time.Time{}
	presenceInterval := probePresenceInterval(a.cfg.Runtime.OfflineAfter)
	for {
		messageType, payload, err := connection.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText && messageType != websocket.MessageBinary {
			continue
		}
		var frame model.AgentFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			_ = writeAgentAck(ctx, connection, model.AgentAck{Type: "error", Accepted: false, ServerTime: time.Now().UTC(), Error: "invalid frame"})
			continue
		}
		now := time.Now().UTC()
		persisted := false
		var refreshedTasks []model.ProbeTask
		if now.Sub(lastConfigRefresh) >= 15*time.Second {
			lastConfigRefresh = now
			if currentNode, nodeErr := a.store.GetNode(ctx, node.ID); nodeErr == nil {
				node = currentNode
			}
			if currentConfig, configErr := a.store.AgentConfig(ctx, node.ID); configErr == nil {
				cfg = currentConfig
			}
			if current, taskErr := a.store.ListProbeTasks(ctx, node.ID); taskErr == nil {
				tasks = current
				refreshedTasks = tasks
			}
		}
		switch frame.Type {
		case "hello":
			if frame.Hello != nil {
				identity = frame.Hello.Identity
				identity.Version = firstNonEmpty(identity.Version, frame.Hello.Version)
				_ = a.store.UpdateAgentSeen(ctx, node.ID, identity, now, a.clientIP(r))
				lastSeenUpdate = now
			}
		case "sample":
			if frame.Sample == nil || node.Role != model.NodeRoleMonitor {
				continue
			}
			persisted = a.acceptSample(ctx, node, cfg, identity, *frame.Sample, a.clientIP(r), now)
		case "heartbeat":
			if node.Role != model.NodeRoleProbe {
				continue
			}
			if lastSeenUpdate.IsZero() || now.Sub(lastSeenUpdate) >= presenceInterval {
				_ = a.store.UpdateAgentSeen(ctx, node.ID, identity, now, a.clientIP(r))
				lastSeenUpdate = now
			}
		case "probe_result":
			if frame.ProbeResult == nil || !probeTaskAllowed(tasks, frame.ProbeResult.TaskID) {
				continue
			}
			a.acceptProbe(node, *frame.ProbeResult, now)
			if node.Role == model.NodeRoleProbe && (lastSeenUpdate.IsZero() || now.Sub(lastSeenUpdate) >= presenceInterval) {
				_ = a.store.UpdateAgentSeen(ctx, node.ID, identity, now, a.clientIP(r))
				lastSeenUpdate = now
			}
		default:
			_ = writeAgentAck(ctx, connection, model.AgentAck{Type: "error", Accepted: false, ServerTime: now, Error: "unsupported frame type"})
			continue
		}
		if err := writeAgentAck(ctx, connection, model.AgentAck{
			Type: "ack", Accepted: true, Persisted: persisted, ServerTime: now,
			NextReportMS: cfg.CollectIntervalSeconds * 1000, Config: cfg, Tasks: refreshedTasks,
		}); err != nil {
			return
		}
	}
}

func probePresenceInterval(offlineAfter time.Duration) time.Duration {
	interval := min(15*time.Second, offlineAfter/2)
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func probeTaskAllowed(tasks []model.ProbeTask, taskID int64) bool {
	for _, task := range tasks {
		if task.ID == taskID && task.Enabled {
			return true
		}
	}
	return false
}

func writeAgentAck(ctx context.Context, connection *websocket.Conn, ack model.AgentAck) error {
	payload, _ := json.Marshal(ack)
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return connection.Write(writeCtx, websocket.MessageText, payload)
}

func durationSeconds(duration time.Duration) string {
	seconds := int(duration.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
