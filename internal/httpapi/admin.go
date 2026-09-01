package httpapi

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (a *API) handleAdminNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.store.ListNodes(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list nodes")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.Node]{Data: nodes})
}

func (a *API) handleAdminUpdateNode(w http.ResponseWriter, r *http.Request) {
	node, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read node")
		return
	}
	if node.Role != model.NodeRoleMonitor {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	var update model.Node
	if !decodeJSON(w, r, &update, 128<<10) {
		return
	}
	update.ID = node.ID
	update.Name = strings.TrimSpace(update.Name)
	update.Group, update.Region = strings.TrimSpace(update.Group), strings.TrimSpace(update.Region)
	update.CountryCode, update.Currency = strings.ToUpper(strings.TrimSpace(update.CountryCode)), strings.ToUpper(strings.TrimSpace(update.Currency))
	update.TrafficLimitType = strings.ToLower(strings.TrimSpace(update.TrafficLimitType))
	update.Tags = normalizeTags(update.Tags)
	locationChanged := update.Region != node.Region || update.CountryCode != node.CountryCode || !sameCoordinate(update.Latitude, node.Latitude) || !sameCoordinate(update.Longitude, node.Longitude)
	update.LocationManual = node.LocationManual
	if locationChanged {
		update.LocationManual = update.Region != "" || update.CountryCode != "" || update.Latitude != nil || update.Longitude != nil
	}
	if err := validateNodeUpdate(update); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_node", err.Error())
		return
	}
	// Preserve fields that are exclusively managed by the agent or database.
	update.Role, update.LatencyEnabled = node.Role, node.LatencyEnabled
	update.InstallID, update.Hostname, update.AgentVersion = node.InstallID, node.Hostname, node.AgentVersion
	update.OS, update.Arch, update.CPUName, update.CPUCores = node.OS, node.Arch, node.CPUName, node.CPUCores
	update.Virtualization, update.KernelVersion = node.Virtualization, node.KernelVersion
	update.IPv4, update.IPv6 = node.IPv4, node.IPv6
	update.SourceIP = node.SourceIP
	update.CreatedAt, update.LastSeenAt = node.CreatedAt, node.LastSeenAt
	if err := a.store.UpdateNode(r.Context(), update); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", "could not update node")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "node.update", node.ID, update.Name, time.Now().UTC())
	updated, err := a.store.GetNode(r.Context(), node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not reload node")
		return
	}
	a.traffic.ConfigureNode(updated)
	writeJSON(w, http.StatusOK, model.Envelope[model.Node]{Data: updated})
}

func (a *API) handleAdminSetNodeLatency(w http.ResponseWriter, r *http.Request) {
	node, err := a.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && node.Role != model.NodeRoleMonitor) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read node")
		return
	}
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &request, 4<<10) {
		return
	}
	if node.LatencyEnabled != request.Enabled {
		node.LatencyEnabled = request.Enabled
		if err := a.store.UpdateNode(r.Context(), node); err != nil {
			writeError(w, http.StatusInternalServerError, "update_failed", "could not update latency measurement capability")
			return
		}
		a.revokeAgentStreams(node.ID)
		admin := adminFromContext(r.Context())
		action := "node.latency.disable"
		if request.Enabled {
			action = "node.latency.enable"
		}
		_ = a.store.AppendAudit(r.Context(), admin.Username, action, node.ID, node.Name, time.Now().UTC())
	}
	updated, err := a.store.GetNode(r.Context(), node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not reload node")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.Node]{Data: updated})
}

func sameCoordinate(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (a *API) handleAdminDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, nodeErr := a.store.GetNode(r.Context(), id)
	if nodeErr != nil || node.Role != model.NodeRoleMonitor {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err := a.store.DeleteNode(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", "could not delete node")
		return
	}
	a.revokeAgentStreams(id)
	a.hub.Delete(id)
	a.traffic.Delete(id)
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "node.delete", id, "", time.Now().UTC())
	w.WriteHeader(http.StatusNoContent)
}

func normalizeTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			result = append(result, tag)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func validateNodeUpdate(node model.Node) error {
	if node.Name == "" || len(node.Name) > 128 {
		return errors.New("node name is required and must not exceed 128 characters")
	}
	if len(node.Group) > 128 || len(node.Region) > 128 || len(node.PublicRemark) > 4096 || len(node.PrivateRemark) > 4096 {
		return errors.New("group, region, or remarks exceed their size limit")
	}
	if node.CountryCode != "" {
		if len(node.CountryCode) != 2 || node.CountryCode[0] < 'A' || node.CountryCode[0] > 'Z' || node.CountryCode[1] < 'A' || node.CountryCode[1] > 'Z' {
			return errors.New("country code must contain two ASCII letters")
		}
	}
	if (node.Latitude == nil) != (node.Longitude == nil) ||
		(node.Latitude != nil && (*node.Latitude < -90 || *node.Latitude > 90 || *node.Longitude < -180 || *node.Longitude > 180)) {
		return errors.New("latitude and longitude must be provided together and be in range")
	}
	if len(node.Tags) > 64 {
		return errors.New("a node may have at most 64 tags")
	}
	for _, tag := range node.Tags {
		if len(tag) > 64 {
			return errors.New("tags must not exceed 64 characters")
		}
	}
	if math.IsNaN(node.Price) || math.IsInf(node.Price, 0) || node.Price < 0 || node.Price > 1_000_000_000 {
		return errors.New("price must be a finite non-negative value")
	}
	if node.Currency == "" || len(node.Currency) > 8 || node.BillingCycleDays < 1 || node.BillingCycleDays > 3650 {
		return errors.New("currency and a billing cycle from 1 to 3650 days are required")
	}
	if node.TrafficLimit < 0 || !slices.Contains([]string{"sum", "max", "up", "down"}, node.TrafficLimitType) || node.TrafficResetDay < 1 || node.TrafficResetDay > 31 {
		return errors.New("traffic policy must use sum, max, up, or down with a reset day from 1 to 31")
	}
	if node.Weight < -1_000_000 || node.Weight > 1_000_000 {
		return errors.New("node weight is out of range")
	}
	return nil
}

func normalizeAgentList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func validateAgentConfig(cfg model.AgentConfig, allowDefault bool) error {
	if !(allowDefault && cfg.CollectIntervalSeconds == 0 && cfg.PersistIntervalSeconds == 0) {
		if cfg.CollectIntervalSeconds < 1 || cfg.CollectIntervalSeconds > 300 {
			return errors.New("collection interval must be 1 to 300 seconds")
		}
		if cfg.PersistIntervalSeconds < cfg.CollectIntervalSeconds || cfg.PersistIntervalSeconds > 24*60*60 {
			return errors.New("persistence interval must be at least the collection interval and at most one day")
		}
	}
	if len(cfg.IncludeNICs) > 64 || len(cfg.ExcludeNICs) > 64 || len(cfg.IncludeMountpoints) > 64 {
		return errors.New("collector filter list is too large")
	}
	if !(allowDefault && cfg.ProbeConcurrency == 0) && (cfg.ProbeConcurrency < 1 || cfg.ProbeConcurrency > 32) {
		return errors.New("probe concurrency must be between 1 and 32")
	}
	for _, value := range append(append(append([]string{}, cfg.IncludeNICs...), cfg.ExcludeNICs...), cfg.IncludeMountpoints...) {
		if len(value) > 255 || strings.ContainsRune(value, '\x00') {
			return errors.New("collector filter values must not exceed 255 characters")
		}
	}
	return nil
}

func (a *API) handleAdminAgentConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.store.AgentConfig(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read Agent configuration")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.AgentConfig]{Data: cfg})
}

