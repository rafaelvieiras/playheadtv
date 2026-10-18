package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// pairingTestRemote is a TEST-NET-3 documentation address; the pairing routes
// are public and rate-limit by peer address (see clientAddr in
// password_reset.go), so the tests drive the mux directly to control it.
const pairingTestRemote = "203.0.113.9:1234"

// pairingPost drives a public pairing route as a raw peer, bypassing
// doRequest's fixed session (an unconfigured device has no token yet).
func pairingPost(t *testing.T, mux *http.ServeMux, remote, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type pairingStart struct {
	Pin        string `json:"pin"`
	PollSecret string `json:"poll_secret"`
}

// startPairing runs POST /pair and returns the shown PIN and poll secret.
func startPairing(t *testing.T, mux *http.ServeMux, name, platform string) pairingStart {
	t.Helper()
	rec := pairingPost(t, mux, pairingTestRemote, "/pair",
		`{"name":`+strconvQuote(name)+`,"platform":`+strconvQuote(platform)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /pair: got %d %s", rec.Code, rec.Body.String())
	}
	var out pairingStart
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /pair: %v", err)
	}
	if len(out.Pin) != 9 || out.PollSecret == "" {
		t.Fatalf("POST /pair returned pin=%q poll_secret=%q", out.Pin, out.PollSecret)
	}
	return out
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// poll posts a poll secret and returns the decoded body map.
func poll(t *testing.T, mux *http.ServeMux, secret string) *httptest.ResponseRecorder {
	t.Helper()
	return pairingPost(t, mux, pairingTestRemote, "/pair/poll", `{"poll_secret":`+strconvQuote(secret)+`}`)
}

// approve posts valid app_user credentials against a PIN.
func approve(t *testing.T, mux *http.ServeMux, pin, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"pin": pin, "email": email, "password": password})
	return pairingPost(t, mux, pairingTestRemote, "/pin/approve", string(body))
}

func pairRows(t *testing.T, db storage.DB) []domain.Pairing {
	t.Helper()
	var rows []domain.Pairing
	if err := db.Query(context.Background(), &rows, "SELECT "+pairingColumns+" FROM pairing"); err != nil {
		t.Fatalf("query pairing: %v", err)
	}
	return rows
}

func seedJellyfinSource(t *testing.T, db storage.DB, url, key string) {
	t.Helper()
	cipher, err := crypto.Encrypt(testEncKey, []byte(key))
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := db.Exec(context.Background(),
		`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"jellyfin-src", DefaultTenantID, string(domain.SourceJellyfin), "Jellyfin",
		url, cipher, string(domain.SyncPull), now, 1, now, now, now); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
}

func TestPairingHappyPath(t *testing.T) {
	db, mux := newTestDB(t)
	p := startPairing(t, mux, "Living Room", "android_tv")

	rec := poll(t, mux, p.PollSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending poll: got %d %s", rec.Code, rec.Body.String())
	}
	var pending struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" {
		t.Fatalf("pending poll status = %q", pending.Status)
	}

	if rec := approve(t, mux, p.Pin, testUserEmail, testUserPassword); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d %s", rec.Code, rec.Body.String())
	}

	rec = poll(t, mux, p.PollSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("approved poll: got %d %s", rec.Code, rec.Body.String())
	}
	var approved struct {
		Status      string `json:"status"`
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.DeviceToken == "" {
		t.Fatalf("approved poll = %+v", approved)
	}

	// The raw device token must never be stored: only its hash.
	var devs []domain.Device
	if err := db.Query(context.Background(), &devs, "SELECT "+deviceColumns+" FROM device WHERE tenant_id = ?", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("device rows = %d, want 1", len(devs))
	}
	if devs[0].APITokenHash != hashToken(approved.DeviceToken) || devs[0].APITokenHash == approved.DeviceToken {
		t.Fatalf("device token stored raw or wrong hash")
	}

	// Delivery is once-only: the pairing row is gone, so a second poll 404s.
	if rec := poll(t, mux, p.PollSecret); rec.Code != http.StatusNotFound {
		t.Fatalf("second poll: got %d, want 404", rec.Code)
	}
	if n := len(pairRows(t, db)); n != 0 {
		t.Fatalf("pairing rows after delivery = %d, want 0", n)
	}
}

func TestPairingDeliversJellyfinConfig(t *testing.T) {
	db, mux := newTestDB(t)
	seedJellyfinSource(t, db, "https://jellyfin.example.com", "jellyfin-api-key-1")
	p := startPairing(t, mux, "Bedroom", "android_tv")

	if rec := approve(t, mux, p.Pin, testUserEmail, testUserPassword); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d %s", rec.Code, rec.Body.String())
	}
	rec := poll(t, mux, p.PollSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: got %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status      string `json:"status"`
		JellyfinURL string `json:"jellyfin_url"`
		JellyfinKey string `json:"jellyfin_api_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "approved" || out.JellyfinURL != "https://jellyfin.example.com" || out.JellyfinKey != "jellyfin-api-key-1" {
		t.Fatalf("poll = %+v", out)
	}
}

