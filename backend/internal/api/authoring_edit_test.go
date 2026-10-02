package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func newManualBlockWithItems(t *testing.T, mux *http.ServeMux, itemIDs ...string) (string, []domain.BlockItem) {
	t.Helper()
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"B"}`)
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	var out []domain.BlockItem
	for _, id := range itemIDs {
		rec = doRequest(t, mux, "POST", "/blocks/"+b.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, id))
		var bi domain.BlockItem
		mustDecode(t, rec.Body.Bytes(), &bi)
		out = append(out, bi)
	}
	return b.ID, out
}

func listItems(t *testing.T, mux *http.ServeMux, blockID string) []domain.BlockItem {
	t.Helper()
	rec := doRequest(t, mux, "GET", "/blocks/"+blockID+"/items", "")
	var items []domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &items)
	return items
}

func TestUpdateBlockItemReorderAndTrim(t *testing.T) {
	db, mux := newTestDB(t)
	a, b, c := seedCatalogItem(t, db, "a"), seedCatalogItem(t, db, "b"), seedCatalogItem(t, db, "c")
	blockID, items := newManualBlockWithItems(t, mux, a, b, c)

	// Gap from a delete: positions become 0,2; reorder must renormalize.
	doRequest(t, mux, "DELETE", "/blocks/"+blockID+"/items/"+items[1].ID, "")

	rec := doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+items[2].ID, `{"position":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body.String())
	}
	var got domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &got)
	if got.Position != 0 {
		t.Errorf("returned position = %d, want 0", got.Position)
	}
	list := listItems(t, mux, blockID)
	if len(list) != 2 || list[0].ID != items[2].ID || list[0].Position != 0 || list[1].ID != items[0].ID || list[1].Position != 1 {
		t.Fatalf("unexpected order: %+v", list)
	}

	// Move to the middle of three items.
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, b))
	var added domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &added)
	rec = doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+added.ID, `{"position":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	list = listItems(t, mux, blockID)
	if len(list) != 3 || list[1].ID != added.ID {
		t.Fatalf("unexpected order: %+v", list)
	}
	for i, it := range list {
		if it.Position != i {
			t.Errorf("position[%d] = %d, not contiguous", i, it.Position)
		}
	}

	// Trim only: position untouched; both together also work.
	rec = doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+added.ID, `{"trim_duration_ms":500}`)
	mustDecode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.TrimDurationMs == nil || *got.TrimDurationMs != 500 || got.Position != 1 {
		t.Fatalf("trim: %d %+v", rec.Code, got)
	}
	rec = doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+added.ID, `{"position":2,"trim_duration_ms":700}`)
	mustDecode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Position != 2 || *got.TrimDurationMs != 700 {
		t.Fatalf("both: %d %+v", rec.Code, got)
	}
}

func TestUpdateBlockItemErrors(t *testing.T) {
	db, mux := newTestDB(t)
	blockID, items := newManualBlockWithItems(t, mux, seedCatalogItem(t, db, "a"), seedCatalogItem(t, db, "b"))
	path := "/blocks/" + blockID + "/items/" + items[0].ID

	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"empty body":        {path, `{}`, 400},
		"position too big":  {path, `{"position":2}`, 400},
		"negative position": {path, `{"position":-1}`, 400},
		"zero trim":         {path, `{"trim_duration_ms":0}`, 400},
		"bad json":          {path, `nope`, 400},
		"unknown item":      {"/blocks/" + blockID + "/items/nope", `{"position":0}`, 404},
		"unknown block":     {"/blocks/nope/items/" + items[0].ID, `{"position":0}`, 404},
	} {
		if rec := doRequest(t, mux, "PUT", tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, tc.want, rec.Body.String())
		}
	}
	// Rejected requests changed nothing.
	if list := listItems(t, mux, blockID); list[0].ID != items[0].ID || list[1].ID != items[1].ID {
		t.Errorf("order changed by rejected requests: %+v", list)
	}

	seedGenreBlockCatalogItem(t, db, "d1", "Comedy")
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Dyn","criteria":{"kind":"movie","genres":["Comedy"]}}`)
	var dyn domain.Block
	mustDecode(t, rec.Body.Bytes(), &dyn)
	dynItems := listItems(t, mux, dyn.ID)
	if len(dynItems) == 0 {
		t.Fatal("dynamic block has no items")
	}
	rec = doRequest(t, mux, "PUT", "/blocks/"+dyn.ID+"/items/"+dynItems[0].ID, `{"trim_duration_ms":5}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("dynamic block: status = %d, want 409", rec.Code)
	}
}

func TestUpdateFillerPoolItem(t *testing.T) {
	db, mux := newTestDB(t)
	ci := seedCatalogItem(t, db, "f")
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"P"}`)
	var pool domain.FillerPool
	mustDecode(t, rec.Body.Bytes(), &pool)
	rec = doRequest(t, mux, "POST", "/filler-pools/"+pool.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, ci))
	var item domain.FillerPoolItem
	mustDecode(t, rec.Body.Bytes(), &item)
	path := "/filler-pools/" + pool.ID + "/items/" + item.ID

	rec = doRequest(t, mux, "PUT", path, `{"weight":5}`)
	var got domain.FillerPoolItem
	mustDecode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Weight != 5 || got.ID != item.ID {
		t.Fatalf("update: %d %+v", rec.Code, got)
	}
	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var list []domain.FillerPoolItem
	mustDecode(t, rec.Body.Bytes(), &list)
	if list[0].Weight != 5 {
		t.Errorf("persisted weight = %d, want 5", list[0].Weight)
	}

	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"zero":         {path, `{"weight":0}`, 400},
		"negative":     {path, `{"weight":-2}`, 400},
		"missing":      {path, `{}`, 400},
		"fractional":   {path, `{"weight":1.5}`, 400},
		"unknown item": {"/filler-pools/" + pool.ID + "/items/nope", `{"weight":2}`, 404},
		"unknown pool": {"/filler-pools/nope/items/" + item.ID, `{"weight":2}`, 404},
	} {
		if rec := doRequest(t, mux, "PUT", tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.want)
		}
	}

	seedGenreBlockCatalogItem(t, db, "g1", "Comedy")
	rec = doRequest(t, mux, "POST", "/filler-pools", `{"name":"Dyn","criteria":{"kind":"movie","genres":["Comedy"]}}`)
	var dyn domain.FillerPool
	mustDecode(t, rec.Body.Bytes(), &dyn)
	rec = doRequest(t, mux, "GET", "/filler-pools/"+dyn.ID+"/items", "")
	mustDecode(t, rec.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatal("dynamic pool has no items")
	}
	if rec := doRequest(t, mux, "PUT", "/filler-pools/"+dyn.ID+"/items/"+list[0].ID, `{"weight":3}`); rec.Code != http.StatusConflict {
		t.Errorf("dynamic pool: status = %d, want 409", rec.Code)
	}
}

