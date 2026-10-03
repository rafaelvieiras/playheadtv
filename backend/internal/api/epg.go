package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerEPG(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("GET /epg", getEPG(db))
	mux.HandleFunc("GET /epg/meta", getEPGMeta(db, mat))
	mux.HandleFunc("GET /channels/{id}/epg-preview", previewChannelGridReset(db, mat))
	mux.HandleFunc("GET /channels/{id}/epg-warnings", channelEPGWarnings(db, mat))
}

const epgEntryColumns = "id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at"

// getEPG only reads epg_entry as it stands today — there is no materializer
// yet (separate roadmap item), so this is expected to return an empty list
// until one exists. Window filtering matches entries overlapping [start,end]
// when those query params are given, not entries strictly starting inside it.
func getEPG(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		query := "SELECT " + epgEntryColumns + " FROM epg_entry WHERE tenant_id = ?"
		args := []any{DefaultTenantID}

		if channelID := q.Get("channel_id"); channelID != "" {
			query += " AND channel_id = ?"
			args = append(args, channelID)
		}
		if v := q.Get("start"); v != "" {
			start, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "start must be an epoch millis integer")
				return
			}
			query += " AND end_at >= ?"
			args = append(args, start)
		}
		if v := q.Get("end"); v != "" {
			end, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "end must be an epoch millis integer")
				return
			}
			query += " AND start_at <= ?"
			args = append(args, end)
		}
		query += " ORDER BY channel_id, start_at"

		var entries []domain.EpgEntry
		if err := db.Query(r.Context(), &entries, query, args...); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

type epgMetaResponse struct {
	Revision   int64 `json:"revision"`
	ValidUntil int64 `json:"valid_until"`
	// FreezeMarginMs is how far ahead of now a continuous-schedule channel's
	// programs are frozen: they will not change unless an author force-resets
	// the channel (see materializer.DefaultFreezeMargin).
	FreezeMarginMs int64 `json:"freeze_margin_ms"`
}

func getEPGMeta(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		freezeMs := mat.FreezeMargin().Milliseconds()
		var rev domain.EpgRevision
		err := db.Query(r.Context(), &rev,
			"SELECT tenant_id, revision, valid_until, updated_at FROM epg_revision WHERE tenant_id = ?",
			DefaultTenantID,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Expected before the EPG materializer exists.
				writeJSON(w, http.StatusOK, epgMetaResponse{Revision: 0, ValidUntil: 0, FreezeMarginMs: freezeMs})
				return
			}
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, epgMetaResponse{Revision: rev.Revision, ValidUntil: rev.ValidUntil, FreezeMarginMs: freezeMs})
	}
}

// previewChannelGridReset is a dry-run of POST /channels/{id}/reset-epg: it
// returns the entries (same JSON shape as GET /epg) the channel would have if
// its grid were reset now, from the stored configuration with no freeze
// margin. It writes nothing: epg_entry and epg_revision stay untouched.
// start/end (epoch millis) filter by overlap exactly like GET /epg; both are
// optional.
func previewChannelGridReset(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		q := r.URL.Query()
		var start, end int64
		if v := q.Get("start"); v != "" {
			start, err = strconv.ParseInt(v, 10, 64)
			if err != nil || start <= 0 {
				writeError(w, http.StatusBadRequest, "start must be a positive epoch millis integer")
				return
			}
		}
		if v := q.Get("end"); v != "" {
			end, err = strconv.ParseInt(v, 10, 64)
			if err != nil || end <= 0 {
				writeError(w, http.StatusBadRequest, "end must be a positive epoch millis integer")
				return
			}
		}
		entries, err := mat.PreviewReset(r.Context(), DefaultTenantID, ch, start, end)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not preview the channel grid")
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

// channelEPGWarnings reports the problems in a channel's stored configuration
// that leave parts of its programming silently absent, today a criteria item
// (or dynamic block) whose query fails to evaluate: [{kind, block_id,
// block_item_id?, message}], empty when there is none. Read-only; it runs the
// same loader as the materializer.
func channelEPGWarnings(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		warnings, err := mat.Warnings(r.Context(), DefaultTenantID, ch)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not evaluate the channel configuration")
			return
		}
		writeJSON(w, http.StatusOK, warnings)
	}
}
