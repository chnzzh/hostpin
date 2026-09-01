package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) handlePublicLatency(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if errors.Is(err, store.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read latency measurements")
		return
	}
	overview, err := a.latencyOverview(r.Context(), authenticated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read latency measurements")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.LatencyOverview]{Data: overview})
}

func (a *API) latencyOverview(ctx context.Context, authenticated bool) (model.LatencyOverview, error) {
	now := time.Now().UTC()
	probeNodes, err := a.store.ListLatencyNodes(ctx, authenticated)
	if err != nil {
		return model.LatencyOverview{}, err
	}
	publicProbeNodes := make([]model.LatencyProbeNode, 0, len(probeNodes))
	visibleProbes := make(map[string]struct{}, len(probeNodes))
	for _, node := range probeNodes {
		if node.Hidden && !authenticated {
			continue
		}
		online := node.LastSeenAt != nil && now.Sub(*node.LastSeenAt) <= a.cfg.Runtime.OfflineAfter
		publicProbeNodes = append(publicProbeNodes, model.LatencyProbeNode{
			ID: node.ID, Role: node.Role, Name: node.Name, Region: node.Region, CountryCode: node.CountryCode,
			Tags: append([]string{}, node.Tags...), OS: node.OS, Arch: node.Arch,
			LastSeenAt: node.LastSeenAt, Online: online,
		})
		visibleProbes[node.ID] = struct{}{}
	}

	nodes, err := a.store.ListNodes(ctx, authenticated)
	if err != nil {
		return model.LatencyOverview{}, err
	}
	visibleTargets := make(map[string]model.Node, len(nodes))
	for _, node := range nodes {
		if authenticated || !node.Hidden {
			visibleTargets[node.ID] = node
		}
	}
	tasks, err := a.store.ListProbeTasks(ctx, "")
	if err != nil {
		return model.LatencyOverview{}, err
	}
	publicTargets := make([]model.LatencyTarget, 0)
	visibleTasks := make(map[int64]struct{})
	for _, task := range tasks {
		if task.Purpose != "latency" || (!authenticated && !task.Public) {
			continue
		}
		node, ok := visibleTargets[task.TargetNodeID]
		if !ok {
			continue
		}
		publicTargets = append(publicTargets, model.LatencyTarget{
			TaskID: task.ID, Node: node.Public(), Type: task.Type,
			IntervalSeconds: task.IntervalSeconds, Samples: task.Samples,
		})
		visibleTasks[task.ID] = struct{}{}
	}
	latest, err := a.store.LatestLatencyResults(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return model.LatencyOverview{}, err
	}
	filtered := make([]model.LatencyResult, 0, len(latest))
	for _, result := range latest {
		if _, ok := visibleProbes[result.ProbeNodeID]; !ok {
			continue
		}
		if _, ok := visibleTasks[result.TaskID]; !ok {
			continue
		}
		if !authenticated && result.Error != "" {
			result.Error = "probe failed"
		}
		filtered = append(filtered, result)
	}
	return model.LatencyOverview{
		ProbeNodes: publicProbeNodes, Targets: publicTargets, Latest: filtered,
		OfflineAfterMS: a.cfg.Runtime.OfflineAfter.Milliseconds(),
	}, nil
}

func (a *API) handlePublicLatencyHistory(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if errors.Is(err, store.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not check public access")
		return
	}
	probeNodeID := strings.TrimSpace(r.URL.Query().Get("probe_node_id"))
	targetNodeID := strings.TrimSpace(r.URL.Query().Get("target_node_id"))
	if probeNodeID == "" || targetNodeID == "" {
		writeError(w, http.StatusBadRequest, "latency_pair_required", "probe_node_id and target_node_id are required")
		return
	}
	probeNode, err := a.store.GetNode(r.Context(), probeNodeID)
	if err != nil || !probeNode.CanMeasureLatency() || (probeNode.Hidden && !authenticated) {
		writeError(w, http.StatusNotFound, "probe_node_not_found", "measurement node was not found")
		return
	}
	targetNode, err := a.store.GetNode(r.Context(), targetNodeID)
	if err != nil || targetNode.Role != model.NodeRoleMonitor || (targetNode.Hidden && !authenticated) {
		writeError(w, http.StatusNotFound, "target_node_not_found", "target node was not found")
		return
	}
	tasks, err := a.store.ListProbeTasks(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read latency targets")
		return
	}
	visible := false
	for _, task := range tasks {
		if task.Purpose == "latency" && task.TargetNodeID == targetNodeID && (authenticated || task.Public) {
			visible = true
			break
		}
	}
	if !visible {
		writeError(w, http.StatusNotFound, "latency_target_not_found", "latency target was not found")
		return
	}
	hours, _ := strconv.ParseFloat(r.URL.Query().Get("hours"), 64)
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, 24*90)
	maxPoints, _ := strconv.Atoi(r.URL.Query().Get("max_points"))
	if maxPoints <= 0 || maxPoints > 2000 {
		maxPoints = 600
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	results, err := a.store.LatencyHistory(r.Context(), probeNodeID, targetNodeID, start, end, 20000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not query latency history")
		return
	}
	summary, err := a.store.LatencyWindowSummary(r.Context(), probeNodeID, targetNodeID, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not summarize latency history")
		return
	}
	results = downsampleLatency(results, maxPoints)
	if !authenticated {
		for index := range results {
			if results[index].Error != "" {
				results[index].Error = "probe failed"
			}
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Data    []model.LatencyResult      `json:"data"`
		Summary model.LatencyWindowSummary `json:"summary"`
	}{Data: results, Summary: summary})
}

func downsampleLatency(input []model.LatencyResult, maxPoints int) []model.LatencyResult {
	if maxPoints <= 0 || len(input) <= maxPoints {
		return input
	}
	if maxPoints == 1 {
		return input[len(input)-1:]
	}
	output := make([]model.LatencyResult, 0, maxPoints)
	step := float64(len(input)-1) / float64(maxPoints-1)
	last := -1
	for index := 0; index < maxPoints; index++ {
		position := int(float64(index) * step)
		if position != last {
			output = append(output, input[position])
			last = position
		}
	}
	if output[len(output)-1].ReceivedAt != input[len(input)-1].ReceivedAt {
		output[len(output)-1] = input[len(input)-1]
	}
	return output
}

func (a *API) handleAdminLatency(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.store.ListLatencyNodes(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list measurement nodes")
		return
	}
	targets, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list latency targets")
		return
	}
	targets = probeTasksByPurpose(targets, "latency")
	monitors, err := a.store.ListNodes(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list monitored nodes")
		return
	}
	latest, err := a.store.LatestLatencyResults(r.Context(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list latency results")
		return
	}
	baseURL := publicBase(a.cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": nodes, "targets": targets, "monitored_nodes": monitors, "latest": latest,
		"offline_after_ms":        a.cfg.Runtime.OfflineAfter.Milliseconds(),
		"install_command":         fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- --probe-node", baseURL),
		"windows_installer":       baseURL + "/install.ps1",
		"windows_install_command": fmt.Sprintf("Invoke-WebRequest -UseBasicParsing '%s/install.ps1' -OutFile .\\hostpin-install.ps1; .\\hostpin-install.ps1 -ProbeNode", baseURL),
	})
}

