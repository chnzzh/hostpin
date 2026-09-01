package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

const (
	sessionCookieName = "hostpin_session"
	csrfCookieName    = "hostpin_csrf"
)

type adminContextKey struct{}
type sessionContextKey struct{}
type apiKeyContextKey struct{}

type setupRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	EnrollmentPIN   string `json:"enrollment_pin"`
	SiteName        string `json:"site_name"`
	SiteDescription string `json:"site_description"`
}

func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	complete, err := a.store.SetupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read setup state")
		return
	}
	if complete {
		writeError(w, http.StatusConflict, "already_configured", "Hostpin is already configured")
		return
	}
	var request setupRequest
	if !decodeJSON(w, r, &request, 32<<10) {
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	request.SiteName = strings.TrimSpace(request.SiteName)
	if len(request.Username) < 3 || len(request.Username) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_username", "username must contain 3 to 64 characters")
		return
	}
	if len(request.Password) < 12 || len(request.Password) > 512 {
		writeError(w, http.StatusBadRequest, "weak_password", "password must contain at least 12 characters")
		return
	}
	if len(request.EnrollmentPIN) < 6 || len(request.EnrollmentPIN) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_pin", "enrollment PIN must contain 6 to 64 characters")
		return
	}
	if request.SiteName == "" {
		request.SiteName = "Hostpin"
	}
	passwordHash, err := security.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "could not secure administrator password")
		return
	}
	pinHash, err := security.HashPIN(request.EnrollmentPIN)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "could not secure enrollment PIN")
		return
	}
	now := time.Now().UTC()
	adminID, err := uuid.NewV7()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create administrator identity")
		return
	}
	admin := store.Admin{
		ID: adminID.String(), Username: request.Username, PasswordHash: passwordHash,
		CreatedAt: now, UpdatedAt: now,
	}
	settings := model.DefaultSiteSettings()
	settings.EnrollmentPINWeak = security.IsWeakPIN(request.EnrollmentPIN)
	settings.GeoIPEnabled = a.cfg.GeoIP.Enabled
	settings.GeoIPProvider = a.cfg.GeoIP.Provider
	settings.Name = request.SiteName
	if strings.TrimSpace(request.SiteDescription) != "" {
		settings.Description = strings.TrimSpace(request.SiteDescription)
	}
	if err := a.store.Initialize(r.Context(), admin, pinHash, settings); err != nil {
		if errors.Is(err, store.ErrAlreadySetup) {
			writeError(w, http.StatusConflict, "already_configured", "Hostpin is already configured")
			return
		}
		a.logger.Error("initialize Hostpin", "error", err)
		writeError(w, http.StatusInternalServerError, "setup_failed", "could not initialize Hostpin")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "setup.complete", "site", "initial setup", now)
	session, csrf, err := a.issueSession(r.Context(), admin, a.clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "setup completed but login session could not be created")
		return
	}
	a.setSessionCookies(w, session, csrf)
	writeJSON(w, http.StatusCreated, map[string]any{
		"configured": true, "username": admin.Username,
		"weak_pin": security.IsWeakPIN(request.EnrollmentPIN), "csrf_token": csrf,
	})
}

type loginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !decodeJSON(w, r, &request, 32<<10) {
		return
	}
	admin, err := a.store.GetAdminByUsername(r.Context(), strings.TrimSpace(request.Username))
	if err != nil || !security.VerifyHash(admin.PasswordHash, request.Password) {
		time.Sleep(200 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if admin.TOTPSecretEnc != "" {
		secret, err := a.secrets.Open(admin.TOTPSecretEnc)
		if err != nil || !totp.Validate(strings.TrimSpace(request.TOTPCode), secret) {
			if !consumeRecoveryCode(&admin, request.RecoveryCode) {
				writeError(w, http.StatusUnauthorized, "totp_required", "a valid two-factor code is required")
				return
			}
			admin.UpdatedAt = time.Now().UTC()
			if err := a.store.UpdateAdmin(r.Context(), admin); err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", "could not consume recovery code")
				return
			}
		}
	}
	session, csrf, err := a.issueSession(r.Context(), admin, a.clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "could not create login session")
		return
	}
	a.setSessionCookies(w, session, csrf)
	_ = a.store.AppendAudit(r.Context(), admin.Username, "auth.login", admin.ID, a.clientIP(r), time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"logged_in": true, "username": admin.Username, "csrf_token": csrf})
}

