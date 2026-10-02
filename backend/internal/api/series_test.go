package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedSeries inserts a media source and n half-hour episodes of one series
// (external id "ser-<key>"), returning the media source id.
func seedSeries(t *testing.T, db storage.DB, key, title string, n int) string {
	t.Helper()
	ctx := context.Background()
	msID := "ms-series-" + key
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'local_folder', 'seed', 'push', ?, ?)`,
		msID, DefaultTenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	for i := 1; i <= n; i++ {
		if err := db.Exec(ctx,
			`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, series_external_id, season_number, episode_number, duration_ms, synced_at, search_text)
			 VALUES (?, ?, ?, ?, 'episode', ?, ?, ?, 1, ?, 1800000, ?, ?)`,
			fmt.Sprintf("ep-%s-%d", key, i), DefaultTenantID, msID, fmt.Sprintf("ext-%s-%d", key, i),
			fmt.Sprintf("Episode %d", i), title, "ser-"+key, i, now, catalogsync.SearchText(fmt.Sprintf("Episode %d", i), &title),
		); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}
	return msID
}

func newBlock(t *testing.T, mux *http.ServeMux, body string) string {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/blocks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create block: %d %s", rec.Code, rec.Body.String())
	}
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	return b.ID
}

func TestListSeries(t *testing.T) {
	db, mux := newTestDB(t)
	// "Épico Show" is deliberate accented test data for accent-insensitive series search.
	seedSeries(t, db, "a", "Épico Show", 3)
	seedSeries(t, db, "b", "Other", 2)
	seedCatalogItem(t, db, "movie")

	rec := doRequest(t, mux, "GET", "/series", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var all []struct {
		MediaSourceID    string  `json:"media_source_id"`
		SeriesExternalID *string `json:"series_external_id"`
		SeriesTitle      string  `json:"series_title"`
		EpisodeCount     int     `json:"episode_count"`
		TotalDurationMs  int64   `json:"total_duration_ms"`
	}
	mustDecode(t, rec.Body.Bytes(), &all)
	if len(all) != 2 || all[0].SeriesTitle != "Other" || all[0].EpisodeCount != 2 || all[0].TotalDurationMs != 2*1800000 ||
		all[1].SeriesTitle != "Épico Show" || all[1].EpisodeCount != 3 || all[1].SeriesExternalID == nil || *all[1].SeriesExternalID != "ser-a" {
		t.Fatalf("unexpected series list: %+v", all)
	}

	// The series title matches without accents or case; an episode title alone does not.
	rec = doRequest(t, mux, "GET", "/series?q=EPICO", "")
	mustDecode(t, rec.Body.Bytes(), &all)
	if len(all) != 1 || all[0].SeriesTitle != "Épico Show" {
		t.Errorf("accent-insensitive q: %+v", all)
	}
	rec = doRequest(t, mux, "GET", "/series?q=episode+1", "")
	mustDecode(t, rec.Body.Bytes(), &all)
	if len(all) != 0 {
		t.Errorf("q matched an episode title, not the series: %+v", all)
	}
	rec = doRequest(t, mux, "GET", "/series?media_source_id=ms-series-b", "")
	mustDecode(t, rec.Body.Bytes(), &all)
	if len(all) != 1 || all[0].SeriesTitle != "Other" {
		t.Errorf("media_source_id filter: %+v", all)
	}
	if rec = doRequest(t, mux, "GET", "/series?limit=0", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", rec.Code)
	}
}

func TestAddSeriesBlockItem(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 4)
	movie := seedCatalogItem(t, db, "m")
	blockID := newBlock(t, mux, `{"name":"Mixed"}`)

	// A movie, then the series (default order original), then another series by title.
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, movie))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add movie: %d %s", rec.Code, rec.Body.String())
	}
	var movieItem domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &movieItem)
	if movieItem.ItemType != "catalog" || movieItem.Series != nil {
		t.Errorf("catalog item = %+v", movieItem)
	}

	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items",
		fmt.Sprintf(`{"item_type":"series","series":{"media_source_id":%q,"series_external_id":"ser-a"}}`, ms))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add series: %d %s", rec.Code, rec.Body.String())
	}
	var series domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &series)
	if series.ItemType != "series" || series.Position != 1 || series.CatalogItemID != "" || series.Series == nil ||
		series.Series.PlaybackOrder != "original" || series.Series.EpisodeCount != 4 || series.Series.TotalDurationMs != 4*1800000 {
		t.Fatalf("series item = %+v / %+v", series, series.Series)
	}
	if strings.Contains(rec.Body.String(), "seed") || strings.Contains(rec.Body.String(), "spec") {
		t.Errorf("internal fields leaked: %s", rec.Body.String())
	}

	// By title, without item_type (implied by the series object), with an order.
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items",
		fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","playback_order":"shuffle"}}`, ms))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add series by title: %d %s", rec.Code, rec.Body.String())
	}
	var byTitle domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &byTitle)
	if byTitle.Series == nil || byTitle.Series.PlaybackOrder != "shuffle" || byTitle.Series.EpisodeCount != 4 {
		t.Errorf("by-title item = %+v", byTitle.Series)
	}

	// GET lists all three, the series as one row each with its count.
	list := listItems(t, mux, blockID)
	if len(list) != 3 || list[0].ItemType != "catalog" || list[1].Series == nil || list[1].Series.EpisodeCount != 4 || list[2].Series == nil {
		t.Fatalf("list = %+v", list)
	}

	// Reordering works the same for a series item.
	rec = doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+series.ID, `{"position":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body.String())
	}
	list = listItems(t, mux, blockID)
	if list[0].ID != series.ID || list[1].ID != movieItem.ID {
		t.Errorf("reorder result: %+v", list)
	}
}

func TestAddSeriesBlockItemValidation(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 2)
	movie := seedCatalogItem(t, db, "m")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	dynamic := newBlock(t, mux, `{"name":"Dyn","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)

	post := func(block, body string) *httptest.ResponseRecorder {
		return doRequest(t, mux, "POST", "/blocks/"+block+"/items", body)
	}
	cases := []struct {
		name, block, body string
		want              int
	}{
		{"unknown type", blockID, `{"item_type":"playlist","catalog_item_id":"x"}`, 400},
		{"series without object", blockID, `{"item_type":"series"}`, 400},
		{"series with catalog id", blockID, fmt.Sprintf(`{"item_type":"series","catalog_item_id":%q,"series":{"media_source_id":%q,"series_title":"Show"}}`, movie, ms), 400},
		{"series with trim", blockID, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"},"trim_duration_ms":1000}`, ms), 400},
		{"catalog with series", blockID, fmt.Sprintf(`{"item_type":"catalog","catalog_item_id":%q,"series":{"media_source_id":%q,"series_title":"Show"}}`, movie, ms), 400},
		{"no media source", blockID, `{"series":{"series_title":"Show"}}`, 400},
		{"no identity", blockID, fmt.Sprintf(`{"series":{"media_source_id":%q}}`, ms), 400},
		{"bad order", blockID, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","playback_order":"backwards"}}`, ms), 400},
		{"slice not supported", blockID, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","slice":{"first":3}}}`, ms), 400},
		{"unknown series", blockID, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Nope"}}`, ms), 400},
		{"unknown source", blockID, `{"series":{"media_source_id":"zzz","series_title":"Show"}}`, 400},
		{"dynamic block", dynamic, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"}}`, ms), 409},
		{"unknown block", "nope", fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"}}`, ms), 404},
	}
	for _, c := range cases {
		if rec := post(c.block, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if list := listItems(t, mux, blockID); len(list) != 0 {
		t.Errorf("a rejected request left items behind: %+v", list)
	}
	// A null slice is the same as none.
	rec := post(blockID, fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","slice":null}}`, ms))
	if rec.Code != http.StatusCreated {
		t.Errorf("null slice: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSeriesBlockItem(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 3)
	movie := seedCatalogItem(t, db, "m")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, movie))
	var movieItem domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &movieItem)
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"}}`, ms))
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	base := "/blocks/" + blockID + "/items/"

	// Give the item some materialized state to prove an order change restarts it.
	ctx := context.Background()
	chanID := "chan-x"
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx, "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES (?, ?, 1, 'c', 'UTC', ?, ?)", chanID, DefaultTenantID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, ?, ?, 5)", DefaultTenantID, chanID, item.ID); err != nil {
		t.Fatal(err)
	}
	for i, end := range []int64{now - 1000, now + 3600000} { // one aired, one to come
		if err := db.Exec(ctx, `INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at, source_block_item_id, series_cycle)
			VALUES (?, ?, ?, 'program', 'ep-a-1', ?, ?, ?, ?, 0)`, fmt.Sprintf("e%d", i), DefaultTenantID, chanID, end-1800000, end, now, item.ID); err != nil {
			t.Fatal(err)
		}
	}

	rec = doRequest(t, mux, "PUT", base+item.ID, `{"playback_order":"release_date"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change order: %d %s", rec.Code, rec.Body.String())
	}
	var got domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.Series == nil || got.Series.PlaybackOrder != "release_date" || got.Series.EpisodeCount != 3 {
		t.Errorf("updated item = %+v", got.Series)
	}
	var origins []int64
	db.Query(ctx, &origins, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID)
	if len(origins) != 1 || origins[0] < now {
		t.Errorf("cursor origin = %v, want moved to now", origins)
	}
	var remaining []string
	db.Query(ctx, &remaining, "SELECT id FROM epg_entry WHERE source_block_item_id = ?", item.ID)
	if len(remaining) != 1 || remaining[0] != "e0" {
		t.Errorf("entries after order change = %v, want only the aired one (history)", remaining)
	}

	// Setting the same order again leaves the cursor alone.
	db.Exec(ctx, "UPDATE series_cursor SET origin_ms = 7")
	if rec = doRequest(t, mux, "PUT", base+item.ID, `{"playback_order":"release_date"}`); rec.Code != http.StatusOK {
		t.Fatalf("same order: %d", rec.Code)
	}
	db.Query(ctx, &origins, "SELECT origin_ms FROM series_cursor")
	if origins[0] != 7 {
		t.Errorf("an unchanged order reset the cursor: %v", origins)
	}

	for name, c := range map[string]struct{ id, body string }{
		"bad order":        {item.ID, `{"playback_order":"sideways"}`},
		"trim on series":   {item.ID, `{"trim_duration_ms":1000}`},
		"order on catalog": {movieItem.ID, `{"playback_order":"shuffle"}`},
		"nothing":          {item.ID, `{}`},
	} {
		if rec := doRequest(t, mux, "PUT", base+c.id, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}

	// Delete works for a series item.
	if rec = doRequest(t, mux, "DELETE", base+item.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if list := listItems(t, mux, blockID); len(list) != 1 || list[0].ID != movieItem.ID {
		t.Errorf("after delete: %+v", list)
	}
	var cursors []int
	db.Query(ctx, &cursors, "SELECT COUNT(*) FROM series_cursor")
	if cursors[0] != 0 {
		t.Errorf("series_cursor outlived its item")
	}
}

func TestSeriesItemReflectsCatalogChanges(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 2)
	blockID := newBlock(t, mux, `{"name":"B"}`)
	doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"series":{"media_source_id":%q,"series_external_id":"ser-a"}}`, ms))
	if err := db.Exec(context.Background(), "DELETE FROM catalog_item WHERE id = 'ep-a-1'"); err != nil {
		t.Fatal(err)
	}
	list := listItems(t, mux, blockID)
	if len(list) != 1 || list[0].Series == nil || list[0].Series.EpisodeCount != 1 {
		t.Fatalf("episode count did not follow the catalog: %+v", list)
	}
}

func TestSeriesBlockItemAmount(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 3)
	movie := seedCatalogItem(t, db, "m")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	dynamic := newBlock(t, mux, `{"name":"Dyn","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	post := func(block, amount string) *httptest.ResponseRecorder {
		extra := ""
		if amount != "" {
			extra = `,"amount":` + amount
		}
		return doRequest(t, mux, "POST", "/blocks/"+block+"/items",
			fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show"%s}}`, ms, extra))
	}
	type want struct {
		mode  string
		count int
	}
	for name, c := range map[string]struct {
		amount string
		want   want
	}{
		"nothing selected defaults to one episode": {"", want{"count", 1}},
		"empty object defaults to one":             {`{}`, want{"count", 1}},
		"count":                                    {`{"mode":"count","count":3}`, want{"count", 3}},
		"count without mode":                       {`{"count":2}`, want{"count", 2}},
		"count mode without count is one":          {`{"mode":"count"}`, want{"count", 1}},
		"slot end":                                 {`{"mode":"slot_end"}`, want{"slot_end", 0}},
		"series end":                               {`{"mode":"series_end"}`, want{"series_end", 0}},
	} {
		rec := post(blockID, c.amount)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		var item domain.BlockItem
		mustDecode(t, rec.Body.Bytes(), &item)
		if item.Series == nil || item.Series.Amount.Mode != c.want.mode || item.Series.Amount.Count != c.want.count {
			t.Errorf("%s: amount = %+v, want %+v", name, item.Series, c.want)
		}
	}

	for name, amount := range map[string]string{
		"zero count":              `{"mode":"count","count":0}`,
		"negative count":          `{"mode":"count","count":-2}`,
		"unknown mode":            `{"mode":"forever"}`,
		"count with slot end":     `{"mode":"slot_end","count":2}`,
		"count with series end":   `{"mode":"series_end","count":1}`,
		"count not an integer":    `{"mode":"count","count":1.5}`,
		"count of the wrong type": `{"mode":"count","count":"2"}`,
	} {
		if rec := post(blockID, amount); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := post(dynamic, `{"mode":"slot_end"}`); rec.Code != http.StatusConflict {
		t.Errorf("dynamic block: %d, want 409", rec.Code)
	}

	// Update: change the amount, keep the cursor; reject on a catalog item and bad values.
	rec := post(blockID, `{"mode":"count","count":2}`)
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, movie))
	var movieItem domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &movieItem)
	ctx := context.Background()
	if err := db.Exec(ctx, "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('chan-x', ?, 1, 'c', 'UTC', 1, 1)", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, 'chan-x', ?, 5)", DefaultTenantID, item.ID); err != nil {
		t.Fatal(err)
	}
	base := "/blocks/" + blockID + "/items/"
	rec = doRequest(t, mux, "PUT", base+item.ID, `{"amount":{"mode":"series_end"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update amount: %d %s", rec.Code, rec.Body.String())
	}
	var got domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.Series == nil || got.Series.Amount.Mode != "series_end" || got.Series.PlaybackOrder != "original" {
		t.Errorf("updated item = %+v", got.Series)
	}
	rec = doRequest(t, mux, "PUT", base+item.ID, `{"amount":{"count":4},"playback_order":"shuffle"}`)
	mustDecode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Series.Amount.Mode != "count" || got.Series.Amount.Count != 4 || got.Series.PlaybackOrder != "shuffle" {
		t.Errorf("amount and order together: %d %+v", rec.Code, got.Series)
	}
	var origins []int64
	db.Query(ctx, &origins, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID)
	if len(origins) != 1 || origins[0] < 6 {
		t.Errorf("cursor origin = %v, want moved by the order change", origins)
	}
	db.Exec(ctx, "UPDATE series_cursor SET origin_ms = 7")
	if rec = doRequest(t, mux, "PUT", base+item.ID, `{"amount":{"mode":"slot_end"}}`); rec.Code != http.StatusOK {
		t.Fatalf("amount only: %d", rec.Code)
	}
	db.Query(ctx, &origins, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID)
	if origins[0] != 7 {
		t.Errorf("an amount change reset the cursor: %v", origins)
	}
	for name, c := range map[string]struct{ id, body string }{
		"amount on catalog": {movieItem.ID, `{"amount":{"mode":"slot_end"}}`},
		"bad mode":          {item.ID, `{"amount":{"mode":"x"}}`},
		"zero count":        {item.ID, `{"amount":{"mode":"count","count":0}}`},
		"count with end":    {item.ID, `{"amount":{"mode":"series_end","count":3}}`},
	} {
		if rec := doRequest(t, mux, "PUT", base+c.id, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := doRequest(t, mux, "PUT", "/blocks/"+dynamic+"/items/x", `{"amount":{"mode":"slot_end"}}`); rec.Code != http.StatusConflict {
		t.Errorf("update on dynamic block: %d, want 409", rec.Code)
	}

	// A series item stored before amounts existed reads as slot_end.
	legacy := fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show","playback_order":"original","seed":1}`, ms)
	if err := db.Exec(ctx, "INSERT INTO block_item (id, block_id, position, item_type, spec) VALUES ('legacy', ?, 99, 'series', ?)", blockID, legacy); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range listItems(t, mux, blockID) {
		if it.ID == "legacy" {
			found = true
			if it.Series == nil || it.Series.Amount.Mode != "slot_end" {
				t.Errorf("legacy item amount = %+v, want slot_end", it.Series)
			}
		}
	}
	if !found {
		t.Error("legacy item not listed")
	}
}
