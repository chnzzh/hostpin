package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

func (a *API) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	sessions, err := a.store.ListSessions(r.Context(), admin.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list sessions")
		return
	}
	currentHash := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		currentHash = security.HashToken(cookie.Value)
	}
	result := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, map[string]any{
			"id": session.TokenHash, "ip_address": session.IPAddress,
			"user_agent": session.UserAgent, "created_at": session.CreatedAt,
			"expires_at": session.ExpiresAt, "current": session.TokenHash == currentHash,
		})
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]map[string]any]{Data: result})
}

func (a *API) handleAdminDeleteSession(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	id := chi.URLParam(r, "id")
	sessions, err := a.store.ListSessions(r.Context(), admin.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read sessions")
		return
	}
	allowed := false
	for _, session := range sessions {
		if session.TokenHash == id {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "session_not_found", "session was not found")
		return
	}
	if err := a.store.DeleteSession(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke session")
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil && security.HashToken(cookie.Value) == id {
		a.clearSessionCookies(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	current := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		current = security.HashToken(cookie.Value)
	}
	if err := a.store.DeleteAdminSessions(r.Context(), admin.ID, current); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke sessions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

type pendingTOTP struct {
	AdminID  string    `json:"admin_id"`
	Secret   string    `json:"secret"`
	ExpireAt time.Time `json:"expires_at"`
}

func (a *API) handleAdminTOTPSetup(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Hostpin", AccountName: admin.Username})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "totp_error", "could not generate TOTP secret")
		return
	}
	pending := pendingTOTP{AdminID: admin.ID, Secret: key.Secret(), ExpireAt: time.Now().UTC().Add(10 * time.Minute)}
	payload, _ := json.Marshal(pending)
	setupToken, err := a.secrets.Seal(string(payload))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption_error", "could not protect TOTP setup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": key.Secret(), "otpauth_url": key.URL(), "setup_token": setupToken, "expires_at": pending.ExpireAt})
}

func (a *API) handleAdminTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SetupToken string `json:"setup_token"`
		Code       string `json:"code"`
	}
	if !decodeJSON(w, r, &request, 16<<10) {
		return
	}
	plaintext, err := a.secrets.Open(request.SetupToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_setup_token", "TOTP setup token is invalid")
		return
	}
	var pending pendingTOTP
	admin := adminFromContext(r.Context())
	if json.Unmarshal([]byte(plaintext), &pending) != nil || pending.AdminID != admin.ID || time.Now().After(pending.ExpireAt) || !totp.Validate(strings.TrimSpace(request.Code), pending.Secret) {
		writeError(w, http.StatusBadRequest, "invalid_totp", "TOTP code or setup token is invalid")
		return
	}
	encrypted, err := a.secrets.Seal(pending.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption_error", "could not protect TOTP secret")
		return
	}
	codes, hashes, err := recoveryCodes(10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_error", "could not generate recovery codes")
		return
	}
	admin.TOTPSecretEnc, admin.RecoveryHashes, admin.UpdatedAt = encrypted, hashes, time.Now().UTC()
	if err := a.store.UpdateAdmin(r.Context(), admin); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not enable TOTP")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "security.totp.enable", admin.ID, "", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recovery_codes": codes})
}

func recoveryCodes(count int) ([]string, []string, error) {
	codes, hashes := make([]string, 0, count), make([]string, 0, count)
	for range count {
		raw, err := security.RandomURLToken(9)
		if err != nil {
			return nil, nil, err
		}
		raw = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(raw, "-", "A"), "_", "B"))
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12]
		codes = append(codes, code)
		hashes = append(hashes, security.HashToken(code))
	}
	return codes, hashes, nil
}

func (a *API) handleAdminTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	admin := adminFromContext(r.Context())
	if !security.VerifyHash(admin.PasswordHash, request.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_password", "administrator password is invalid")
		return
	}
	admin.TOTPSecretEnc, admin.RecoveryHashes, admin.UpdatedAt = "", nil, time.Now().UTC()
	if err := a.store.UpdateAdmin(r.Context(), admin); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not disable TOTP")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "security.totp.disable", admin.ID, "", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

