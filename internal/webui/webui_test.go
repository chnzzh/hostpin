package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAFallbackDoesNotRedirect(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/admin/nodes", nil)
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("SPA fallback redirected to %q", location)
	}
	if !strings.Contains(recorder.Body.String(), `<div id="app"></div>`) {
		t.Fatal("SPA index was not served")
	}
}

func TestHasAssetOnlyMatchesEmbeddedFiles(t *testing.T) {
	matches, err := fs.Glob(assets, "dist/assets/*")
	if err != nil || len(matches) == 0 {
		t.Fatalf("embedded asset fixture is missing: %v", err)
	}
	if !HasAsset("/" + strings.TrimPrefix(matches[0], "dist/")) {
		t.Fatal("embedded hashed asset was not detected")
	}
	for _, candidate := range []string{"/", "/index.html", "/assets/theme-owned.js", "/../go.mod"} {
		if HasAsset(candidate) {
			t.Errorf("non-asset %q was detected as embedded", candidate)
		}
	}
}