func TestPairingRejectsWrongPinAndCredentials(t *testing.T) {
	_, mux := newTestDB(t)
	p := startPairing(t, mux, "Phone", "android")

	if rec := approve(t, mux, "ZZZZ-ZZZZ", testUserEmail, testUserPassword); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong PIN: got %d, want 400", rec.Code)
	}
	if rec := approve(t, mux, p.Pin, testUserEmail, "wrong-password"); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong password: got %d, want 400", rec.Code)
	}
	if rec := approve(t, mux, p.Pin, "nobody@example.com", testUserPassword); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown e-mail: got %d, want 400", rec.Code)
	}
}

func TestPairingDeletesAfterFailedApprovalLimit(t *testing.T) {
	db, mux := newTestDB(t)
	p := startPairing(t, mux, "Tablet", "windows")

	for i := 0; i < maxPairingFailures-1; i++ {
		if rec := approve(t, mux, p.Pin, testUserEmail, "wrong-password"); rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: got %d, want 400", i+1, rec.Code)
		}
	}
	rows := pairRows(t, db)
	if len(rows) != 1 || rows[0].FailedAttempts != int64(maxPairingFailures-1) {
		t.Fatalf("rows after %d failures = %+v", maxPairingFailures-1, rows)
	}
	if rec := poll(t, mux, p.PollSecret); rec.Code != http.StatusOK {
		t.Fatalf("poll before deletion: got %d, want 200", rec.Code)
	}

	// The failing attempt that crosses the limit deletes the row.
	if rec := approve(t, mux, p.Pin, testUserEmail, "wrong-password"); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit attempt: got %d, want 400", rec.Code)
	}
	if n := len(pairRows(t, db)); n != 0 {
		t.Fatalf("pairing rows after limit = %d, want 0", n)
	}
	if rec := poll(t, mux, p.PollSecret); rec.Code != http.StatusNotFound {
		t.Fatalf("poll after deletion: got %d, want 404", rec.Code)
	}
}

func TestPairingExpiry(t *testing.T) {
	db, mux := newTestDB(t)
	p := startPairing(t, mux, "Old Device", "android_tv")

	// Age the row past its TTL, as if the client stopped polling for 10 min.
	if err := db.Exec(context.Background(),
		"UPDATE pairing SET expires_at = ?", time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if rec := poll(t, mux, p.PollSecret); rec.Code != http.StatusNotFound {
		t.Fatalf("poll expired: got %d, want 404", rec.Code)
	}
	if n := len(pairRows(t, db)); n != 0 {
		t.Fatalf("expired rows = %d, want 0", n)
	}
}

func TestPairingStoresOnlyHashes(t *testing.T) {
	db, mux := newTestDB(t)
	p := startPairing(t, mux, "Secret Check", "android_tv")

	rows := pairRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.PinHash != hashToken(normalizePairingPin(p.Pin)) || r.PinHash == p.Pin {
		t.Fatalf("PIN not stored as its normalized hash")
	}
	if r.PollSecretHash != hashToken(p.PollSecret) || r.PollSecretHash == p.PollSecret {
		t.Fatalf("poll secret not stored as its hash")
	}
}

func TestPairingRateLimit(t *testing.T) {
	_, mux := newTestDB(t)
	for i := 0; i < pairPerAddrLimit; i++ {
		rec := pairingPost(t, mux, pairingTestRemote, "/pair", `{"name":"Flood","platform":"android_tv"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("pair %d: got %d, want 200", i+1, rec.Code)
		}
	}
	if rec := pairingPost(t, mux, pairingTestRemote, "/pair", `{"name":"Flood","platform":"android_tv"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("pair over limit: got %d, want 429", rec.Code)
	}
	if rec := poll(t, mux, "deadbeef"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown poll secret: got %d, want 404", rec.Code)
	}
}
