package api

import (
	"net/http"
	"strings"
	"testing"
)

// A hostile client must not be able to make the public pairing routes buffer an
// arbitrarily large body.
func TestPairingRoutesRejectOversizedBodies(t *testing.T) {
	_, mux := newTestDB(t)
	huge := `{"name":"` + strings.Repeat("a", 64<<10) + `","platform":"android_tv"}`
	for _, path := range []string{"/pair", "/pair/poll", "/pin/approve"} {
		rec := pairingPost(t, mux, pairingTestRemote, path, huge)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s with a 64 KiB body: got %d, want 400", path, rec.Code)
		}
	}
}

// Two approvals that both carry a Jellyfin address must leave exactly one
// media_source row: the insert is a single conditional statement.
func TestPairingJellyfinConfigIsCreatedOnce(t *testing.T) {
	db, mux := newTestDB(t)
	for i := 0; i < 3; i++ {
		started := startPairing(t, mux, "TV", "android_tv")
		body := `{"pin":` + strconvQuote(started.Pin) + `,"email":` + strconvQuote(testUserEmail) + `,"password":` + strconvQuote(testUserPassword) + `,` +
			`"jellyfin_url":"http://jellyfin.example.com:8096","jellyfin_api_key":"key-` + strconvQuote(started.Pin)[1:3] + `"}`
		rec := pairingPost(t, mux, pairingTestRemote, "/pin/approve", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("approve %d: got %d %s", i, rec.Code, rec.Body.String())
		}
	}
	var counts []int64
	if err := db.Query(t.Context(), &counts, "SELECT COUNT(*) FROM media_source WHERE type = 'jellyfin'"); err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("jellyfin media_source rows = %v, want exactly 1", counts)
	}
}

// A malformed Jellyfin address on the approval form is ignored rather than
// stored: it would later be used to build request URLs.
func TestPairingIgnoresInvalidJellyfinAddress(t *testing.T) {
	db, mux := newTestDB(t)
	started := startPairing(t, mux, "TV", "android_tv")
	body := `{"pin":` + strconvQuote(started.Pin) + `,"email":` + strconvQuote(testUserEmail) + `,"password":` + strconvQuote(testUserPassword) + `,` +
		`"jellyfin_url":"javascript:alert(1)","jellyfin_api_key":"k"}`
	if rec := pairingPost(t, mux, pairingTestRemote, "/pin/approve", body); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d %s", rec.Code, rec.Body.String())
	}
	var counts []int64
	if err := db.Query(t.Context(), &counts, "SELECT COUNT(*) FROM media_source WHERE type = 'jellyfin'"); err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(counts) != 1 || counts[0] != 0 {
		t.Fatalf("a javascript: address was stored: rows = %v", counts)
	}
}

// The per-PIN counter is keyed on caller-chosen text, so it must not grow once
// the per-address limit has cut the caller off.
func TestApprovalPerPinLimiterNotFedByBlockedAddress(t *testing.T) {
	byAddr := newAttemptLimiter(2, pairingWindow)
	byPin := newAttemptLimiter(5, pairingWindow)
	for i := 0; i < 50; i++ {
		okAddr := byAddr.allow("203.0.113.9")
		_ = okAddr && byPin.allow(strconvQuote(string(rune('A'+i%26)))+string(rune('0'+i%10)))
	}
	if len(byPin.hits) > 2 {
		t.Fatalf("per-PIN limiter grew to %d keys despite a blocked address", len(byPin.hits))
	}
}
