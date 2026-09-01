package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chnzzh/hostpin/internal/core"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
)

const maxTrafficCorrectionBytes = uint64(1<<63 - 1)

type trafficCorrectionRequest struct {
	RXBytes uint64 `json:"rx_bytes"`
	TXBytes uint64 `json:"tx_bytes"`
}

type trafficCorrectionStatus struct {
	Available        bool       `json:"available"`
	Active           bool       `json:"active"`
	PeriodStart      time.Time  `json:"period_start"`
	SampleReceivedAt *time.Time `json:"sample_received_at,omitempty"`
	RawRXBytes       uint64     `json:"raw_rx_bytes"`
	RawTXBytes       uint64     `json:"raw_tx_bytes"`
	RXBytes          uint64     `json:"rx_bytes"`
	TXBytes          uint64     `json:"tx_bytes"`
	RXAdjustment     int64      `json:"rx_adjustment"`
	TXAdjustment     int64      `json:"tx_adjustment"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

func (a *API) trafficCorrectionNode(r *http.Request) (model.Node, bool) {
	node, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) || err == nil && node.Role != model.NodeRoleMonitor {
		return model.Node{}, false
	}
	if err != nil {
		return model.Node{}, false
	}
	return node, true
}

func (a *API) trafficCorrectionSnapshot(node model.Node, now time.Time) trafficCorrectionStatus {
	periodStart := core.TrafficPeriodStart(now, node.TrafficResetDay)
	status := trafficCorrectionStatus{
		PeriodStart: periodStart, RXAdjustment: node.TrafficRXCorrection,
		TXAdjustment: node.TrafficTXCorrection, UpdatedAt: node.TrafficCorrectionUpdatedAt,
		Active: node.TrafficCorrectionPeriodStart != nil && node.TrafficCorrectionPeriodStart.Equal(periodStart),
	}
	sample, ok := a.hub.Latest(node.ID)
	if !ok {
		return status
	}
	raw := a.traffic.Uncorrect(node.ID, sample)
	status.SampleReceivedAt = &sample.ReceivedAt
	status.RawRXBytes, status.RawTXBytes = raw.MonthlyRxBytes, raw.MonthlyTxBytes
	status.RXBytes, status.TXBytes = sample.MonthlyRxBytes, sample.MonthlyTxBytes
	status.Available = core.TrafficPeriodStart(sample.ReceivedAt, node.TrafficResetDay).Equal(periodStart)
	return status
}

func (a *API) handleAdminTrafficCorrection(w http.ResponseWriter, r *http.Request) {
	node, ok := a.trafficCorrectionNode(r)
	if !ok {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[trafficCorrectionStatus]{Data: a.trafficCorrectionSnapshot(node, time.Now().UTC())})
}

func (a *API) handleAdminSaveTrafficCorrection(w http.ResponseWriter, r *http.Request) {
	node, ok := a.trafficCorrectionNode(r)
	if !ok {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	var request trafficCorrectionRequest
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	if request.RXBytes > maxTrafficCorrectionBytes || request.TXBytes > maxTrafficCorrectionBytes {
		writeError(w, http.StatusBadRequest, "traffic_out_of_range", "traffic totals must not exceed the signed 64-bit database limit")
		return
	}
	now := time.Now().UTC()
	current := a.trafficCorrectionSnapshot(node, now)
	if !current.Available {
		writeError(w, http.StatusConflict, "traffic_sample_unavailable", "a sample from the current traffic period is required before correction")
		return
	}
	if current.RawRXBytes > maxTrafficCorrectionBytes || current.RawTXBytes > maxTrafficCorrectionBytes {
		writeError(w, http.StatusConflict, "traffic_out_of_range", "raw traffic totals exceed the correction range")
		return
	}
	rxAdjustment := trafficAdjustment(request.RXBytes, current.RawRXBytes)
	txAdjustment := trafficAdjustment(request.TXBytes, current.RawTXBytes)
	periodStart := current.PeriodStart
	if err := a.store.UpdateTrafficCorrection(r.Context(), node.ID, rxAdjustment, txAdjustment, &periodStart, now); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", "could not save traffic correction")
		return
	}
	node.TrafficRXCorrection, node.TrafficTXCorrection = rxAdjustment, txAdjustment
	node.TrafficCorrectionPeriodStart, node.TrafficCorrectionUpdatedAt = &periodStart, &now
	a.traffic.ConfigureNode(node)
	if latest, exists := a.hub.Latest(node.ID); exists {
		raw := a.traffic.Uncorrect(node.ID, latest)
		// latest still contains the previous correction, so recover it using the
		// values captured before the new policy was installed.
		raw.MonthlyRxBytes, raw.MonthlyTxBytes = current.RawRXBytes, current.RawTXBytes
		a.hub.ReplaceLatest(a.traffic.Correct(node.ID, raw))
	}
	admin := adminFromContext(r.Context())
	detail := fmt.Sprintf("rx=%d tx=%d period=%s", request.RXBytes, request.TXBytes, periodStart.Format(time.RFC3339))
	_ = a.store.AppendAudit(r.Context(), admin.Username, "traffic.correction.update", node.ID, detail, now)
	writeJSON(w, http.StatusOK, model.Envelope[trafficCorrectionStatus]{Data: a.trafficCorrectionSnapshot(node, now)})
}

func (a *API) handleAdminClearTrafficCorrection(w http.ResponseWriter, r *http.Request) {
	node, ok := a.trafficCorrectionNode(r)
	if !ok {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	now := time.Now().UTC()
	current := a.trafficCorrectionSnapshot(node, now)
	if err := a.store.UpdateTrafficCorrection(r.Context(), node.ID, 0, 0, nil, now); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", "could not clear traffic correction")
		return
	}
	node.TrafficRXCorrection, node.TrafficTXCorrection = 0, 0
	node.TrafficCorrectionPeriodStart, node.TrafficCorrectionUpdatedAt = nil, &now
	a.traffic.ConfigureNode(node)
	if latest, exists := a.hub.Latest(node.ID); exists {
		latest.MonthlyRxBytes, latest.MonthlyTxBytes = current.RawRXBytes, current.RawTXBytes
		a.hub.ReplaceLatest(latest)
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "traffic.correction.clear", node.ID, "", now)
	writeJSON(w, http.StatusOK, model.Envelope[trafficCorrectionStatus]{Data: a.trafficCorrectionSnapshot(node, now)})
}

func trafficAdjustment(target, raw uint64) int64 {
	if target >= raw {
		return int64(target - raw)
	}
	return -int64(raw - target)
}