func (a *API) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	admin := adminFromContext(r.Context())
	if !security.VerifyHash(admin.PasswordHash, request.Current) {
		writeError(w, http.StatusUnauthorized, "invalid_password", "current password is invalid")
		return
	}
	if len(request.New) < 12 || len(request.New) > 512 {
		writeError(w, http.StatusBadRequest, "weak_password", "new password must contain 12 to 512 characters")
		return
	}
	hash, err := security.HashPassword(request.New)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", "could not secure new password")
		return
	}
	admin.PasswordHash, admin.UpdatedAt = hash, time.Now().UTC()
	if err := a.store.UpdateAdmin(r.Context(), admin); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not change password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}

func (a *API) handleAdminAPIKeys(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	keys, err := a.store.ListAPIKeys(r.Context(), admin.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list API keys")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.APIKey]{Data: keys})
}

func (a *API) handleAdminCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, &request, 4096) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 128 || request.ExpiresInDays < 0 || request.ExpiresInDays > 3650 {
		writeError(w, http.StatusBadRequest, "invalid_api_key", "name and a valid expiry are required")
		return
	}
	admin := adminFromContext(r.Context())
	token, tokenID, tokenHash, err := security.NewAPIKeyToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "could not generate API key")
		return
	}
	id, _ := uuid.NewV7()
	now := time.Now().UTC()
	key := model.APIKey{ID: id.String(), Name: request.Name, Scopes: []string{"admin"}, CreatedAt: now}
	if request.ExpiresInDays > 0 {
		expires := now.Add(time.Duration(request.ExpiresInDays) * 24 * time.Hour)
		key.ExpiresAt = &expires
	}
	if err := a.store.CreateAPIKey(r.Context(), store.APIKeyRecord{Key: key, AdminID: admin.ID, TokenID: tokenID, TokenHash: tokenHash}); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not create API key")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "api_key.create", key.ID, key.Name, now)
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "token": token})
}

func (a *API) handleAdminDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	admin := adminFromContext(r.Context())
	if err := a.store.DeleteAPIKey(r.Context(), admin.ID, chi.URLParam(r, "id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "api_key_not_found", "API key was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke API key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminShareLinks(w http.ResponseWriter, r *http.Request) {
	links, err := a.store.ListShareLinks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list share links")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.ShareLink]{Data: links})
}

func (a *API) handleAdminCreateShareLink(w http.ResponseWriter, r *http.Request) {
	var request struct {
		NodeIDs   []string  `json:"node_ids"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decodeJSON(w, r, &request, 64<<10) {
		return
	}
	now := time.Now().UTC()
	if len(request.NodeIDs) == 0 || len(request.NodeIDs) > 100 || !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(365*24*time.Hour)) {
		writeError(w, http.StatusBadRequest, "invalid_share_link", "choose 1 to 100 nodes and an expiry within one year")
		return
	}
	slices.Sort(request.NodeIDs)
	request.NodeIDs = slices.Compact(request.NodeIDs)
	for _, nodeID := range request.NodeIDs {
		if _, err := a.store.GetNode(r.Context(), nodeID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_node", "share link contains an unknown node")
			return
		}
	}
	token, tokenHash, err := security.NewShareToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "could not create share link")
		return
	}
	id, _ := uuid.NewV7()
	link := model.ShareLink{ID: id.String(), NodeIDs: request.NodeIDs, ExpiresAt: request.ExpiresAt.UTC(), CreatedAt: now}
	if err := a.store.CreateShareLink(r.Context(), store.ShareLinkRecord{Link: link, TokenHash: tokenHash}); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not create share link")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "share_link.create", link.ID, strings.Join(link.NodeIDs, ","), now)
	writeJSON(w, http.StatusCreated, map[string]any{"link": link, "url": publicBase(a.cfg) + "/share/" + token})
}

func (a *API) handleAdminRevokeShareLink(w http.ResponseWriter, r *http.Request) {
	if err := a.store.RevokeShareLink(r.Context(), chi.URLParam(r, "id"), time.Now().UTC()); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "share_link_not_found", "share link was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke share link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
