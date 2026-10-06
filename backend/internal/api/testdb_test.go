package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// testSessionToken is a fixed, known app_user session token seeded directly
// into the DB by newTestDB, so the dozens of pre-existing call sites of
// doRequest (written before auth existed) don't each need to carry a
// dynamically-issued token around. doRequest attaches it to every request
// automatically.
const testSessionToken = "test-session-token"

// testEncKey is a fixed, deterministic stand-in for the real
// crypto.LoadOrGenerateKey-derived key, used everywhere tests call
// Register directly instead of going through cmd/server/main.go.
var testEncKey = [32]byte{
	1, 2, 3, 4, 5, 6, 7, 8,
	9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24,
	25, 26, 27, 28, 29, 30, 31, 32,
}

// testUserEmail/testUserPassword are the credentials of the app_user newTestDB
// seeds alongside testSessionToken. auth_test.go uses them to log in for real
// (via POST /sessions) in tests that need a freshly issued token instead of
// the fixed testSessionToken — since newTestDB already seeds one app_user,
// bootstrap-specific tests (POST /app-users succeeding) use their own bare
// setup instead of newTestDB.
const (
	testUserEmail    = "test@example.com"
	testUserPassword = "test-password-not-used"
)

// newTestDB opens a fresh temp-file SQLite DB, runs migrations, seeds the
// DefaultTenantID row plus a test app_user and a valid session for
// testSessionToken, and returns a *http.ServeMux with every resource
// registered and RequireAuth wrapped around it — the same setup path as
// cmd/server/main.go.
//
// The returned value keeps the concrete *http.ServeMux type (not
// http.Handler) because at least one pre-existing test file
// (schedules_test.go's setupChannelAndBlock) declares a helper with that
// exact parameter type rather than relying on type inference; changing this
// return type would break that file, which this slice must not touch. To
// still apply RequireAuth around literally everything (including when a test
// calls ServeHTTP directly, bypassing doRequest), the returned mux is an
// outer mux whose only registered pattern ("/") delegates to
// RequireAuth(db, inner) — RequireAuth itself returns a plain http.Handler,
// so wrapping it this way is the only way to keep it under a *http.ServeMux
// value.
func newTestDB(t *testing.T) (*storage.SQLite, *http.ServeMux) {
	t.Helper()
	db, mux, _ := newTestDBWithMaterializer(t)
	return db, mux
}

// newTestDBWithMaterializer is newTestDB that also hands back the materializer
// the API was registered with, for tests that run or start it.
func newTestDBWithMaterializer(t *testing.T) (*storage.SQLite, *http.ServeMux, *materializer.Materializer) {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "playheadtv.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	err = db.Exec(ctx,
		"INSERT INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		DefaultTenantID, "Test Tenant", "self_hosted", time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	testUserID := "test-user"
	passwordHash, err := hashPassword(testUserPassword)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	err = db.Exec(ctx,
		"INSERT INTO app_user (id, tenant_id, email, password_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		testUserID, DefaultTenantID, testUserEmail, passwordHash, time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("seed app_user: %v", err)
	}
	err = db.Exec(ctx,
		"INSERT INTO session (id, tenant_id, user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		"test-session", DefaultTenantID, testUserID, hashToken(testSessionToken),
		time.Now().UnixMilli(), time.Now().Add(24*time.Hour).UnixMilli(),
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	inner := http.NewServeMux()
	// Existing API tests don't exercise the materializer/syncer running
	// (Start is never called on either), so plain instances are enough to
	// satisfy Register.
	mat := materializer.New(db, time.Hour)
	sync := pullsync.New(db, testEncKey, mat)
	Register(inner, db, mat, sync, testEncKey)

	outer := http.NewServeMux()
	outer.Handle("/", RequireAuth(db, inner))
	return db, outer, mat
}

// doRequest always authenticates as the fixed test session (see
// testSessionToken) so the dozens of pre-existing tests, written before auth
// existed, keep passing unmodified. Tests that need to exercise auth itself
// (missing header, device tokens, expired/revoked sessions, ...) bypass this
// helper and drive the mux returned by newTestDB directly with their own
// *http.Request/ServeHTTP call.
func doRequest(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+testSessionToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// newCollection creates a collection through the API and returns its id; the
// query is a condition tree JSON.
func newCollection(t *testing.T, mux *http.ServeMux, query string) string {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/collections", `{"name":"Collection `+strings.ReplaceAll(t.Name(), `"`, "")+`","query":`+query+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create collection: %d %s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// doRequestAny is doRequest for any http.Handler.
func doRequestAny(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testSessionToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
