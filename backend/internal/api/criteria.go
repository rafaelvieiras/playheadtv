package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerCriteria(mux *http.ServeMux, db storage.DB) {
	mux.HandleFunc("GET /criteria-migration-review", listCriteriaReview(db))
}

// parseCriteriaField validates the optional criteria of a block or filler pool
// write and returns the canonical condition tree JSON to store. An absent or
// null value means "not dynamic" and returns (nil, nil). The error text is
// safe to return to the client.
func parseCriteriaField(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	q, err := catalogsync.ParseQuery(raw)
	if err != nil {
		if errors.Is(err, catalogsync.ErrLegacyCriteria) {
			return nil, err
		}
		return nil, errors.New("criteria: " + err.Error())
	}
	s := q.JSON()
	return &s, nil
}

// listCriteriaReview lists the dynamic blocks and filler pools whose legacy
// criteria could not be converted to a condition tree without changing what
// they match (see catalogsync.MigrateLegacyCriteria): the legacy value is
// still in use, and each entry carries the proposed query plus how the matched
// set would change. Applying one is a PUT of the block/pool with that query.
func listCriteriaReview(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := catalogsync.ListCriteriaReview(r.Context(), db, DefaultTenantID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// clearReview drops the migration review row of a block/pool whose criteria
// was just rewritten (or that was deleted).
func clearReview(ctx context.Context, db storage.DB, ownerType, ownerID string) error {
	return catalogsync.ClearCriteriaReview(ctx, db, DefaultTenantID, ownerType, ownerID)
}
