// Package mcpserver exposes a deliberately partial slice of the REST API
// (docs/ROADMAP.md Phase 1) as MCP tools, over the streamable-HTTP transport
// from github.com/modelcontextprotocol/go-sdk, in the same binary as the
// REST API and the admin SPA (docs/ROADMAP.md Phase 2 "MCP server").
//
// Every tool talks to storage.DB directly, mirroring the SQL/validation of
// the corresponding internal/api handler rather than calling the REST API
// over HTTP loopback or introducing a new service layer — see the handlers
// in internal/api/*.go for the source of truth each tool here copies from.
// Like internal/materializer, this package does not import internal/api: it
// duplicates the small set of constants it needs (DefaultTenantID, column
// aliases) instead, so it keeps working unchanged once real multi-tenancy
// (the Auth roadmap item) exists and so it never depends on unexported
// internal/api identifiers.
//
// Authentication is not this package's concern: Register wires the MCP
// handler onto mux at POST/GET /mcp, which — precisely because it is not
// added to isOpenPath or deviceOrSessionGET in internal/api/auth.go — falls
// into RequireAuth's default "everything else requires a session" branch by
// exclusion. Callers must send the same "Authorization: Bearer <session
// token>" header POST /sessions returns, exactly like the web UI does; no
// session/auth logic is duplicated here.
//
// Scope: every REST operation the web UI uses has a tool (coverage.go lists
// which tool serves which route, and TestEveryRESTRouteHasAnMCPTool enforces
// it). The older tools above talk to storage.DB directly or through the shared
// domain packages; the REST-backed ones (rest.go, rest_tools.go) dispatch to
// the REST handler in-process, so they import internal/api and carry no logic
// of their own. Destructive tools require confirm=true.
package mcpserver

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// defaultTenantID mirrors api.DefaultTenantID (internal/api/respond.go) —
// duplicated rather than imported, see the package doc comment above.
const defaultTenantID = "default"

// probeTimeout mirrors internal/api/media_sources.go's probeClient timeout.
const probeTimeout = 5 * time.Second

// nowMillis is the same epoch-millis convention every internal/api handler
// uses for CreatedAt/UpdatedAt/etc.
func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// Register builds the MCP server for the tools documented in the package
// comment and mounts it at /mcp on mux, using the streamable-HTTP transport
// (the SDK's recommended in-binary HTTP transport). Called once from
// cmd/server/main.go, right after api.Register — same db/mat/encKey the REST
// handlers were built with, so a write made through a tool is immediately
// visible to the REST API and vice versa.
func Register(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer, encKey [32]byte) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "playheadtv",
		Version: "1.0.0",
	}, nil)

	registerChannelTools(server, db, mat)
	registerMediaSourceTools(server, db, encKey)
	registerCatalogTools(server, db)
	registerCollectionTools(server, db, mat)
	registerBlockTools(server, db, mat)
	registerFillerTools(server, db, mat)
	registerCutRuleTools(server, db, mat)
	registerScheduleTools(server, db, mat)
	registerEPGTools(server, db)
	registerAuthoringTools(server, db, mat)
	// Every other REST operation, as a thin in-process dispatch (rest.go).
	registerRESTTools(server, db, mux)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
	mux.Handle("/mcp", handler)
}
