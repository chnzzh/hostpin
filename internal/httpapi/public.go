package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

func (a *API) handlePublicSite(w http.ResponseWriter, r *http.Request) {
	complete, err := a.store.SetupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read site")
		return
	}
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read site settings")
		return
	}
	_, _, authErr := a.currentAdmin(r)
	publicSettings := map[string]any{
		"name": settings.Name, "description": settings.Description, "private": settings.Private,
		"enrollment_enabled": settings.EnrollmentEnabled, "theme": settings.Theme,
		"record_enabled": settings.RecordEnabled,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_complete": complete, "settings": publicSettings, "authenticated": authErr == nil,
		"version": versionInfo(),
	})
}

func (a *API) publicAccess(r *http.Request) (bool, error) {
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

func (a *API) handlePublicNodes(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if errors.Is(err, store.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read site settings")
		return
	}
	nodes, err := a.store.ListNodes(r.Context(), authenticated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list nodes")
		return
	}
	now := time.Now().UTC()
	result := make([]model.NodeSnapshot, 0, len(nodes))
	for _, node := range nodes {
		if node.Hidden && !authenticated {
			continue
		}
		public := node.Public()
		if sample, ok := a.hub.Latest(node.ID); ok {
			public.Online = now.Sub(sample.ReceivedAt) <= a.cfg.Runtime.OfflineAfter
			copy := sample
			result = append(result, model.NodeSnapshot{Node: public, Metric: &copy})
		} else {
			public.Online = false
			result = append(result, model.NodeSnapshot{Node: public})
		}
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.NodeSnapshot]{Data: result})
}

func (a *API) handlePublicNode(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	node, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) || node.Role != model.NodeRoleMonitor || (node.Hidden && !authenticated) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read node")
		return
	}
	public := node.Public()
	var metric *model.MetricSample
	if sample, ok := a.hub.Latest(node.ID); ok {
		public.Online = time.Since(sample.ReceivedAt) <= a.cfg.Runtime.OfflineAfter
		metric = &sample
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.NodeSnapshot]{Data: model.NodeSnapshot{Node: public, Metric: metric}})
}

func (a *API) handlePublicHistory(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		writeError(w, http.StatusBadRequest, "node_required", "node_id is required")
		return
	}
	node, err := a.store.GetNode(r.Context(), nodeID)
	if errors.Is(err, store.ErrNotFound) || node.Role != model.NodeRoleMonitor || (node.Hidden && !authenticated) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	hours, _ := strconv.ParseFloat(r.URL.Query().Get("hours"), 64)
	if hours <= 0 {
		hours = 1
	}
	if hours > 24*365 {
		hours = 24 * 365
	}
	maxPoints, _ := strconv.Atoi(r.URL.Query().Get("max_points"))
	if maxPoints <= 0 || maxPoints > 2000 {
		maxPoints = 500
	}
	end := time.Now().UTC()
	samples, err := a.store.History(r.Context(), store.HistoryQuery{
		NodeID: nodeID, Start: end.Add(-time.Duration(hours * float64(time.Hour))),
		End: end, MaxPoints: maxPoints,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not query history")
		return
	}
	for index := range samples {
		samples[index] = a.traffic.Correct(node.ID, samples[index])
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.MetricSample]{Data: samples})
}

func (a *API) handlePublicProbes(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	nodeID := r.URL.Query().Get("node_id")
	if nodeID != "" {
		node, nodeErr := a.store.GetNode(r.Context(), nodeID)
		if errors.Is(nodeErr, store.ErrNotFound) || node.Role != model.NodeRoleMonitor || (node.Hidden && !authenticated) {
			writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
			return
		}
		if nodeErr != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "could not read node")
			return
		}
	}
	tasks, err := a.store.ListProbeTasks(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list probes")
		return
	}
	purpose := r.URL.Query().Get("purpose")
	carrier := purpose == "carrier"
	switch purpose {
	case "", model.ProbePurposeCustom:
		tasks = probeTasksByPurpose(tasks, model.ProbePurposeCustom)
	case "carrier":
		tasks = carrierProbeTasks(tasks)
	default:
		writeError(w, http.StatusBadRequest, "invalid_probe_purpose", "purpose must be custom or carrier")
		return
	}
	if nodeID == "" {
		tasks, err = a.filterVisibleProbeTasks(r.Context(), tasks, authenticated)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", "could not filter probes")
			return
		}
	}
	for index := range tasks {
		tasks[index].Target = ""
		tasks[index].ExpectedValue = ""
		tasks[index].NodeIDs = nil
	}
	if nodeID != "" {
		hours, _ := strconv.ParseFloat(r.URL.Query().Get("hours"), 64)
		if hours <= 0 {
			hours = 24
		}
		if hours > 24*90 {
			hours = 24 * 90
		}
		end := time.Now().UTC()
		historyLimit, maxPoints := 240, 240
		if carrier {
			historyLimit = 20000
			maxPoints, _ = strconv.Atoi(r.URL.Query().Get("max_points"))
			if maxPoints <= 0 || maxPoints > 2000 {
				maxPoints = 600
			}
		}
		result := make([]model.PublicProbeSnapshot, 0, len(tasks))
		for _, task := range tasks {
			history, historyErr := a.store.ProbeHistory(r.Context(), nodeID, task.ID, end.Add(-time.Duration(hours*float64(time.Hour))), end, historyLimit)
			if historyErr != nil {
				writeError(w, http.StatusInternalServerError, "store_error", "could not query probe history")
				return
			}
			for index := range history {
				history[index].Value = ""
				if history[index].Error != "" {
					history[index].Error = "probe failed"
				}
			}
			if carrier {
				history = downsampleProbeResults(history, maxPoints)
			}
			result = append(result, model.PublicProbeSnapshot{Task: task, Results: history})
		}
		writeJSON(w, http.StatusOK, model.Envelope[[]model.PublicProbeSnapshot]{Data: result})
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.ProbeTask]{Data: tasks})
}

