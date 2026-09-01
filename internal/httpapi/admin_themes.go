package httpapi

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/webui"
	"github.com/go-chi/chi/v5"
)

const maxThemeUpload = 25 << 20

func (a *API) handleAdminThemes(w http.ResponseWriter, r *http.Request) {
	themes, err := a.store.ListThemes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list themes")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.Theme]{Data: themes})
}

func (a *API) handleAdminUploadTheme(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxThemeUpload+(1<<20))
	if err := r.ParseMultipartForm(maxThemeUpload + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "theme upload is invalid or too large")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("theme")
	if err != nil {
		writeError(w, http.StatusBadRequest, "theme_required", "multipart field 'theme' is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxThemeUpload+1))
	if err != nil || len(data) > maxThemeUpload {
		writeError(w, http.StatusBadRequest, "theme_too_large", "theme archive exceeds 25 MiB")
		return
	}
	installed, err := a.themes.Install(r.Context(), data, r.FormValue("sha256"), "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_theme", err.Error())
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "theme.install", installed.Manifest.Short, installed.Checksum, time.Now().UTC())
	writeJSON(w, http.StatusCreated, model.Envelope[model.Theme]{Data: installed})
}

func (a *API) handleAdminInstallThemeURL(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	if !decodeJSON(w, r, &request, 32<<10) {
		return
	}
	if len(strings.TrimSpace(request.SHA256)) != 64 {
		writeError(w, http.StatusBadRequest, "checksum_required", "a 64-character SHA-256 checksum is required for URL installs")
		return
	}
	installed, err := a.themes.InstallFromURL(r.Context(), request.URL, request.SHA256)
	if err != nil {
		writeError(w, http.StatusBadRequest, "theme_install_failed", err.Error())
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "theme.install_url", installed.Manifest.Short, request.URL, time.Now().UTC())
	writeJSON(w, http.StatusCreated, model.Envelope[model.Theme]{Data: installed})
}

func (a *API) handleAdminThemeSettings(w http.ResponseWriter, r *http.Request) {
	var settings map[string]any
	if !decodeJSON(w, r, &settings, 1<<20) {
		return
	}
	if settings == nil || len(settings) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_theme_settings", "theme settings must be a JSON object with at most 256 keys")
		return
	}
	theme, err := a.themes.SaveSettings(r.Context(), chi.URLParam(r, "short"), settings)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "theme_not_found", "theme was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_theme_settings", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[model.Theme]{Data: theme})
}

func (a *API) handleAdminActivateTheme(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "short")
	if short != "default" {
		if _, err := a.store.GetTheme(r.Context(), short); err != nil {
			writeError(w, http.StatusNotFound, "theme_not_found", "theme was not found")
			return
		}
	}
	settings, err := a.store.SiteSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read site settings")
		return
	}
	settings.Theme = short
	if err := a.store.SaveSiteSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not activate theme")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "theme.activate", short, "", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"theme": short, "active": true})
}

func (a *API) handleAdminDeleteTheme(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "short")
	settings, _ := a.store.SiteSettings(r.Context())
	if settings.Theme == short {
		writeError(w, http.StatusConflict, "theme_active", "activate another theme before deleting this one")
		return
	}
	if err := a.themes.Delete(r.Context(), short); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "theme_not_found", "theme was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "theme_delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminThemePreview(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "short")
	if _, err := a.store.GetTheme(r.Context(), short); err != nil {
		writeError(w, http.StatusNotFound, "theme_not_found", "theme was not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": "/?preview_theme=" + short})
}

func (a *API) handleAdminThemeMarket(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		settings, _ := a.store.SiteSettings(r.Context())
		if len(settings.ThemeMarketSources) > 0 {
			source = settings.ThemeMarketSources[0]
		}
	}
	if source == "" {
		writeError(w, http.StatusBadRequest, "market_source_required", "theme market source is required")
		return
	}
	payload, err := a.themes.FetchMarket(r.Context(), source)
	if err != nil {
		writeError(w, http.StatusBadGateway, "market_fetch_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(payload)
}

func (a *API) handleThemeAsset(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "short")
	relative := strings.TrimPrefix(path.Clean("/"+chi.URLParam(r, "*")), "/")

	// Administrators need access to manifests and screenshots while managing or
	// previewing an installed theme. Visitors may only read files from the active
	// theme's dist directory; otherwise an inactive theme could become a public
	// file bucket for configuration and source files shipped in its archive.
	if _, _, err := a.currentAdmin(r); err != nil {
		settings, settingsErr := a.store.SiteSettings(r.Context())
		if settingsErr != nil || settings.Theme != short || !strings.HasPrefix(relative, "dist/") {
			http.NotFound(w, r)
			return
		}
	}
	a.themes.ServeAsset(w, r, short, relative)
}

func (a *API) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if webui.HasAsset(r.URL.Path) {
		a.defaultFrontend.ServeHTTP(w, r)
		return
	}
	settings, err := a.store.SiteSettings(r.Context())
	nativeFrontend := r.URL.Path == "/setup" || r.URL.Path == "/login" ||
		strings.HasPrefix(r.URL.Path, "/admin") || strings.HasPrefix(r.URL.Path, "/terminal") ||
		strings.HasPrefix(r.URL.Path, "/manage")
	if err == nil && !nativeFrontend {
		selected := settings.Theme
		if preview := r.URL.Query().Get("preview_theme"); preview != "" {
			if _, _, authErr := a.currentAdmin(r); authErr == nil {
				selected = preview
			}
		}
		if a.themes.ServeTheme(w, r, selected, settings) {
			return
		}
	}
	a.defaultFrontend.ServeHTTP(w, r)
}
