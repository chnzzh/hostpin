package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"
)

type komariResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (a *API) mountKomari(router chi.Router) {
	router.Get("/assets/flags/{code}", a.handleKomariFlagAsset)
	router.Get("/assets/logo/{name}", a.handleKomariLogoAsset)
	router.Get("/api/public", a.handleKomariPublic)
	router.Get("/api/version", a.handleKomariVersion)
	router.Get("/api/me", a.handleKomariMe)
	router.Post("/api/login", a.handleKomariLogin)
	router.Get("/api/logout", a.handleKomariLogout)
	router.Get("/api/nodes", a.handleKomariNodes)
	router.Get("/api/recent/{uuid}", a.handleKomariRecent)
	router.Get("/api/records/load", a.handleKomariLoadHistory)
	router.Get("/api/records/ping", a.handleKomariPingHistory)
	router.Get("/api/task/ping", a.handleKomariPingTasks)
	router.Get("/api/clients", a.handleKomariClients)
	router.HandleFunc("/api/rpc2", a.handleRPC2)
}

func (a *API) handleKomariPublic(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: a.komariPublicSettings(r.Context(), settings)})
}

func (a *API) komariPublicSettings(ctx context.Context, settings model.SiteSettings) map[string]any {
	return map[string]any{
		"cors_origin_check_enabled": true,
		"custom_body":               settings.CustomBody, "custom_head": settings.CustomHead, "description": settings.Description,
		"disable_password_login": false, "oauth_enable": false, "oauth_provider": "",
		"ping_record_preserve_time": settings.FiveMinuteRetentionHours,
		"private_site":              settings.Private, "record_enabled": settings.RecordEnabled,
		"record_preserve_time": settings.RawRetentionHours, "sitename": settings.Name,
		"theme": settings.Theme, "theme_settings": a.themes.PublicSettings(ctx, settings.Theme),
	}
}

func (a *API) handleKomariVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: map[string]string{"version": version, "hash": commit}})
}

func (a *API) handleKomariMe(w http.ResponseWriter, r *http.Request) {
	admin, _, err := a.currentAdmin(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false, "username": "Guest"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_in": true, "username": admin.Username, "uuid": admin.ID,
		"2fa_enabled": admin.TOTPSecretEnc != "", "sso_id": "", "sso_type": "",
	})
}

func (a *API) handleKomariLogin(w http.ResponseWriter, r *http.Request) {
	// Komari themes use the same credentials and cookie. Native login response is
	// accepted by current themes, while this wrapper preserves the legacy shape.
	var request loginRequest
	if !decodeJSON(w, r, &request, 32<<10) {
		return
	}
	admin, err := a.store.GetAdminByUsername(r.Context(), strings.TrimSpace(request.Username))
	if err != nil || !security.VerifyHash(admin.PasswordHash, request.Password) {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "invalid credentials"})
		return
	}
	if admin.TOTPSecretEnc != "" {
		secret, openErr := a.secrets.Open(admin.TOTPSecretEnc)
		if openErr != nil || !totp.Validate(request.TOTPCode, secret) {
			writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "2FA required"})
			return
		}
	}
	session, csrf, err := a.issueSession(r.Context(), admin, a.clientIP(r), r.UserAgent())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "session error"})
		return
	}
	a.setSessionCookies(w, session, csrf)
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: map[string]any{"set-cookie": map[string]string{"session_token": "set"}}})
}

func (a *API) handleKomariLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.store.DeleteSession(r.Context(), security.HashToken(cookie.Value))
	}
	a.clearSessionCookies(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *API) komariAccess(r *http.Request) (bool, error) {
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil {
		return false, err
	}
	_, _, authErr := a.currentAdmin(r)
	authenticated := authErr == nil
	if settings.Private && !authenticated {
		return false, store.ErrUnauthorized
	}
	return authenticated, nil
}

func (a *API) handleKomariNodes(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.komariAccess(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "private site"})
		return
	}
	nodes, err := a.komariNodes(r.Context(), authenticated)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: nodes})
}

