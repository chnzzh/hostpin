package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
)

type carrierDefinition struct {
	Key     string
	Purpose string
	Name    string
	Target  string
}

var carrierDefinitions = []carrierDefinition{
	{Key: "telecom", Purpose: model.ProbePurposeCarrierTelecom, Name: "China Telecom", Target: "gd-ct-dualstack.ip.zstaticcdn.com:443"},
	{Key: "unicom", Purpose: model.ProbePurposeCarrierUnicom, Name: "China Unicom", Target: "gd-cu-dualstack.ip.zstaticcdn.com:443"},
	{Key: "mobile", Purpose: model.ProbePurposeCarrierMobile, Name: "China Mobile", Target: "gd-cm-dualstack.ip.zstaticcdn.com:443"},
}

func (a *API) ensureCarrierProbeTasks(ctx context.Context) error {
	tasks, err := a.store.ListAllProbeTasks(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if model.IsCarrierProbePurpose(task.Purpose) {
			existing[task.Purpose] = struct{}{}
		}
	}
	for _, definition := range carrierDefinitions {
		if _, ok := existing[definition.Purpose]; ok {
			continue
		}
		_, err := a.store.SaveProbeTask(ctx, model.ProbeTask{
			Name: definition.Name, Type: model.ProbeTCP, Target: definition.Target,
			IntervalSeconds: 120, TimeoutSeconds: 3, Purpose: definition.Purpose,
			RunOn: model.NodeRoleMonitor, Public: true, Samples: 4, Enabled: true,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func carrierProbeTasks(tasks []model.ProbeTask) []model.ProbeTask {
	byPurpose := make(map[string]model.ProbeTask, len(carrierDefinitions))
	for _, task := range tasks {
		if model.IsCarrierProbePurpose(task.Purpose) {
			byPurpose[task.Purpose] = task
		}
	}
	result := make([]model.ProbeTask, 0, len(carrierDefinitions))
	for _, definition := range carrierDefinitions {
		if task, ok := byPurpose[definition.Purpose]; ok {
			result = append(result, task)
		}
	}
	return result
}

func carrierDefinitionForKey(key string) (carrierDefinition, bool) {
	for _, definition := range carrierDefinitions {
		if definition.Key == key {
			return definition, true
		}
	}
	return carrierDefinition{}, false
}

func (a *API) handleAdminCarrierProbes(w http.ResponseWriter, r *http.Request) {
	if err := a.ensureCarrierProbeTasks(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not initialize carrier probes")
		return
	}
	tasks, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list carrier probes")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.ProbeTask]{Data: carrierProbeTasks(tasks)})
}

func (a *API) handleAdminSaveCarrierProbe(w http.ResponseWriter, r *http.Request) {
	definition, ok := carrierDefinitionForKey(strings.ToLower(strings.TrimSpace(chi.URLParam(r, "carrier"))))
	if !ok {
		writeError(w, http.StatusNotFound, "carrier_not_found", "carrier must be telecom, unicom, or mobile")
		return
	}
	if err := a.ensureCarrierProbeTasks(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not initialize carrier probes")
		return
	}
	all, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read carrier probe")
		return
	}
	var current model.ProbeTask
	for _, task := range all {
		if task.Purpose == definition.Purpose {
			current = task
			break
		}
	}
	if current.ID == 0 {
		writeError(w, http.StatusNotFound, "carrier_not_found", "carrier probe was not found")
		return
	}
	var update model.ProbeTask
	if !decodeJSON(w, r, &update, 32<<10) {
		return
	}
	update.ID, update.Name, update.Purpose = current.ID, definition.Name, definition.Purpose
	update.Target = strings.TrimSpace(update.Target)
	update.RunOn, update.NodeIDs, update.TargetNodeID = model.NodeRoleMonitor, nil, ""
	update.ExpectedStatus, update.ExpectedValue, update.Public = 0, "", true
	if update.Type != model.ProbeICMP && update.Type != model.ProbeTCP {
		writeError(w, http.StatusBadRequest, "invalid_carrier_probe", "carrier probes must use ICMP or TCP")
		return
	}
	if err := validateProbeTask(update); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_carrier_probe", err.Error())
		return
	}
	saved, err := a.store.SaveProbeTask(r.Context(), update)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "carrier_not_found", "carrier probe was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save carrier probe")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "carrier_probe.update", definition.Key, saved.Target, time.Now().UTC())
	writeJSON(w, http.StatusOK, model.Envelope[model.ProbeTask]{Data: saved})
}
