package api

import (
	"net/http"
	"strconv"

	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerSeries(mux *http.ServeMux, db storage.DB) {
	mux.HandleFunc("GET /series", listSeries(db))
}

// listSeries lists the series the catalog holds (episodes grouped by media
// source, external id and title) with their episode count and total duration:
// what a series picker needs to add a series item to a block. q matches the
// series title accent- and case-insensitively.
func listSeries(db storage.DB) http.HandlerFunc {
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
		out, err := series.List(r.Context(), db, DefaultTenantID, q.Get("q"), q.Get("media_source_id"), limit, offset)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if out == nil {
			out = []series.Summary{}
		}
		writeJSON(w, http.StatusOK, out)
	}
}
