package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedSpecial adds a season 0 episode to the series seedSeries made.
func seedSpecial(t *testing.T, db storage.DB, key, title string, ms string) {
	t.Helper()
	if err := db.Exec(context.Background(),
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, series_external_id, season_number, episode_number, duration_ms, synced_at, search_text)
		 VALUES (?, ?, ?, ?, 'episode', 'Special', ?, ?, 0, 1, 600000, ?, 'special')`,
		"ep-"+key+"-sp", DefaultTenantID, ms, "ext-"+key+"-sp", title, "ser-"+key, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed special: %v", err)
	}
}

func TestSeriesSliceCreateReadAndValidation(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 10)
	seedSpecial(t, db, "a", "Show", ms)
	blockID := newBlock(t, mux, `{"name":"B"}`)

	post := func(slice string) (int, domain.BlockItem, string) {
		rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","slice":%s}}`, ms, slice))
		var it domain.BlockItem
		if rec.Code == http.StatusCreated {
			mustDecode(t, rec.Body.Bytes(), &it)
		}
		return rec.Code, it, rec.Body.String()
	}

	code, it, body := post(`{"exclude_specials":true,"skip_first":2,"first":5}`)
	if code != http.StatusCreated {
		t.Fatalf("create sliced: %d %s", code, body)
	}
	if it.Series == nil || it.Series.EpisodeCount != 5 || it.Series.TotalDurationMs != 5*1800000 ||
		it.Series.Slice == nil || !it.Series.Slice.ExcludeSpecials || *it.Series.Slice.First != 5 {
		t.Fatalf("view = %+v / %+v", it.Series, it.Series.Slice)
	}
	if strings.Contains(body, "seed") {
		t.Errorf("response leaks a seed: %s", body)
	}
	// The slice is read back from the block's item list too.
	if list := listItems(t, mux, blockID); len(list) != 1 || list[0].Series.EpisodeCount != 5 {
		t.Fatalf("list = %+v", list)
	}

	// No slice or an empty one is a whole series (season 0 left out by the block default: 10 of 11).
	if _, it, _ := post(`{}`); it.Series == nil || it.Series.Slice != nil || it.Series.EpisodeCount != 10 {
		t.Errorf("empty slice view = %+v", it.Series)
	}
	// A sample stores a seed, never served, and the count is fixed.
	if code, it, body := post(`{"sample":4}`); code != http.StatusCreated || it.Series.EpisodeCount != 4 || strings.Contains(body, "seed") {
		t.Errorf("sample: %d %s", code, body)
	}

	for name, slice := range map[string]string{
		"first and last":         `{"first":1,"last":1}`,
		"zero first":             `{"first":0}`,
		"negative skip":          `{"skip_first":-2}`,
		"season range reversed":  `{"season_from":3,"season_to":1}`,
		"episode range reversed": `{"episode_from":5,"episode_to":2}`,
		"specials contradiction": `{"seasons":[0,1],"exclude_specials":true}`,
		"matches nothing":        `{"seasons":[7]}`,
		"skips everything":       `{"skip_first":50}`,
		"wrong type":             `{"first":"three"}`,
	} {
		if code, _, body := post(slice); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}
	if list := listItems(t, mux, blockID); len(list) != 3 {
		t.Errorf("rejected requests left items behind: %d items", len(list))
	}
}