func (a *API) komariNodes(ctx context.Context, authenticated bool) ([]map[string]any, error) {
	nodes, err := a.store.ListNodes(ctx, authenticated)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		if node.Hidden && !authenticated {
			continue
		}
		entry := map[string]any{
			"uuid": node.ID, "name": node.Name, "cpu_name": node.CPUName,
			"virtualization": node.Virtualization, "arch": node.Arch,
			"cpu_cores": node.CPUCores, "cpu_physical_cores": 0, "os": node.OS,
			"kernel_version": node.KernelVersion, "gpu_name": gpuName(a.hub, node.ID),
			"region": komariRegion(node.Region, node.CountryCode), "region_name": node.Region,
			"country_code": node.CountryCode, "latitude": node.Latitude, "longitude": node.Longitude,
			"weight": node.Weight,
			"price":  node.Price, "billing_cycle": node.BillingCycleDays,
			"auto_renewal": node.AutoRenewal, "currency": node.Currency,
			"expired_at": nullableTime(node.ExpiresAt), "group": node.Group,
			"tags": strings.Join(node.Tags, ";"), "public_remark": node.PublicRemark,
			"hidden": node.Hidden, "traffic_limit": node.TrafficLimit,
			"traffic_limit_type": node.TrafficLimitType,
			"created_at":         node.CreatedAt, "updated_at": node.UpdatedAt,
		}
		if sample, ok := a.hub.Latest(node.ID); ok {
			entry["mem_total"] = sample.MemoryTotal
			entry["swap_total"] = sample.SwapTotal
			entry["disk_total"] = sample.DiskTotal
		} else {
			entry["mem_total"], entry["swap_total"], entry["disk_total"] = 0, 0, 0
		}
		if authenticated {
			entry["ipv4"], entry["ipv6"], entry["version"] = node.IPv4, node.IPv6, node.AgentVersion
			entry["remark"] = node.PrivateRemark
		}
		result = append(result, entry)
	}
	return result, nil
}

func gpuName(hub interface {
	Latest(string) (model.MetricSample, bool)
}, nodeID string) string {
	if sample, ok := hub.Latest(nodeID); ok && len(sample.GPUs) > 0 {
		return sample.GPUs[0].Name
	}
	return "None"
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (a *API) komariLatest(ctx context.Context, authenticated bool) (map[string]map[string]any, error) {
	nodes, err := a.store.ListNodes(ctx, authenticated)
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]any)
	now := time.Now().UTC()
	for _, node := range nodes {
		if node.Hidden && !authenticated {
			continue
		}
		sample, ok := a.hub.Latest(node.ID)
		if !ok {
			result[node.ID] = map[string]any{"client": node.ID, "online": false}
			continue
		}
		result[node.ID] = komariStatus(sample, now.Sub(sample.ReceivedAt) <= a.cfg.Runtime.OfflineAfter)
	}
	return result, nil
}

func komariStatus(sample model.MetricSample, online bool) map[string]any {
	gpuAverage := 0.0
	for _, gpu := range sample.GPUs {
		gpuAverage += gpu.Utilization
	}
	if len(sample.GPUs) > 0 {
		gpuAverage /= float64(len(sample.GPUs))
	}
	return map[string]any{
		"client": sample.NodeID, "time": sample.ReceivedAt.Format(time.RFC3339Nano),
		"cpu": sample.CPU, "gpu": gpuAverage, "ram": sample.MemoryUsed,
		"ram_total": sample.MemoryTotal, "swap": sample.SwapUsed,
		"swap_total": sample.SwapTotal, "load": sample.Load1, "load5": sample.Load5,
		"load15": sample.Load15, "temp": sample.Temperature, "disk": sample.DiskUsed,
		"disk_total": sample.DiskTotal, "net_in": int64(sample.NetRxBPS),
		"net_out": int64(sample.NetTxBPS), "net_total_up": sample.NetTxBytes,
		"net_total_down": sample.NetRxBytes, "process": sample.Processes,
		"connections": sample.TCPConnections, "connections_udp": sample.UDPConnections,
		"online": online, "uptime": sample.UptimeSeconds, "message": sample.Message,
	}
}

