package api

import (
	"database/sql"
	"errors"
	"io"
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

		query := "SELECT " + catalogsync.ListColumns + " FROM catalog_item WHERE tenant_id = ?"
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
		if err := catalogsync.LoadRelations(r.Context(), db, DefaultTenantID, items, false); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// criteriaPreviewSampleSize is how many matching items POST
// /catalog-items/preview-criteria returns as a sample.
const criteriaPreviewSampleSize = 20

// previewCriteria takes a condition tree (the body is the query itself, same
// validation as block/pool criteria) and reports what it matches right now,
// persisting nothing.
func previewCriteria(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxQueryBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		q, err := catalogsync.ParseQuery(body)
		if err != nil {
			if errors.Is(err, catalogsync.ErrLegacyCriteria) {
				writeError(w, http.StatusBadRequest, err.Error())
			} else {
				writeError(w, http.StatusBadRequest, "criteria: "+err.Error())
			}
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

// maxQueryBodyBytes bounds the body of a query preview.
const maxQueryBodyBytes = 64 << 10

// getCatalogItem returns one item with everything GET /catalog-items returns
// plus the detail-only fields: cast, directors, production_locations and
// chapters. It exists mainly for a playback client resolving a
// GET /epg entry's catalog_item_id into the source-native ID (ExternalID)
// and MediaSourceID it needs to build a stream URL — GET /catalog-items
// alone only supports search/browse, not a lookup by ID.
func getCatalogItem(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rows []domain.CatalogItem
		err := db.Query(r.Context(), &rows,
			"SELECT "+catalogsync.DetailColumns+" FROM catalog_item WHERE tenant_id = ? AND id = ?",
			DefaultTenantID, r.PathValue("id"),
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if len(rows) == 0 {
			handleDBError(w, sql.ErrNoRows)
			return
		}
		if err := catalogsync.LoadRelations(r.Context(), db, DefaultTenantID, rows, true); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rows[0])
	}
}