func TestSeriesSliceUpdateRestartsCursorAndKeepsSampleSeed(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 10)
	blockID := newBlock(t, mux, `{"name":"B"}`)
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"series":{"media_source_id":%q,"series_title":"Show","slice":{"sample":3}}}`, ms))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	url := "/blocks/" + blockID + "/items/" + item.ID

	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx, "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c1', ?, 1, 'c', 'UTC', ?, ?)", DefaultTenantID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, 'c1', ?, 5)", DefaultTenantID, item.ID); err != nil {
		t.Fatal(err)
	}
	origin := func() int64 {
		var o []int64
		if err := db.Query(ctx, &o, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID); err != nil || len(o) != 1 {
			t.Fatalf("cursor: %v %v", o, err)
		}
		return o[0]
	}
	seedOf := func() int64 {
		var specs []string
		if err := db.Query(ctx, &specs, "SELECT spec FROM block_item WHERE id = ?", item.ID); err != nil || len(specs) != 1 {
			t.Fatal(err)
		}
		var s domain.SeriesSpec
		if err := json.Unmarshal([]byte(specs[0]), &s); err != nil {
			t.Fatal(err)
		}
		return s.SliceSeed
	}
	seed := seedOf()
	if seed == 0 {
		t.Fatal("sampling slice stored no seed")
	}

	// Same slice again: nothing changes, the cursor is kept.
	if rec := doRequest(t, mux, "PUT", url, `{"slice":{"sample":3}}`); rec.Code != http.StatusOK || origin() != 5 {
		t.Fatalf("identical slice: %d %s origin=%d", rec.Code, rec.Body.String(), origin())
	}
	// Narrowing it while still sampling keeps the seed and restarts the cursor.
	rec = doRequest(t, mux, "PUT", url, `{"slice":{"sample":3,"seasons":[1],"first":8}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if origin() == 5 {
		t.Errorf("a changed slice kept the cursor origin")
	}
	if seedOf() != seed {
		t.Errorf("sample seed changed while still sampling")
	}
	// Dropping the sample drops the seed; an empty object clears the slice.
	if rec := doRequest(t, mux, "PUT", url, `{"slice":{"first":2}}`); rec.Code != http.StatusOK || seedOf() != 0 {
		t.Fatalf("drop sample: %d %s seed=%d", rec.Code, rec.Body.String(), seedOf())
	}
	rec = doRequest(t, mux, "PUT", url, `{"slice":{}}`)
	var got domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Series.Slice != nil || got.Series.EpisodeCount != 10 {
		t.Fatalf("clear: %d %+v", rec.Code, got.Series)
	}
	// Invalid updates change nothing.
	if rec := doRequest(t, mux, "PUT", url, `{"slice":{"first":1,"last":1}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("contradictory update = %d", rec.Code)
	}
	if rec := doRequest(t, mux, "PUT", url, `{"slice":{"seasons":[9]}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty-result update = %d", rec.Code)
	}
}

func TestSeriesSliceOnlyValidForSeriesItems(t *testing.T) {
	db, mux := newTestDB(t)
	movie := seedCatalogItem(t, db, "m")
	blockID, items := newManualBlockWithItems(t, mux, movie)
	rec := doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+items[0].ID, `{"slice":{"first":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("slice on a catalog item = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestSeriesSlicePreview(t *testing.T) {
	db, mux := newTestDB(t)
	ms := seedSeries(t, db, "a", "Show", 10)
	seedSpecial(t, db, "a", "Show", ms)
	other := seedSeries(t, db, "b", "Other", 3)

	preview := func(body string) (int, map[string]int64) {
		rec := doRequest(t, mux, "POST", "/series/slice-preview", body)
		var out map[string]int64
		if rec.Code == http.StatusOK {
			mustDecode(t, rec.Body.Bytes(), &out)
		}
		return rec.Code, out
	}
	code, out := preview(fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show","slice":{"exclude_specials":true,"last":4}}`, ms))
	if code != 200 || out["episode_count"] != 4 || out["series_episode_count"] != 11 || out["total_duration_ms"] != 4*1800000 {
		t.Fatalf("preview = %d %v", code, out)
	}
	// No slice: the whole series, season 0 out by default. A slice matching nothing is an answer, not an error.
	if code, out := preview(fmt.Sprintf(`{"media_source_id":%q,"series_external_id":"ser-a"}`, ms)); code != 200 || out["episode_count"] != 10 {
		t.Errorf("no slice = %d %v", code, out)
	}
	if code, out := preview(fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show","slice":{"seasons":[5]}}`, ms)); code != 200 || out["episode_count"] != 0 || out["series_episode_count"] != 11 {
		t.Errorf("empty result = %d %v", code, out)
	}
	// The series is looked up within its own media source.
	if code, out := preview(fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show"}`, other)); code != 200 || out["series_episode_count"] != 0 {
		t.Errorf("other source = %d %v", code, out)
	}
	for name, body := range map[string]string{
		"invalid slice":   fmt.Sprintf(`{"media_source_id":%q,"series_title":"Show","slice":{"first":1,"last":1}}`, ms),
		"no media source": `{"series_title":"Show"}`,
		"no identity":     fmt.Sprintf(`{"media_source_id":%q}`, ms),
		"bad json":        `{`,
	} {
		if code, _ := preview(body); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
}
