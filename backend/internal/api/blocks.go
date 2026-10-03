package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/blockitem"
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
	mux.HandleFunc("GET /blocks/list-mode-review", listListModeReview(db))
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

// blockColumns reads the block with its collection (and the collection's name,
// for display) and whether it is dynamic: references a collection, or is a
// legacy row still held for review that keeps playing its deprecated criteria
// column. The criteria column itself is never selected for clients.
const blockColumns = "id, tenant_id, name, description, collection_id, " +
	"(SELECT c.name FROM collection c WHERE c.tenant_id = block.tenant_id AND c.id = block.collection_id) AS collection_name, " +
	"(collection_id IS NOT NULL OR criteria IS NOT NULL) AS dynamic, " +
	"playback_order, list_mode, interleave_spec, playback_seed, created_at, updated_at"

// blockItemColumns reads the typed block_item: a series item has a NULL
// catalog_item_id, read back as the empty string.
const blockItemColumns = "id, block_id, position, item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms, spec"

type blockRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// CollectionID is optional. nil (the JSON field absent or explicitly null)
	// means a manually managed block; a collection id makes it dynamic — its
	// block_item rows are then owned by catalogsync.ResolveDynamicBlocks, not
	// by POST/DELETE .../items.
	CollectionID *string `json:"collection_id"`
	// Criteria is the removed inline query: rejected with a pointer to
	// collections when non-null.
	Criteria json.RawMessage `json:"criteria"`
	// PlaybackOrder (dynamic blocks only) is title (default: every match by
	// title, looped) or interleaved (round-robin across series, see
	// Interleave). Like the rest of the PUT it is a full replace.
	PlaybackOrder string                  `json:"playback_order"`
	Interleave    *domain.InterleaveInput `json:"interleave"`
	// ListMode (manual blocks only) is repeat (the default: the list starts
	// over when it ends, until the next timed item or, for a continuous item,
	// indefinitely) or once. On a PUT, omitting it keeps the stored mode.
	ListMode string `json:"list_mode"`
}

