package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testJellyfinAPIKey is deliberately long and distinctive so tests can assert
// it never appears in any response body (only its last 4 chars may).
const testJellyfinAPIKey = "key-1234567890-abcdefghijklmnop-SECRET"

func decodeServerConfig(t *testing.T, rec *httptest.ResponseRecorder) serverConfigResponse {
	t.Helper()
	var got serverConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode server config: %v (body %s)", err, rec.Body.String())
	}
	return got
}

func TestServerConfigAuthRequired(t *testing.T) {
	_, mux := newTestDB(t)

	for _, tc := range []struct{ method, body string }{
		{"GET", ""},
		{"PUT", `{"base_url":"https://jellyfin.example.com","api_key":"` + testJellyfinAPIKey + `"}`},
	} {
		req := httptest.NewRequest(tc.method, "/server-config/jellyfin", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without auth: status = %d, want 401, body = %s", tc.method, rec.Code, rec.Body.String())
		}
	}
}

func TestServerConfigGetUnset(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "GET", "/server-config/jellyfin", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET unset: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeServerConfig(t, rec)
	if got.BaseURL != "" || got.APIKeySet || got.APIKeyHint != "" {
		t.Errorf("GET unset = %+v, want empty base_url/hint and api_key_set false", got)
	}
}

func TestServerConfigPutThenGetMasksKey(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://jellyfin.example.com","api_key":"`+testJellyfinAPIKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testJellyfinAPIKey) {
		t.Fatalf("PUT response leaked the full API key: %s", rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/server-config/jellyfin", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after PUT: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testJellyfinAPIKey) {
		t.Fatalf("GET response leaked the full API key: %s", rec.Body.String())
	}
	got := decodeServerConfig(t, rec)
	if got.BaseURL != "https://jellyfin.example.com" {
		t.Errorf("base_url = %q, want https://jellyfin.example.com", got.BaseURL)
	}
	if !got.APIKeySet {
		t.Errorf("api_key_set = false, want true")
	}
	if want := "CRET"; got.APIKeyHint != want {
		t.Errorf("api_key_hint = %q, want %q (last 4 only)", got.APIKeyHint, want)
	}
}

func TestServerConfigUpdateKeepsKeyWhenEmpty(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://jellyfin.example.com","api_key":"`+testJellyfinAPIKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first PUT: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://other.example.com","api_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT with empty api_key: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testJellyfinAPIKey) {
		t.Fatalf("PUT response leaked the full API key: %s", rec.Body.String())
	}
	got := decodeServerConfig(t, rec)
	if got.BaseURL != "https://other.example.com" {
		t.Errorf("base_url = %q, want https://other.example.com", got.BaseURL)
	}
	if !got.APIKeySet || got.APIKeyHint != "CRET" {
		t.Errorf("kept key = set:%v hint:%q, want set:true hint:CRET", got.APIKeySet, got.APIKeyHint)
	}

	// The stored key must still be the original one, not empty.
	rec = doRequest(t, mux, "GET", "/server-config/jellyfin", "")
	got = decodeServerConfig(t, rec)
	if !got.APIKeySet || got.APIKeyHint != "CRET" {
		t.Errorf("GET after keep = %+v, want key still set with hint CRET", got)
	}
}

func TestServerConfigPutRequiresKeyWhenNoneStored(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://jellyfin.example.com","api_key":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT empty key with nothing stored: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestServerConfigValidation(t *testing.T) {
	_, mux := newTestDB(t)

	for _, baseURL := range []string{"", "not-a-url", "ftp://jellyfin.example.com", "http://", "https:///path"} {
		rec := doRequest(t, mux, "PUT", "/server-config/jellyfin",
			`{"base_url":"`+baseURL+`","api_key":"`+testJellyfinAPIKey+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT base_url=%q: status = %d, want 400, body = %s", baseURL, rec.Code, rec.Body.String())
		}
	}

	// A malformed body must also be rejected without touching storage.
	rec := doRequest(t, mux, "PUT", "/server-config/jellyfin", `{"base_url":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT malformed body: status = %d, want 400", rec.Code)
	}
}

func TestServerConfigFullKeyNeverInResponse(t *testing.T) {
	_, mux := newTestDB(t)

	bodies := []string{}
	rec := doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://jellyfin.example.com","api_key":"`+testJellyfinAPIKey+`"}`)
	bodies = append(bodies, rec.Body.String())
	rec = doRequest(t, mux, "GET", "/server-config/jellyfin", "")
	bodies = append(bodies, rec.Body.String())
	rec = doRequest(t, mux, "PUT", "/server-config/jellyfin",
		`{"base_url":"https://jellyfin.example.com","api_key":""}`)
	bodies = append(bodies, rec.Body.String())

	for i, b := range bodies {
		if strings.Contains(b, testJellyfinAPIKey) {
			t.Errorf("response %d leaked the full API key: %s", i, b)
		}
	}
}