func downsampleProbeResults(input []model.ProbeResult, maxPoints int) []model.ProbeResult {
	if maxPoints <= 0 || len(input) <= maxPoints {
		return input
	}
	if maxPoints == 1 {
		return input[len(input)-1:]
	}
	output := make([]model.ProbeResult, 0, maxPoints)
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

func (a *API) visibleNodeSet(ctx context.Context, authenticated bool) (map[string]struct{}, error) {
	nodes, err := a.store.ListNodes(ctx, authenticated)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if authenticated || !node.Hidden {
			allowed[node.ID] = struct{}{}
		}
	}
	return allowed, nil
}

func (a *API) filterVisibleProbeTasks(ctx context.Context, tasks []model.ProbeTask, authenticated bool) ([]model.ProbeTask, error) {
	if authenticated {
		return tasks, nil
	}
	allowed, err := a.visibleNodeSet(ctx, false)
	if err != nil {
		return nil, err
	}
	result := make([]model.ProbeTask, 0, len(tasks))
	for _, task := range tasks {
		if len(task.NodeIDs) == 0 {
			if len(allowed) > 0 {
				result = append(result, task)
			}
			continue
		}
		visibleIDs := make([]string, 0, len(task.NodeIDs))
		for _, nodeID := range task.NodeIDs {
			if _, ok := allowed[nodeID]; ok {
				visibleIDs = append(visibleIDs, nodeID)
			}
		}
		if len(visibleIDs) > 0 {
			task.NodeIDs = visibleIDs
			result = append(result, task)
		}
	}
	return result, nil
}

func (a *API) filterVisibleProbeRecords(ctx context.Context, records []model.ProbeResult, authenticated bool) ([]model.ProbeResult, error) {
	tasks, err := a.store.ListProbeTasks(ctx, "")
	if err != nil {
		return nil, err
	}
	komariTasks := make(map[int64]struct{}, len(tasks))
	for _, task := range tasks {
		if isKomariPingTask(task) {
			komariTasks[task.ID] = struct{}{}
		}
	}
	var allowed map[string]struct{}
	if !authenticated {
		allowed, err = a.visibleNodeSet(ctx, false)
		if err != nil {
			return nil, err
		}
	}
	result := make([]model.ProbeResult, 0, len(records))
	for _, record := range records {
		if _, ok := komariTasks[record.TaskID]; !ok {
			continue
		}
		if !authenticated {
			if _, ok := allowed[record.NodeID]; !ok {
				continue
			}
		}
		result = append(result, record)
	}
	return result, nil
}

func (a *API) handlePublicLive(w http.ResponseWriter, r *http.Request) {
	authenticated, err := a.publicAccess(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "private_site", "this site is private")
		return
	}
	allowed, err := a.publicLiveNodeIDs(r.Context(), authenticated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list nodes")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.websocketOriginPatterns()})
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := a.connectionContext(r.Context())
	defer cancel()
	initial, _ := json.Marshal(map[string]any{
		"type": "snapshot", "at": time.Now().UTC(), "samples": a.hub.Snapshot(allowed),
		"offline_after_ms": a.cfg.Runtime.OfflineAfter.Milliseconds(),
	})
	if err := connection.Write(ctx, websocket.MessageText, initial); err != nil {
		return
	}
	updates, unsubscribe := a.hub.Subscribe(allowed)
	defer unsubscribe()
	revalidate := time.NewTicker(15 * time.Second)
	defer revalidate.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-revalidate.C:
			currentAuthenticated, accessErr := a.publicAccess(r)
			if accessErr != nil || currentAuthenticated != authenticated {
				return
			}
			currentAllowed, listErr := a.publicLiveNodeIDs(ctx, currentAuthenticated)
			if listErr != nil || !slices.Equal(currentAllowed, allowed) {
				return
			}
		case update := <-updates:
			payload, _ := json.Marshal(update)
			writeCtx, cancel := contextWithTimeout(ctx, 5*time.Second)
			err := connection.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (a *API) publicLiveNodeIDs(ctx context.Context, authenticated bool) ([]string, error) {
	nodes, err := a.store.ListNodes(ctx, authenticated)
	if err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if authenticated || !node.Hidden {
			allowed = append(allowed, node.ID)
		}
	}
	return allowed, nil
}

func (a *API) websocketOriginPatterns() []string {
	patterns := make([]string, 0, len(a.allowedOrigins))
	for origin := range a.allowedOrigins {
		if parsed, err := urlParse(origin); err == nil && parsed.Host != "" {
			patterns = append(patterns, parsed.Host)
		}
	}
	return patterns
}
