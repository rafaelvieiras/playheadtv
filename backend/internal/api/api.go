package api

import (
	"net/http"
	"sort"

	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Register wires every REST resource handler onto mux. Called once from
// cmd/server/main.go, after the DefaultTenantID row is guaranteed to exist.
// mat is never run synchronously here — handlers that write to a scheduling
// table only call mat.Trigger() to ask for a background regeneration (see
// docs/ARCHITECTURE.md "EPG materialization"); sync is the same non-blocking
// pattern for pull-mode media sources (registerMediaSources' create/update
// call sync.Trigger()). encKey encrypts/decrypts data-at-rest fields
// (currently just media_source.credential_cipher); see
// internal/crypto.LoadOrGenerateKey for how callers should obtain it.
func Register(mux Router, db storage.DB, mat *materializer.Materializer, sync *pullsync.Syncer, encKey [32]byte) {
	registerAuth(mux, db)
	registerPairing(mux, db, encKey)
	registerServerConfig(mux, db, encKey)
	registerChannels(mux, db, mat)
	registerMediaSources(mux, db, mat, sync, encKey)
	registerCatalogItems(mux, db)
	registerSeries(mux, db)
	registerCriteria(mux, db, mat)
	registerCollections(mux, db, mat)
	registerBlocks(mux, db, mat)
	registerFiller(mux, db, mat)
	registerSchedules(mux, db, mat)
	registerWatermarks(mux, db)
	registerEPG(mux, db, mat)
	registerStreamPreferences(mux, db)
}

// Router is the slice of *http.ServeMux the register functions use. Taking an
// interface lets Routes record the route table from the same code that wires
// the real mux, so nothing has to keep a second list in step.
type Router interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

type routeRecorder struct{ patterns []string }

func (rr *routeRecorder) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	rr.patterns = append(rr.patterns, pattern)
}

// Routes returns every "METHOD /path" pattern Register wires, sorted. It runs
// Register against a recorder with no database, so it only reflects what is
// registered and never serves anything. The MCP parity test (see
// internal/mcpserver) is built on it.
func Routes() []string {
	rr := &routeRecorder{}
	Register(rr, nil, nil, nil, [32]byte{})
	sort.Strings(rr.patterns)
	return rr.patterns
}
