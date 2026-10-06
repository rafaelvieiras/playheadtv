package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerFiller(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	// filler_pool's own CRUD affects EPG materialization only when its
	// criteria changes (createFillerPool/updateFillerPool trigger both
	// ResolveDynamicPools and mat themselves, right after the write) — a
	// plain name-only pool still doesn't need it.
	mux.HandleFunc("POST /filler-pools", createFillerPool(db, mat))
	mux.HandleFunc("GET /filler-pools", listFillerPools(db))
	mux.HandleFunc("GET /filler-pools/{id}", getFillerPool(db))
	mux.HandleFunc("PUT /filler-pools/{id}", updateFillerPool(db, mat))
	mux.HandleFunc("DELETE /filler-pools/{id}", deleteFillerPool(db))

	mux.HandleFunc("POST /filler-pools/{id}/items", addFillerPoolItem(db, mat))
	mux.HandleFunc("GET /filler-pools/{id}/items", listFillerPoolItems(db))
	mux.HandleFunc("PUT /filler-pools/{id}/items/{item_id}", updateFillerPoolItem(db, mat))
	mux.HandleFunc("DELETE /filler-pools/{id}/items/{item_id}", deleteFillerPoolItem(db, mat))

	mux.HandleFunc("POST /filler-rules", createFillerRule(db, mat))
	mux.HandleFunc("GET /filler-rules", listFillerRules(db))
	mux.HandleFunc("GET /filler-rules/{id}", getFillerRule(db))
	mux.HandleFunc("PUT /filler-rules/{id}", updateFillerRule(db, mat))
	mux.HandleFunc("DELETE /filler-rules/{id}", deleteFillerRule(db, mat))

	registerBreakTemplates(mux, db, mat)
	registerCutRuleSets(mux, db)
}

// fillerPoolColumns: see blockColumns for collection_name and dynamic.
const fillerPoolColumns = "id, tenant_id, name, collection_id, " +
	"(SELECT c.name FROM collection c WHERE c.tenant_id = filler_pool.tenant_id AND c.id = filler_pool.collection_id) AS collection_name, " +
	"(collection_id IS NOT NULL OR criteria IS NOT NULL) AS dynamic, created_at"
const fillerPoolItemColumns = "id, filler_pool_id, catalog_item_id, weight"

// fillerRuleColumns aliases scope_type/selection_strategy to the struct
// field names domain.FillerRule actually has (Scope/Selection) — storage's
// scan matches columns by snake_case(field name), and those two fields don't
// spell out to the DB's column names.
const fillerRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, filler_pool_id, selection_strategy AS selection, position, max_fit_attempts, created_at"

type fillerPoolRequest struct {
	Name string `json:"name"`
	// CollectionID is optional. nil (the JSON field absent or explicitly null)
	// means a manually managed pool; a collection id makes it dynamic — its
	// filler_pool_item rows are then owned by catalogsync.ResolveDynamicPools,
	// not by POST/DELETE .../items.
	CollectionID *string `json:"collection_id"`
	// Criteria is the removed inline query: rejected when non-null.
	Criteria json.RawMessage `json:"criteria"`
}