func consumeRecoveryCode(admin *store.Admin, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	hash := security.HashToken(strings.ToUpper(code))
	for index, candidate := range admin.RecoveryHashes {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(candidate)) == 1 {
			admin.RecoveryHashes = append(admin.RecoveryHashes[:index], admin.RecoveryHashes[index+1:]...)
			return true
		}
	}
	return false
}

func (a *API) issueSession(ctx context.Context, admin store.Admin, ip, userAgent string) (store.Session, string, error) {
	token, err := security.RandomURLToken(32)
	if err != nil {
		return store.Session{}, "", err
	}
	csrf, err := security.RandomURLToken(24)
	if err != nil {
		return store.Session{}, "", err
	}
	now := time.Now().UTC()
	session := store.Session{
		TokenHash: security.HashToken(token), AdminID: admin.ID,
		CSRFHash: security.HashToken(csrf), IPAddress: ip, UserAgent: truncate(userAgent, 512),
		CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	if err := a.store.CreateSession(ctx, session); err != nil {
		return store.Session{}, "", err
	}
	// Raw token is held only long enough to set the HttpOnly cookie.
	session.TokenHash = token
	return session, csrf, nil
}

func (a *API) setSessionCookies(w http.ResponseWriter, session store.Session, csrf string) {
	maxAge := int(time.Until(session.ExpiresAt).Seconds())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: session.TokenHash, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: csrf, Path: "/", MaxAge: maxAge,
		HttpOnly: false, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (a *API) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == sessionCookieName, Secure: a.secureCookies, SameSite: http.SameSiteLaxMode})
	}
}

func (a *API) currentAdmin(r *http.Request) (store.Admin, store.Session, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return store.Admin{}, store.Session{}, store.ErrUnauthorized
	}
	session, err := a.store.GetSession(r.Context(), security.HashToken(cookie.Value))
	if err != nil {
		return store.Admin{}, store.Session{}, store.ErrUnauthorized
	}
	admin, err := a.store.GetAdminByID(r.Context(), session.AdminID)
	if err != nil {
		return store.Admin{}, store.Session{}, store.ErrUnauthorized
	}
	return admin, session, nil
}

func (a *API) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, session, err := a.currentAdmin(r)
		apiKey := false
		if err != nil {
			scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if ok && strings.EqualFold(scheme, "Bearer") {
				if tokenID, tokenHash, parseErr := security.ParseAPIKeyToken(strings.TrimSpace(raw)); parseErr == nil {
					var key model.APIKey
					admin, key, err = a.store.AuthenticateAPIKey(r.Context(), tokenID, tokenHash, time.Now().UTC())
					apiKey = err == nil && slices.Contains(key.Scopes, "admin")
					if !apiKey {
						err = store.ErrUnauthorized
					}
				}
			}
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "administrator authentication is required")
			return
		}
		ctx := context.WithValue(r.Context(), adminContextKey{}, admin)
		if apiKey {
			ctx = context.WithValue(ctx, apiKeyContextKey{}, true)
		} else {
			ctx = context.WithValue(ctx, sessionContextKey{}, session)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if apiKey, _ := r.Context().Value(apiKeyContextKey{}).(bool); apiKey {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := r.Context().Value(sessionContextKey{}).(store.Session)
		cookie, err := r.Cookie(csrfCookieName)
		header := r.Header.Get("X-CSRF-Token")
		if !ok || err != nil || header == "" || cookie.Value != header ||
			subtle.ConstantTimeCompare([]byte(session.CSRFHash), []byte(security.HashToken(header))) != 1 {
			writeError(w, http.StatusForbidden, "csrf_failed", "CSRF validation failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	complete, _ := a.store.SetupComplete(r.Context())
	admin, _, err := a.currentAdmin(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"setup_complete": complete, "logged_in": false})
		return
	}
	csrf := ""
	if cookie, err := r.Cookie(csrfCookieName); err == nil {
		csrf = cookie.Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_complete": complete, "logged_in": true, "username": admin.Username,
		"two_factor_enabled": admin.TOTPSecretEnc != "", "csrf_token": csrf,
	})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		if csrfCookie, csrfErr := r.Cookie(csrfCookieName); csrfErr == nil &&
			csrfCookie.Value == r.Header.Get("X-CSRF-Token") {
			_ = a.store.DeleteSession(r.Context(), security.HashToken(cookie.Value))
		}
	}
	a.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
}

func adminFromContext(ctx context.Context) store.Admin {
	admin, _ := ctx.Value(adminContextKey{}).(store.Admin)
	return admin
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
