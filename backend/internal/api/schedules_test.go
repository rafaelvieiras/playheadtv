package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func setupChannelAndBlock(t *testing.T, mux *http.ServeMux) (channelID, blockID string) {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"Channel 1"}`)
	var ch domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &ch)

	rec = doRequest(t, mux, "POST", "/blocks", `{"name":"Block 1"}`)
	var b domain.Block
	json.Unmarshal(rec.Body.Bytes(), &b)

	return ch.ID, b.ID
}

func TestScheduleEntriesCRUD(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, blockID := setupChannelAndBlock(t, mux)

	body := fmt.Sprintf(`{"channel_id":%q,"block_id":%q,"recurrence_type":"weekly","day_of_week":1,"start_time_local":"08:00"}`, channelID, blockID)
	rec := doRequest(t, mux, "POST", "/schedule-entries", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var entry domain.ScheduleEntry
	json.Unmarshal(rec.Body.Bytes(), &entry)
	if !entry.Active {
		t.Error("Active should default to true")
	}

	rec = doRequest(t, mux, "GET", "/schedule-entries", "")
	var list []domain.ScheduleEntry
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/schedule-entries/"+entry.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	updateBody := fmt.Sprintf(`{"channel_id":%q,"block_id":%q,"recurrence_type":"once","start_at":1700000000000}`, channelID, blockID)
	rec = doRequest(t, mux, "PUT", "/schedule-entries/"+entry.ID, updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.ScheduleEntry
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Recurrence != domain.RecurrenceOnce || updated.StartAt == nil || *updated.StartAt != 1700000000000 {
		t.Fatalf("unexpected updated entry: %+v", updated)
	}

	rec = doRequest(t, mux, "DELETE", "/schedule-entries/"+entry.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/schedule-entries/"+entry.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestScheduleEntryWeeklyRequiresDayAndTime(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, blockID := setupChannelAndBlock(t, mux)

	body := fmt.Sprintf(`{"channel_id":%q,"block_id":%q,"recurrence_type":"weekly"}`, channelID, blockID)
	rec := doRequest(t, mux, "POST", "/schedule-entries", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleEntryOnceRequiresStartAt(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, blockID := setupChannelAndBlock(t, mux)

	body := fmt.Sprintf(`{"channel_id":%q,"block_id":%q,"recurrence_type":"once"}`, channelID, blockID)
	rec := doRequest(t, mux, "POST", "/schedule-entries", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleEntryInvalidChannelFK(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)

	body := fmt.Sprintf(`{"channel_id":"does-not-exist","block_id":%q,"recurrence_type":"once","start_at":1}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedule-entries", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