func komariNestedStatus(sample model.MetricSample) map[string]any {
	return map[string]any{
		"cpu":         map[string]any{"usage": sample.CPU},
		"ram":         map[string]any{"total": sample.MemoryTotal, "used": sample.MemoryUsed},
		"swap":        map[string]any{"total": sample.SwapTotal, "used": sample.SwapUsed},
		"load":        map[string]any{"load1": sample.Load1, "load5": sample.Load5, "load15": sample.Load15},
		"disk":        map[string]any{"total": sample.DiskTotal, "used": sample.DiskUsed},
		"network":     map[string]any{"up": sample.NetTxBPS, "down": sample.NetRxBPS, "totalUp": sample.NetTxBytes, "totalDown": sample.NetRxBytes},
		"connections": map[string]any{"tcp": sample.TCPConnections, "udp": sample.UDPConnections},
		"uptime":      sample.UptimeSeconds, "process": sample.Processes, "message": sample.Message,
		"updated_at": sample.ReceivedAt.Format(time.RFC3339Nano),
	}
}

func (a *API) handleKomariRecent(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	authenticated, accessErr := a.komariAccess(r)
	if accessErr != nil || a.ensureVisibleNode(r.Context(), uuid, authenticated) != nil {
		writeJSON(w, http.StatusNotFound, komariResponse{Status: "error", Message: "node not found"})
		return
	}
	samples, err := a.store.RecentMetrics(r.Context(), uuid, time.Now().Add(-time.Minute))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	result := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		result = append(result, komariNestedStatus(sample))
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: result})
}

func (a *API) handleKomariLoadHistory(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.komariAccess(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "private site"})
		return
	}
	result, err := a.komariLoadRecords(r.Context(), authenticated, r.URL.Query().Get("uuid"), parseHours(r.URL.Query().Get("hours")), 4000)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, komariResponse{Status: "error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: result})
}

func (a *API) komariLoadRecords(ctx context.Context, authenticated bool, nodeID string, hours float64, maxPoints int) (map[string]any, error) {
	if nodeID == "" {
		return nil, errors.New("uuid is required")
	}
	node, err := a.store.GetNode(ctx, nodeID)
	if err != nil || (node.Hidden && !authenticated) {
		return nil, errors.New("node not found")
	}
	end := time.Now().UTC()
	samples, err := a.store.History(ctx, store.HistoryQuery{NodeID: nodeID, Start: end.Add(-time.Duration(hours * float64(time.Hour))), End: end, MaxPoints: maxPoints})
	if err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		records = append(records, komariStatus(sample, true))
	}
	return map[string]any{"count": len(records), "records": records, "has_gpu_data": hasGPU(samples)}, nil
}

func hasGPU(samples []model.MetricSample) bool {
	for _, sample := range samples {
		if len(sample.GPUs) > 0 {
			return true
		}
	}
	return false
}

func (a *API) handleKomariPingHistory(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.komariAccess(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "private site"})
		return
	}
	nodeID := r.URL.Query().Get("uuid")
	if nodeID != "" {
		if a.ensureVisibleNode(r.Context(), nodeID, authenticated) != nil {
			writeJSON(w, http.StatusNotFound, komariResponse{Status: "error", Message: "node not found"})
			return
		}
	}
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	end := time.Now().UTC()
	records, err := a.store.ProbeHistory(r.Context(), nodeID, taskID, end.Add(-time.Duration(parseHours(r.URL.Query().Get("hours"))*float64(time.Hour))), end, 4000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	records, err = a.filterVisibleProbeRecords(r.Context(), records, authenticated)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: a.komariProbeRecords(r.Context(), records, authenticated)})
}

func (a *API) komariProbeRecords(ctx context.Context, records []model.ProbeResult, authenticated bool) map[string]any {
	tasks, _ := a.store.ListProbeTasks(ctx, "")
	tasks, _ = a.filterVisibleProbeTasks(ctx, tasks, authenticated)
	taskMap := make(map[int64]model.ProbeTask, len(tasks))
	for _, task := range tasks {
		if isKomariPingTask(task) {
			taskMap[task.ID] = task
		}
	}
	output := make([]map[string]any, 0, len(records))
	for _, result := range records {
		successful, lost := probeSampleBreakdown(result, taskMap[result.TaskID])
		for index := 0; index < successful+lost; index++ {
			value := result.LatencyMS
			if index >= successful {
				value = -1
			}
			at := result.ReceivedAt.Add(time.Duration(index) * time.Nanosecond)
			output = append(output, map[string]any{"task_id": result.TaskID, "time": at.Format(time.RFC3339Nano), "value": value, "client": result.NodeID})
		}
	}
	return map[string]any{"count": len(output), "records": output, "tasks": komariTaskList(tasks), "basic_info": probeBasicInfo(records, taskMap)}
}

