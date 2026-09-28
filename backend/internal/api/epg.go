package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerEPG(mux *http.ServeMux, db storage.DB) {
	mux.HandleFunc("GET /epg", getEPG(db))
	mux.HandleFunc("GET /epg/meta", getEPGMeta(db))
}

const epgEntryColumns = "id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_entry_id, generated_at"

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
}

func getEPGMeta(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rev domain.EpgRevision
		err := db.Query(r.Context(), &rev,
			"SELECT tenant_id, revision, valid_until, updated_at FROM epg_revision WHERE tenant_id = ?",
			DefaultTenantID,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Expected before the EPG materializer exists.
				writeJSON(w, http.StatusOK, epgMetaResponse{Revision: 0, ValidUntil: 0})
				return
			}
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, epgMetaResponse{Revision: rev.Revision, ValidUntil: rev.ValidUntil})
	}
}
