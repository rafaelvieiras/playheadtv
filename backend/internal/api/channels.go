package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// mat is only needed because a channel's own ScheduleID assignment affects
// EPG materialization (create/update trigger it) — issue #7's
// Schedule/Playout split. Nothing else about a channel does.
func registerChannels(mux Router, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /channels", createChannel(db, mat))
	mux.HandleFunc("GET /channels", listChannels(db))
	mux.HandleFunc("GET /channels/{id}", getChannel(db))
	mux.HandleFunc("PUT /channels/{id}", updateChannel(db, mat))
	mux.HandleFunc("DELETE /channels/{id}", deleteChannel(db))
	mux.HandleFunc("POST /channels/{id}/reset-epg", resetChannelEPG(db, mat))
}

const channelColumns = "id, tenant_id, number, name, guide_art_url, timezone, schedule_id, base_schedule_id, created_at, updated_at"

type channelRequest struct {
	Number      int     `json:"number"`
	Name        string  `json:"name"`
	GuideArtURL *string `json:"guide_art_url"`
	Timezone    string  `json:"timezone"`
	// ScheduleID assigns a Schedule to this channel (issue #7); nil/omitted
	// means no schedule (e.g. a pure dynamic-filler-pool channel).
	ScheduleID *string `json:"schedule_id"`
	// BaseScheduleID is the optional second Schedule that plays real
	// programming in every gap ScheduleID leaves (see
	// domain.Channel.BaseScheduleID). It must differ from ScheduleID.
	BaseScheduleID *string `json:"base_schedule_id"`
}

// ValidateChannelSchedules checks a channel's two schedule references: the
// base schedule must exist for the tenant and must not be the pinned one.
// The message of the returned error is meant for the client.
func ValidateChannelSchedules(ctx context.Context, db storage.DB, scheduleID, baseScheduleID *string) error {
	if baseScheduleID == nil {
		return nil
	}
	if scheduleID != nil && *scheduleID == *baseScheduleID {
		return errors.New("base_schedule_id must be a different schedule than schedule_id")
	}
	var ids []string
	if err := db.Query(ctx, &ids, "SELECT id FROM schedule WHERE tenant_id = ? AND id = ?", DefaultTenantID, *baseScheduleID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("base_schedule_id does not reference an existing schedule")
	}
	return nil
}

func fetchChannel(db storage.DB, r *http.Request, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := db.Query(r.Context(), &ch,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return ch, err
}

func createChannel(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req channelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if req.Timezone == "" {
			req.Timezone = "UTC"
		}
		if err := ValidateChannelSchedules(r.Context(), db, req.ScheduleID, req.BaseScheduleID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		now := time.Now().UnixMilli()
		ch := domain.Channel{
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			Number:         req.Number,
			Name:           req.Name,
			GuideArtURL:    req.GuideArtURL,
			Timezone:       req.Timezone,
			ScheduleID:     req.ScheduleID,
			BaseScheduleID: req.BaseScheduleID,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err := db.Exec(r.Context(),
			`INSERT INTO channel (id, tenant_id, number, name, guide_art_url, timezone, schedule_id, base_schedule_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.TenantID, ch.Number, ch.Name, ch.GuideArtURL, ch.Timezone, ch.ScheduleID, ch.BaseScheduleID, ch.CreatedAt, ch.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ch.ScheduleID != nil || ch.BaseScheduleID != nil {
			mat.ResetAfterChannelEdit(r.Context(), DefaultTenantID, ch)
		}
		writeJSON(w, http.StatusCreated, ch)
	}
}

func listChannels(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var channels []domain.Channel
		err := db.Query(r.Context(), &channels,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? ORDER BY number",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, channels)
	}
}

func getChannel(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

func updateChannel(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		before, err := fetchChannel(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}

		var req channelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if req.Timezone == "" {
			req.Timezone = "UTC"
		}
		if err := ValidateChannelSchedules(r.Context(), db, req.ScheduleID, req.BaseScheduleID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		err = db.Exec(r.Context(),
			`UPDATE channel SET number = ?, name = ?, guide_art_url = ?, timezone = ?, schedule_id = ?, base_schedule_id = ?, updated_at = ?
			 WHERE tenant_id = ? AND id = ?`,
			req.Number, req.Name, req.GuideArtURL, req.Timezone, req.ScheduleID, req.BaseScheduleID, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}

		ch, err := fetchChannel(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		// PUT is a full replace (same semantics createChannel/updateBlock
		// already have) — a schedule_id change either way (assigned, cleared,
		// or swapped) needs a regeneration; comparing before/after avoids
		// triggering one on every unrelated field edit (e.g. renaming a
		// channel that has no schedule at all).
		scheduleChanged := !sameStringPtr(before.ScheduleID, ch.ScheduleID) ||
			!sameStringPtr(before.BaseScheduleID, ch.BaseScheduleID)
		if scheduleChanged {
			mat.ResetAfterChannelEdit(r.Context(), DefaultTenantID, ch)
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

func deleteChannel(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchChannel(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM channel WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// resetChannelEPG force-resets one channel's grid: the materialized future and
// the program currently on the air are discarded, continuous rotations restart
// from the beginning, and the channel is regenerated before the response is
// sent. Viewers switch program immediately.
func resetChannelEPG(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := mat.ResetChannel(r.Context(), DefaultTenantID, ch); err != nil {
			writeError(w, http.StatusInternalServerError, "could not reset the channel grid")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
