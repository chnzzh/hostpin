package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/google/uuid"
)

func TestTemporaryEnrollmentPINAdminLifecycleAndAuthorization(t *testing.T) {
	ctx := context.Background()
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "temporary-pin-api.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC()
	permanentHash, err := security.HashPIN("permanent-pin")
	if err != nil {
		t.Fatal(err)
	}
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, permanentHash, model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	api := &API{store: repository, limiter: security.NewEnrollmentLimiter()}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/enrollment/temporary-pin", bytes.NewReader([]byte(`{"expires_in_minutes":30}`)))
	request = request.WithContext(context.WithValue(request.Context(), adminContextKey{}, admin))
	response := httptest.NewRecorder()
	api.handleAdminCreateTemporaryPIN(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("temporary PIN creation returned HTTP %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store, max-age=0" {
		t.Fatalf("temporary PIN response can be cached: %q", response.Header().Get("Cache-Control"))
	}
	var created model.Envelope[temporaryEnrollmentPINView]
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Data.PIN) != 8 || created.Data.Status != "active" || created.Data.ExpiresAt.Sub(created.Data.CreatedAt) < 29*time.Minute {
		t.Fatalf("unexpected temporary PIN response: %#v", created.Data)
	}
	if temporaryID, ok := api.authorizeEnrollmentPIN(ctx, created.Data.PIN, now.Add(time.Minute)); !ok || temporaryID != created.Data.ID {
		t.Fatalf("temporary PIN was not authorized: id=%q ok=%v", temporaryID, ok)
	}
	if temporaryID, ok := api.authorizeEnrollmentPIN(ctx, "permanent-pin", now); !ok || temporaryID != "" {
		t.Fatalf("permanent PIN stopped working: id=%q ok=%v", temporaryID, ok)
	}
	if _, ok := api.authorizeEnrollmentPIN(ctx, "not-the-pin", now); ok {
		t.Fatal("unknown PIN was authorized")
	}
	getResponse := httptest.NewRecorder()
	api.handleAdminTemporaryPIN(getResponse, httptest.NewRequest(http.MethodGet, "/api/v1/admin/enrollment/temporary-pin", nil))
	var current model.Envelope[*temporaryEnrollmentPINView]
	if err := json.Unmarshal(getResponse.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Data == nil || current.Data.PIN != "" || current.Data.ID != created.Data.ID {
		t.Fatalf("temporary PIN read leaked plaintext or lost status: %#v", current.Data)
	}
	revokeRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/enrollment/temporary-pin", nil)
	revokeRequest = revokeRequest.WithContext(context.WithValue(revokeRequest.Context(), adminContextKey{}, admin))
	revokeResponse := httptest.NewRecorder()
	api.handleAdminRevokeTemporaryPIN(revokeResponse, revokeRequest)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("temporary PIN revoke returned HTTP %d: %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	var revoked model.Envelope[*temporaryEnrollmentPINView]
	if err := json.Unmarshal(revokeResponse.Body.Bytes(), &revoked); err != nil || revoked.Data == nil || revoked.Data.Status != "revoked" {
		t.Fatalf("temporary PIN revoke response was incomplete: %#v %v", revoked.Data, err)
	}
	if _, ok := api.authorizeEnrollmentPIN(ctx, created.Data.PIN, now.Add(time.Minute)); ok {
		t.Fatal("revoked temporary PIN remained authorized")
	}
}

func TestTemporaryEnrollmentPINGeneratorUsesEightDigits(t *testing.T) {
	seen := make(map[string]struct{})
	for range 32 {
		pin, err := generateTemporaryEnrollmentPIN()
		if err != nil {
			t.Fatal(err)
		}
		if len(pin) != 8 {
			t.Fatalf("generated PIN has length %d: %q", len(pin), pin)
		}
		for _, character := range pin {
			if character < '0' || character > '9' {
				t.Fatalf("generated PIN is not numeric: %q", pin)
			}
		}
		seen[pin] = struct{}{}
	}
	if len(seen) < 31 {
		t.Fatalf("temporary PIN generator repeated unexpectedly often: %d unique", len(seen))
	}
}
