package api

import (
	"net/http"
	"strconv"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerSeries(mux *http.ServeMux, db storage.DB) {
	mux.HandleFunc("GET /series", listSeries(db))
	mux.HandleFunc("POST /series/slice-preview", previewSeriesSlice(db))
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

// seriesSlicePreviewRequest is a series (media source plus external id or
// title) and the slice to try on it.
type seriesSlicePreviewRequest struct {
	MediaSourceID    string              `json:"media_source_id"`
	SeriesExternalID string              `json:"series_external_id"`
	SeriesTitle      string              `json:"series_title"`
	Slice            *domain.SeriesSlice `json:"slice"`
}

// previewSeriesSlice counts the episodes (and their total duration) a series
// would expand to under a slice, along with the whole series count, without
// storing anything: what the block editor shows while a slice is being edited.
// The slice is validated like a block item write; a slice that matches nothing
// is a valid answer (count 0).
func previewSeriesSlice(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req seriesSlicePreviewRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.MediaSourceID == "" {
			writeError(w, http.StatusBadRequest, "media_source_id is required")
			return
		}
		if req.SeriesExternalID == "" && req.SeriesTitle == "" {
			writeError(w, http.StatusBadRequest, "series_external_id or series_title is required")
			return
		}
		id := domain.SeriesSpec{MediaSourceID: req.MediaSourceID, SeriesExternalID: req.SeriesExternalID, SeriesTitle: req.SeriesTitle}
		out, err := series.PreviewSlice(r.Context(), db, DefaultTenantID, id, req.Slice)
		if err != nil {
			if series.IsInvalid(err) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
