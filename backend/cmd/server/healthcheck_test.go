package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckURL(t *testing.T) {
	cases := map[string]string{
		":9394":          "http://127.0.0.1:9394/healthz",
		"0.0.0.0:8000":   "http://127.0.0.1:8000/healthz",
		"127.0.0.1:7000": "http://127.0.0.1:7000/healthz",
		"":               "http://127.0.0.1:9394/healthz",
		"garbage":        "http://127.0.0.1:9394/healthz",
	}
	for addr, want := range cases {
		if got := healthcheckURL(addr); got != want {
			t.Errorf("healthcheckURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestRunHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	addr := ":" + port

	if err := runHealthcheck(addr); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := runHealthcheck(addr); err == nil {
		t.Fatal("a 503 must fail the healthcheck")
	}
	srv.Close()
	if err := runHealthcheck(addr); err == nil {
		t.Fatal("an unreachable server must fail the healthcheck")
	}
}
