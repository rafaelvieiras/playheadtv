package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerSchedules wires both halves of issue #7's Schedule/Playout split:
// Schedule itself (a reusable, named group of rules — assigning one to a
// channel is PUT /channels/{id}'s job, see channels.go) and ScheduleItem (one
// recurrence rule inside it, schedule_entry's old shape minus channel_id).
func registerSchedules(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /schedules", createSchedule(db))
	mux.HandleFunc("GET /schedules", listSchedules(db))
	mux.HandleFunc("GET /schedules/{id}", getSchedule(db))
	mux.HandleFunc("PUT /schedules/{id}", updateSchedule(db))
	mux.HandleFunc("DELETE /schedules/{id}", deleteSchedule(db, mat))

	mux.HandleFunc("POST /schedules/{id}/items", createScheduleItem(db, mat))
	mux.HandleFunc("GET /schedules/{id}/items", listScheduleItems(db))
	mux.HandleFunc("GET /schedule-items/{id}", getScheduleItem(db))
	mux.HandleFunc("PUT /schedule-items/{id}", updateScheduleItem(db, mat))
	mux.HandleFunc("DELETE /schedule-items/{id}", deleteScheduleItem(db, mat))
}

const scheduleColumns = "id, tenant_id, name, description, created_at, updated_at"

// scheduleItemColumns aliases recurrence_type to Recurrence, the struct field
// name domain.ScheduleItem actually has — same pattern as fillerRuleColumns
// in filler.go.
const scheduleItemColumns = "id, tenant_id, schedule_id, block_id, recurrence_type AS recurrence, day_of_week, start_time_local, start_at, active, created_at, updated_at"

type scheduleRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func fetchSchedule(db storage.DB, r *http.Request, id string) (domain.Schedule, error) {
	var s domain.Schedule
	err := db.Query(r.Context(), &s,
		"SELECT "+scheduleColumns+" FROM schedule WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return s, err
}

func createSchedule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scheduleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		now := time.Now().UnixMilli()
		s := domain.Schedule{
			ID:          uuid.NewString(),
			TenantID:    DefaultTenantID,
			Name:        req.Name,
			Description: req.Description,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err := db.Exec(r.Context(),
			"INSERT INTO schedule (id, tenant_id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			s.ID, s.TenantID, s.Name, s.Description, s.CreatedAt, s.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s)
	}
}

func listSchedules(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var schedules []domain.Schedule
		err := db.Query(r.Context(), &schedules,
			"SELECT "+scheduleColumns+" FROM schedule WHERE tenant_id = ? ORDER BY name",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, schedules)
	}
}

func getSchedule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := fetchSchedule(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

func updateSchedule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchSchedule(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		var req scheduleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		// A Schedule's own name/description never affects EPG materialization
		// (only its items and who it's assigned to do), so this never calls
		// mat.Trigger().
		err := db.Exec(r.Context(),
			"UPDATE schedule SET name = ?, description = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			req.Name, req.Description, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		s, err := fetchSchedule(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

func deleteSchedule(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchSchedule(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		// ON DELETE CASCADE drops this Schedule's schedule_item rows;
		// channel.schedule_id referencing it is NOT ON DELETE SET NULL (SQLite
		// requires that to be spelled out at creation time, which the initial
		// migration didn't need), so a channel still pointing at it would
		// violate the FK — surfaced as a normal handleDBError, same as any
		// other in-use-reference delete in this codebase.
		if err := db.Exec(r.Context(), "DELETE FROM schedule WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}

type scheduleItemRequest struct {
	BlockID        string  `json:"block_id"`
	RecurrenceType string  `json:"recurrence_type"`
	DayOfWeek      *int    `json:"day_of_week"`
	StartTimeLocal *string `json:"start_time_local"`
	StartAt        *int64  `json:"start_at"`
	Active         *bool   `json:"active"`
}

func validateScheduleItemRequest(req scheduleItemRequest) (string, bool) {
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

func fetchScheduleItem(db storage.DB, r *http.Request, id string) (domain.ScheduleItem, error) {
	var si domain.ScheduleItem
	err := db.Query(r.Context(), &si,
		"SELECT "+scheduleItemColumns+" FROM schedule_item WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return si, err
}

func createScheduleItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheduleID := r.PathValue("id")
		if _, err := fetchSchedule(db, r, scheduleID); err != nil {
			handleDBError(w, err)
			return
		}
		var req scheduleItemRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateScheduleItemRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		now := time.Now().UnixMilli()
		si := domain.ScheduleItem{
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			ScheduleID:     scheduleID,
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
			`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			si.ID, si.TenantID, si.ScheduleID, si.BlockID, string(si.Recurrence), si.DayOfWeek, si.StartTimeLocal,
			si.StartAt, si.Active, si.CreatedAt, si.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, si)
	}
}

func listScheduleItems(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheduleID := r.PathValue("id")
		if _, err := fetchSchedule(db, r, scheduleID); err != nil {
			handleDBError(w, err)
			return
		}
		var items []domain.ScheduleItem
		err := db.Query(r.Context(), &items,
			"SELECT "+scheduleItemColumns+" FROM schedule_item WHERE tenant_id = ? AND schedule_id = ? ORDER BY created_at",
			DefaultTenantID, scheduleID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

func getScheduleItem(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := fetchScheduleItem(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, si)
	}
}

func updateScheduleItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchScheduleItem(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		var req scheduleItemRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateScheduleItemRequest(req); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		// scheduleID is immutable via PUT (items don't move between schedules).
		err := db.Exec(r.Context(),
			`UPDATE schedule_item SET block_id = ?, recurrence_type = ?, day_of_week = ?,
			 start_time_local = ?, start_at = ?, active = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
			req.BlockID, req.RecurrenceType, req.DayOfWeek, req.StartTimeLocal, req.StartAt,
			active, time.Now().UnixMilli(), DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		si, err := fetchScheduleItem(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, si)
	}
}

func deleteScheduleItem(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchScheduleItem(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM schedule_item WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}