func (a *API) handleAdminUpdateLatencyNode(w http.ResponseWriter, r *http.Request) {
	node, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !node.CanMeasureLatency() {
		writeError(w, http.StatusNotFound, "probe_node_not_found", "measurement node was not found")
		return
	}
	var update model.Node
	if !decodeJSON(w, r, &update, 128<<10) {
		return
	}
	update.ID, update.Role, update.LatencyEnabled = node.ID, node.Role, node.LatencyEnabled
	update.Name = strings.TrimSpace(update.Name)
	update.Group, update.Region = strings.TrimSpace(update.Group), strings.TrimSpace(update.Region)
	update.CountryCode = strings.ToUpper(strings.TrimSpace(update.CountryCode))
	update.Tags = normalizeTags(update.Tags)
	locationChanged := update.Region != node.Region || update.CountryCode != node.CountryCode || !sameCoordinate(update.Latitude, node.Latitude) || !sameCoordinate(update.Longitude, node.Longitude)
	update.LocationManual = node.LocationManual
	if locationChanged {
		update.LocationManual = update.Region != "" || update.CountryCode != "" || update.Latitude != nil || update.Longitude != nil
	}
	// Preserve identity and irrelevant billing defaults so the generic node store remains lossless.
	update.InstallID, update.Hostname, update.AgentVersion = node.InstallID, node.Hostname, node.AgentVersion
	update.OS, update.Arch, update.CPUName, update.CPUCores = node.OS, node.Arch, node.CPUName, node.CPUCores
	update.Virtualization, update.KernelVersion, update.IPv4, update.IPv6 = node.Virtualization, node.KernelVersion, node.IPv4, node.IPv6
	update.SourceIP, update.CreatedAt, update.LastSeenAt = node.SourceIP, node.CreatedAt, node.LastSeenAt
	update.Price, update.Currency, update.BillingCycleDays = node.Price, node.Currency, node.BillingCycleDays
	update.ExpiresAt, update.AutoRenewal = node.ExpiresAt, node.AutoRenewal
	update.TrafficLimit, update.TrafficLimitType, update.TrafficResetDay = node.TrafficLimit, node.TrafficLimitType, node.TrafficResetDay
	if err := validateNodeUpdate(update); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_probe_node", err.Error())
		return
	}
	if err := a.store.UpdateNode(r.Context(), update); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", "could not update measurement node")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "latency.node.update", node.ID, update.Name, time.Now().UTC())
	updated, _ := a.store.GetNode(r.Context(), node.ID)
	writeJSON(w, http.StatusOK, model.Envelope[model.Node]{Data: updated})
}

func (a *API) handleAdminDeleteLatencyNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, err := a.store.GetNode(r.Context(), id)
	if err != nil || !node.CanMeasureLatency() {
		writeError(w, http.StatusNotFound, "probe_node_not_found", "measurement node was not found")
		return
	}
	if node.Role == model.NodeRoleMonitor {
		node.LatencyEnabled = false
		if err := a.store.UpdateNode(r.Context(), node); err != nil {
			writeError(w, http.StatusInternalServerError, "update_failed", "could not disable latency measurement")
			return
		}
		a.revokeAgentStreams(id)
		admin := adminFromContext(r.Context())
		_ = a.store.AppendAudit(r.Context(), admin.Username, "node.latency.disable", id, node.Name, time.Now().UTC())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.store.DeleteNode(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", "could not delete measurement node")
		return
	}
	a.revokeAgentStreams(id)
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "latency.node.delete", id, node.Name, time.Now().UTC())
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminSaveLatencyTarget(w http.ResponseWriter, r *http.Request) {
	var task model.ProbeTask
	if !decodeJSON(w, r, &task, 64<<10) {
		return
	}
	if rawID := chi.URLParam(r, "id"); rawID != "" {
		parsedID, parseErr := parsePositiveID(rawID)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_id", "invalid latency target id")
			return
		}
		task.ID = parsedID
	}
	task.Name, task.Target = strings.TrimSpace(task.Name), strings.TrimSpace(task.Target)
	task.Purpose, task.RunOn, task.NodeIDs = "latency", model.NodeRoleProbe, nil
	task.ExpectedStatus, task.ExpectedValue = 0, ""
	if task.Samples <= 0 {
		task.Samples = 3
	}
	if task.Type != model.ProbeICMP && task.Type != model.ProbeTCP {
		writeError(w, http.StatusBadRequest, "invalid_latency_target", "latency targets must use ICMP or TCP")
		return
	}
	targetNode, err := a.store.GetNode(r.Context(), task.TargetNodeID)
	if err != nil || targetNode.Role != model.NodeRoleMonitor {
		writeError(w, http.StatusBadRequest, "invalid_target_node", "target_node_id must identify a monitored server")
		return
	}
	if task.Name == "" {
		task.Name = targetNode.Name
	}
	if err := validateProbeTask(task); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_latency_target", err.Error())
		return
	}
	all, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not inspect latency targets")
		return
	}
	updateFound := task.ID == 0
	for _, existing := range all {
		if existing.Purpose == "latency" && existing.TargetNodeID == task.TargetNodeID && existing.ID != task.ID {
			writeError(w, http.StatusConflict, "latency_target_exists", "this monitored server already has a latency target")
			return
		}
		if task.ID != 0 && existing.ID == task.ID {
			if existing.Purpose != "latency" {
				writeError(w, http.StatusNotFound, "latency_target_not_found", "latency target was not found")
				return
			}
			updateFound = true
		}
	}
	if !updateFound {
		writeError(w, http.StatusNotFound, "latency_target_not_found", "latency target was not found")
		return
	}
	saved, err := a.store.SaveProbeTask(r.Context(), task)
	if err != nil {
		if current, listErr := a.store.ListAllProbeTasks(r.Context()); listErr == nil {
			for _, existing := range current {
				if existing.Purpose == "latency" && existing.TargetNodeID == task.TargetNodeID && existing.ID != task.ID {
					writeError(w, http.StatusConflict, "latency_target_exists", "this monitored server already has a latency target")
					return
				}
			}
		}
		writeError(w, http.StatusInternalServerError, "store_error", "could not save latency target")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "latency.target.save", strconv.FormatInt(saved.ID, 10), targetNode.Name, time.Now().UTC())
	status := http.StatusOK
	if task.ID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, model.Envelope[model.ProbeTask]{Data: saved})
}

func (a *API) handleAdminDeleteLatencyTarget(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositiveID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid latency target id")
		return
	}
	all, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not inspect latency targets")
		return
	}
	found := false
	for _, task := range all {
		if task.ID == id && task.Purpose == "latency" {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "latency_target_not_found", "latency target was not found")
		return
	}
	if err := a.store.DeleteProbeTask(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not delete latency target")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "latency.target.delete", strconv.FormatInt(id, 10), "", time.Now().UTC())
	w.WriteHeader(http.StatusNoContent)
}
