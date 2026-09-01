package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chnzzh/hostpin/internal/alerting"
	"github.com/chnzzh/hostpin/internal/backup"
	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/core"
	"github.com/chnzzh/hostpin/internal/geoip"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/notification"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/theme"
	"github.com/chnzzh/hostpin/internal/webui"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type API struct {
	cfg             config.Config
	store           store.Store
	hub             *core.Hub
	traffic         *core.TrafficTracker
	persister       *core.Persister
	limiter         *security.EnrollmentLimiter
	geoip           *geoip.Client
	secrets         *security.SecretBox
	alerts          *alerting.Engine
	notifier        *notification.Dispatcher
	themes          *theme.Manager
	backups         *backup.Manager
	requestRestore  func()
	defaultFrontend http.Handler
	logger          *slog.Logger
	secureCookies   bool
	trustedProxies  []netip.Prefix
	enrollmentCIDRs []netip.Prefix
	allowedOrigins  map[string]struct{}
	agentStreamsMu  sync.Mutex
	agentStreams    map[string]map[chan struct{}]struct{}
	recordEnabled   atomic.Bool
	shutdown        chan struct{}
	shutdownOnce    sync.Once
}

func New(cfg config.Config, repository store.Store, hub *core.Hub, persister *core.Persister, secrets *security.SecretBox, alerts *alerting.Engine, notifier *notification.Dispatcher, themes *theme.Manager, backups *backup.Manager, requestRestore func(), logger *slog.Logger) *API {
	publicURL, _ := url.Parse(cfg.PublicURL)
	api := &API{
		cfg: cfg, store: repository, hub: hub, persister: persister,
		limiter: security.NewEnrollmentLimiter(),
		geoip:   geoip.New(cfg.GeoIP.Enabled, cfg.GeoIP.Provider, cfg.GeoIP.Timeout, cfg.GeoIP.CacheTTL),
		secrets: secrets, alerts: alerts, notifier: notifier, themes: themes, backups: backups,
		requestRestore:  requestRestore,
		defaultFrontend: webui.Handler(), logger: logger, secureCookies: publicURL.Scheme == "https",
		allowedOrigins: make(map[string]struct{}), agentStreams: make(map[string]map[chan struct{}]struct{}),
		shutdown: make(chan struct{}),
	}
	api.recordEnabled.Store(true)
	nodes, _ := repository.ListNodes(context.Background(), true)
	nodeMap := make(map[string]model.Node, len(nodes))
	for _, node := range nodes {
		nodeMap[node.ID] = node
	}
	api.traffic = core.NewTrafficTracker()
	rawLatest := hub.Snapshot(nil)
	api.traffic.Load(rawLatest, nodeMap)
	correctedLatest := make(map[string]model.MetricSample, len(rawLatest))
	for nodeID, sample := range rawLatest {
		correctedLatest[nodeID] = api.traffic.Correct(nodeID, sample)
	}
	hub.Load(correctedLatest)
	if settings, err := repository.SiteSettings(context.Background()); err == nil {
		api.geoip.Configure(settings.GeoIPEnabled, settings.GeoIPProvider)
		api.recordEnabled.Store(settings.RecordEnabled)
	}
	ensureCtx, ensureCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := api.ensureCarrierProbeTasks(ensureCtx); err != nil {
		logger.Warn("could not initialize carrier latency tasks", "error", err)
	}
	ensureCancel()
	for _, raw := range cfg.Security.TrustedProxies {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			api.trustedProxies = append(api.trustedProxies, prefix)
		}
	}
	for _, raw := range cfg.Security.EnrollmentCIDRs {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			api.enrollmentCIDRs = append(api.enrollmentCIDRs, prefix)
		}
	}
	api.allowedOrigins[publicURL.Scheme+"://"+publicURL.Host] = struct{}{}
	for _, origin := range cfg.Security.AllowedOrigins {
		api.allowedOrigins[strings.TrimSuffix(origin, "/")] = struct{}{}
	}
	return api
}

func (a *API) Shutdown() {
	a.shutdownOnce.Do(func() { close(a.shutdown) })
	a.agentStreamsMu.Lock()
	for nodeID, streams := range a.agentStreams {
		for revoked := range streams {
			close(revoked)
		}
		delete(a.agentStreams, nodeID)
	}
	a.agentStreamsMu.Unlock()
}

func (a *API) connectionContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-a.shutdown:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (a *API) setRecordEnabled(enabled bool) {
	previous := a.recordEnabled.Swap(enabled)
	if enabled && !previous {
		a.hub.ResetPersistence()
	}
}

