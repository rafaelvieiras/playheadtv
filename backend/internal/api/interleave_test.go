package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestCriteriaItemInterleavedOrder(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c", "Comedy")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	col := newCollection(t, mux, comedyQuery)
	add := func(order, interleave string) (*domain.BlockItem, int, string) {
		body := `{"item_type":"criteria","criteria":{"collection_id":"` + col + `","playback_order":"` + order + `","amount":{"mode":"all"}`
		if interleave != "" {
			body += `,"interleave":` + interleave
		}
		rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", body+`}}`)
		var item domain.BlockItem
		if rec.Code == http.StatusCreated {
			mustDecode(t, rec.Body.Bytes(), &item)
		}
		return &item, rec.Code, rec.Body.String()
	}

	// Defaults: one per round, restart on exhaustion, series by title.
	item, code, body := add("interleaved", "")
	il := item.Criteria.Interleave
	if code != http.StatusCreated || item.Criteria.PlaybackOrder != "interleaved" || il == nil || il.PerRound != 1 || il.OnExhaustion != "restart" || il.SeriesOrder != "title" {
		t.Fatalf("defaults: %d %s", code, body)
	}
	if strings.Contains(body, "seed") {
		t.Errorf("seed served: %s", body)
	}
	item, code, body = add("interleaved", `{"per_round":3,"on_exhaustion":"drop","series_order":"shuffle"}`)
	il = item.Criteria.Interleave
	if code != http.StatusCreated || il == nil || il.PerRound != 3 || il.OnExhaustion != "drop" || il.SeriesOrder != "shuffle" {
		t.Fatalf("explicit: %d %s", code, body)
	}
	if list := listItems(t, mux, blockID); len(list) != 2 || list[1].Criteria.Interleave == nil || list[1].Criteria.Interleave.PerRound != 3 {
		t.Fatalf("list = %+v", list)
	}
	// A non-interleaved item has no interleave view.
	if item, code, body = add("title", ""); code != http.StatusCreated || item.Criteria.Interleave != nil {
		t.Fatalf("title: %d %s", code, body)
	}

	for name, tc := range map[string]struct{ order, il string }{
		"zero per round":        {"interleaved", `{"per_round":0}`},
		"negative per round":    {"interleaved", `{"per_round":-2}`},
		"unknown exhaustion":    {"interleaved", `{"on_exhaustion":"loop"}`},
		"unknown series order":  {"interleaved", `{"series_order":"original"}`},
		"interleaved as series": {"interleaved", `{"series_order":"interleaved"}`},
		"interleave with title": {"title", `{"per_round":2}`},
	} {
		if _, code, body := add(tc.order, tc.il); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}
	// A series item cannot be interleaved.
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", `{"series":{"media_source_id":"x","series_title":"y","playback_order":"interleaved"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("series interleaved: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateCriteriaItemInterleave(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c", "Comedy")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(newCollection(t, mux, comedyQuery), "title", `{"mode":"all"}`))
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	path := "/blocks/" + blockID + "/items/" + item.ID
	ctx := context.Background()
	if err := db.Exec(ctx, "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('ch', ?, 1, 'C', 'UTC', 1, 1)", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, 'ch', ?, 7)", DefaultTenantID, item.ID); err != nil {
		t.Fatal(err)
	}
	origin := func() int64 {
		var o []int64
		if err := db.Query(ctx, &o, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID); err != nil || len(o) != 1 {
			t.Fatalf("cursor: %v %v", o, err)
		}
		return o[0]
	}
	put := func(body string) (*domain.BlockItem, int, string) {
		rec := doRequest(t, mux, "PUT", path, body)
		var out domain.BlockItem
		if rec.Code == http.StatusOK {
			mustDecode(t, rec.Body.Bytes(), &out)
		}
		return &out, rec.Code, rec.Body.String()
	}

	// interleave parameters need the interleaved order.
	if _, code, body := put(`{"interleave":{"per_round":2}}`); code != http.StatusBadRequest {
		t.Fatalf("interleave on a title item: %d %s", code, body)
	}
	got, code, body := put(`{"playback_order":"interleaved","interleave":{"per_round":2,"on_exhaustion":"drop"}}`)
	if code != 200 || got.Criteria.PlaybackOrder != "interleaved" || got.Criteria.Interleave.PerRound != 2 || got.Criteria.Interleave.OnExhaustion != "drop" {
		t.Fatalf("switch: %d %s", code, body)
	}
	if origin() == 7 {
		t.Fatal("switching to interleaved did not restart the cursor")
	}
	reset := func() {
		if err := db.Exec(ctx, "UPDATE series_cursor SET origin_ms = 7"); err != nil {
			t.Fatal(err)
		}
	}
	// on_exhaustion keeps the cursor; per_round and series_order restart it.
	reset()
	got, code, body = put(`{"interleave":{"on_exhaustion":"restart"}}`)
	if code != 200 || got.Criteria.Interleave.OnExhaustion != "restart" || got.Criteria.Interleave.PerRound != 2 || origin() != 7 {
		t.Fatalf("on_exhaustion: %d %s origin=%d", code, body, origin())
	}
	got, code, body = put(`{"interleave":{"per_round":4}}`)
	if code != 200 || got.Criteria.Interleave.PerRound != 4 || origin() == 7 {
		t.Fatalf("per_round: %d %s", code, body)
	}
	reset()
	if _, code, body = put(`{"interleave":{"series_order":"random"}}`); code != 200 || origin() == 7 {
		t.Fatalf("series_order: %d %s", code, body)
	}
	for name, b := range map[string]string{
		"zero per round": `{"interleave":{"per_round":0}}`,
		"bad exhaustion": `{"interleave":{"on_exhaustion":"x"}}`,
		"bad series":     `{"interleave":{"series_order":"x"}}`,
	} {
		if _, code, body := put(b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// Back to title drops the parameters.
	got, code, body = put(`{"playback_order":"title"}`)
	if code != 200 || got.Criteria.Interleave != nil {
		t.Fatalf("back to title: %d %s", code, body)
	}
}

func TestDynamicBlockPlaybackSettings(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "1", "Comedy")
	criteria := `"` + newCollection(t, mux, `{"version":1,"all":[{"field":"genre","op":"equals","value":"Comedy"}]}`) + `"`
	create := func(body string) (domain.Block, int, string) {
		rec := doRequest(t, mux, "POST", "/blocks", body)
		var b domain.Block
		if rec.Code == http.StatusCreated {
			mustDecode(t, rec.Body.Bytes(), &b)
		}
		return b, rec.Code, rec.Body.String()
	}

	// Backwards compatible: no playback settings means title, nothing else.
	b, code, body := create(`{"name":"Plain","collection_id":` + criteria + `}`)
	if code != http.StatusCreated || b.PlaybackOrder != "title" || b.Interleave != nil || !strings.Contains(body, `"playback_order":"title"`) || !strings.Contains(body, `"interleave":null`) {
		t.Fatalf("default: %d %s", code, body)
	}
	b, code, body = create(`{"name":"Inter","collection_id":` + criteria + `,"playback_order":"interleaved"}`)
	if code != http.StatusCreated || b.PlaybackOrder != "interleaved" || b.Interleave == nil || *b.Interleave != (domain.Interleave{PerRound: 1, OnExhaustion: "restart", SeriesOrder: "title"}) {
		t.Fatalf("interleaved defaults: %d %s", code, body)
	}
	if strings.Contains(body, "seed") {
		t.Errorf("seed served: %s", body)
	}
	inter := b
	rec := doRequest(t, mux, "GET", "/blocks/"+inter.ID, "")
	var got domain.Block
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.PlaybackOrder != "interleaved" || got.Interleave == nil || got.Interleave.PerRound != 1 {
		t.Fatalf("GET: %s", rec.Body.String())
	}
	rec = doRequest(t, mux, "GET", "/blocks", "")
	var list []domain.Block
	mustDecode(t, rec.Body.Bytes(), &list)
	seen := map[string]bool{}
	for _, l := range list {
		seen[l.Name+":"+l.PlaybackOrder] = true
		if l.Name == "Inter" && l.Interleave == nil {
			t.Error("list lost the interleave parameters")
		}
	}
	if !seen["Plain:title"] || !seen["Inter:interleaved"] {
		t.Errorf("list = %v", seen)
	}

	for name, body := range map[string]string{
		"interleaved on a manual block": `{"name":"M","playback_order":"interleaved"}`,
		"unknown order":                 `{"name":"X","collection_id":` + criteria + `,"playback_order":"shuffle"}`,
		"interleave with title":         `{"name":"X","collection_id":` + criteria + `,"interleave":{"per_round":2}}`,
		"zero per round":                `{"name":"X","collection_id":` + criteria + `,"playback_order":"interleaved","interleave":{"per_round":0}}`,
		"bad exhaustion":                `{"name":"X","collection_id":` + criteria + `,"playback_order":"interleaved","interleave":{"on_exhaustion":"x"}}`,
		"bad series order":              `{"name":"X","collection_id":` + criteria + `,"playback_order":"interleaved","interleave":{"series_order":"x"}}`,
	} {
		if _, code, body := create(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}

	// PUT: changing the parameters; per_round restarts the block cursor,
	// on_exhaustion does not; the seed survives a plain update.
	ctx := context.Background()
	if err := db.Exec(ctx, "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('ch', ?, 1, 'C', 'UTC', 1, 1)", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO block_cursor (tenant_id, channel_id, block_id, origin_ms) VALUES (?, 'ch', ?, 7)", DefaultTenantID, inter.ID); err != nil {
		t.Fatal(err)
	}
	origin := func() int64 {
		var o []int64
		if err := db.Query(ctx, &o, "SELECT origin_ms FROM block_cursor WHERE block_id = ?", inter.ID); err != nil || len(o) != 1 {
			t.Fatalf("cursor: %v %v", o, err)
		}
		return o[0]
	}
	seed := func() int64 {
		var s []int64
		if err := db.Query(ctx, &s, "SELECT playback_seed FROM block WHERE id = ?", inter.ID); err != nil || len(s) != 1 {
			t.Fatalf("seed: %v %v", s, err)
		}
		return s[0]
	}
	if seed() == 0 {
		t.Fatal("interleaved block stored no seed")
	}
	firstSeed := seed()
	put := func(body string) (domain.Block, int, string) {
		rec := doRequest(t, mux, "PUT", "/blocks/"+inter.ID, body)
		var b domain.Block
		if rec.Code == http.StatusOK {
			mustDecode(t, rec.Body.Bytes(), &b)
		}
		return b, rec.Code, rec.Body.String()
	}
	b, code, body = put(`{"name":"Inter","collection_id":` + criteria + `,"playback_order":"interleaved","interleave":{"per_round":1,"on_exhaustion":"drop"}}`)
	if code != 200 || b.Interleave.OnExhaustion != "drop" || origin() != 7 || seed() != firstSeed {
		t.Fatalf("on_exhaustion: %d %s origin=%d", code, body, origin())
	}
	b, code, body = put(`{"name":"Inter","collection_id":` + criteria + `,"playback_order":"interleaved","interleave":{"per_round":3,"on_exhaustion":"drop"}}`)
	if code != 200 || b.Interleave.PerRound != 3 || origin() == 7 || seed() != firstSeed {
		t.Fatalf("per_round: %d %s", code, body)
	}
	// A full replace: omitting the settings returns to the default title order.
	b, code, body = put(`{"name":"Inter","collection_id":` + criteria + `}`)
	if code != 200 || b.PlaybackOrder != "title" || b.Interleave != nil {
		t.Fatalf("revert: %d %s", code, body)
	}
	if _, code, body = put(`{"name":"Inter","playback_order":"interleaved"}`); code != http.StatusBadRequest {
		t.Fatalf("interleaved without a collection: %d %s", code, body)
	}
}

func TestPreviewEpgReflectsInterleavedBlock(t *testing.T) {
	// The guide dry-run goes through the materializer's own loader; this checks
	// the API-created block is what the materializer reads (settings + seed
	// wiring), not the walk itself (see the materializer tests).
	db, mux := newTestDB(t)
	seedSeries(t, db, "A", "Alpha", 3)
	seedSeries(t, db, "B", "Bravo", 2)
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Cartoons","collection_id":"`+newCollection(t, mux, `{"version":1,"all":[{"field":"kind","op":"equals","value":"episode"}]}`)+`","playback_order":"interleaved"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("block: %d %s", rec.Code, rec.Body.String())
	}
	var block domain.Block
	mustDecode(t, rec.Body.Bytes(), &block)
	rec = doRequest(t, mux, "POST", "/schedules", `{"name":"S"}`)
	var sched domain.Schedule
	mustDecode(t, rec.Body.Bytes(), &sched)
	rec = doRequest(t, mux, "POST", "/schedules/"+sched.ID+"/items", `{"block_id":"`+block.ID+`","recurrence_type":"continuous"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("schedule item: %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C","schedule_id":"`+sched.ID+`"}`)
	var ch domain.Channel
	mustDecode(t, rec.Body.Bytes(), &ch)
	rec = doRequest(t, mux, "GET", "/channels/"+ch.ID+"/epg-preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var entries []struct {
		CatalogItemID string `json:"catalog_item_id"`
	}
	mustDecode(t, rec.Body.Bytes(), &entries)
	if len(entries) < 2 { // the test materializer's window is one hour
		t.Fatalf("preview has %d entries", len(entries))
	}
	var order []string
	for _, e := range entries[:2] {
		order = append(order, e.CatalogItemID)
	}
	// Series A (3 episodes) and B (2): a1 then b1, not a1 then a2 (title order).
	want := []string{"ep-A-1", "ep-B-1"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("preview order = %v, want %v", order, want)
	}
}
