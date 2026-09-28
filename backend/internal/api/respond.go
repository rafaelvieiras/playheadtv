// Package api wires the REST endpoints from docs/ROADMAP.md's Phase 1 "REST
// API" bullet onto storage.DB. Every handler operates on DefaultTenantID —
// there's no auth yet (see TODO in cmd/server/main.go), so multi-tenancy
// isn't reachable from the API even though the schema supports it.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// DefaultTenantID is the implicit tenant every handler operates on until
// Auth (a separate roadmap item) exists.
const DefaultTenantID = "default"

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// decodeJSON reports malformed bodies to the caller as a plain error; callers
// turn that into a 400.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// handleDBError maps a storage error to the right HTTP status, per the
// conventions in the task brief: sql.ErrNoRows -> 404, UNIQUE violations ->
// 409, FOREIGN KEY/CHECK violations -> 400 (relying on PRAGMA foreign_keys =
// ON instead of validating referenced IDs by hand), anything else -> 500,
// logged server-side without leaking details to the client.
func handleDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		writeError(w, http.StatusConflict, "already exists")
	case strings.Contains(msg, "FOREIGN KEY constraint failed"),
		strings.Contains(msg, "CHECK constraint failed"):
		writeError(w, http.StatusBadRequest, "invalid reference or value")
	default:
		log.Printf("api: internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