func fetchBlock(db storage.DB, r *http.Request, id string) (domain.Block, error) {
	var b domain.Block
	err := db.Query(r.Context(), &b,
		"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	b.FillInterleave()
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
		if err := rejectInlineCriteria(req.Criteria); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		collection, err := collectionRef(r.Context(), db, req.CollectionID)
		if err != nil {
			writeCollectionRefError(w, err)
			return
		}
		playback, _, err := blockitem.NewBlockPlayback(collection != nil, req.PlaybackOrder, req.Interleave, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		listMode, _, err := blockitem.NewListMode(collection != nil, req.ListMode, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		now := time.Now().UnixMilli()
		b := domain.Block{
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			Name:           req.Name,
			Description:    req.Description,
			CollectionID:   collection,
			Dynamic:        collection != nil,
			PlaybackOrder:  playback.Order,
			InterleaveSpec: playback.InterleaveSpec,
			PlaybackSeed:   playback.Seed,
			ListMode:       listMode,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		b.FillInterleave()
		err = db.Exec(r.Context(),
			"INSERT INTO block (id, tenant_id, name, description, collection_id, playback_order, list_mode, interleave_spec, playback_seed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			b.ID, b.TenantID, b.Name, b.Description, b.CollectionID, b.PlaybackOrder, listMode, b.InterleaveSpec, b.PlaybackSeed, b.CreatedAt, b.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.CollectionID != nil {
			if err := catalogsync.ResolveDynamicBlocks(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		b, err = fetchBlock(db, r, b.ID)
		if err != nil {
			handleDBError(w, err)
			return
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
		for i := range blocks {
			blocks[i].FillInterleave()
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
		prev, err := fetchBlock(db, r, id)
		if err != nil {
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
		// omitting collection_id here reverts a previously dynamic block to
		// manual, even if it already had one — intentional, not an
		// accidental partial update.
		if err := rejectInlineCriteria(req.Criteria); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		collection, err := collectionRef(r.Context(), db, req.CollectionID)
		if err != nil {
			writeCollectionRefError(w, err)
			return
		}
		playback, restart, err := blockitem.NewBlockPlayback(collection != nil, req.PlaybackOrder, req.Interleave, &prev)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		listMode, explicitMode, err := blockitem.NewListMode(collection != nil, req.ListMode, &prev)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		err = db.Exec(r.Context(),
			// The deprecated criteria snapshot is kept while the block references a
			// collection (the way back from the migration) and cleared when the
			// block reverts to manual, so it cannot read as a legacy dynamic row.
			"UPDATE block SET name = ?, description = ?, collection_id = ?, criteria = CASE WHEN ? IS NULL THEN NULL ELSE criteria END, playback_order = ?, list_mode = ?, interleave_spec = ?, playback_seed = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			req.Name, req.Description, collection, collection, playback.Order, listMode, playback.InterleaveSpec, playback.Seed, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		// Deciding the mode (or making the block dynamic) settles the
		// migration review of a block that was switched to repeat.
		if explicitMode || collection != nil {
			if err := blockitem.ClearListModeReview(r.Context(), db, DefaultTenantID, id); err != nil {
				handleDBError(w, err)
				return
			}
		}
		if listMode != prev.ListMode && collection == nil {
			mat.Trigger()
		}
		if restart {
			if err := blockitem.RestartBlockCursor(r.Context(), db, DefaultTenantID, id); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		// The query source was just replaced, so a pending criteria review no
		// longer applies.
		if err := clearReview(r.Context(), db, catalogsync.OwnerBlock, id); err != nil {
			handleDBError(w, err)
			return
		}
		if collection != nil {
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
		if err := clearReview(r.Context(), db, catalogsync.OwnerBlock, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// blockItemRequest adds a block item. item_type is "catalog" (the default,
// unless series or criteria is given), "series" or "criteria". A catalog item
// takes catalog_item_id and optionally trim_duration_ms; a series item takes
// the series object and no trim (every episode plays in full); a criteria item
// takes the criteria object and no trim either.
type blockItemRequest struct {
	ItemType       string               `json:"item_type"`
	CatalogItemID  string               `json:"catalog_item_id"`
	TrimDurationMs *int64               `json:"trim_duration_ms"`
	Series         *seriesItemRequest   `json:"series"`
	Criteria       *criteriaItemRequest `json:"criteria"`
}

// criteriaItemRequest is a criteria item: a collection (collection_id), the order
// its matches are walked in (title, release_date, random or shuffle; default
// title) and how many of them one visit of the block rotation airs. amount is
// required: {"mode":"count","count":N} or {"mode":"all"}.
type criteriaItemRequest struct {
	CollectionID string `json:"collection_id"`
	// Query is the removed embedded query: rejected when non-null.
	Query         json.RawMessage           `json:"query"`
	PlaybackOrder string                    `json:"playback_order"`
	Amount        *domain.SeriesAmountInput `json:"amount"`
	// Interleave configures the interleaved order: per_round (episodes per
	// series per round, default 1), on_exhaustion (restart, the default: a series that runs out
	// restarts on its own and keeps its turn; or drop: it leaves the rounds for good) and series_order (title, release_date, random, shuffle).
	Interleave *domain.InterleaveInput `json:"interleave"`
}

// seriesItemRequest identifies a series (media source plus external id or
// title) and how to walk it. playback_order defaults to original; amount is
// how much one visit of the block rotation airs (default: one episode); slice
// optionally narrows the series to part of its episodes (see domain.SeriesSlice).
type seriesItemRequest struct {
	MediaSourceID    string                    `json:"media_source_id"`
	SeriesExternalID string                    `json:"series_external_id"`
	SeriesTitle      string                    `json:"series_title"`
	PlaybackOrder    string                    `json:"playback_order"`
	Amount           *domain.SeriesAmountInput `json:"amount"`
	Slice            *domain.SeriesSlice       `json:"slice"`
}

// decorateItems fills the series view (episode count, total duration) of the
// series items in items.
func decorateItems(r *http.Request, db storage.DB, items []domain.BlockItem) error {
	return blockitem.Decorate(r.Context(), db, DefaultTenantID, items)
}

func addBlockItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		b, err := fetchBlock(db, r, blockID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Dynamic {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its collection")
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
			if req.Criteria != nil {
				itemType = domain.BlockItemCriteria
			}
		}
		var specJSON *string
		switch itemType {
		case domain.BlockItemCatalog:
			if req.Series != nil {
				writeError(w, http.StatusBadRequest, "series is only valid for item_type series")
				return
			}
			if req.Criteria != nil {
				writeError(w, http.StatusBadRequest, "criteria is only valid for item_type criteria")
				return
			}
			if req.CatalogItemID == "" {
				writeError(w, http.StatusBadRequest, "catalog_item_id is required")
				return
			}
		case domain.BlockItemSeries:
			if req.Criteria != nil {
				writeError(w, http.StatusBadRequest, "criteria is only valid for item_type criteria")
				return
			}
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
			if err := series.CheckSpec(r.Context(), db, DefaultTenantID, spec); err != nil {
				if !series.IsInvalid(err) {
					handleDBError(w, err)
					return
				}
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			encoded, err := series.Encode(spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			specJSON = &encoded
		case domain.BlockItemCriteria:
			if req.CatalogItemID != "" {
				writeError(w, http.StatusBadRequest, "catalog_item_id is not valid for a criteria item")
				return
			}
			if req.Series != nil {
				writeError(w, http.StatusBadRequest, "series is only valid for item_type series")
				return
			}
			if req.TrimDurationMs != nil {
				writeError(w, http.StatusBadRequest, "trim_duration_ms is not valid for a criteria item; add a trimmed item as its own item before it")
				return
			}
			if req.Criteria == nil {
				writeError(w, http.StatusBadRequest, "criteria is required for item_type criteria")
				return
			}
			if err := rejectInlineCriteria(req.Criteria.Query); err != nil {
				writeError(w, http.StatusBadRequest, "criteria.query is no longer accepted: create a collection (POST /collections) and send its id as criteria.collection_id")
				return
			}
			spec, err := blockitem.NewCriteriaSpec(r.Context(), db, DefaultTenantID, req.Criteria.CollectionID, req.Criteria.PlaybackOrder, req.Criteria.Amount, req.Criteria.Interleave)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			encoded, err := blockitem.EncodeCriteriaSpec(spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			specJSON = &encoded
		default:
			writeError(w, http.StatusBadRequest, "item_type must be catalog, series or criteria")
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
	// Amount sets how much a series or criteria item airs per visit.
	Amount *domain.SeriesAmountInput `json:"amount"`
	// CollectionID points a criteria item at another collection (criteria items
	// only); the cursor is kept.
	CollectionID *string `json:"collection_id"`
	// Query is the removed embedded query: rejected when non-null.
	Query json.RawMessage `json:"query"`
	// Interleave overlays the interleave parameters of a criteria item (criteria
	// items with the interleaved order only).
	Interleave *domain.InterleaveInput `json:"interleave"`
	// Slice replaces the slice of a series item (series items only); an empty
	// object clears it. A changed slice restarts the item's cursor.
	Slice *domain.SeriesSlice `json:"slice"`
}

// updateBlockItem edits one item of a manual block: position (a reorder that
// leaves the block's positions contiguous 0..N-1), trim_duration_ms (a
// positive value, catalog items only) and/or playback_order (series and
// criteria items; it restarts the item's cursor on every channel using the
// block) and/or amount (series and criteria items; the cursor is kept) and/or
// collection_id (criteria items; the cursor is kept). An absent or null field is left
// unchanged.
func updateBlockItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blockID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		b, err := fetchBlock(db, r, blockID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if b.Dynamic {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its collection")
			return
		}
		var req blockItemUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := rejectInlineCriteria(req.Query); err != nil {
			writeError(w, http.StatusBadRequest, "query is no longer accepted: create a collection (POST /collections) and send its id as collection_id")
			return
		}
		hasCollection := req.CollectionID != nil
		if req.Position == nil && req.TrimDurationMs == nil && req.PlaybackOrder == nil && req.Amount == nil && !hasCollection && req.Interleave == nil && req.Slice == nil {
			writeError(w, http.StatusBadRequest, "position, trim_duration_ms, playback_order, amount, collection_id, interleave or slice is required")
			return
		}
		if req.PlaybackOrder != nil && !domain.ValidPlaybackOrder(*req.PlaybackOrder) && !domain.ValidCriteriaOrder(*req.PlaybackOrder) {
			writeError(w, http.StatusBadRequest, "playback_order must be one of original, random, shuffle, release_date (series) or title, release_date, random, shuffle, interleaved (criteria)")
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
		if req.TrimDurationMs != nil && target.IsWalked() {
			writeError(w, http.StatusBadRequest, "trim_duration_ms is not valid for a series or criteria item")
			return
		}
		if req.PlaybackOrder != nil && !target.IsWalked() {
			writeError(w, http.StatusBadRequest, "playback_order is only valid for a series or criteria item")
			return
		}
		if req.Amount != nil && !target.IsWalked() {
			writeError(w, http.StatusBadRequest, "amount is only valid for a series or criteria item")
			return
		}
		if req.Interleave != nil && !target.IsCriteria() {
			writeError(w, http.StatusBadRequest, "interleave is only valid for a criteria item")
			return
		}
		if hasCollection && !target.IsCriteria() {
			writeError(w, http.StatusBadRequest, "collection_id is only valid for a criteria item")
			return
		}
		if req.Slice != nil && !target.IsSeries() {
			writeError(w, http.StatusBadRequest, "slice is only valid for a series item")
			return
		}
		if req.Slice != nil {
			spec, err := series.ParseSpec(target.Spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			updated, changed, err := series.UpdateSlice(r.Context(), db, DefaultTenantID, spec, req.Slice)
			if err != nil {
				if !series.IsInvalid(err) {
					handleDBError(w, err)
					return
				}
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if changed {
				encoded, err := series.Encode(updated)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				if err := db.Exec(r.Context(), "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, blockID, itemID); err != nil {
					handleDBError(w, err)
					return
				}
				target.Spec = &encoded
				if err := restartItemCursor(r.Context(), db, itemID); err != nil {
					handleDBError(w, err)
					return
				}
			}
		}
		if target.IsCriteria() {
			if req.PlaybackOrder != nil && !domain.ValidCriteriaOrder(*req.PlaybackOrder) {
				writeError(w, http.StatusBadRequest, "playback_order must be one of title, release_date, random, shuffle, interleaved for a criteria item")
				return
			}
			spec, err := blockitem.ParseCriteriaSpec(target.Spec)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			updated, orderChanged, err := blockitem.UpdateCriteriaSpec(r.Context(), db, DefaultTenantID, spec, req.CollectionID, req.PlaybackOrder, req.Amount, req.Interleave)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			encoded, err := blockitem.EncodeCriteriaSpec(updated)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if err := db.Exec(r.Context(), "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, blockID, itemID); err != nil {
				handleDBError(w, err)
				return
			}
			target.Spec = &encoded
			if orderChanged {
				if err := restartItemCursor(r.Context(), db, itemID); err != nil {
					handleDBError(w, err)
					return
				}
			}
		}
		var newAmount *domain.SeriesAmount
		if req.Amount != nil && target.IsSeries() {
			a, err := series.NewAmount(req.Amount)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			newAmount = &a
		}
		if req.PlaybackOrder != nil && target.IsSeries() && !domain.ValidPlaybackOrder(*req.PlaybackOrder) {
			writeError(w, http.StatusBadRequest, "playback_order must be one of original, random, shuffle, release_date")
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
		if req.PlaybackOrder != nil && target.IsSeries() {
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
				if err := db.Exec(r.Context(), "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, blockID, itemID); err != nil {
					handleDBError(w, err)
					return
				}
				if err := restartItemCursor(r.Context(), db, itemID); err != nil {
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

// restartItemCursor restarts a walked (series or criteria) item under a new
// playback order: the cursor's origin moves to now and what is still to air
// from the item is dropped (the regeneration that follows rebuilds it).
func restartItemCursor(ctx context.Context, db storage.DB, itemID string) error {
	nowMs := time.Now().UnixMilli()
	if err := db.Exec(ctx, "UPDATE series_cursor SET origin_ms = ? WHERE tenant_id = ? AND block_item_id = ?", nowMs, DefaultTenantID, itemID); err != nil {
		return err
	}
	return db.Exec(ctx, "DELETE FROM epg_entry WHERE tenant_id = ? AND source_block_item_id = ? AND end_at > ?", DefaultTenantID, itemID, nowMs)
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
		if b.Dynamic {
			writeError(w, http.StatusConflict, "this block is dynamic; items are managed automatically from its collection")
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

// listModeReviewEntry is a block the list_mode migration switched to repeat
// while it is scheduled at fixed times (weekly or once items): its grid may now
// differ, so the operator is asked to check whether it should play once.
type listModeReviewEntry struct {
	BlockID   string                `json:"block_id"`
	BlockName string                `json:"block_name"`
	Schedules []listModeReviewSched `json:"schedules"`
}

type listModeReviewSched struct {
	ScheduleID   string `json:"schedule_id"`
	ScheduleName string `json:"schedule_name"`
}

// loadListModeReview returns the blocks still awaiting review: those the
// migration listed that are still manual, still repeat and still have a timed
// schedule item. Writing a mode on the block (or deleting it) removes it from
// the list for good.
func loadListModeReview(ctx context.Context, db storage.DB) ([]listModeReviewEntry, error) {
	var rows []struct {
		BlockID      string
		BlockName    string
		ScheduleID   string
		ScheduleName string
	}
	err := db.Query(ctx, &rows,
		"SELECT DISTINCT b.id AS block_id, b.name AS block_name, s.id AS schedule_id, s.name AS schedule_name "+
			"FROM block_list_mode_review r "+
			"JOIN block b ON b.tenant_id = r.tenant_id AND b.id = r.block_id "+
			"JOIN schedule_item si ON si.block_id = b.id AND si.recurrence_type IN ('weekly', 'once') "+
			"JOIN schedule s ON s.id = si.schedule_id "+
			"WHERE r.tenant_id = ? AND b.list_mode = 'repeat' AND b.collection_id IS NULL AND b.criteria IS NULL "+
			"ORDER BY b.name, b.id, s.name, s.id",
		DefaultTenantID,
	)
	if err != nil {
		return nil, err
	}
	out := []listModeReviewEntry{}
	for _, row := range rows {
		if n := len(out); n == 0 || out[n-1].BlockID != row.BlockID {
			out = append(out, listModeReviewEntry{BlockID: row.BlockID, BlockName: row.BlockName, Schedules: []listModeReviewSched{}})
		}
		last := &out[len(out)-1]
		last.Schedules = append(last.Schedules, listModeReviewSched{ScheduleID: row.ScheduleID, ScheduleName: row.ScheduleName})
	}
	return out, nil
}

// listListModeReview serves GET /blocks/list-mode-review (read-only).
func listListModeReview(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := loadListModeReview(r.Context(), db)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// LogListModeReview logs, at startup, the blocks awaiting the list_mode review
// (see loadListModeReview) so the operator sees them without opening the UI.
func LogListModeReview(ctx context.Context, db storage.DB) error {
	entries, err := loadListModeReview(ctx, db)
	if err != nil {
		return err
	}
	for _, e := range entries {
		names := make([]string, 0, len(e.Schedules))
		for _, s := range e.Schedules {
			names = append(names, s.ScheduleName)
		}
		log.Printf("blocks: review needed: block %q (%s) now repeats its list and is scheduled at fixed times in %s; set its list mode to play once if it should not repeat",
			e.BlockName, e.BlockID, strings.Join(names, ", "))
	}
	return nil
}
