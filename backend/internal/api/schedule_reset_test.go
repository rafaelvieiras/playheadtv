package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedRotation inserts one source, two 1h movies and a dynamic block
// rotating through them, and returns the block id.
func seedRotation(t *testing.T, db storage.DB) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		if err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
	      VALUES ('src', ?, 'local_folder', 'Local', 'push', ?, ?)`, DefaultTenantID, now, now)
	for _, id := range []string{"m0", "m1"} {
		exec(`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		      VALUES (?, ?, 'src', ?, 'movie', ?, 3600000, ?)`, id, DefaultTenantID, id, id, now)
	}
	exec("INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('rot', ?, 'Rot', '{}', ?, ?)", DefaultTenantID, now, now)
	exec("INSERT INTO block_item (id, block_id, position, catalog_item_id) VALUES ('bi0', 'rot', 0, 'm0')")
	exec("INSERT INTO block_item (id, block_id, position, catalog_item_id) VALUES ('bi1', 'rot', 1, 'm1')")
	return "rot"
}

func futureEntries(t *testing.T, db storage.DB, channelID string) []domain.EpgEntry {
	t.Helper()
	var entries []domain.EpgEntry
	err := db.Query(context.Background(), &entries,
		"SELECT id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at, rotation_next FROM epg_entry WHERE channel_id = ? AND end_at > ? ORDER BY start_at",
		channelID, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func mustDecode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestScheduleEditsResetAffectedChannels(t *testing.T) {
	db, mux := newTestDB(t)
	blockID := seedRotation(t, db)
	scheduleID := setupSchedule(t, mux)

	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C1","schedule_id":"`+scheduleID+`"}`)
	var ch domain.Channel
	mustDecode(t, rec.Body.Bytes(), &ch)
	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Fatalf("empty schedule produced %d entries", n)
	}

	// Create: the channel is regenerated synchronously, no manual reset.
	rec = doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", `{"block_id":"`+blockID+`","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", rec.Code, rec.Body.String())
	}
	var item domain.ScheduleItem
	mustDecode(t, rec.Body.Bytes(), &item)
	first := futureEntries(t, db, ch.ID)
	if len(first) == 0 {
		t.Fatal("creating a continuous item did not regenerate the channel")
	}
	if first[0].StartAt > time.Now().UnixMilli()+1000 {
		t.Errorf("hole right after the edit: first program starts %v", time.UnixMilli(first[0].StartAt))
	}

	// Update: reset discards even the airing entry (fresh ids, rotation restarts).
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, `{"block_id":"`+blockID+`","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update item: %d %s", rec.Code, rec.Body.String())
	}
	second := futureEntries(t, db, ch.ID)
	if len(second) == 0 || second[0].ID == first[0].ID {
		t.Fatalf("updating an item did not reset the grid: %+v", second)
	}

	// Deactivate through update: the grid empties.
	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, `{"block_id":"`+blockID+`","recurrence_type":"continuous","active":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate item: %d", rec.Code)
	}
	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Fatalf("deactivated item left %d entries", n)
	}

	rec = doRequest(t, mux, "PUT", "/schedule-items/"+item.ID, `{"block_id":"`+blockID+`","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusOK || len(futureEntries(t, db, ch.ID)) == 0 {
		t.Fatalf("reactivate failed: %d", rec.Code)
	}

	// Delete: the grid empties right away.
	rec = doRequest(t, mux, "DELETE", "/schedule-items/"+item.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete item: %d", rec.Code)
	}
	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Fatalf("deleting the item left %d entries", n)
	}
}

func TestChannelScheduleAssignmentResetsGrid(t *testing.T) {
	db, mux := newTestDB(t)
	blockID := seedRotation(t, db)
	scheduleID := setupSchedule(t, mux)
	rec := doRequest(t, mux, "POST", "/schedules/"+scheduleID+"/items", `{"block_id":"`+blockID+`","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create item: %d", rec.Code)
	}
	rec = doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C1"}`)
	var ch domain.Channel
	mustDecode(t, rec.Body.Bytes(), &ch)
	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Fatalf("unscheduled channel has %d entries", n)
	}

	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"C1","schedule_id":"`+scheduleID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body.String())
	}
	assigned := futureEntries(t, db, ch.ID)
	if len(assigned) == 0 {
		t.Fatal("assigning a schedule did not regenerate the channel")
	}

	// A rename (no schedule change) leaves the grid alone.
	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"Renamed","schedule_id":"`+scheduleID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d", rec.Code)
	}
	if again := futureEntries(t, db, ch.ID); again[0].ID != assigned[0].ID {
		t.Error("a rename reset the grid")
	}

	// Clearing the schedule resets too.
	rec = doRequest(t, mux, "PUT", "/channels/"+ch.ID, `{"number":1,"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Fatalf("clearing the schedule left %d entries", n)
	}
}
