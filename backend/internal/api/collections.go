package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerCollections(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /collections", createCollection(db))
	mux.HandleFunc("GET /collections", listCollections(db))
	mux.HandleFunc("GET /collections/{id}", getCollection(db))
	mux.HandleFunc("PUT /collections/{id}", updateCollection(db, mat))
	mux.HandleFunc("DELETE /collections/{id}", deleteCollection(db))
	mux.HandleFunc("GET /collections/{id}/usage", collectionUsage(db))
	mux.HandleFunc("GET /collections/{id}/preview", previewCollection(db))
}

// collectionRequest is the body of POST and PUT /collections (PUT is a full
// replace). query is a versioned condition tree (catalogsync.Query).
type collectionRequest struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Query       json.RawMessage `json:"query"`
}

// parseCollectionRequest validates a collection write and returns the name,
// description and canonical query it stores. The error text is safe to return.
func parseCollectionRequest(r *http.Request) (string, *string, *catalogsync.Query, error) {
	var req collectionRequest
	if err := decodeJSON(r, &req); err != nil {
		return "", nil, nil, errors.New("invalid request body")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", nil, nil, errors.New("name is required")
	}
	q, err := catalogsync.ParseQuery(req.Query)
	if err != nil {
		if errors.Is(err, catalogsync.ErrLegacyCriteria) {
			return "", nil, nil, err
		}
		return "", nil, nil, errors.New("query: " + err.Error())
	}
	return name, req.Description, q, nil
}

func createCollection(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, desc, q, err := parseCollectionRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		id := uuid.NewString()
		now := time.Now().UnixMilli()
		if err := db.Exec(r.Context(),
			"INSERT INTO collection (id, tenant_id, name, description, query, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, DefaultTenantID, name, desc, q.JSON(), now, now,
		); err != nil {
			handleDBError(w, err)
			return
		}
		c, err := catalogsync.GetCollection(r.Context(), db, DefaultTenantID, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	}
}

func listCollections(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cs []domain.Collection
		if err := db.Query(r.Context(), &cs,
			"SELECT "+catalogsync.CollectionColumns+" FROM collection WHERE tenant_id = ? ORDER BY name, id", DefaultTenantID); err != nil {
			handleDBError(w, err)
			return
		}
		for i := range cs {
			cs[i].Fill()
		}
		if cs == nil {
			cs = []domain.Collection{}
		}
		writeJSON(w, http.StatusOK, cs)
	}
}

// collectionOr404 loads a collection of the default tenant, writing the 404
// itself (a collection of another tenant is a 404 too).
func collectionOr404(w http.ResponseWriter, r *http.Request, db storage.DB) (domain.Collection, bool) {
	c, err := catalogsync.GetCollection(r.Context(), db, DefaultTenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, catalogsync.ErrCollectionNotFound) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			handleDBError(w, err)
		}
		return c, false
	}
	return c, true
}

func getCollection(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, ok := collectionOr404(w, r, db); ok {
			writeJSON(w, http.StatusOK, c)
		}
	}
}