func TestPreviewCriteria(t *testing.T) {
	db, mux := newTestDB(t)
	for i := 0; i < 25; i++ {
		seedGenreBlockCatalogItem(t, db, fmt.Sprintf("c%02d", i), "Comedy, Drama")
	}
	seedGenreBlockCatalogItem(t, db, "other", "Horror")
	countRows := func() int {
		var n []int
		db.Query(context.Background(), &n, "SELECT (SELECT COUNT(*) FROM block) + (SELECT COUNT(*) FROM filler_pool) + (SELECT COUNT(*) FROM block_item) + (SELECT COUNT(*) FROM filler_pool_item)")
		return n[0]
	}
	before := countRows()

	rec := doRequest(t, mux, "POST", "/catalog-items/preview-criteria", `{"kind":"movie","genres":["Comedy"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, %s", rec.Code, rec.Body.String())
	}
	var res catalogsync.CriteriaPreview
	mustDecode(t, rec.Body.Bytes(), &res)
	if res.MatchCount != 25 || res.TotalDurationMs != 25000 || len(res.Sample) != 20 {
		t.Errorf("preview = count %d dur %d sample %d", res.MatchCount, res.TotalDurationMs, len(res.Sample))
	}

	rec = doRequest(t, mux, "POST", "/catalog-items/preview-criteria", `{"kind":"movie","genres":["Nope"]}`)
	mustDecode(t, rec.Body.Bytes(), &res)
	if res.MatchCount != 0 || res.TotalDurationMs != 0 || res.Sample == nil || len(res.Sample) != 0 {
		t.Errorf("empty preview = %+v", res)
	}
	if !strings.Contains(rec.Body.String(), `"sample":[]`) {
		t.Errorf("empty sample should serialize as []: %s", rec.Body.String())
	}

	for name, body := range map[string]string{
		"bad kind":    `{"kind":"other","genres":["Comedy"]}`,
		"no criteria": `{"kind":"movie"}`,
		"bad json":    `x`,
	} {
		if rec := doRequest(t, mux, "POST", "/catalog-items/preview-criteria", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if after := countRows(); after != before {
		t.Errorf("preview persisted rows: %d -> %d", before, after)
	}
}

func TestChannelEPGPreviewIsReadOnly(t *testing.T) {
	db, mux := newTestDB(t)
	blockID := seedRotation(t, db)
	scheduleID := setupSchedule(t, mux)
	rec := doRequest(t, mux, "POST", "/channels", `{"number":1,"name":"C1","schedule_id":"`+scheduleID+`"}`)
	var ch domain.Channel
	mustDecode(t, rec.Body.Bytes(), &ch)

	// Insert the schedule item straight into the DB so nothing regenerates:
	// the real epg_entry stays empty while the stored config would produce a grid.
	now := time.Now().UnixMilli()
	if err := db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, active, created_at, updated_at)
		 VALUES ('si', ?, ?, ?, 'continuous', 1, ?, ?)`, DefaultTenantID, scheduleID, blockID, now, now); err != nil {
		t.Fatal(err)
	}
	metaBefore := doRequest(t, mux, "GET", "/epg/meta", "").Body.String()

	rec = doRequest(t, mux, "GET", "/channels/"+ch.ID+"/epg-preview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var preview []domain.EpgEntry
	mustDecode(t, rec.Body.Bytes(), &preview)
	if len(preview) < 1 {
		t.Fatal("preview is empty")
	}
	for i, e := range preview {
		if e.ChannelID != ch.ID || e.Kind != domain.EpgProgram || e.EndAt <= now {
			t.Errorf("entry %d unexpected: %+v", i, e)
		}
		if i > 0 && e.StartAt < preview[i-1].EndAt {
			t.Errorf("entry %d overlaps previous", i)
		}
	}
	if preview[0].CatalogItemID != "m0" {
		t.Errorf("rotation should restart from the beginning, got %s", preview[0].CatalogItemID)
	}

	if n := len(futureEntries(t, db, ch.ID)); n != 0 {
		t.Errorf("preview wrote %d epg_entry rows", n)
	}
	if after := doRequest(t, mux, "GET", "/epg/meta", "").Body.String(); after != metaBefore {
		t.Errorf("revision changed: %s -> %s", metaBefore, after)
	}

	// Window filter and validation.
	rec = doRequest(t, mux, "GET", fmt.Sprintf("/channels/%s/epg-preview?start=%d", ch.ID, preview[len(preview)-1].EndAt+1), "")
	var windowed []domain.EpgEntry
	mustDecode(t, rec.Body.Bytes(), &windowed)
	if len(windowed) != 0 {
		t.Errorf("window past the grid has %d entries, want 0", len(windowed))
	}
	if rec := doRequest(t, mux, "GET", "/channels/"+ch.ID+"/epg-preview?start=x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad start: %d", rec.Code)
	}
	if rec := doRequest(t, mux, "GET", "/channels/nope/epg-preview", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel: %d", rec.Code)
	}

	// An actual reset produces the same programs the preview showed.
	if rec := doRequest(t, mux, "POST", "/channels/"+ch.ID+"/reset-epg", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d", rec.Code)
	}
	real := futureEntries(t, db, ch.ID)
	if len(real) != len(preview) || real[0].CatalogItemID != preview[0].CatalogItemID {
		t.Errorf("reset differs from preview: %+v vs %+v", real, preview)
	}
}
