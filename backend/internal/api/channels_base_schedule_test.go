package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func newSchedule(t *testing.T, mux *http.ServeMux, name string) string {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/schedules", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var s domain.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

func TestChannelBaseScheduleRoundTrip(t *testing.T) {
	_, mux := newTestDB(t)
	pinned := newSchedule(t, mux, "Pinned")
	base := newSchedule(t, mux, "Base")

	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","schedule_id":"`+pinned+`","base_schedule_id":"`+base+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ch domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &ch)
	if ch.BaseScheduleID == nil || *ch.BaseScheduleID != base || ch.ScheduleID == nil || *ch.ScheduleID != pinned {
		t.Fatalf("created channel = %+v", ch)
	}

	rec = doRequest(t, mux, "GET", "/channels/"+ch.ID, "")
	var got domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.BaseScheduleID == nil || *got.BaseScheduleID != base {
		t.Fatalf("read channel = %+v", got)
	}

	// PUT is a full replace: leaving the field out clears it.
	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"C","schedule_id":"`+pinned+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.BaseScheduleID != nil {
		t.Fatalf("base_schedule_id not cleared: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"base_schedule_id":null`) {
		t.Fatalf("base_schedule_id missing from the response: %s", rec.Body.String())
	}
}

func TestChannelBaseScheduleValidation(t *testing.T) {
	_, mux := newTestDB(t)
	pinned := newSchedule(t, mux, "Pinned")

	// The two layers must be different schedules.
	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","schedule_id":"`+pinned+`","base_schedule_id":"`+pinned+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same schedule on create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// An unknown schedule is a client error, not a server error.
	rec = doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","base_schedule_id":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown base on create: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","schedule_id":"`+pinned+`"}`)
	var ch domain.Channel
	json.Unmarshal(rec.Body.Bytes(), &ch)
	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"C","schedule_id":"`+pinned+`","base_schedule_id":"`+pinned+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same schedule on update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// A channel with only a base schedule is allowed.
	base := newSchedule(t, mux, "Base")
	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"C","base_schedule_id":"`+base+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("base only: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteScheduleUsedAsBaseIsRefused(t *testing.T) {
	_, mux := newTestDB(t)
	base := newSchedule(t, mux, "Base")
	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","base_schedule_id":"`+base+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "DELETE", "/schedules/"+base, "")
	if rec.Code == http.StatusNoContent {
		t.Fatalf("deleting a schedule a channel uses as base succeeded")
	}
}
