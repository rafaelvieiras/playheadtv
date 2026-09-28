package api

import (
	"encoding/json"
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
	mux.HandleFunc("DELETE /filler-pools/{id}/items/{item_id}", deleteFillerPoolItem(db, mat))

	mux.HandleFunc("POST /filler-rules", createFillerRule(db, mat))
	mux.HandleFunc("GET /filler-rules", listFillerRules(db))
	mux.HandleFunc("GET /filler-rules/{id}", getFillerRule(db))
	mux.HandleFunc("PUT /filler-rules/{id}", updateFillerRule(db, mat))
	mux.HandleFunc("DELETE /filler-rules/{id}", deleteFillerRule(db, mat))
}

const fillerPoolColumns = "id, tenant_id, name, criteria, created_at"
const fillerPoolItemColumns = "id, filler_pool_id, catalog_item_id, weight"

// fillerRuleColumns aliases scope_type/selection_strategy to the struct
// field names domain.FillerRule actually has (Scope/Selection) — storage's
// scan matches columns by snake_case(field name), and those two fields don't
// spell out to the DB's column names.
const fillerRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, filler_pool_id, selection_strategy AS selection, position, created_at"

// fillerPoolCriteriaRequest is the JSON shape a client sends/receives for a
// dynamic pool's criteria — mirrors catalogsync's internal poolCriteria
// (kept as a separate type since that one is unexported and this package
// shouldn't reach into catalogsync's internals just to reuse a struct).
type fillerPoolCriteriaRequest struct {
	Kind     string   `json:"kind"`
	Genres   []string `json:"genres"`
	YearFrom *int     `json:"year_from"`
	YearTo   *int     `json:"year_to"`
}

type fillerPoolRequest struct {
	Name string `json:"name"`
	// Criteria is optional. nil (the JSON field absent or explicitly null)
	// means a manually managed pool; a non-nil value makes it dynamic —
	// its filler_pool_item rows are then owned by
	// catalogsync.ResolveDynamicPools, not by POST/DELETE .../items.
	Criteria *fillerPoolCriteriaRequest `json:"criteria"`
}

func validatePoolCriteria(c *fillerPoolCriteriaRequest) (string, bool) {
	switch domain.CatalogItemKind(c.Kind) {
	case domain.CatalogMovie, domain.CatalogEpisode:
	default:
		return "criteria.kind must be one of movie, episode", false
	}
	if len(c.Genres) == 0 && c.YearFrom == nil && c.YearTo == nil {
		return "criteria requires at least one of genres, year_from, year_to", false
	}
	return "", true
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
		p := domain.FillerPool{
			ID:        uuid.NewString(),
			TenantID:  DefaultTenantID,
			Name:      req.Name,
			Criteria:  criteria,
			CreatedAt: time.Now().UnixMilli(),
		}
		err := db.Exec(r.Context(),
			"INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES (?, ?, ?, ?, ?)",
			p.ID, p.TenantID, p.Name, p.Criteria, p.CreatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.Criteria != nil {
			if err := catalogsync.ResolveDynamicPools(r.Context(), db, DefaultTenantID); err != nil {
				handleDBError(w, err)
				return
			}
			mat.Trigger()
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
		// criteria here reverts a previously dynamic pool to manual, even if
		// it already had one — intentional, not an accidental partial update.
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
		if err := db.Exec(r.Context(),
			"UPDATE filler_pool SET name = ?, criteria = ? WHERE tenant_id = ? AND id = ?",
			req.Name, criteria, DefaultTenantID, id,
		); err != nil {
			handleDBError(w, err)
			return
		}
		if criteria != nil {
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
		if p.Criteria != nil {
			writeError(w, http.StatusConflict, "this pool is dynamic; items are managed automatically from its criteria")
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

func deleteFillerPoolItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		poolID := r.PathValue("id")
		itemID := r.PathValue("item_id")
		p, err := fetchFillerPool(db, r, poolID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if p.Criteria != nil {
			writeError(w, http.StatusConflict, "this pool is dynamic; items are managed automatically from its criteria")
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
		fr := domain.FillerRule{
			ID:           uuid.NewString(),
			TenantID:     DefaultTenantID,
			Scope:        domain.FillerScope(req.ScopeType),
			ScopeID:      req.ScopeID,
			Kind:         domain.FillerKind(req.Kind),
			FillerPoolID: req.FillerPoolID,
			Selection:    domain.SelectionStrategy(req.SelectionStrategy),
			Position:     req.Position,
			CreatedAt:    time.Now().UnixMilli(),
		}
		err := db.Exec(r.Context(),
			`INSERT INTO filler_rule (id, tenant_id, scope_type, scope_id, kind, filler_pool_id, selection_strategy, position, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fr.ID, fr.TenantID, string(fr.Scope), fr.ScopeID, string(fr.Kind), fr.FillerPoolID,
			string(fr.Selection), fr.Position, fr.CreatedAt,
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
		if _, err := fetchFillerRule(db, r, id); err != nil {
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
		err := db.Exec(r.Context(),
			`UPDATE filler_rule SET scope_type = ?, scope_id = ?, kind = ?, filler_pool_id = ?, selection_strategy = ?, position = ?
			 WHERE tenant_id = ? AND id = ?`,
			req.ScopeType, req.ScopeID, req.Kind, req.FillerPoolID, req.SelectionStrategy, req.Position,
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
