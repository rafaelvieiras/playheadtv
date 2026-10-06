package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// healthcheckURL is where the "healthcheck" subcommand probes: the local
// server's /healthz, on the port PLAYHEADTV_ADDR listens on (":9394",
// "0.0.0.0:9394", "127.0.0.1:9394" all probe loopback).
func healthcheckURL(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "9394"
	}
	return "http://127.0.0.1:" + port + "/healthz"
}

// runHealthcheck is `server healthcheck`: exit status 0 when the local server
// answers /healthz with 200. The production image is distroless (no shell, no
// curl), so a container healthcheck has nothing else to run.
func runHealthcheck(addr string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(healthcheckURL(addr))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %s", strings.TrimSpace(resp.Status))
	}
	return nil
}
