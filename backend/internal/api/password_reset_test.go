package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

const testNewPassword = "fake-new-password-1"

// consume posts to the open consume route from the given peer address.
func consume(t *testing.T, mux *http.ServeMux, remote, token, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "new_password": password})
	req := httptest.NewRequest("POST", "/password-resets/consume", strings.NewReader(string(body)))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func login200(t *testing.T, mux *http.ServeMux, password string) bool {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": testUserEmail, "password": password})
	req := httptest.NewRequest("POST", "/sessions", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code == http.StatusOK
}

func sessionWorks(mux *http.ServeMux) bool {
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+testSessionToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code == http.StatusOK
}

func pendingResets(t *testing.T, db storage.DB) int {
	t.Helper()
	var n []int64
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM password_reset"); err != nil {
		t.Fatal(err)
	}
	return int(n[0])
}

func TestPasswordResetLifecycle(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()

	// A device token that must survive the reset.
	rec := doRequest(t, mux, "POST", "/devices", `{"name":"tv","platform":"linux"}`)
	var dev createDeviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &dev); err != nil || dev.Token == "" {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body.String())
	}

	reset, err := CreatePasswordReset(ctx, db, "", DefaultResetTTL)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Email != testUserEmail {
		t.Fatalf("email = %q", reset.Email)
	}
	// Only a hash is stored.
	var stored []string
	if err := db.Query(ctx, &stored, "SELECT token_hash FROM password_reset"); err != nil || len(stored) != 1 {
		t.Fatalf("stored: %v %v", stored, err)
	}
	if stored[0] == reset.Token || stored[0] != hashToken(reset.Token) {
		t.Fatal("token is not stored as its hash")
	}

	rec = consume(t, mux, "192.0.2.1:1000", reset.Token, testNewPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("consume: %d %s", rec.Code, rec.Body.String())
	}
	var resp consumeResetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.SessionsRevoked || !resp.DeviceTokensKept {
		t.Fatalf("response = %+v", resp)
	}
	if strings.Contains(rec.Body.String(), reset.Token) || strings.Contains(rec.Body.String(), testNewPassword) {
		t.Fatal("response echoes a secret")
	}

	if login200(t, mux, testUserPassword) {
		t.Fatal("old password still works")
	}
	if !login200(t, mux, testNewPassword) {
		t.Fatal("new password does not work")
	}
	if sessionWorks(mux) {
		t.Fatal("old session survived the reset")
	}
	req := httptest.NewRequest("GET", "/epg", nil)
	req.Header.Set("Authorization", "Bearer "+dev.Token)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("device token was revoked")
	}

	// Single use.
	rec = consume(t, mux, "192.0.2.1:1000", reset.Token, "another-fake-pass-2")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reuse: %d", rec.Code)
	}
	if !login200(t, mux, testNewPassword) {
		t.Fatal("reuse changed the password")
	}
}