// updateCollection replaces name, description and query. The query applies to
// every user of the collection at once (the web UI shows them first): the
// dynamic blocks and pools that use it are re-resolved right away and the
// materializer is asked to regenerate; criteria items evaluate it live.
func updateCollection(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prev, ok := collectionOr404(w, r, db)
		if !ok {
			return
		}
		name, desc, q, err := parseCollectionRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := db.Exec(r.Context(),
			"UPDATE collection SET name = ?, description = ?, query = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			name, desc, q.JSON(), time.Now().UnixMilli(), DefaultTenantID, prev.ID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		if q.JSON() != prev.Query {
			if err := catalogsync.ResolveCollection(r.Context(), db, DefaultTenantID, prev.ID); err != nil {
				handleDBError(w, err)
				return
			}
			if prev.UsageCount > 0 {
				mat.Trigger()
			}
		}
		c, err := catalogsync.GetCollection(r.Context(), db, DefaultTenantID, prev.ID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

type collectionInUseBody struct {
	Error string                 `json:"error"`
	Usage domain.CollectionUsage `json:"usage"`
}

// deleteCollection refuses (409, listing the blocks, pools and criteria items
// that use it) while anything references the collection.
func deleteCollection(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := collectionOr404(w, r, db)
		if !ok {
			return
		}
		var usage domain.CollectionUsage
		var inUse bool
		// Check and delete in one transaction so nothing can start using the
		// collection in between.
		err := db.Transact(r.Context(), func(tx storage.DB) error {
			var err error
			if usage, err = catalogsync.CollectionUsage(r.Context(), tx, DefaultTenantID, c.ID); err != nil {
				return err
			}
			if usage.Count() > 0 {
				inUse = true
				return nil
			}
			return tx.Exec(r.Context(), "DELETE FROM collection WHERE tenant_id = ? AND id = ?", DefaultTenantID, c.ID)
		})
		if err != nil {
			handleDBError(w, err)
			return
		}
		if inUse {
			writeJSON(w, http.StatusConflict, collectionInUseBody{
				Error: "this collection is in use by " + usageSummary(usage) + "; point them at another collection first",
				Usage: usage,
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func usageSummary(u domain.CollectionUsage) string {
	var parts []string
	plural := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return itoa(n) + " " + one + "s"
	}
	if n := len(u.Blocks); n > 0 {
		parts = append(parts, plural(n, "block"))
	}
	if n := len(u.FillerPools); n > 0 {
		parts = append(parts, plural(n, "filler pool"))
	}
	if n := len(u.BlockItems); n > 0 {
		parts = append(parts, plural(n, "criteria item"))
	}
	return strings.Join(parts, ", ")
}

func itoa(n int) string { return strconv.Itoa(n) }

func collectionUsage(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := collectionOr404(w, r, db)
		if !ok {
			return
		}
		u, err := catalogsync.CollectionUsage(r.Context(), db, DefaultTenantID, c.ID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

// previewCollection reports what a stored collection currently matches, same
// shape as POST /catalog-items/preview-criteria.
func previewCollection(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := collectionOr404(w, r, db)
		if !ok {
			return
		}
		q, err := catalogsync.ParseQuery([]byte(c.Query))
		if err != nil {
			writeError(w, http.StatusConflict, "the stored query is invalid: "+err.Error())
			return
		}
		res, err := catalogsync.PreviewQuery(r.Context(), db, DefaultTenantID, q, criteriaPreviewSampleSize)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// errInlineCriteria is returned for a write that still sends an inline query.
var errInlineCriteria = errors.New("inline criteria are no longer accepted: create a collection (POST /collections) and send its id as collection_id")

// rejectInlineCriteria reports a non-null legacy "criteria" field.
func rejectInlineCriteria(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return errInlineCriteria
}

// collectionRef validates the optional collection_id of a block or pool write:
// absent, null or empty is "no collection" (a manual block/pool); anything
// else must be a collection of the tenant, else it returns
// catalogsync.ErrCollectionNotFound (a missing collection and one of another
// tenant look the same). Use writeCollectionRefError for the failure.
func collectionRef(ctx context.Context, db storage.DB, id *string) (*string, error) {
	if id == nil || *id == "" {
		return nil, nil
	}
	if _, err := catalogsync.LoadCollectionQuery(ctx, db, DefaultTenantID, *id); err != nil {
		return nil, err
	}
	return id, nil
}

// writeCollectionRefError answers a failed collectionRef: 400 for an unknown
// collection, the usual mapping for anything else.
func writeCollectionRefError(w http.ResponseWriter, err error) {
	if errors.Is(err, catalogsync.ErrCollectionNotFound) {
		writeError(w, http.StatusBadRequest, "collection_id does not match a collection")
		return
	}
	handleDBError(w, err)
}
