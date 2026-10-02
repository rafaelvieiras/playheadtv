package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerBlocks(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	// A block's own name/description/criteria all affect EPG materialization
	// when criteria changes (createBlock/updateBlock trigger both
	// ResolveDynamicBlocks and mat themselves, right after the write) — a
	// plain name-only block still doesn't need it.
	mux.HandleFunc("POST /blocks", createBlock(db, mat))
	mux.HandleFunc("GET /blocks", listBlocks(db))
	mux.HandleFunc("GET /blocks/{id}", getBlock(db))
	mux.HandleFunc("PUT /blocks/{id}", updateBlock(db, mat))
	mux.HandleFunc("DELETE /blocks/{id}", deleteBlock(db))

	// Only block_item writes affect EPG materialization (a block's own
	// name/description doesn't) — see docs/ROADMAP.md's EPG materializer
	// bullet for the exact table list this triggers on.
	mux.HandleFunc("POST /blocks/{id}/items", addBlockItem(db, mat))
	mux.HandleFunc("GET /blocks/{id}/items", listBlockItems(db))
	mux.HandleFunc("PUT /blocks/{id}/items/{item_id}", updateBlockItem(db, mat))
	mux.HandleFunc("DELETE /blocks/{id}/items/{item_id}", deleteBlockItem(db, mat))
}

const blockColumns = "id, tenant_id, name, description, criteria, created_at, updated_at"

// blockItemColumns reads the typed block_item: a series item has a NULL
// catalog_item_id, read back as the empty string.
const blockItemColumns = "id, block_id, position, item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms, spec"

type blockRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Criteria is optional. nil (the JSON field absent or explicitly null)
	// means a manually managed block; a non-nil value makes it dynamic —
	// its block_item rows are then owned by catalogsync.ResolveDynamicBlocks,
	// not by POST/DELETE .../items. Reuses fillerPoolCriteriaRequest/
	// validatePoolCriteria (internal/api/filler.go) — same JSON shape, same
	// validation rules, no reason to duplicate either.
	Criteria *fillerPoolCriteriaRequest `json:"criteria"`
}