func (a *API) registerAgentStream(nodeID string) (<-chan struct{}, func()) {
	revoked := make(chan struct{})
	a.agentStreamsMu.Lock()
	if a.agentStreams[nodeID] == nil {
		a.agentStreams[nodeID] = make(map[chan struct{}]struct{})
	}
	a.agentStreams[nodeID][revoked] = struct{}{}
	a.agentStreamsMu.Unlock()
	return revoked, func() {
		a.agentStreamsMu.Lock()
		delete(a.agentStreams[nodeID], revoked)
		if len(a.agentStreams[nodeID]) == 0 {
			delete(a.agentStreams, nodeID)
		}
		a.agentStreamsMu.Unlock()
	}
}

func (a *API) revokeAgentStreams(nodeID string) {
	a.agentStreamsMu.Lock()
	for revoked := range a.agentStreams[nodeID] {
		close(revoked)
	}
	delete(a.agentStreams, nodeID)
	a.agentStreamsMu.Unlock()
}

func (a *API) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(a.recoverer)
	router.Use(a.securityHeaders)
	router.Use(a.cors)
	router.Use(a.requestLog)

	router.Get("/healthz", a.handleHealth)
	router.Get("/readyz", a.handleReady)
	router.Get("/install.sh", a.handleInstallSH)
	router.Get("/install.ps1", a.handleInstallPowerShell)
	router.Get("/uninstall.sh", a.handleUninstallSH)
	router.Get("/uninstall.ps1", a.handleUninstallPowerShell)
	router.Handle("/themes/{short}/*", http.HandlerFunc(a.handleThemeAsset))

	router.Route("/api/v1", func(r chi.Router) {
		r.Get("/status", a.handleStatus)
		r.Post("/setup", a.handleSetup)
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", a.handleLogin)
			r.Post("/logout", a.handleLogout)
			r.Get("/me", a.handleMe)
		})
		r.Post("/enrollments", a.handleEnrollment)
		r.Route("/agent", func(r chi.Router) {
			r.Post("/reports", a.handleAgentReport)
			r.Get("/stream", a.handleAgentStream)
			r.Get("/config", a.handleAgentConfig)
		})
		r.Route("/public", func(r chi.Router) {
			r.Get("/site", a.handlePublicSite)
			r.Get("/nodes", a.handlePublicNodes)
			r.Get("/nodes/{id}", a.handlePublicNode)
			r.Get("/history", a.handlePublicHistory)
			r.Get("/probes", a.handlePublicProbes)
			r.Get("/latency", a.handlePublicLatency)
			r.Get("/latency/history", a.handlePublicLatencyHistory)
			r.Get("/live", a.handlePublicLive)
			r.Get("/share/{token}", a.handlePublicShare)
			r.Get("/share/{token}/history", a.handlePublicShareHistory)
			r.Get("/share/{token}/live", a.handlePublicShareLive)
		})
		r.Route("/admin", func(r chi.Router) {
			r.Use(a.requireAdmin)
			r.Use(a.requireCSRF)
			r.Get("/nodes", a.handleAdminNodes)
			r.Put("/nodes/{id}", a.handleAdminUpdateNode)
			r.Delete("/nodes/{id}", a.handleAdminDeleteNode)
			r.Put("/nodes/{id}/latency", a.handleAdminSetNodeLatency)
			r.Get("/nodes/{id}/traffic-correction", a.handleAdminTrafficCorrection)
			r.Put("/nodes/{id}/traffic-correction", a.handleAdminSaveTrafficCorrection)
			r.Delete("/nodes/{id}/traffic-correction", a.handleAdminClearTrafficCorrection)
			r.Get("/nodes/{id}/agent-config", a.handleAdminAgentConfig)
			r.Put("/nodes/{id}/agent-config", a.handleAdminSaveAgentConfig)
			r.Get("/settings", a.handleAdminSettings)
			r.Put("/settings", a.handleAdminSaveSettings)
			r.Get("/backups/status", a.handleAdminBackupStatus)
			r.Post("/backups/export", a.handleAdminExportBackup)
			r.Post("/backups/import", a.handleAdminImportBackup)
			r.Put("/enrollment/pin", a.handleAdminRotatePIN)
			r.Get("/enrollment/temporary-pin", a.handleAdminTemporaryPIN)
			r.Post("/enrollment/temporary-pin", a.handleAdminCreateTemporaryPIN)
			r.Delete("/enrollment/temporary-pin", a.handleAdminRevokeTemporaryPIN)
			r.Get("/probes", a.handleAdminProbeTasks)
			r.Post("/probes", a.handleAdminSaveProbeTask)
			r.Put("/probes/{id}", a.handleAdminSaveProbeTask)
			r.Delete("/probes/{id}", a.handleAdminDeleteProbeTask)
			r.Get("/carrier-probes", a.handleAdminCarrierProbes)
			r.Put("/carrier-probes/{carrier}", a.handleAdminSaveCarrierProbe)
			r.Get("/latency", a.handleAdminLatency)
			r.Put("/latency/nodes/{id}", a.handleAdminUpdateLatencyNode)
			r.Delete("/latency/nodes/{id}", a.handleAdminDeleteLatencyNode)
			r.Post("/latency/targets", a.handleAdminSaveLatencyTarget)
			r.Put("/latency/targets/{id}", a.handleAdminSaveLatencyTarget)
			r.Delete("/latency/targets/{id}", a.handleAdminDeleteLatencyTarget)
			r.Get("/alerts/rules", a.handleAdminAlertRules)
			r.Post("/alerts/rules", a.handleAdminSaveAlertRule)
			r.Put("/alerts/rules/{id}", a.handleAdminSaveAlertRule)
			r.Delete("/alerts/rules/{id}", a.handleAdminDeleteAlertRule)
			r.Get("/alerts/events", a.handleAdminAlertEvents)
			r.Get("/notifications", a.handleAdminNotificationChannels)
			r.Post("/notifications", a.handleAdminSaveNotificationChannel)
			r.Put("/notifications/{id}", a.handleAdminSaveNotificationChannel)
			r.Delete("/notifications/{id}", a.handleAdminDeleteNotificationChannel)
			r.Post("/notifications/{id}/test", a.handleAdminTestNotificationChannel)
			r.Get("/audit", a.handleAdminAudit)
			r.Get("/security/sessions", a.handleAdminSessions)
			r.Delete("/security/sessions/{id}", a.handleAdminDeleteSession)
			r.Post("/security/sessions/revoke-others", a.handleAdminRevokeOtherSessions)
			r.Post("/security/totp/setup", a.handleAdminTOTPSetup)
			r.Post("/security/totp/confirm", a.handleAdminTOTPConfirm)
			r.Delete("/security/totp", a.handleAdminTOTPDisable)
			r.Put("/security/password", a.handleAdminChangePassword)
			r.Get("/api-keys", a.handleAdminAPIKeys)
			r.Post("/api-keys", a.handleAdminCreateAPIKey)
			r.Delete("/api-keys/{id}", a.handleAdminDeleteAPIKey)
			r.Get("/share-links", a.handleAdminShareLinks)
			r.Post("/share-links", a.handleAdminCreateShareLink)
			r.Delete("/share-links/{id}", a.handleAdminRevokeShareLink)
			r.Get("/themes", a.handleAdminThemes)
			r.Post("/themes/upload", a.handleAdminUploadTheme)
			r.Post("/themes/install", a.handleAdminInstallThemeURL)
			r.Put("/themes/{short}/settings", a.handleAdminThemeSettings)
			r.Get("/themes/{short}/preview", a.handleAdminThemePreview)
			r.Post("/themes/{short}/activate", a.handleAdminActivateTheme)
			r.Delete("/themes/{short}", a.handleAdminDeleteTheme)
			r.Get("/themes/market", a.handleAdminThemeMarket)
		})
	})

	// Komari-compatible theme-facing routes.
	a.mountKomari(router)

	router.Handle("/*", http.HandlerFunc(a.handleFrontend))
	return router
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	if a.persister.Degraded() {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"status":               map[bool]string{true: "degraded", false: "ok"}[a.persister.Degraded()],
		"persistence_degraded": a.persister.Degraded(), "dropped": a.persister.Dropped(),
	})
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "database": a.store.Driver()})
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	setup, err := a.store.SetupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read setup state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_complete": setup, "version": versionInfo()})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("http panic", "panic", recovered, "stack", string(debug.Stack()), "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if a.secureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
		if origin != "" {
			if _, ok := a.allowedOrigins[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/healthz") {
			a.logger.Debug("http request", "method", r.Method, "path", r.URL.Path,
				"duration_ms", time.Since(started).Milliseconds(), "request_id", middleware.GetReqID(r.Context()))
		}
	})
}

func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !containsPrefix(a.trustedProxies, peer) {
		return host
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		candidate, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			continue
		}
		if !containsPrefix(a.trustedProxies, candidate) {
			return candidate.String()
		}
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Real-IP")); forwarded != "" {
		if candidate, err := netip.ParseAddr(forwarded); err == nil {
			return candidate.String()
		}
	}
	return host
}

func containsPrefix(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (a *API) enrollmentSourceAllowed(ip string) bool {
	if len(a.enrollmentCIDRs) == 0 {
		return true
	}
	address, err := netip.ParseAddr(ip)
	return err == nil && containsPrefix(a.enrollmentCIDRs, address)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) bool {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := "invalid JSON body"
		if !errors.Is(err, io.EOF) {
			message = err.Error()
		}
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, model.Envelope[any]{Error: &model.APIError{Code: code, Message: message}})
}

func publicBase(cfg config.Config) string { return strings.TrimSuffix(cfg.PublicURL, "/") }

func versionInfo() map[string]string {
	return map[string]string{"version": version, "commit": commit}
}

var version = "dev"
var commit = "unknown"

func SetVersion(v, c string) { version, commit = v, c }

var _ = context.Background
var _ = fmt.Sprintf
