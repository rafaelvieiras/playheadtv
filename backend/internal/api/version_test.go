package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/buildinfo"
)

// versionTestHandler mounts VersionHandler the way cmd/server does, behind
// RequireAuth.
func versionTestHandler(t *testing.T) http.Handler {
	t.Helper()
	db, _ := newTestDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", VersionHandler())
	return RequireAuth(db, mux)
}

// GET /version is public (no credentials) and reports the build version and
// the integer API version.
func TestVersionIsPublicAndReportsBuild(t *testing.T) {
	h := versionTestHandler(t)

	req := httptest.NewRequest("GET", "/version", nil) // deliberately no Authorization header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /version without credentials: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Version    string `json:"version"`
		APIVersion int    `json:"api_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Version != buildinfo.Version {
		t.Errorf("version = %q, want %q", got.Version, buildinfo.Version)
	}
	if got.APIVersion != buildinfo.APIVersion || got.APIVersion < 1 {
		t.Errorf("api_version = %d, want %d (>= 1)", got.APIVersion, buildinfo.APIVersion)
	}
}

// A build without -ldflags reports "dev".
func TestVersionDefaultsToDev(t *testing.T) {
	if buildinfo.Version != "dev" {
		t.Skipf("built with an injected version %q", buildinfo.Version)
	}
}

// Only GET is allowed.
func TestVersionRejectsWrite(t *testing.T) {
	rec := doRequestAny(t, versionTestHandler(t), "POST", "/version", `{}`)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("POST /version: %d", rec.Code)
	}
}