func (a *API) handleAdminSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	current, err := a.store.AgentConfig(r.Context(), nodeID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node_not_found", "node was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read Agent configuration")
		return
	}
	var cfg model.AgentConfig
	if !decodeJSON(w, r, &cfg, 64<<10) {
		return
	}
	cfg.IncludeNICs = normalizeAgentList(cfg.IncludeNICs)
	cfg.ExcludeNICs = normalizeAgentList(cfg.ExcludeNICs)
	cfg.IncludeMountpoints = normalizeAgentList(cfg.IncludeMountpoints)
	cfg.ConfigVersion = current.ConfigVersion
	if err := validateAgentConfig(cfg, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agent_config", err.Error())
		return
	}
	if err := a.store.SaveAgentConfig(r.Context(), nodeID, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save Agent configuration")
		return
	}
	updated, err := a.store.AgentConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not reload Agent configuration")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "agent_config.update", nodeID, "config version "+strconv.FormatInt(updated.ConfigVersion, 10), time.Now().UTC())
	writeJSON(w, http.StatusOK, model.Envelope[model.AgentConfig]{Data: updated})
}

func (a *API) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.SiteSettings]{Data: settings})
}

func (a *API) handleAdminSaveSettings(w http.ResponseWriter, r *http.Request) {
	var settings model.SiteSettings
	if !decodeJSON(w, r, &settings, 128<<10) {
		return
	}
	settings.Name = strings.TrimSpace(settings.Name)
	if settings.Name == "" || settings.RawRetentionHours < 24 ||
		settings.FiveMinuteRetentionHours < settings.RawRetentionHours ||
		settings.HourlyRetentionHours < settings.FiveMinuteRetentionHours {
		writeError(w, http.StatusBadRequest, "invalid_settings", "site name and ordered retention periods are required")
		return
	}
	if len(settings.Name) > 128 || len(settings.Description) > 2048 || len(settings.CustomHead) > 256<<10 || len(settings.CustomBody) > 256<<10 || len(settings.ThemeMarketSources) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_settings", "one or more site settings exceed their size limit")
		return
	}
	for _, source := range settings.ThemeMarketSources {
		parsed, err := url.Parse(source)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			writeError(w, http.StatusBadRequest, "invalid_market_source", "theme market sources must use HTTP or HTTPS")
			return
		}
	}
	if settings.GeoIPEnabled {
		parsed, err := url.Parse(settings.GeoIPProvider)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || !strings.Contains(settings.GeoIPProvider, "{ip}") {
			writeError(w, http.StatusBadRequest, "invalid_geoip_provider", "GeoIP provider must be an absolute HTTP(S) URL containing {ip}")
			return
		}
	}
	if current, err := a.store.SiteSettings(r.Context()); err == nil {
		// Weakness is derived only when the PIN changes; an API caller cannot
		// clear the persistent warning through the general settings endpoint.
		settings.EnrollmentPINWeak = current.EnrollmentPINWeak
	}
	if err := a.store.SaveSiteSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save settings")
		return
	}
	a.setRecordEnabled(settings.RecordEnabled)
	a.geoip.Configure(settings.GeoIPEnabled, settings.GeoIPProvider)
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "settings.update", "site", "", time.Now().UTC())
	writeJSON(w, http.StatusOK, model.Envelope[model.SiteSettings]{Data: settings})
}

func (a *API) handleAdminRotatePIN(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PIN string `json:"pin"`
	}
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	if len(request.PIN) < 6 || len(request.PIN) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_pin", "enrollment PIN must contain 6 to 64 characters")
		return
	}
	hash, err := security.HashPIN(request.PIN)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "could not secure enrollment PIN")
		return
	}
	if err := a.store.SetSetting(r.Context(), "enrollment_pin_hash", hash); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not rotate enrollment PIN")
		return
	}
	weak := security.IsWeakPIN(request.PIN)
	if settings, settingsErr := a.store.SiteSettings(r.Context()); settingsErr == nil {
		settings.EnrollmentPINWeak = weak
		if settingsErr = a.store.SaveSiteSettings(r.Context(), settings); settingsErr != nil {
			a.logger.Error("persist weak PIN warning", "error", settingsErr)
		}
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "enrollment.pin.rotate", "site", "", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"rotated": true, "weak_pin": weak})
}

