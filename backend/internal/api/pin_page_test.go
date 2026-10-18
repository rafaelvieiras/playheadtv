package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// pinPageGet drives GET /pin as a raw public peer, the way a phone browser
// would, without any session cookie or device token.
func pinPageGet(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = pairingTestRemote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// pinApproveForm posts the plain HTML form the GET /pin page renders. accept
// controls whether the handler answers HTML or JSON.
func pinApproveForm(t *testing.T, mux *http.ServeMux, accept string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/pin/approve", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", accept)
	req.RemoteAddr = pairingTestRemote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func assertPinPageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
		"X-Frame-Options":         "DENY",
		"Content-Type":            "text/html; charset=utf-8",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestPinPagePrefillsNormalisedCode(t *testing.T) {
	_, mux := newTestDB(t)
	rec := pinPageGet(t, mux, "/pin?code=abcd-efgh")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pin: got %d %s", rec.Code, rec.Body.String())
	}
	assertPinPageHeaders(t, rec)
	body := rec.Body.String()
	if !strings.Contains(body, `value="ABCD-EFGH"`) {
		t.Errorf("form does not prefill the normalised PIN: %s", body)
	}
	if !strings.Contains(body, `action="/pin/approve"`) {
		t.Errorf("form does not post to /pin/approve")
	}
}

func TestPinPageEscapesHostileCode(t *testing.T) {
	_, mux := newTestDB(t)
	rec := pinPageGet(t, mux, "/pin?code="+url.QueryEscape(`<script>alert(1)</script>`))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pin: got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Errorf("hostile ?code= was not escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;") {
		t.Errorf("expected the angle bracket to be escaped, got: %s", body)
	}
}

func TestPinPageJellyfinFieldsOnlyWhenUnconfigured(t *testing.T) {
	db, mux := newTestDB(t)

	body := pinPageGet(t, mux, "/pin").Body.String()
	if !strings.Contains(body, `name="jellyfin_url"`) || !strings.Contains(body, `name="jellyfin_api_key"`) {
		t.Errorf("unconfigured server should show the Jellyfin fields")
	}

	const apiKey = "not-a-real-api-key"
	seedJellyfinSource(t, db, "https://jellyfin.example.com", apiKey)

	body = pinPageGet(t, mux, "/pin").Body.String()
	if strings.Contains(body, `name="jellyfin_url"`) || strings.Contains(body, `name="jellyfin_api_key"`) {
		t.Errorf("configured server must hide the Jellyfin fields")
	}
	if strings.Contains(body, apiKey) {
		t.Errorf("GET /pin leaked the stored Jellyfin API key")
	}
}

func TestPinApproveFormSuccessPage(t *testing.T) {
	db, mux := newTestDB(t)
	seedJellyfinSource(t, db, "http://jellyfin.example.com:8096", "seed-key")
	p := startPairing(t, mux, "Living Room", "android_tv")

	rec := pinApproveForm(t, mux, "text/html,application/xhtml+xml", url.Values{
		"pin":      {p.Pin},
		"email":    {testUserEmail},
		"password": {testUserPassword},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("form approve: got %d %s", rec.Code, rec.Body.String())
	}
	assertPinPageHeaders(t, rec)
	body := rec.Body.String()
	if !strings.Contains(body, "Device paired") {
		t.Errorf("success page missing confirmation: %s", body)
	}
	if strings.Contains(body, testUserPassword) {
		t.Errorf("success page leaked the submitted password")
	}
}

func TestPinApproveFormFailurePage(t *testing.T) {
	_, mux := newTestDB(t)
	p := startPairing(t, mux, "Living Room", "android_tv")

	rec := pinApproveForm(t, mux, "text/html", url.Values{
		"pin":      {p.Pin},
		"email":    {testUserEmail},
		"password": {"definitely-the-wrong-password"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("failed form approve: got %d %s", rec.Code, rec.Body.String())
	}
	assertPinPageHeaders(t, rec)
	body := rec.Body.String()
	if !strings.Contains(body, "Pairing failed") {
		t.Errorf("failure page missing the error: %s", body)
	}
	if strings.Contains(body, "definitely-the-wrong-password") || strings.Contains(body, p.PollSecret) {
		t.Errorf("failure page leaked a secret")
	}
}

func TestPinApproveJSONStaysJSON(t *testing.T) {
	db, mux := newTestDB(t)
	seedJellyfinSource(t, db, "http://jellyfin.example.com:8096", "seed-key")
	p := startPairing(t, mux, "Living Room", "android_tv")

	rec := pinApproveForm(t, mux, "application/json", url.Values{
		"pin":      {p.Pin},
		"email":    {testUserEmail},
		"password": {testUserPassword},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("json approve: got %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON for a non-HTML request", ct)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("JSON request got an HTML page")
	}
}
