package api

import "testing"

func TestRoutesListsRegisteredPatterns(t *testing.T) {
	got := map[string]bool{}
	for _, p := range Routes() {
		got[p] = true
	}
	for _, want := range []string{"POST /channels", "PUT /channels/{id}", "GET /epg", "POST /sessions", "GET /stream-preferences/config"} {
		if !got[want] {
			t.Errorf("Routes() is missing %q", want)
		}
	}
}
