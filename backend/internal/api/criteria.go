package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerCriteria(mux Router, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("GET /criteria-migration-review", listCriteriaReview(db))
	mux.HandleFunc("POST /criteria-migration-review/apply", applyReview(db, mat))
}

// listCriteriaReview lists the dynamic blocks and filler pools whose legacy
// criteria could not be converted to a condition tree without changing what
// they match (see catalogsync.MigrateLegacyCriteria), plus those whose query
// could not be moved into a collection unchanged (MigrateToCollections): the
// stored value is still in use, and each entry carries the proposed query plus
// how the matched set would change. Applying one is POST
// /criteria-migration-review/apply, which produces a collection.
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

// clearReview drops the migration review row of a block/pool/item that now
// uses a collection (or was deleted).
func clearReview(ctx context.Context, db storage.DB, ownerType, ownerID string) error {
	return catalogsync.ClearCriteriaReview(ctx, db, DefaultTenantID, ownerType, ownerID)
}

// applyReviewRequest picks one held row; query optionally replaces the
// proposed tree (required when the legacy criteria has no proposal).
type applyReviewRequest struct {
	OwnerType string          `json:"owner_type"`
	OwnerID   string          `json:"owner_id"`
	Query     json.RawMessage `json:"query"`
}

type applyReviewResponse struct {
	CollectionID string            `json:"collection_id"`
	Created      bool              `json:"created"`
	Collection   domain.Collection `json:"collection"`
}

// applyReview resolves one row held for review: it creates (or reuses, for an
// identical tree) a collection with the proposed query (or the one sent),
// points the block, pool or criteria item at it, drops the review row and
// re-resolves membership. The owner's deprecated stored query stays as the
// snapshot.
func applyReview(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req applyReviewRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		switch req.OwnerType {
		case catalogsync.OwnerBlock, catalogsync.OwnerFillerPool, catalogsync.OwnerBlockItem:
		default:
			writeError(w, http.StatusBadRequest, "owner_type must be block, filler_pool or block_item")
			return
		}
		id, created, err := catalogsync.ApplyReviewProposal(r.Context(), db, DefaultTenantID, req.OwnerType, req.OwnerID, req.Query)
		if err != nil {
			switch {
			case errors.Is(err, catalogsync.ErrNotHeld):
				writeError(w, http.StatusNotFound, err.Error())
			case strings.HasPrefix(err.Error(), "query:") || strings.HasPrefix(err.Error(), "this legacy criteria"):
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				handleDBError(w, err)
			}
			return
		}
		if req.OwnerType == catalogsync.OwnerFillerPool {
			err = catalogsync.ResolveDynamicPools(r.Context(), db, DefaultTenantID)
		} else if req.OwnerType == catalogsync.OwnerBlock {
			err = catalogsync.ResolveDynamicBlocks(r.Context(), db, DefaultTenantID)
		}
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		c, err := catalogsync.GetCollection(r.Context(), db, DefaultTenantID, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, applyReviewResponse{CollectionID: id, Created: created, Collection: c})
	}
}