func effectiveProbeLoss(record model.ProbeResult) float64 {
	loss := min(100.0, max(0.0, record.LossPercent))
	if (!record.Success || record.LatencyMS < 0) && loss == 0 {
		return 100
	}
	return loss
}

func probeSampleBreakdown(record model.ProbeResult, task model.ProbeTask) (int, int) {
	samples := max(1, task.Samples)
	if !record.Success || record.LatencyMS < 0 {
		return 0, samples
	}
	lost := min(samples, max(0, int(math.Round(effectiveProbeLoss(record)/100*float64(samples)))))
	return samples - lost, lost
}

func probeBasicInfo(records []model.ProbeResult, tasks map[int64]model.ProbeTask) []map[string]any {
	type summary struct {
		total, lost int
		min, max    float64
	}
	byNode := map[string]*summary{}
	for _, record := range records {
		item := byNode[record.NodeID]
		if item == nil {
			item = &summary{min: math.MaxFloat64}
			byNode[record.NodeID] = item
		}
		successful, lost := probeSampleBreakdown(record, tasks[record.TaskID])
		item.total += successful + lost
		item.lost += lost
		if successful > 0 {
			item.min = min(item.min, record.LatencyMS)
			item.max = max(item.max, record.LatencyMS)
		}
	}
	result := make([]map[string]any, 0, len(byNode))
	for nodeID, item := range byNode {
		if item.min == math.MaxFloat64 {
			item.min = -1
		}
		result = append(result, map[string]any{"client": nodeID, "loss": float64(item.lost) / float64(item.total) * 100, "min": item.min, "max": item.max})
	}
	return result
}

func (a *API) handleKomariPingTasks(w http.ResponseWriter, r *http.Request) {
	authenticated, accessErr := a.komariAccess(r)
	if accessErr != nil {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "private site"})
		return
	}
	tasks, err := a.store.ListProbeTasks(r.Context(), "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	tasks, err = a.filterVisibleProbeTasks(r.Context(), tasks, authenticated)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, komariResponse{Status: "error", Message: "store error"})
		return
	}
	writeJSON(w, http.StatusOK, komariResponse{Status: "success", Data: komariTaskList(tasks)})
}

func komariTaskList(tasks []model.ProbeTask) []map[string]any {
	result := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		if !isKomariPingTask(task) {
			continue
		}
		result = append(result, map[string]any{"id": task.ID, "name": task.Name, "clients": task.NodeIDs, "default_on": len(task.NodeIDs) == 0, "type": string(task.Type), "interval": task.IntervalSeconds, "samples": max(1, task.Samples)})
	}
	return result
}

func isKomariPingTask(task model.ProbeTask) bool {
	return task.Purpose == model.ProbePurposeCustom || model.IsCarrierProbePurpose(task.Purpose)
}

func (a *API) handleKomariClients(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.komariAccess(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, komariResponse{Status: "error", Message: "private site"})
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.websocketOriginPatterns()})
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := a.connectionContext(r.Context())
	defer cancel()
	_, _, err = connection.Read(ctx)
	if err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		statuses, err := a.komariLatest(ctx, authenticated)
		if err != nil {
			return
		}
		nested := make(map[string]any, len(statuses))
		online := make([]string, 0)
		for id, status := range statuses {
			if value, _ := status["online"].(bool); value {
				online = append(online, id)
			}
			if sample, ok := a.hub.Latest(id); ok {
				nested[id] = komariNestedStatus(sample)
			}
		}
		payload, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"online": online, "data": nested}})
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = connection.Write(writeCtx, websocket.MessageText, payload)
		cancel()
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
