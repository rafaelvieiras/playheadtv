package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerSchedules(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /schedule-entries", createScheduleEntry(db, mat))
	mux.HandleFunc("GET /schedule-entries", listScheduleEntries(db))
	mux.HandleFunc("GET /schedule-entries/{id}", getScheduleEntry(db))
	mux.HandleFunc("PUT /schedule-entries/{id}", updateScheduleEntry(db, mat))
	mux.HandleFunc("DELETE /schedule-entries/{id}", deleteScheduleEntry(db, mat))
}

// scheduleEntryColumns aliases recurrence_type to Recurrence, the struct
// field name domain.ScheduleEntry actually has — see the same note on
// fillerRuleColumns in filler.go.
const scheduleEntryColumns = "id, tenant_id, channel_id, block_id, recurrence_type AS recurrence, day_of_week, start_time_local, start_at, active, created_at, updated_at"

type scheduleEntryRequest struct {
	ChannelID      string  `json:"channel_id"`
	BlockID        string  `json:"block_id"`
	RecurrenceType string  `json:"recurrence_type"`
	DayOfWeek      *int    `json:"day_of_week"`
	StartTimeLocal *string `json:"start_time_local"`
	StartAt        *int64  `json:"start_at"`
	Active         *bool   `json:"active"`
}

func validateScheduleEntryRequest(req scheduleEntryRequest) (string, bool) {
	if req.ChannelID == "" {
		return "channel_id is required", false
	}
	if req.BlockID == "" {
		return "block_id is required", false
	}
	switch domain.RecurrenceType(req.RecurrenceType) {
	case domain.RecurrenceWeekly:
		if req.DayOfWeek == nil || *req.DayOfWeek < 0 || *req.DayOfWeek > 6 {
			return "day_of_week (0-6) is required for weekly recurrence", false
		}
		if req.StartTimeLocal == nil {
			return "start_time_local is required for weekly recurrence", false
		}
		if _, err := time.Parse("15:04", *req.StartTimeLocal); err != nil {
			return "start_time_local must be HH:MM", false
		}
	case domain.RecurrenceOnce:
		if req.StartAt == nil {
			return "start_at is required for once recurrence", false
		}
	default:
		return "recurrence_type must be one of weekly, once", false
	}
	return "", true
}

func fetchScheduleEntry(db storage.DB, r *http.Request, id string) (domain.ScheduleEntry, error) {
	var se domain.ScheduleEntry
	err := db.Query(r.Context(), &se,
		"SELECT "+scheduleEntryColumns+" FROM schedule_entry WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return se, err
}

func createScheduleEntry(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scheduleEntryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateScheduleEntryRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		now := time.Now().UnixMilli()
		se := domain.ScheduleEntry{
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			ChannelID:      req.ChannelID,
			BlockID:        req.BlockID,
			Recurrence:     domain.RecurrenceType(req.RecurrenceType),
			DayOfWeek:      req.DayOfWeek,
			StartTimeLocal: req.StartTimeLocal,
			StartAt:        req.StartAt,
			Active:         active,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err := db.Exec(r.Context(),
			`INSERT INTO schedule_entry (id, tenant_id, channel_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			se.ID, se.TenantID, se.ChannelID, se.BlockID, string(se.Recurrence), se.DayOfWeek, se.StartTimeLocal,
			se.StartAt, se.Active, se.CreatedAt, se.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, se)
	}
}

func listScheduleEntries(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var entries []domain.ScheduleEntry
		err := db.Query(r.Context(), &entries,
			"SELECT "+scheduleEntryColumns+" FROM schedule_entry WHERE tenant_id = ? ORDER BY created_at",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

func getScheduleEntry(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		se, err := fetchScheduleEntry(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, se)
	}
}

func updateScheduleEntry(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchScheduleEntry(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		var req scheduleEntryRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateScheduleEntryRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		err := db.Exec(r.Context(),
			`UPDATE schedule_entry SET channel_id = ?, block_id = ?, recurrence_type = ?, day_of_week = ?,
			 start_time_local = ?, start_at = ?, active = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
			req.ChannelID, req.BlockID, req.RecurrenceType, req.DayOfWeek, req.StartTimeLocal, req.StartAt,
			active, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		se, err := fetchScheduleEntry(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, se)
	}
}

func deleteScheduleEntry(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchScheduleEntry(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM schedule_entry WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}
