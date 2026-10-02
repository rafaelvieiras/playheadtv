package api

import (
	"net/http"
	"strconv"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerCatalogItems(mux *http.ServeMux, db storage.DB) {
	mux.HandleFunc("GET /catalog-items", listCatalogItems(db))
	mux.HandleFunc("POST /catalog-items/preview-criteria", previewCriteria(db))
	mux.HandleFunc("GET /catalog-items/{id}", getCatalogItem(db))
}

const catalogItemColumns = "id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at, genres, production_year"

const (
	defaultCatalogItemLimit = 50
	maxCatalogItemLimit     = 200
)

func listCatalogItems(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		limit := defaultCatalogItemLimit
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = n
		}
		if limit > maxCatalogItemLimit {
			limit = maxCatalogItemLimit
		}
		offset := 0
		if v := q.Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			offset = n
		}

		query := "SELECT " + catalogItemColumns + " FROM catalog_item WHERE tenant_id = ?"
		args := []any{DefaultTenantID}

		if title := q.Get("q"); title != "" {
			clause, arg := catalogsync.SearchClause(title)
			query += clause
			args = append(args, arg)
		}
		if mediaSourceID := q.Get("media_source_id"); mediaSourceID != "" {
			query += " AND media_source_id = ?"
			args = append(args, mediaSourceID)
		}
		if kind := q.Get("kind"); kind != "" {
			query += " AND kind = ?"
			args = append(args, kind)
		}
		query += " ORDER BY title LIMIT ? OFFSET ?"
		args = append(args, limit, offset)

		var items []domain.CatalogItem
		if err := db.Query(r.Context(), &items, query, args...); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// criteriaPreviewSampleSize is how many matching items POST
// /catalog-items/preview-criteria returns as a sample.
const criteriaPreviewSampleSize = 20

// previewCriteria takes the same criteria object as block/pool criteria
// (same validation) and reports what it matches right now, persisting nothing.
func previewCriteria(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fillerPoolCriteriaRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validatePoolCriteria(&req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		res, err := catalogsync.PreviewCriteria(r.Context(), db, DefaultTenantID, req.Kind, req.Genres, req.YearFrom, req.YearTo, criteriaPreviewSampleSize)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// getCatalogItem exists mainly for a playback client resolving a
// GET /epg entry's catalog_item_id into the source-native ID (ExternalID)
// and MediaSourceID it needs to build a stream URL — GET /catalog-items
// alone only supports search/browse, not a lookup by ID.
func getCatalogItem(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domain.CatalogItem
		err := db.Query(r.Context(), &item,
			"SELECT "+catalogItemColumns+" FROM catalog_item WHERE tenant_id = ? AND id = ?",
			DefaultTenantID, r.PathValue("id"),
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
