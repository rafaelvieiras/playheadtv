package api

import (
	"context"
	"encoding/json"
	"io"
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

// newBareTestDB is like newTestDB but seeds only the tenant row, with no
// app_user/session — newTestDB always seeds one app_user (for
// testSessionToken), which would make POST /app-users immediately return 409
// for every test using it. Bootstrap-specific tests need a tenant with zero
// app_users to exercise the real "first call creates, second call 409" path.
func newBareTestDB(t *testing.T) (*storage.SQLite, *http.ServeMux) {
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

	inner := http.NewServeMux()
	mat := materializer.New(db, time.Hour)
	sync := pullsync.New(db, testEncKey, mat, nil)
	Register(inner, db, mat, sync, testEncKey, nil)

	outer := http.NewServeMux()
	outer.Handle("/", RequireAuth(db, inner))
	return db, outer
}

func TestAppUserBootstrapOnlyOnce(t *testing.T) {
	_, mux := newBareTestDB(t)

	rec := doRequest(t, mux, "POST", "/app-users", `{"email":"admin@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first bootstrap: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "POST", "/app-users", `{"email":"other@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second bootstrap: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAppUserBootstrapValidation(t *testing.T) {
	_, mux := newBareTestDB(t)

	rec := doRequest(t, mux, "POST", "/app-users", `{"email":"","password":"correct-horse"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty email: status = %d, want 400", rec.Code)
	}

	rec = doRequest(t, mux, "POST", "/app-users", `{"email":"admin@example.com","password":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: status = %d, want 400", rec.Code)
	}
}

func TestSeedAdminUserCreatesOnlyOnce(t *testing.T) {
	db, mux := newBareTestDB(t)
	ctx := context.Background()

	if err := SeedAdminUser(ctx, db, "admin@example.com", "admin"); err != nil {
		t.Fatalf("SeedAdminUser (1st): %v", err)
	}

	rec := doRequest(t, mux, "POST", "/sessions", `{"email":"admin@example.com","password":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after seed: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// A second call must not overwrite the already-seeded user, same
	// one-user-per-tenant invariant POST /app-users enforces.
	if err := SeedAdminUser(ctx, db, "someone-else@example.com", "different"); err != nil {
		t.Fatalf("SeedAdminUser (2nd): %v", err)
	}
	rec = doRequest(t, mux, "POST", "/sessions", `{"email":"admin@example.com","password":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after 2nd seed call: status = %d, want unchanged 200, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLoginWrongPasswordAndUnknownEmailAreIndistinguishable(t *testing.T) {
	_, mux := newBareTestDB(t)

	rec := doRequest(t, mux, "POST", "/app-users", `{"email":"admin@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bootstrap: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	wrongPassword := doRequest(t, mux, "POST", "/sessions", `{"email":"admin@example.com","password":"wrong-password"}`)
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401, body = %s", wrongPassword.Code, wrongPassword.Body.String())
	}
	unknownEmail := doRequest(t, mux, "POST", "/sessions", `{"email":"nobody@example.com","password":"whatever"}`)
	if unknownEmail.Code != http.StatusUnauthorized {
		t.Fatalf("unknown email: status = %d, want 401, body = %s", unknownEmail.Code, unknownEmail.Body.String())
	}

	var wrongBody, unknownBody errorBody
	if err := json.Unmarshal(wrongPassword.Body.Bytes(), &wrongBody); err != nil {
		t.Fatalf("unmarshal wrong password body: %v", err)
	}
	if err := json.Unmarshal(unknownEmail.Body.Bytes(), &unknownBody); err != nil {
		t.Fatalf("unmarshal unknown email body: %v", err)
	}
	if wrongBody.Error != unknownBody.Error {
		t.Fatalf("error messages differ: %q vs %q", wrongBody.Error, unknownBody.Error)
	}
	lower := strings.ToLower(wrongBody.Error)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "wrong password") || strings.Contains(lower, "no such") {
		t.Fatalf("error message leaks which case failed: %q", wrongBody.Error)
	}

	rec = doRequest(t, mux, "POST", "/sessions", `{"email":"admin@example.com","password":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct login: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var loginResp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if loginResp.Token == "" {
		t.Fatal("login response has no token")
	}
	if loginResp.User.Email != "admin@example.com" {
		t.Fatalf("unexpected user in login response: %+v", loginResp.User)
	}
}

// loginTestUser logs in as the app_user newTestDB seeds (testUserEmail /
// testUserPassword) and returns the freshly issued token — distinct from the
// fixed testSessionToken doRequest injects, so tests that need "a real login
// token" don't just reuse that constant.
func loginTestUser(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/sessions", `{"email":"`+testUserEmail+`","password":"`+testUserPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var loginResp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if loginResp.Token == "" {
		t.Fatal("login response has no token")
	}
	return loginResp.Token
}

func TestMeRequiresAuth(t *testing.T) {
	_, mux := newTestDB(t)

	req := httptest.NewRequest("GET", "/me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /me without Authorization: status = %d, want 401", rec.Code)
	}

	token := loginTestUser(t, mux)

	req = httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /me with valid session: status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var meBody map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &meBody); err != nil {
		t.Fatalf("unmarshal /me body: %v", err)
	}
	if meBody["email"] != testUserEmail {
		t.Fatalf("unexpected /me body: %+v", meBody)
	}
}

func TestLogoutInvalidatesToken(t *testing.T) {
	_, mux := newTestDB(t)

	token := loginTestUser(t, mux)

	req := httptest.NewRequest("DELETE", "/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("GET /me after logout: status = %d, want 401", rec2.Code)
	}
}

func TestDeviceTokenAuthorizesEPGAndRevocationInvalidatesIt(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/devices", `{"name":"Living Room","platform":"android_tv"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var createResp createDeviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create device response: %v", err)
	}
	if createResp.Token == "" || createResp.Device.ID == "" {
		t.Fatalf("unexpected create device response: %+v", createResp)
	}

	req := httptest.NewRequest("GET", "/epg", nil)
	req.Header.Set("Authorization", "Bearer "+createResp.Token)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /epg with device token: status = %d, body = %s", rec2.Code, rec2.Body.String())
	}

	// A playback client also reads which plugins exist: that is how it knows a
	// channel from its guide can be played here at all.
	for _, path := range []string{"/plugins"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+createResp.Token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with device token: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}

	del := doRequest(t, mux, "DELETE", "/devices/"+createResp.Device.ID, "")
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete device: status = %d, body = %s", del.Code, del.Body.String())
	}

	req = httptest.NewRequest("GET", "/epg", nil)
	req.Header.Set("Authorization", "Bearer "+createResp.Token)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("GET /epg with revoked device token: status = %d, want 401", rec3.Code)
	}
}

func TestSessionOnlyRouteRejectsMissingAuth(t *testing.T) {
	_, mux := newTestDB(t)

	req := httptest.NewRequest("GET", "/channels", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /channels without Authorization: status = %d, want 401", rec.Code)
	}
}

func TestAdminShellPathsAreOpen(t *testing.T) {
	_, mux := newTestDB(t)

	for _, path := range []string{"/admin", "/admin/", "/admin/channels"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s without Authorization: status = 401, want the auth check to let it through", path)
		}
	}
}

func TestDeviceOrSessionRouteAcceptsBoth(t *testing.T) {
	_, mux := newTestDB(t)

	// The fixed test session token (injected by doRequest) works.
	rec := doRequest(t, mux, "GET", "/epg", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /epg with test session: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// A freshly created device token also works.
	createRec := doRequest(t, mux, "POST", "/devices", `{"name":"Kitchen","platform":"linux"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create device: status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createResp createDeviceResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create device response: %v", err)
	}

	req := httptest.NewRequest("GET", "/epg", nil)
	req.Header.Set("Authorization", "Bearer "+createResp.Token)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /epg with device token: status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}

// TestAPairedDeviceTurnsAPluginOffButCannotInstallOne pins the line between
// what a playback device may change about plugins and what only the admin may.
//
// The device is the one that sees a broken plugin first — its guide came from
// that plugin, so the channel it cannot play is right there — and turning one off
// or removing it pulls in no new code. Installing downloads a program from the
// registry and runs it on the backend: that stays with the admin who configured
// the registry.
func TestAPairedDeviceTurnsAPluginOffButCannotInstallOne(t *testing.T) {
	_, mux := newTestDB(t)

	createRec := doRequest(t, mux, "POST", "/devices", `{"name":"TV","platform":"android"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create device: status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createResp createDeviceResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create device: %v", err)
	}
	token := createResp.Token

	asDevice := func(method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Allowed: the two calls about an installed plugin. They fail on the missing
	// plugin (404), never on authentication (401), which is what this asserts.
	for _, call := range []struct{ method, path, body string }{
		{"PUT", "/plugins/fake", `{"enabled":false}`},
		{"DELETE", "/plugins/fake", ""},
	} {
		rec := asDevice(call.method, call.path, call.body)
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("%s %s with a device token: status = %d, want it allowed past auth (body = %s)",
				call.method, call.path, rec.Code, rec.Body.String())
		}
	}

	// Not allowed: installing runs code on the backend from the registry, and the
	// artifact download that serves it stays admin-only too.
	for _, call := range []struct{ method, path, body string }{
		{"POST", "/plugins/fake/install", `{"version":"1.0.0"}`},
		{"GET", "/plugins/fake/1.0.0/linux-amd64", ""},
	} {
		rec := asDevice(call.method, call.path, call.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a device token: status = %d, want 401", call.method, call.path, rec.Code)
		}
	}
}
