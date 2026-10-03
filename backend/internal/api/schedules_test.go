package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

func setupSchedule(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/schedules", `{"name":"Weekday grid"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var s domain.Schedule
	json.Unmarshal(rec.Body.Bytes(), &s)
	return s.ID
}

func TestSchedulesCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/schedules", `{"name":"Weekday grid","description":"Mon-Fri sitcoms"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created domain.Schedule
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || created.Name != "Weekday grid" {
		t.Fatalf("unexpected created schedule: %+v", created)
	}

	rec = doRequest(t, mux, "GET", "/schedules", "")
	var list []domain.Schedule
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/schedules/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "PUT", "/schedules/"+created.ID, `{"name":"Weekday grid v2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.Schedule
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "Weekday grid v2" {
		t.Fatalf("unexpected updated schedule: %+v", updated)
	}

	rec = doRequest(t, mux, "DELETE", "/schedules/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/schedules/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestScheduleMissingName(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/schedules", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleItemsCRUD(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)

	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"weekly","days_of_week":[1],"start_time_local":"08:00"}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var item domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &item)
	if !item.Active || item.ScheduleID != scheduleID {
		t.Fatalf("unexpected created item: %+v", item)
	}

	rec = doRequest(t, mux, "GET", "/schedules/"+scheduleID+"/items", "")
	var list []domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/schedule-items/"+item.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	updateBody := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"once","start_at":1700000000000}`, blockID)
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Recurrence != domain.RecurrenceOnce || updated.StartAt == nil || *updated.StartAt != 1700000000000 {
		t.Fatalf("unexpected updated item: %+v", updated)
	}

	rec = doRequest(t, mux, "DELETE", "/schedule-items/"+item.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/schedule-items/"+item.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestScheduleItemWeeklyRequiresDayAndTime(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)

	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"weekly"}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleItemOnceRequiresStartAt(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)

	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"once"}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleItemInvalidScheduleID(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)

	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"once","start_at":1}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedules/does-not-exist/items", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestChannelScheduleAssignment exercises issue #7's other half of the split
// — a Schedule reused by more than one channel, each still independently
// addressable via its own channel.schedule_id.
func TestChannelScheduleAssignment(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, _ := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)

	rec := doRequest(t, mux, "PUT", "/channels/"+channelID,
		fmt.Sprintf(`{"number":1,"name":"Channel 1","schedule_id":%q}`, scheduleID))
	if rec.Code != http.StatusOK {
		t.Fatalf("assign: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.ScheduleID == nil || *updated.ScheduleID != scheduleID {
		t.Fatalf("unexpected channel after assignment: %+v", updated)
	}

	// Clearing it back to no schedule (omitted field, PUT is a full replace).
	rec = doRequest(t, mux, "PUT", "/channels/"+channelID, `{"number":1,"name":"Channel 1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var cleared domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &cleared)
	if cleared.ScheduleID != nil {
		t.Fatalf("expected schedule_id cleared, got %+v", cleared)
	}
}

func TestChannelInvalidScheduleFK(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, _ := setupChannelAndBlock(t, mux)

	rec := doRequest(t, mux, "PUT", "/channels/"+channelID,
		`{"number":1,"name":"Channel 1","schedule_id":"does-not-exist"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleItemContinuousAtMostOnePerSchedule(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)
	otherScheduleID := setupSchedule(t, mux)

	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"continuous"}`, blockID)
	rec := doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first continuous: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var first domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &first)
	if first.Recurrence != domain.RecurrenceContinuous || len(first.DaysOfWeek) != 0 || first.StartAt != nil {
		t.Fatalf("unexpected continuous item: %+v", first)
	}

	rec = doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second continuous: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	// Another schedule may have its own.
	rec = doRequest(t, mux, "POST", "/schedules/"+otherScheduleID+"/items", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("continuous in another schedule: status = %d", rec.Code)
	}

	// Turning a timed item into a second continuous one is rejected too...
	weekly := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"weekly","days_of_week":[1],"start_time_local":"08:00"}`, blockID)
	rec = doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", weekly)
	var timed domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &timed)
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+timed.ID, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT to second continuous: status = %d, want 409", rec.Code)
	}
	// ...but re-saving the existing continuous item is fine.
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+first.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT existing continuous: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestResetChannelEPG(t *testing.T) {
	_, mux := newTestDB(t)
	channelID, _ := setupChannelAndBlock(t, mux)

	rec := doRequest(t, mux, "POST", "/channels/"+channelID+"/reset-epg", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "POST", "/channels/does-not-exist/reset-epg", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reset unknown channel: status = %d, want 404", rec.Code)
	}
}

