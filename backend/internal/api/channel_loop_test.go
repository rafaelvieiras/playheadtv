package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// TestChannelLoop_CreatesScheduleAndItem is issue #103: one write makes a
// channel that loops a collection, and a repeated call is a no-op.
func TestChannelLoop_CreatesScheduleAndItem(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "m1", "Comedy")
	col := newCollection(t, mux, comedyQuery)

	body := `{"number":900,"name":"Loop","timezone":"UTC","loop":{"collection_id":"` + col + `","playback_order":"shuffle"}}`
	rec := doRequest(t, mux, "POST", "/channels", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var resp channelLoopResponse
	mustDecode(t, rec.Body.Bytes(), &resp)
	if resp.ScheduleID == "" || resp.ScheduleItemID == "" || resp.Channel.ScheduleID == nil || *resp.Channel.ScheduleID != resp.ScheduleID {
		t.Fatalf("response = %+v", resp)
	}
	var items []domain.ScheduleItem
	if err := db.Query(t.Context(), &items,
		"SELECT id, tenant_id, schedule_id, COALESCE(block_id,'') AS block_id, COALESCE(collection_id,'') AS collection_id, COALESCE(playback_order,'') AS playback_order, recurrence_type AS recurrence, days_of_week, start_time_local, start_at, active, created_at, updated_at FROM schedule_item WHERE schedule_id = ?", resp.ScheduleID,
	); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CollectionID != col || items[0].PlaybackOrder != "shuffle" || items[0].Recurrence != domain.RecurrenceContinuous {
		t.Fatalf("loop item = %+v", items)
	}

	// The same call again is a no-op that answers the same ids.
	rec = doRequest(t, mux, "POST", "/channels", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body.String())
	}
	var again channelLoopResponse
	mustDecode(t, rec.Body.Bytes(), &again)
	if again.Channel.ID != resp.Channel.ID || again.ScheduleItemID != resp.ScheduleItemID {
		t.Fatalf("repeat created something new: %+v", again)
	}

	// A channel that already has a schedule is not replaced by a loop.
	other := newCollection(t, mux, comedyQuery)
	rec = doRequest(t, mux, "PUT", "/channels/"+resp.Channel.ID,
		`{"number":900,"name":"Loop","timezone":"UTC","loop":{"collection_id":"`+other+`","playback_order":"random"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("put loop on a channel with a schedule: %d %s", rec.Code, rec.Body.String())
	}

	// Invalid input is refused, and nothing is created.
	for _, bad := range []string{
		`{"number":901,"name":"X","loop":{}}`,
		`{"number":901,"name":"X","loop":{"collection_id":"` + col + `","playback_order":"backwards"}}`,
		`{"number":901,"name":"X","loop":{"collection_id":"nope","playback_order":"shuffle"}}`,
		`{"number":901,"name":"X","schedule_id":"` + resp.ScheduleID + `","loop":{"collection_id":"` + col + `","playback_order":"shuffle"}}`,
	} {
		if rec := doRequest(t, mux, "POST", "/channels", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("bad loop %s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
}

// TestScheduleItemCollectionTarget checks the #100 validation rules over HTTP.
func TestScheduleItemCollectionTarget(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "m1", "Comedy")
	col := newCollection(t, mux, comedyQuery)
	blockID := newBlock(t, mux, `{"name":"B"}`)
	schedID := newSchedule(t, mux, "S")

	cases := map[string]string{
		"no target":           `{"recurrence_type":"continuous"}`,
		"both targets":        `{"block_id":"` + blockID + `","collection_id":"` + col + `","playback_order":"shuffle","recurrence_type":"continuous"}`,
		"collection no order": `{"collection_id":"` + col + `","recurrence_type":"continuous"}`,
		"order without coll":  `{"block_id":"` + blockID + `","playback_order":"shuffle","recurrence_type":"continuous"}`,
		"bad order":           `{"collection_id":"` + col + `","playback_order":"backwards","recurrence_type":"continuous"}`,
		"unknown collection":  `{"collection_id":"nope","playback_order":"shuffle","recurrence_type":"continuous"}`,
	}
	for name, body := range cases {
		if rec := doRequest(t, mux, "POST", "/schedules/"+schedID+"/items", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	rec := doRequest(t, mux, "POST", "/schedules/"+schedID+"/items",
		`{"collection_id":"`+col+`","playback_order":"shuffle","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var si domain.ScheduleItem
	mustDecode(t, rec.Body.Bytes(), &si)
	if si.CollectionID != col || si.PlaybackOrder != "shuffle" || si.BlockID != "" {
		t.Fatalf("item = %+v", si)
	}
	// It is served with the target it has, never with a stray block id.
	if strings.Contains(rec.Body.String(), `"block_id"`) {
		t.Errorf("a collection item leaked a block_id: %s", rec.Body.String())
	}
}
