package api

import (
	"net/http"

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
func Register(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer, sync *pullsync.Syncer, encKey [32]byte) {
	registerAuth(mux, db)
	registerChannels(mux, db, mat)
	registerMediaSources(mux, db, mat, sync, encKey)
	registerCatalogItems(mux, db)
	registerSeries(mux, db)
	registerCriteria(mux, db, mat)
	registerCollections(mux, db, mat)
	registerBlocks(mux, db, mat)
	registerFiller(mux, db, mat)
	registerSchedules(mux, db, mat)
	registerEPG(mux, db, mat)
	registerStreamPreferences(mux, db)
}