func TestEPGMetaExposesFreezeMargin(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "GET", "/epg/meta", "")
	var meta struct {
		FreezeMarginMs int64 `json:"freeze_margin_ms"`
	}
	json.Unmarshal(rec.Body.Bytes(), &meta)
	if meta.FreezeMarginMs != 3*60*60*1000 {
		t.Fatalf("freeze_margin_ms = %d, want 3h in ms (body %s)", meta.FreezeMarginMs, rec.Body.String())
	}
}

func TestScheduleItemWeeklyMultipleDays(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)
	url := "/schedules/" + scheduleID + "/items"

	// A list is stored sorted and returned as a list.
	body := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"weekly","days_of_week":[5,4],"start_time_local":"22:00"}`, blockID)
	rec := doRequest(t, mux, "POST", url, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var item domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &item)
	if len(item.DaysOfWeek) != 2 || item.DaysOfWeek[0] != 4 || item.DaysOfWeek[1] != 5 {
		t.Fatalf("created days_of_week = %v, want [4 5]", item.DaysOfWeek)
	}
	if strings.Contains(rec.Body.String(), "day_of_week\"") {
		t.Fatalf("response still carries the old single-day field: %s", rec.Body.String())
	}

	// The list round-trips through GET and PUT.
	rec = doRequest(t, mux, "GET", "/schedule-items/"+item.ID, "")
	var got domain.ScheduleItem
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.DaysOfWeek) != 2 {
		t.Fatalf("get days_of_week = %v", got.DaysOfWeek)
	}
	put := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"weekly","days_of_week":[0,1,2,3,4,5,6],"start_time_local":"18:00"}`, blockID)
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.DaysOfWeek) != 7 {
		t.Fatalf("updated days_of_week = %v, want all seven", got.DaysOfWeek)
	}

	// Converting to a non-weekly item empties the list (and JSON stays an array).
	once := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"once","start_at":1700000000000}`, blockID)
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, once)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"days_of_week":[]`) {
		t.Fatalf("convert to once: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleItemWeeklyDaysValidation(t *testing.T) {
	_, mux := newTestDB(t)
	_, blockID := setupChannelAndBlock(t, mux)
	scheduleID := setupSchedule(t, mux)
	url := "/schedules/" + scheduleID + "/items"

	cases := map[string]string{
		"missing":            `"recurrence_type":"weekly","start_time_local":"08:00"`,
		"empty":              `"recurrence_type":"weekly","days_of_week":[],"start_time_local":"08:00"`,
		"duplicate":          `"recurrence_type":"weekly","days_of_week":[1,1],"start_time_local":"08:00"`,
		"negative":           `"recurrence_type":"weekly","days_of_week":[-1],"start_time_local":"08:00"`,
		"out of range":       `"recurrence_type":"weekly","days_of_week":[2,7],"start_time_local":"08:00"`,
		"old single-day key": `"recurrence_type":"weekly","day_of_week":1,"start_time_local":"08:00"`,
		"once with days":     `"recurrence_type":"once","days_of_week":[1],"start_at":1`,
		"continuous w/ days": `"recurrence_type":"continuous","days_of_week":[1]`,
	}
	for name, fields := range cases {
		body := fmt.Sprintf(`{"block_id":%q,%s}`, blockID, fields)
		for _, method := range []string{"POST", "PUT"} {
			target := url
			if method == "PUT" {
				// Seed an item to update.
				seed := fmt.Sprintf(`{"block_id":%q,"recurrence_type":"once","start_at":1}`, blockID)
				rec := doRequest(t, mux, "POST", url, seed)
				var it domain.ScheduleItem
				json.Unmarshal(rec.Body.Bytes(), &it)
				target = "/schedule-items/" + it.ID
			}
			if rec := doRequest(t, mux, method, target, body); rec.Code != http.StatusBadRequest {
				t.Errorf("%s %s: status = %d, want 400, body = %s", name, method, rec.Code, rec.Body.String())
			}
		}
	}
}