func TestPasswordResetReplacedByNewRun(t *testing.T) {
	db, mux := newTestDB(t)
	first, _ := CreatePasswordReset(context.Background(), db, "", DefaultResetTTL)
	second, err := CreatePasswordReset(context.Background(), db, testUserEmail, DefaultResetTTL)
	if err != nil {
		t.Fatal(err)
	}
	if pendingResets(t, db) != 1 {
		t.Fatal("a second run must replace the pending token")
	}
	if rec := consume(t, mux, "192.0.2.1:1", first.Token, testNewPassword); rec.Code != http.StatusBadRequest {
		t.Fatalf("replaced token: %d", rec.Code)
	}
	if rec := consume(t, mux, "192.0.2.1:1", second.Token, testNewPassword); rec.Code != http.StatusOK {
		t.Fatalf("new token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPasswordResetExpired(t *testing.T) {
	db, mux := newTestDB(t)
	reset, _ := CreatePasswordReset(context.Background(), db, "", DefaultResetTTL)
	if err := db.Exec(context.Background(), "UPDATE password_reset SET expires_at = ?", time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	rec := consume(t, mux, "192.0.2.1:1", reset.Token, testNewPassword)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired: %d", rec.Code)
	}
	if !login200(t, mux, testUserPassword) {
		t.Fatal("expired token changed the password")
	}
	if pendingResets(t, db) != 0 {
		t.Fatal("expired token should be dropped")
	}
}

func TestPasswordResetNoOracle(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	used, _ := CreatePasswordReset(ctx, db, "", DefaultResetTTL)
	consume(t, mux, "192.0.2.9:1", used.Token, testNewPassword)
	expired, _ := CreatePasswordReset(ctx, db, "", DefaultResetTTL)
	db.Exec(ctx, "UPDATE password_reset SET expires_at = 1")

	var bodies []string
	for _, tok := range []string{"unknown-fake-token", used.Token, expired.Token} {
		rec := consume(t, mux, "192.0.2.2:1", tok, "fake-password-xyz")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
		CreatePasswordReset(ctx, db, "", DefaultResetTTL) // keep one pending for the next probe
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("responses differ: %q", bodies)
	}
}

func TestPasswordResetBurnedAfterFailures(t *testing.T) {
	db, mux := newTestDB(t)
	reset, _ := CreatePasswordReset(context.Background(), db, "", DefaultResetTTL)
	for i := 0; i < maxResetFailures; i++ {
		// A different token string each time, so only the failure counter acts.
		rec := consume(t, mux, "192.0.2.3:1", "wrong-fake-token-"+string(rune('a'+i)), testNewPassword)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if pendingResets(t, db) != 0 {
		t.Fatal("token should be burned after repeated failures")
	}
	if rec := consume(t, mux, "192.0.2.3:1", reset.Token, testNewPassword); rec.Code != http.StatusBadRequest {
		t.Fatalf("correct token after burn: %d", rec.Code)
	}
}

func TestPasswordResetMinimumLength(t *testing.T) {
	db, mux := newTestDB(t)
	reset, _ := CreatePasswordReset(context.Background(), db, "", DefaultResetTTL)
	rec := consume(t, mux, "192.0.2.4:1", reset.Token, "short")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", rec.Code)
	}
	if pendingResets(t, db) != 1 {
		t.Fatal("a rejected password must not spend the token")
	}
	if rec := consume(t, mux, "192.0.2.4:1", reset.Token, testNewPassword); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d", rec.Code)
	}
}

func TestPasswordResetRateLimitPerAddress(t *testing.T) {
	_, mux := newTestDB(t)
	var last int
	for i := 0; i < resetPerAddrLimit+1; i++ {
		last = consume(t, mux, "192.0.2.5:1", "wrong-fake-token-"+strings.Repeat("x", i+1), testNewPassword).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("last status = %d, want 429", last)
	}
	// Another address is unaffected.
	if rec := consume(t, mux, "192.0.2.6:1", "wrong-fake-token", testNewPassword); rec.Code != http.StatusBadRequest {
		t.Fatalf("other address: %d", rec.Code)
	}
}

func TestPasswordResetRateLimitPerToken(t *testing.T) {
	_, mux := newTestDB(t)
	var last int
	for i := 0; i < resetPerTokenLimit+1; i++ {
		// Distinct addresses: only the token budget can trip.
		last = consume(t, mux, "192.0.2."+string(rune('1'+i))+":1", "same-fake-token", testNewPassword).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("last status = %d, want 429", last)
	}
}

func TestAttemptLimiterWindowResets(t *testing.T) {
	l := newAttemptLimiter(2, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	if !l.allow("k") || !l.allow("k") || l.allow("k") {
		t.Fatal("limit not enforced")
	}
	now = now.Add(time.Minute)
	if !l.allow("k") {
		t.Fatal("window did not reset")
	}
}

func TestCreatePasswordResetValidation(t *testing.T) {
	db, _ := newTestDB(t)
	ctx := context.Background()
	for _, ttl := range []time.Duration{0, time.Second, 2 * time.Hour} {
		if _, err := CreatePasswordReset(ctx, db, "", ttl); err == nil {
			t.Fatalf("ttl %s accepted", ttl)
		}
	}
	if _, err := CreatePasswordReset(ctx, db, "nobody@example.com", DefaultResetTTL); err == nil {
		t.Fatal("unknown email accepted")
	}
	// Two users: email becomes required.
	db.Exec(ctx, "INSERT INTO app_user (id, tenant_id, email, password_hash, created_at) VALUES ('u2', ?, 'two@example.com', 'x', 1)", DefaultTenantID)
	if _, err := CreatePasswordReset(ctx, db, "", DefaultResetTTL); err == nil {
		t.Fatal("no email accepted with several users")
	}
	if _, err := CreatePasswordReset(ctx, db, "two@example.com", DefaultResetTTL); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePasswordResetNoUser(t *testing.T) {
	db, _ := newBareTestDB(t)
	if _, err := CreatePasswordReset(context.Background(), db, "", DefaultResetTTL); err == nil {
		t.Fatal("expected an error with no user")
	}
}
