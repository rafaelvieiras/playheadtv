package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func createBlockWith(t *testing.T, mux *http.ServeMux, body string) domain.Block {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/blocks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status = %d, body = %s", body, rec.Code, rec.Body.String())
	}
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	return b
}

func TestBlockListMode_DefaultsToRepeatAndAcceptsOnce(t *testing.T) {
	_, mux := newTestDB(t)
	if b := createBlockWith(t, mux, `{"name":"Default"}`); b.ListMode != domain.ListModeRepeat {
		t.Errorf("default list_mode = %q, want repeat", b.ListMode)
	}
	once := createBlockWith(t, mux, `{"name":"Once","list_mode":"once"}`)
	if once.ListMode != domain.ListModeOnce {
		t.Errorf("created list_mode = %q, want once", once.ListMode)
	}
	rec := doRequest(t, mux, "GET", "/blocks/"+once.ID, "")
	var got domain.Block
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.ListMode != domain.ListModeOnce {
		t.Errorf("read list_mode = %q, want once", got.ListMode)
	}
	rec = doRequest(t, mux, "GET", "/blocks", "")
	var list []domain.Block
	mustDecode(t, rec.Body.Bytes(), &list)
	for _, b := range list {
		if b.ListMode == "" {
			t.Errorf("listed block %s has no list_mode", b.Name)
		}
	}
}

func TestBlockListMode_ValidationIsStrict(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Bad","list_mode":"loop"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown mode: status = %d, want 400", rec.Code)
	}
	col := doRequest(t, mux, "POST", "/collections", `{"name":"All","query":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]}}`)
	var c domain.Collection
	mustDecode(t, col.Body.Bytes(), &c)
	if c.ID == "" {
		t.Fatalf("create collection: %d %s", col.Code, col.Body.String())
	}
	rec = doRequest(t, mux, "POST", "/blocks", `{"name":"Dyn","collection_id":"`+c.ID+`","list_mode":"once"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "manual") {
		t.Errorf("list_mode on a dynamic block: status = %d, body = %s, want 400 naming manual blocks", rec.Code, rec.Body.String())
	}
	dyn := createBlockWith(t, mux, `{"name":"Dyn","collection_id":"`+c.ID+`"}`)
	if dyn.ListMode != "" {
		t.Errorf("a dynamic block serves list_mode %q, want none", dyn.ListMode)
	}
	manual := createBlockWith(t, mux, `{"name":"Manual"}`)
	rec = doRequest(t, mux, "PUT", "/blocks/"+manual.ID, `{"name":"Manual","collection_id":"`+c.ID+`","list_mode":"repeat"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT list_mode with a collection: status = %d, want 400", rec.Code)
	}
	rec = doRequest(t, mux, "PUT", "/blocks/"+manual.ID, `{"name":"Manual","list_mode":"sometimes"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT unknown mode: status = %d, want 400", rec.Code)
	}
}

func TestBlockListMode_UpdateKeepsTheModeWhenOmitted(t *testing.T) {
	_, mux := newTestDB(t)
	b := createBlockWith(t, mux, `{"name":"Once","list_mode":"once"}`)
	rec := doRequest(t, mux, "PUT", "/blocks/"+b.ID, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	var got domain.Block
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.ListMode != domain.ListModeOnce || got.Name != "Renamed" {
		t.Errorf("after a PUT without list_mode = %+v, want it kept as once", got)
	}
	rec = doRequest(t, mux, "PUT", "/blocks/"+b.ID, `{"name":"Renamed","list_mode":"repeat"}`)
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.ListMode != domain.ListModeRepeat {
		t.Errorf("after setting repeat = %q", got.ListMode)
	}
}

func TestBlockListMode_ReviewListAndItsResolution(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	scheduleID := setupSchedule(t, mux)
	flagged := createBlockWith(t, mux, `{"name":"Seventeen hundred"}`)
	other := createBlockWith(t, mux, `{"name":"Not flagged"}`)
	// The migration's rows, written straight to the table: one block with a
	// weekly item, one with only a continuous item (listed but no longer
	// timed), and a stale entry for a block set to once.
	for _, q := range []string{
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, active, created_at, updated_at)
		 VALUES ('w', 'default', '` + scheduleID + `', '` + flagged.ID + `', 'weekly', 1, '17:00', 1, 1, 1)`,
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, active, created_at, updated_at)
		 VALUES ('c', 'default', '` + scheduleID + `', '` + other.ID + `', 'continuous', 1, 1, 1)`,
		`INSERT INTO block_list_mode_review (tenant_id, block_id, created_at) VALUES ('default', '` + flagged.ID + `', 1)`,
		`INSERT INTO block_list_mode_review (tenant_id, block_id, created_at) VALUES ('default', '` + other.ID + `', 1)`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	rec := doRequest(t, mux, "GET", "/blocks/list-mode-review", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	var review []struct {
		BlockID   string `json:"block_id"`
		BlockName string `json:"block_name"`
		Schedules []struct {
			ScheduleID   string `json:"schedule_id"`
			ScheduleName string `json:"schedule_name"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	if len(review) != 1 || review[0].BlockID != flagged.ID || review[0].BlockName != "Seventeen hundred" ||
		len(review[0].Schedules) != 1 || review[0].Schedules[0].ScheduleID != scheduleID {
		t.Fatalf("review = %+v, want only the block with a timed item", review)
	}

	// A PUT that does not decide the mode leaves it listed; one that does
	// settles it.
	doRequest(t, mux, "PUT", "/blocks/"+flagged.ID, `{"name":"Seventeen hundred"}`)
	rec = doRequest(t, mux, "GET", "/blocks/list-mode-review", "")
	if !strings.Contains(rec.Body.String(), flagged.ID) {
		t.Errorf("a rename removed the block from the review: %s", rec.Body.String())
	}
	doRequest(t, mux, "PUT", "/blocks/"+flagged.ID, `{"name":"Seventeen hundred","list_mode":"once"}`)
	rec = doRequest(t, mux, "GET", "/blocks/list-mode-review", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("review after deciding the mode = %s, want []", rec.Body.String())
	}
}

func TestBlockListMode_LogsTheReviewAtStartup(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	scheduleID := setupSchedule(t, mux)
	b := createBlockWith(t, mux, `{"name":"Prime time"}`)
	for _, q := range []string{
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
		 VALUES ('o', 'default', '` + scheduleID + `', '` + b.ID + `', 'once', 5, 1, 1, 1)`,
		`INSERT INTO block_list_mode_review (tenant_id, block_id, created_at) VALUES ('default', '` + b.ID + `', 1)`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := loadListModeReview(ctx, db)
	if err != nil || len(entries) != 1 || entries[0].BlockName != "Prime time" {
		t.Fatalf("review = %+v, %v", entries, err)
	}
	if err := LogListModeReview(ctx, db); err != nil {
		t.Fatal(err)
	}
}