func fetchFillerPool(db storage.DB, r *http.Request, id string) (domain.FillerPool, error) {
	var p domain.FillerPool
	err := db.Query(r.Context(), &p,
		"SELECT "+fillerPoolColumns+" FROM filler_pool WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return p, err
}

func createFillerPool(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fillerPoolRequest
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
		p := domain.FillerPool{
			ID:           uuid.NewString(),
			TenantID:     DefaultTenantID,
			Name:         req.Name,
			CollectionID: collection,
			Dynamic:      collection != nil,
			CreatedAt:    time.Now().UnixMilli(),
		}
		err = db.Exec(r.Context(),
			"INSERT INTO filler_pool (id, tenant_id, name, collection_id, created_at) VALUES (?, ?, ?, ?, ?)",
			p.ID, p.TenantID, p.Name, p.CollectionID, p.CreatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.CollectionID != nil {
			if err := catalogsync.ResolveDynamicPools(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		p, err = fetchFillerPool(db, r, p.ID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	}
}

func listFillerPools(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var pools []domain.FillerPool
		err := db.Query(r.Context(), &pools,
			"SELECT "+fillerPoolColumns+" FROM filler_pool WHERE tenant_id = ? ORDER BY name",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pools)
	}
}

func getFillerPool(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := fetchFillerPool(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func updateFillerPool(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchFillerPool(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		var req fillerPoolRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		// PUT is a full replace, same semantics name already has: omitting
		// collection_id here reverts a previously dynamic pool to manual, even
		// if it already had one — intentional, not an accidental partial update.
		if err := rejectInlineCriteria(req.Criteria); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		collection, err := collectionRef(r.Context(), db, req.CollectionID)
		if err != nil {
			writeCollectionRefError(w, err)
			return
		}
		// The deprecated criteria snapshot is kept while the pool references a
		// collection and cleared when it reverts to manual.
		if err := db.Exec(r.Context(),
			"UPDATE filler_pool SET name = ?, collection_id = ?, criteria = CASE WHEN ? IS NULL THEN NULL ELSE criteria END WHERE tenant_id = ? AND id = ?",
			req.Name, collection, collection, DefaultTenantID, id,
		); err != nil {
			handleDBError(w, err)
			return
		}
		// Whatever the pool held before is replaced, so a pending legacy
		// criteria review no longer applies.
		if err := clearReview(r.Context(), db, catalogsync.OwnerFillerPool, id); err != nil {
			handleDBError(w, err)
			return
		}
		if collection != nil {
			if err := catalogsync.ResolveDynamicPools(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
		}
		p, err := fetchFillerPool(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func deleteFillerPool(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchFillerPool(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM filler_pool WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := clearReview(r.Context(), db, catalogsync.OwnerFillerPool, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type fillerPoolItemRequest struct {
	CatalogItemID string `json:"catalog_item_id"`
	Weight        int    `json:"weight"`
}

func addFillerPoolItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolID := r.PathValue("id")
		p, err := fetchFillerPool(db, r, poolID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.Dynamic {
			writeError(w, http.StatusConflict, "this pool is dynamic; items are managed automatically from its collection")
			return
		}
		var req fillerPoolItemRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.CatalogItemID == "" {
			writeError(w, http.StatusBadRequest, "catalog_item_id is required")
			return
		}
		if req.Weight == 0 {
			req.Weight = 1 // matches the schema's DEFAULT 1 when omitted
		}
		item := domain.FillerPoolItem{
			ID:            uuid.NewString(),
			FillerPoolID:  poolID,
			CatalogItemID: req.CatalogItemID,
			Weight:        req.Weight,
		}
		err = db.Exec(r.Context(),
			"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, ?)",
			item.ID, item.FillerPoolID, item.CatalogItemID, item.Weight,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, item)
	}
}

func listFillerPoolItems(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolID := r.PathValue("id")
		if _, err := fetchFillerPool(db, r, poolID); err != nil {
			handleDBError(w, err)
			return
		}
		var items []domain.FillerPoolItem
		err := db.Query(r.Context(), &items,
			"SELECT "+fillerPoolItemColumns+" FROM filler_pool_item WHERE filler_pool_id = ?", poolID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

type fillerPoolItemUpdateRequest struct {
	Weight *int `json:"weight"`
}

// updateFillerPoolItem changes one item's selection weight in a manual pool.
func updateFillerPoolItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		p, err := fetchFillerPool(db, r, poolID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.Dynamic {
			writeError(w, http.StatusConflict, "this pool is dynamic; items are managed automatically from its collection")
			return
		}
		var req fillerPoolItemUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Weight == nil || *req.Weight <= 0 {
			writeError(w, http.StatusBadRequest, "weight must be a positive integer")
			return
		}
		var item domain.FillerPoolItem
		if err := db.Query(r.Context(), &item,
			"SELECT "+fillerPoolItemColumns+" FROM filler_pool_item WHERE filler_pool_id = ? AND id = ?", poolID, itemID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(),
			"UPDATE filler_pool_item SET weight = ? WHERE filler_pool_id = ? AND id = ?", *req.Weight, poolID, itemID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		item.Weight = *req.Weight
		mat.Trigger()
		writeJSON(w, http.StatusOK, item)
	}
}

func deleteFillerPoolItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		p, err := fetchFillerPool(db, r, poolID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.Dynamic {
			writeError(w, http.StatusConflict, "this pool is dynamic; items are managed automatically from its collection")
			return
		}
		var item domain.FillerPoolItem
		err = db.Query(r.Context(), &item,
			"SELECT "+fillerPoolItemColumns+" FROM filler_pool_item WHERE filler_pool_id = ? AND id = ?", poolID, itemID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM filler_pool_item WHERE filler_pool_id = ? AND id = ?", poolID, itemID); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}

type fillerRuleRequest struct {
	ScopeType         string `json:"scope_type"`
	ScopeID           string `json:"scope_id"`
	Kind              string `json:"kind"`
	FillerPoolID      string `json:"filler_pool_id"`
	SelectionStrategy string `json:"selection_strategy"`
	Position          int    `json:"position"`
	// MaxFitAttempts: nil keeps the stored value on PUT and means 1 on POST.
	MaxFitAttempts *int `json:"max_fit_attempts"`
}

func fetchFillerRule(db storage.DB, r *http.Request, id string) (domain.FillerRule, error) {
	var fr domain.FillerRule
	err := db.Query(r.Context(), &fr,
		"SELECT "+fillerRuleColumns+" FROM filler_rule WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return fr, err
}

func validateFillerRuleRequest(req fillerRuleRequest) (string, bool) {
	switch domain.FillerScope(req.ScopeType) {
	case domain.ScopeChannel, domain.ScopeBlock:
	default:
		return "scope_type must be one of channel, block", false
	}
	if req.ScopeID == "" {
		return "scope_id is required", false
	}
	switch domain.FillerKind(req.Kind) {
	case domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll, domain.FillerTail, domain.FillerFallback:
	default:
		return "kind must be one of preroll, midroll, postroll, tail, fallback", false
	}
	if req.FillerPoolID == "" {
		return "filler_pool_id is required", false
	}
	switch domain.SelectionStrategy(req.SelectionStrategy) {
	case domain.SelectionSequential, domain.SelectionRandom, domain.SelectionWeighted:
	default:
		return "selection_strategy must be one of sequential, random, weighted", false
	}
	if req.MaxFitAttempts != nil && (*req.MaxFitAttempts < 1 || *req.MaxFitAttempts > domain.MaxFitAttemptsLimit) {
		return fmt.Sprintf("max_fit_attempts must be between 1 and %d", domain.MaxFitAttemptsLimit), false
	}
	return "", true
}

func createFillerRule(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fillerRuleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateFillerRuleRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		maxFit := 1
		if req.MaxFitAttempts != nil {
			maxFit = *req.MaxFitAttempts
		}
		fr := domain.FillerRule{
			MaxFitAttempts: maxFit,
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			Scope:          domain.FillerScope(req.ScopeType),
			ScopeID:        req.ScopeID,
			Kind:           domain.FillerKind(req.Kind),
			FillerPoolID:   req.FillerPoolID,
			Selection:      domain.SelectionStrategy(req.SelectionStrategy),
			Position:       req.Position,
			CreatedAt:      time.Now().UnixMilli(),
		}
		err := db.Exec(r.Context(),
			`INSERT INTO filler_rule (id, tenant_id, scope_type, scope_id, kind, filler_pool_id, selection_strategy, position, max_fit_attempts, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fr.ID, fr.TenantID, string(fr.Scope), fr.ScopeID, string(fr.Kind), fr.FillerPoolID,
			string(fr.Selection), fr.Position, fr.MaxFitAttempts, fr.CreatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, fr)
	}
}

func listFillerRules(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rules []domain.FillerRule
		err := db.Query(r.Context(), &rules,
			"SELECT "+fillerRuleColumns+" FROM filler_rule WHERE tenant_id = ? ORDER BY created_at",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rules)
	}
}

func getFillerRule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fr, err := fetchFillerRule(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fr)
	}
}

func updateFillerRule(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		existing, err := fetchFillerRule(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		var req fillerRuleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateFillerRuleRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		maxFit := existing.MaxFitAttempts
		if req.MaxFitAttempts != nil {
			maxFit = *req.MaxFitAttempts
		}
		err = db.Exec(r.Context(),
			`UPDATE filler_rule SET scope_type = ?, scope_id = ?, kind = ?, filler_pool_id = ?, selection_strategy = ?, position = ?, max_fit_attempts = ?
			 WHERE tenant_id = ? AND id = ?`,
			req.ScopeType, req.ScopeID, req.Kind, req.FillerPoolID, req.SelectionStrategy, req.Position, maxFit,
			DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		fr, err := fetchFillerRule(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fr)
	}
}

func deleteFillerRule(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchFillerRule(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM filler_rule WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}