type temporaryEnrollmentPINView struct {
	ID        string     `json:"id"`
	PIN       string     `json:"pin,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func temporaryEnrollmentPINStatus(pin store.TemporaryEnrollmentPIN, plain string, now time.Time) temporaryEnrollmentPINView {
	status := "active"
	if pin.RevokedAt != nil {
		status = "revoked"
	} else if !pin.ExpiresAt.After(now) {
		status = "expired"
	} else if pin.UsedAt != nil {
		status = "used"
	}
	return temporaryEnrollmentPINView{
		ID: pin.ID, PIN: plain, Status: status, CreatedAt: pin.CreatedAt,
		ExpiresAt: pin.ExpiresAt, UsedAt: pin.UsedAt, RevokedAt: pin.RevokedAt,
	}
}

func (a *API) handleAdminTemporaryPIN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	pin, err := a.store.LatestTemporaryEnrollmentPIN(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, model.Envelope[*temporaryEnrollmentPINView]{Data: nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read temporary enrollment PIN")
		return
	}
	view := temporaryEnrollmentPINStatus(pin, "", time.Now().UTC())
	writeJSON(w, http.StatusOK, model.Envelope[*temporaryEnrollmentPINView]{Data: &view})
}

func (a *API) handleAdminCreateTemporaryPIN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	var request struct {
		ExpiresInMinutes int `json:"expires_in_minutes"`
	}
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	if request.ExpiresInMinutes == 0 {
		request.ExpiresInMinutes = 30
	}
	if request.ExpiresInMinutes < 5 || request.ExpiresInMinutes > 24*60 {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "temporary PIN validity must be between 5 and 1440 minutes")
		return
	}
	plain, err := generateTemporaryEnrollmentPIN()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_error", "could not generate temporary enrollment PIN")
		return
	}
	hash, err := security.HashPIN(plain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "could not secure temporary enrollment PIN")
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create temporary enrollment PIN")
		return
	}
	now := time.Now().UTC()
	pin := store.TemporaryEnrollmentPIN{
		ID: id.String(), PINHash: hash, CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(request.ExpiresInMinutes) * time.Minute),
	}
	if err := a.store.ReplaceTemporaryEnrollmentPIN(r.Context(), pin, now); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save temporary enrollment PIN")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "enrollment.temporary_pin.create", pin.ID, pin.ExpiresAt.Format(time.RFC3339), now)
	view := temporaryEnrollmentPINStatus(pin, plain, now)
	writeJSON(w, http.StatusCreated, model.Envelope[temporaryEnrollmentPINView]{Data: view})
}

func (a *API) handleAdminRevokeTemporaryPIN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	pin, err := a.store.LatestTemporaryEnrollmentPIN(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, model.Envelope[*temporaryEnrollmentPINView]{Data: nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read temporary enrollment PIN")
		return
	}
	now := time.Now().UTC()
	if err := a.store.RevokeTemporaryEnrollmentPIN(r.Context(), pin.ID, now); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke temporary enrollment PIN")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "enrollment.temporary_pin.revoke", pin.ID, "", now)
	pin.RevokedAt = &now
	view := temporaryEnrollmentPINStatus(pin, "", now)
	writeJSON(w, http.StatusOK, model.Envelope[*temporaryEnrollmentPINView]{Data: &view})
}

func generateTemporaryEnrollmentPIN() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", value.Int64()), nil
}

func (a *API) handleAdminProbeTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.store.ListAllProbeTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list probe tasks")
		return
	}
	tasks = probeTasksByPurpose(tasks, "custom")
	writeJSON(w, http.StatusOK, model.Envelope[[]model.ProbeTask]{Data: tasks})
}

func (a *API) handleAdminSaveProbeTask(w http.ResponseWriter, r *http.Request) {
	var task model.ProbeTask
	if !decodeJSON(w, r, &task, 64<<10) {
		return
	}
	if rawID := chi.URLParam(r, "id"); rawID != "" {
		id, err := parsePositiveID(rawID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_id", "invalid probe task id")
			return
		}
		task.ID = id
		all, _ := a.store.ListAllProbeTasks(r.Context())
		for _, existing := range all {
			if existing.ID == task.ID && existing.Purpose != "custom" {
				writeError(w, http.StatusNotFound, "probe_not_found", "probe task was not found")
				return
			}
		}
	}
	task.Purpose, task.RunOn, task.TargetNodeID, task.Public, task.Samples = "custom", model.NodeRoleMonitor, "", false, 1
	task.Name, task.Target = strings.TrimSpace(task.Name), strings.TrimSpace(task.Target)
	slices.Sort(task.NodeIDs)
	task.NodeIDs = slices.Compact(task.NodeIDs)
	if err := validateProbeTask(task); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_probe", err.Error())
		return
	}
	for _, nodeID := range task.NodeIDs {
		node, err := a.store.GetNode(r.Context(), nodeID)
		if err != nil || node.Role != model.NodeRoleMonitor {
			writeError(w, http.StatusBadRequest, "invalid_probe_node", "probe assignment contains an unknown node")
			return
		}
	}
	saved, err := a.store.SaveProbeTask(r.Context(), task)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "probe_not_found", "probe task was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save probe task")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "probe.save", strconv.FormatInt(saved.ID, 10), saved.Name, time.Now().UTC())
	status := http.StatusOK
	if task.ID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, model.Envelope[model.ProbeTask]{Data: saved})
}

func validateProbeTask(task model.ProbeTask) error {
	if strings.TrimSpace(task.Name) == "" || len(task.Name) > 128 {
		return errors.New("probe name is required")
	}
	if strings.TrimSpace(task.Target) == "" || len(task.Target) > 2048 {
		return errors.New("probe target is required")
	}
	if task.IntervalSeconds < 5 || task.IntervalSeconds > 24*60*60 || task.TimeoutSeconds < 1 || task.TimeoutSeconds > 60 || task.TimeoutSeconds > task.IntervalSeconds {
		return errors.New("probe interval must be 5 to 86400 seconds and timeout must be 1 to 60 seconds without exceeding the interval")
	}
	if len(task.ExpectedValue) > 2048 || len(task.NodeIDs) > 1000 {
		return errors.New("probe expectation or node assignment is too large")
	}
	if task.Samples != 0 && (task.Samples < 1 || task.Samples > 10) {
		return errors.New("probe samples must be between 1 and 10")
	}
	switch task.Type {
	case model.ProbeICMP, model.ProbeDNS:
		if strings.ContainsAny(strings.TrimSpace(task.Target), " /\\") {
			return errors.New("ICMP and DNS targets must be host names or addresses")
		}
	case model.ProbeTCP:
		if _, _, err := net.SplitHostPort(task.Target); err != nil {
			return errors.New("TCP target must use host:port")
		}
	case model.ProbeHTTP:
		parsed, err := url.Parse(task.Target)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("HTTP target must be an absolute HTTP(S) URL")
		}
		if task.ExpectedStatus != 0 && (task.ExpectedStatus < 100 || task.ExpectedStatus > 599) {
			return errors.New("expected HTTP status must be 100 to 599")
		}
	default:
		return errors.New("probe type must be icmp, tcp, http, or dns")
	}
	return nil
}

func probeTasksByPurpose(tasks []model.ProbeTask, purpose string) []model.ProbeTask {
	result := make([]model.ProbeTask, 0, len(tasks))
	for _, task := range tasks {
		if task.Purpose == purpose {
			result = append(result, task)
		}
	}
	return result
}

func (a *API) handleAdminDeleteProbeTask(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositiveID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid probe task id")
		return
	}
	if err := a.store.DeleteProbeTask(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "probe_not_found", "probe task was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not delete probe task")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
