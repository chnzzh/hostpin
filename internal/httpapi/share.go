package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

func (a *API) resolveShare(r *http.Request) (model.ShareLink, bool) {
	token := chi.URLParam(r, "token")
	if !strings.HasPrefix(token, security.ShareTokenPrefix) || len(token) < len(security.ShareTokenPrefix)+32 {
		return model.ShareLink{}, false
	}
	link, err := a.store.ResolveShareLink(r.Context(), security.HashToken(token), time.Now().UTC())
	return link, err == nil
}

func (a *API) shareSnapshots(ctx context.Context, link model.ShareLink) []model.NodeSnapshot {
	result := make([]model.NodeSnapshot, 0, len(link.NodeIDs))
	now := time.Now().UTC()
	for _, id := range link.NodeIDs {
		node, err := a.store.GetNode(ctx, id)
		if err != nil {
			continue
		}
		public := node.Public()
		var metric *model.MetricSample
		if sample, ok := a.hub.Latest(id); ok {
			public.Online = now.Sub(sample.ReceivedAt) <= a.cfg.Runtime.OfflineAfter
			copy := sample
			metric = &copy
		}
		result = append(result, model.NodeSnapshot{Node: public, Metric: metric})
	}
	return result
}

func (a *API) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	link, ok := a.resolveShare(r)
	if !ok {
		writeError(w, http.StatusNotFound, "share_link_invalid", "share link is invalid, revoked, or expired")
		return
	}
	settings, _ := a.store.SiteSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"site":       map[string]any{"name": settings.Name, "description": settings.Description},
		"expires_at": link.ExpiresAt, "nodes": a.shareSnapshots(r.Context(), link),
	})
}

func (a *API) handlePublicShareHistory(w http.ResponseWriter, r *http.Request) {
	link, ok := a.resolveShare(r)
	if !ok {
		writeError(w, http.StatusNotFound, "share_link_invalid", "share link is invalid, revoked, or expired")
		return
	}
	nodeID := r.URL.Query().Get("node_id")
	if !slices.Contains(link.NodeIDs, nodeID) {
		writeError(w, http.StatusNotFound, "node_not_shared", "node is not included in this share link")
		return
	}
	hours, _ := strconv.ParseFloat(r.URL.Query().Get("hours"), 64)
	if hours <= 0 || hours > 24*365 {
		hours = 24
	}
	maxPoints, _ := strconv.Atoi(r.URL.Query().Get("max_points"))
	if maxPoints <= 0 || maxPoints > 2000 {
		maxPoints = 500
	}
	end := time.Now().UTC()
	samples, err := a.store.History(r.Context(), store.HistoryQuery{NodeID: nodeID, Start: end.Add(-time.Duration(hours * float64(time.Hour))), End: end, MaxPoints: maxPoints})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not query shared history")
		return
	}
	for index := range samples {
		samples[index] = a.traffic.Correct(nodeID, samples[index])
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.MetricSample]{Data: samples})
}

func (a *API) handlePublicShareLive(w http.ResponseWriter, r *http.Request) {
	link, ok := a.resolveShare(r)
	if !ok {
		writeError(w, http.StatusNotFound, "share_link_invalid", "share link is invalid, revoked, or expired")
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
		"type": "snapshot", "at": time.Now().UTC(), "samples": a.hub.Snapshot(link.NodeIDs),
		"offline_after_ms": a.cfg.Runtime.OfflineAfter.Milliseconds(),
	})
	if err := connection.Write(ctx, websocket.MessageText, initial); err != nil {
		return
	}
	updates, unsubscribe := a.hub.Subscribe(link.NodeIDs)
	defer unsubscribe()
	expires := time.NewTimer(max(time.Until(link.ExpiresAt), time.Millisecond))
	defer expires.Stop()
	revalidate := time.NewTicker(15 * time.Second)
	defer revalidate.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expires.C:
			return
		case <-revalidate.C:
			current, valid := a.resolveShare(r)
			if !valid || current.ID != link.ID {
				return
			}
		case update := <-updates:
			payload, _ := json.Marshal(update)
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