func fetchBlock(db storage.DB, r *http.Request, id string) (domain.Block, error) {
	var b domain.Block
	err := db.Query(r.Context(), &b,
		"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return b, err
}

func createBlock(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req blockRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		var criteria *string
		if req.Criteria != nil {
			if msg, ok := validatePoolCriteria(req.Criteria); !ok {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			encoded, err := json.Marshal(req.Criteria)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			s := string(encoded)
			criteria = &s
		}
		now := time.Now().UnixMilli()
		b := domain.Block{
			ID:          uuid.NewString(),
			TenantID:    DefaultTenantID,
			Name:        req.Name,
			Description: req.Description,
			Criteria:    criteria,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err := db.Exec(r.Context(),
			"INSERT INTO block (id, tenant_id, name, description, criteria, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			b.ID, b.TenantID, b.Name, b.Description, b.Criteria, b.CreatedAt, b.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Criteria != nil {
			if err := catalogsync.ResolveDynamicBlocks(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		writeJSON(w, http.StatusCreated, b)
	}
}

func listBlocks(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var blocks []domain.Block
		err := db.Query(r.Context(), &blocks,
			"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? ORDER BY name",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, blocks)
	}
}

func getBlock(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fetchBlock(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func updateBlock(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchBlock(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		var req blockRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		// PUT is a full replace, same semantics name/description already have:
		// omitting criteria here reverts a previously dynamic block to
		// manual, even if it already had one — intentional, not an
		// accidental partial update.
		var criteria *string
		if req.Criteria != nil {
			if msg, ok := validatePoolCriteria(req.Criteria); !ok {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			encoded, err := json.Marshal(req.Criteria)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			s := string(encoded)
			criteria = &s
		}
		err := db.Exec(r.Context(),
			"UPDATE block SET name = ?, description = ?, criteria = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			req.Name, req.Description, criteria, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if criteria != nil {
			if err := catalogsync.ResolveDynamicBlocks(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		b, err := fetchBlock(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func deleteBlock(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchBlock(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM block WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// blockItemRequest adds a block item. item_type is "catalog" (the default,
// unless series is given) or "series". A catalog item takes catalog_item_id
// and optionally trim_duration_ms; a series item takes the series object and
// no trim (every episode plays in full).
type blockItemRequest struct {
	ItemType       string             `json:"item_type"`
	CatalogItemID  string             `json:"catalog_item_id"`
	TrimDurationMs *int64             `json:"trim_duration_ms"`
	Series         *seriesItemRequest `json:"series"`
}

// seriesItemRequest identifies a series (media source plus external id or
// title) and how to walk it. playback_order defaults to original; amount is
// how much one visit of the block rotation airs (default: one episode); slice
// is reserved and rejected while non-null.
type seriesItemRequest struct {
	MediaSourceID    string                    `json:"media_source_id"`
	SeriesExternalID string                    `json:"series_external_id"`
	SeriesTitle      string                    `json:"series_title"`
	PlaybackOrder    string                    `json:"playback_order"`
	Amount           *domain.SeriesAmountInput `json:"amount"`
	Slice            json.RawMessage           `json:"slice"`
}

// decorateItems fills the series view (episode count, total duration) of the
// series items in items.
func decorateItems(r *http.Request, db storage.DB, items []domain.BlockItem) error {
	return series.Decorate(r.Context(), db, DefaultTenantID, items)
}

func addBlockItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		b, err := fetchBlock(db, r, blockID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Criteria != nil {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its criteria")
			return
		}
		var req blockItemRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		itemType := req.ItemType
		if itemType == "" {
			itemType = domain.BlockItemCatalog
			if req.Series != nil {
				itemType = domain.BlockItemSeries
			}
		}
		var specJSON *string
		switch itemType {
		case domain.BlockItemCatalog:
			if req.Series != nil {
				writeError(w, http.StatusBadRequest, "series is only valid for item_type series")
				return
			}
			if req.CatalogItemID == "" {
				writeError(w, http.StatusBadRequest, "catalog_item_id is required")
				return
			}
		case domain.BlockItemSeries:
			if req.CatalogItemID != "" {
				writeError(w, http.StatusBadRequest, "catalog_item_id is not valid for a series item")
				return
			}
			if req.TrimDurationMs != nil {
				writeError(w, http.StatusBadRequest, "trim_duration_ms is not valid for a series item; add a trimmed episode as its own item before the series")
				return
			}
			if req.Series == nil {
				writeError(w, http.StatusBadRequest, "series is required for item_type series")
				return
			}
			spec, err := series.NewSpec(req.Series.MediaSourceID, req.Series.SeriesExternalID, req.Series.SeriesTitle, req.Series.PlaybackOrder, req.Series.Amount, req.Series.Slice)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			found, err := series.Exists(r.Context(), db, DefaultTenantID, spec)
			if err != nil {
				handleDBError(w, err)
				return
			}
			if !found {
				writeError(w, http.StatusBadRequest, "no episodes of that series in the catalog")
				return
			}
			encoded, err := series.Encode(spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			specJSON = &encoded
		default:
			writeError(w, http.StatusBadRequest, "item_type must be catalog or series")
			return
		}

		var maxPosition []int
		if err := db.Query(r.Context(), &maxPosition,
			"SELECT COALESCE(MAX(position), -1) FROM block_item WHERE block_id = ?", blockID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		position := 0
		if len(maxPosition) == 1 {
			position = maxPosition[0] + 1
		}

		item := domain.BlockItem{
			ID:             uuid.NewString(),
			BlockID:        blockID,
			Position:       position,
			ItemType:       itemType,
			CatalogItemID:  req.CatalogItemID,
			TrimDurationMs: req.TrimDurationMs,
			Spec:           specJSON,
		}
		var catalogItemID *string
		if itemType == domain.BlockItemCatalog {
			catalogItemID = &item.CatalogItemID
		}
		err = db.Exec(r.Context(),
			"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec) VALUES (?, ?, ?, ?, ?, ?, ?)",
			item.ID, item.BlockID, item.Position, item.ItemType, catalogItemID, item.TrimDurationMs, item.Spec,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		items := []domain.BlockItem{item}
		if err := decorateItems(r, db, items); err != nil {
			handleDBError(w, err)
			return
		}
		item = items[0]
		mat.Trigger()
		writeJSON(w, http.StatusCreated, item)
	}
}

func listBlockItems(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		if _, err := fetchBlock(db, r, blockID); err != nil {
			handleDBError(w, err)
			return
		}
		var items []domain.BlockItem
		err := db.Query(r.Context(), &items,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? ORDER BY position",
			blockID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := decorateItems(r, db, items); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

type blockItemUpdateRequest struct {
	Position       *int    `json:"position"`
	TrimDurationMs *int64  `json:"trim_duration_ms"`
	PlaybackOrder  *string `json:"playback_order"`
	// Amount sets how much a series item airs per visit (series items only).
	Amount *domain.SeriesAmountInput `json:"amount"`
}

// updateBlockItem edits one item of a manual block: position (a reorder that
// leaves the block's positions contiguous 0..N-1), trim_duration_ms (a
// positive value, catalog items only) and/or playback_order (series items
// only; it restarts the series on every channel using the block) and/or amount
// (series items only; the series cursor is kept). An absent or null field is
// left unchanged.
func updateBlockItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		b, err := fetchBlock(db, r, blockID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Criteria != nil {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its criteria")
			return
		}
		var req blockItemUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Position == nil && req.TrimDurationMs == nil && req.PlaybackOrder == nil && req.Amount == nil {
			writeError(w, http.StatusBadRequest, "position, trim_duration_ms, playback_order or amount is required")
			return
		}
		var newAmount *domain.SeriesAmount
		if req.Amount != nil {
			a, err := series.NewAmount(req.Amount)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			newAmount = &a
		}
		if req.PlaybackOrder != nil && !domain.ValidPlaybackOrder(*req.PlaybackOrder) {
			writeError(w, http.StatusBadRequest, "playback_order must be one of original, random, shuffle, release_date")
			return
		}
		if req.TrimDurationMs != nil && *req.TrimDurationMs <= 0 {
			writeError(w, http.StatusBadRequest, "trim_duration_ms must be positive")
			return
		}
		var items []domain.BlockItem
		if err := db.Query(r.Context(), &items,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? ORDER BY position", blockID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		from := -1
		for i, it := range items {
			if it.ID == itemID {
				from = i
				break
			}
		}
		if from < 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if req.Position != nil && (*req.Position < 0 || *req.Position >= len(items)) {
			writeError(w, http.StatusBadRequest, "position must be between 0 and the number of items minus one")
			return
		}

		target := items[from]
		if req.TrimDurationMs != nil && target.IsSeries() {
			writeError(w, http.StatusBadRequest, "trim_duration_ms is not valid for a series item")
			return
		}
		if req.PlaybackOrder != nil && !target.IsSeries() {
			writeError(w, http.StatusBadRequest, "playback_order is only valid for a series item")
			return
		}
		if newAmount != nil && !target.IsSeries() {
			writeError(w, http.StatusBadRequest, "amount is only valid for a series item")
			return
		}
		if newAmount != nil {
			spec, err := series.ParseSpec(target.Spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			spec.Amount = newAmount
			encoded, err := series.Encode(spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if err := db.Exec(r.Context(), "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, blockID, itemID); err != nil {
				handleDBError(w, err)
				return
			}
			target.Spec = &encoded
		}
		if req.PlaybackOrder != nil {
			spec, err := series.ParseSpec(target.Spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if spec.PlaybackOrder != *req.PlaybackOrder {
				spec.PlaybackOrder = *req.PlaybackOrder
				encoded, err := series.Encode(spec)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				nowMs := time.Now().UnixMilli()
				if err := db.Exec(r.Context(), "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, blockID, itemID); err != nil {
					handleDBError(w, err)
					return
				}
				// The cursor restarts under the new order: its origin moves
				// to now and what is still to air from this item is dropped
				// (the regeneration below rebuilds it).
				if err := db.Exec(r.Context(), "UPDATE series_cursor SET origin_ms = ? WHERE tenant_id = ? AND block_item_id = ?", nowMs, DefaultTenantID, itemID); err != nil {
					handleDBError(w, err)
					return
				}
				if err := db.Exec(r.Context(), "DELETE FROM epg_entry WHERE tenant_id = ? AND source_block_item_id = ? AND end_at > ?", DefaultTenantID, itemID, nowMs); err != nil {
					handleDBError(w, err)
					return
				}
			}
		}

		if req.TrimDurationMs != nil {
			if err := db.Exec(r.Context(),
				"UPDATE block_item SET trim_duration_ms = ? WHERE block_id = ? AND id = ?",
				*req.TrimDurationMs, blockID, itemID,
			); err != nil {
				handleDBError(w, err)
				return
			}
		}
		if req.Position != nil {
			// Move the item in the ordered list, then rewrite every position
			// as its index (0..N-1). block_item has UNIQUE (block_id,
			// position), so first park all rows on distinct negative values.
			ids := make([]string, 0, len(items))
			for _, it := range items {
				if it.ID != itemID {
					ids = append(ids, it.ID)
				}
			}
			ids = append(ids[:*req.Position], append([]string{itemID}, ids[*req.Position:]...)...)
			if err := db.Exec(r.Context(),
				"UPDATE block_item SET position = -position - 1 WHERE block_id = ?", blockID,
			); err != nil {
				handleDBError(w, err)
				return
			}
			query := "UPDATE block_item SET position = CASE id"
			args := make([]any, 0, 2*len(ids)+1)
			for i, id := range ids {
				query += " WHEN ? THEN ?"
				args = append(args, id, i)
			}
			query += " END WHERE block_id = ?"
			args = append(args, blockID)
			if err := db.Exec(r.Context(), query, args...); err != nil {
				handleDBError(w, err)
				return
			}
		}

		var item domain.BlockItem
		if err := db.Query(r.Context(), &item,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? AND id = ?", blockID, itemID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		updated := []domain.BlockItem{item}
		if err := decorateItems(r, db, updated); err != nil {
			handleDBError(w, err)
			return
		}
		item = updated[0]
		mat.Trigger()
		writeJSON(w, http.StatusOK, item)
	}
}

func deleteBlockItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		b, err := fetchBlock(db, r, blockID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Criteria != nil {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its criteria")
			return
		}
		var item domain.BlockItem
		err = db.Query(r.Context(), &item,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? AND id = ?", blockID, itemID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM block_item WHERE block_id = ? AND id = ?", blockID, itemID); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}
