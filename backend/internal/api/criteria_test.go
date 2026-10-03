package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const comedyQuery = `{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}`

func criteriaItemBody(collectionID, order, amount string) string {
	body := `{"item_type":"criteria","criteria":{"collection_id":"` + collectionID + `"`
	if order != "" {
		body += `,"playback_order":"` + order + `"`
	}
	if amount != "" {
		body += `,"amount":` + amount
	}
	return body + `}}`
}

func TestAddCriteriaBlockItem(t *testing.T) {
	db, mux := newTestDB(t)
	for i := 0; i < 3; i++ {
		seedGenreBlockCatalogItem(t, db, fmt.Sprintf("c%d", i), "Comedy")
	}
	seedGenreBlockCatalogItem(t, db, "h", "Horror")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	col := newCollection(t, mux, comedyQuery)

	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(col, "", `{"mode":"count","count":2}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	if item.ItemType != domain.BlockItemCriteria || item.CatalogItemID != "" || item.Criteria == nil {
		t.Fatalf("item = %+v", item)
	}
	c := item.Criteria
	if c.PlaybackOrder != domain.OrderTitle || c.Amount.Mode != "count" || c.Amount.Count != 2 || c.MatchCount != 3 || c.TotalDurationMs != 3000 {
		t.Errorf("criteria view = %+v, want title order (the default), count 2 and 3 live matches", c)
	}
	// The view names the collection and shows its canonical tree; the seed is
	// never served and the item stores no query of its own.
	if c.CollectionID == nil || *c.CollectionID != col || c.CollectionName == nil || !strings.Contains(jsonOf(c.Query), `"version":1`) || strings.Contains(rec.Body.String(), "seed") {
		t.Errorf("response = %s", rec.Body.String())
	}

	// "All matches" chosen explicitly, another order, listed with the live view.
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(col, "shuffle", `{"mode":"all"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add all: %d %s", rec.Code, rec.Body.String())
	}
	list := listItems(t, mux, blockID)
	if len(list) != 2 || list[1].Criteria == nil || list[1].Criteria.Amount.Mode != "all" || list[1].Criteria.PlaybackOrder != "shuffle" || list[1].Position != 1 {
		t.Fatalf("list = %+v", list)
	}

	// The match count follows the catalog.
	seedGenreBlockCatalogItem(t, db, "c9", "Comedy")
	if list = listItems(t, mux, blockID); list[0].Criteria.MatchCount != 4 {
		t.Errorf("match count after a new match = %d, want 4", list[0].Criteria.MatchCount)
	}

	// Mixed with a fixed catalog item and reorderable like any item.
	movie := seedCatalogItem(t, db, "fixed")
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", `{"catalog_item_id":"`+movie+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add catalog item: %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+list[0].ID, `{"position":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body.String())
	}
	if list = listItems(t, mux, blockID); list[2].ID != item.ID || list[2].Criteria == nil {
		t.Errorf("after reorder = %+v", list)
	}
}

func TestAddCriteriaBlockItemValidation(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c", "Comedy")
	movie := seedCatalogItem(t, db, "m")
	ms := seedSeries(t, db, "a", "Show", 1)
	blockID := newBlock(t, mux, `{"name":"B"}`)
	col := newCollection(t, mux, comedyQuery)
	dynamic := newBlock(t, mux, `{"name":"Dyn","collection_id":"`+col+`"}`)

	count := `{"mode":"count","count":1}`
	cases := []struct {
		name, block, body string
		want              int
	}{
		{"amount is required", blockID, criteriaItemBody(col, "", ""), 400},
		{"all matches must be explicit", blockID, criteriaItemBody(col, "", `{}`), 400},
		{"count without a number", blockID, criteriaItemBody(col, "", `{"mode":"count"}`), 400},
		{"zero count", blockID, criteriaItemBody(col, "", `{"mode":"count","count":0}`), 400},
		{"negative count", blockID, criteriaItemBody(col, "", `{"count":-3}`), 400},
		{"count with all", blockID, criteriaItemBody(col, "", `{"mode":"all","count":2}`), 400},
		{"series amount mode", blockID, criteriaItemBody(col, "", `{"mode":"slot_end"}`), 400},
		{"series order", blockID, criteriaItemBody(col, "original", count), 400},
		{"unknown order", blockID, criteriaItemBody(col, "backwards", count), 400},
		{"no collection", blockID, `{"item_type":"criteria","criteria":{"amount":` + count + `}}`, 400},
		{"unknown collection", blockID, criteriaItemBody("nope", "", count), 400},
		{"inline query is rejected", blockID, `{"item_type":"criteria","criteria":{"query":` + comedyQuery + `,"amount":` + count + `}}`, 400},
		{"inline query next to a collection", blockID, `{"item_type":"criteria","criteria":{"collection_id":"` + col + `","query":` + comedyQuery + `,"amount":` + count + `}}`, 400},
		{"criteria missing", blockID, `{"item_type":"criteria"}`, 400},
		{"catalog id", blockID, `{"item_type":"criteria","catalog_item_id":"` + movie + `","criteria":{"collection_id":"` + col + `","amount":` + count + `}}`, 400},
		{"trim", blockID, `{"item_type":"criteria","trim_duration_ms":5,"criteria":{"collection_id":"` + col + `","amount":` + count + `}}`, 400},
		{"with series", blockID, fmt.Sprintf(`{"item_type":"criteria","series":{"media_source_id":%q,"series_title":"Show"},"criteria":{"collection_id":%q,"amount":%s}}`, ms, col, count), 400},
		{"criteria on a catalog item", blockID, `{"item_type":"catalog","catalog_item_id":"` + movie + `","criteria":{"collection_id":"` + col + `","amount":` + count + `}}`, 400},
		{"criteria on a series item", blockID, fmt.Sprintf(`{"item_type":"series","series":{"media_source_id":%q,"series_title":"Show"},"criteria":{"collection_id":%q,"amount":%s}}`, ms, col, count), 400},
		{"dynamic block", dynamic, criteriaItemBody(col, "", count), 409},
		{"unknown block", "nope", criteriaItemBody(col, "", count), 404},
	}
	for _, c := range cases {
		if rec := doRequest(t, mux, "POST", "/blocks/"+c.block+"/items", c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if list := listItems(t, mux, blockID); len(list) != 0 {
		t.Errorf("a rejected request left items behind: %+v", list)
	}
}

func TestUpdateCriteriaBlockItem(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c", "Comedy")
	seedGenreBlockCatalogItem(t, db, "h", "Horror")
	movie := seedCatalogItem(t, db, "m")
	blockID := newBlock(t, mux, `{"name":"B"}`)
	col := newCollection(t, mux, comedyQuery)
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(col, "title", `{"mode":"count","count":1}`))
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", `{"catalog_item_id":"`+movie+`"}`)
	var fixed domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &fixed)
	path := "/blocks/" + blockID + "/items/" + item.ID

	put := func(body string) (*domain.BlockItem, int, string) {
		rec := doRequest(t, mux, "PUT", path, body)
		var out domain.BlockItem
		if rec.Code == http.StatusOK {
			mustDecode(t, rec.Body.Bytes(), &out)
		}
		return &out, rec.Code, rec.Body.String()
	}

	got, code, body := put(`{"amount":{"mode":"all"}}`)
	if code != 200 || got.Criteria.Amount.Mode != "all" || got.Criteria.PlaybackOrder != "title" {
		t.Fatalf("amount: %d %s", code, body)
	}
	got, code, body = put(`{"amount":{"mode":"count","count":5}}`)
	if code != 200 || got.Criteria.Amount.Count != 5 {
		t.Fatalf("amount count: %d %s", code, body)
	}
	horror := `{"version":1,"all":[{"field":"genre","op":"equals","value":"Horror"}]}`
	got, code, body = put(`{"collection_id":"` + newCollection(t, mux, horror) + `"}`)
	if code != 200 || got.Criteria.MatchCount != 1 || !strings.Contains(jsonOf(got.Criteria.Query), "Horror") || got.Criteria.Amount.Count != 5 {
		t.Fatalf("query: %d %s", code, body)
	}
	got, code, body = put(`{"playback_order":"release_date"}`)
	if code != 200 || got.Criteria.PlaybackOrder != "release_date" {
		t.Fatalf("order: %d %s", code, body)
	}

	// A new playback order restarts the item's cursor; the other edits keep it.
	if err := db.Exec(context.Background(), "INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('ch', ?, 1, 'C', 'UTC', 1, 1)", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(context.Background(), "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES (?, 'ch', ?, 7)", DefaultTenantID, item.ID); err != nil {
		t.Fatal(err)
	}
	origin := func() int64 {
		var o []int64
		if err := db.Query(context.Background(), &o, "SELECT origin_ms FROM series_cursor WHERE block_item_id = ?", item.ID); err != nil || len(o) != 1 {
			t.Fatalf("cursor: %v %v", o, err)
		}
		return o[0]
	}
	put(`{"amount":{"mode":"all"}}`)
	put(`{"collection_id":"` + col + `"}`)
	put(`{"playback_order":"release_date"}`) // unchanged order
	if origin() != 7 {
		t.Errorf("cursor origin = %d after edits that keep the order, want it untouched", origin())
	}
	put(`{"playback_order":"shuffle"}`)
	if origin() == 7 {
		t.Error("cursor origin was not moved by a playback order change")
	}

	for name, tc := range map[string]struct{ body string }{
		"series order":       {`{"playback_order":"original"}`},
		"unknown order":      {`{"playback_order":"nope"}`},
		"bad amount":         {`{"amount":{"mode":"count","count":0}}`},
		"series amount":      {`{"amount":{"mode":"slot_end"}}`},
		"inline query":       {`{"query":` + comedyQuery + `}`},
		"unknown collection": {`{"collection_id":"nope"}`},
		"empty collection":   {`{"collection_id":""}`},
		"trim":               {`{"trim_duration_ms":5}`},
		"nothing":            {`{}`},
	} {
		if _, code, body := put(tc.body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}

	// collection_id is only valid on a criteria item; a catalog item takes none of the series/criteria fields.
	for name, body := range map[string]string{
		"collection on a catalog item": `{"collection_id":"` + col + `"}`,
		"amount on a catalog item":     `{"amount":{"mode":"all"}}`,
		"order on a catalog item":      `{"playback_order":"title"}`,
	} {
		if rec := doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+fixed.ID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
}

func TestDynamicBlockAndPoolCriteriaReviewFlow(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	seedGenreBlockCatalogItem(t, db, "a", "Action")
	seedGenreBlockCatalogItem(t, db, "aa", "Action & Adventure")

	// Legacy dynamic block and pool as an older version stored them.
	now := int64(1)
	legacy := `{"kind":"movie","genres":["Action"]}`
	if err := db.Exec(ctx, "INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('lb', ?, 'Legacy block', ?, ?, ?)", DefaultTenantID, legacy, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES ('lp', ?, 'Legacy pool', ?, ?)", DefaultTenantID, legacy, now); err != nil {
		t.Fatal(err)
	}
	report, err := catalogsync.MigrateLegacyCriteria(ctx, db)
	if err != nil || report.Held != 2 || report.Converted != 0 {
		t.Fatalf("migration = %+v %v", report, err)
	}

	rec := doRequest(t, mux, "GET", "/criteria-migration-review", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	var review []catalogsync.ReviewItem
	mustDecode(t, rec.Body.Bytes(), &review)
	if len(review) != 2 || review[0].OwnerType != "block" || review[0].OwnerID != "lb" || review[0].BeforeCount != 2 || review[0].AfterCount != 1 ||
		len(review[0].OnlyBefore) != 1 || review[0].OnlyBefore[0].Title != "Item aa" || review[1].OwnerType != "filler_pool" {
		t.Fatalf("review = %+v", review)
	}
	if !strings.Contains(jsonOf(review[0].ProposedQuery), `"genre"`) {
		t.Errorf("proposal = %s", jsonOf(review[0].ProposedQuery))
	}

	// Both are dynamic and still play the legacy value (no collection yet).
	rec = doRequest(t, mux, "GET", "/blocks/lb", "")
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	if !b.Dynamic || b.CollectionID != nil || strings.Contains(rec.Body.String(), `"criteria"`) {
		t.Fatalf("held block = %s", rec.Body.String())
	}
	if err := catalogsync.ResolveDynamic(ctx, db, DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	if items := listItems(t, mux, "lb"); len(items) != 2 {
		t.Errorf("held block items = %d, want the substring matches (2)", len(items))
	}

	// Applying the proposal creates a collection, points the block at it and
	// clears the review entry.
	rec = doRequest(t, mux, "POST", "/criteria-migration-review/apply", `{"owner_type":"block","owner_id":"lb"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply proposal: %d %s", rec.Code, rec.Body.String())
	}
	var applied applyReviewResponse
	mustDecode(t, rec.Body.Bytes(), &applied)
	if !applied.Created || applied.Collection.Name != "Legacy block" || applied.Collection.UsageCount != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	rec = doRequest(t, mux, "GET", "/blocks/lb", "")
	mustDecode(t, rec.Body.Bytes(), &b)
	if b.CollectionID == nil || *b.CollectionID != applied.CollectionID {
		t.Fatalf("block after apply = %s", rec.Body.String())
	}
	if items := listItems(t, mux, "lb"); len(items) != 1 {
		t.Errorf("block items after applying = %d, want the exact match only", len(items))
	}
	rec = doRequest(t, mux, "GET", "/criteria-migration-review", "")
	mustDecode(t, rec.Body.Bytes(), &review)
	if len(review) != 1 || review[0].OwnerID != "lp" {
		t.Errorf("review after applying one = %+v", review)
	}
	// Not held any more: applying again is a 404; the pool applies with an edited query
	// and reuses the collection when the tree is identical.
	if rec := doRequest(t, mux, "POST", "/criteria-migration-review/apply", `{"owner_type":"block","owner_id":"lb"}`); rec.Code != http.StatusNotFound {
		t.Errorf("second apply: %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "POST", "/criteria-migration-review/apply", `{"owner_type":"filler_pool","owner_id":"lp"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply pool: %d %s", rec.Code, rec.Body.String())
	}
	mustDecode(t, rec.Body.Bytes(), &applied)
	if applied.Created || applied.Collection.UsageCount != 2 {
		t.Errorf("identical tree should reuse the collection: %+v", applied)
	}
	rec = doRequest(t, mux, "GET", "/criteria-migration-review", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty review = %s, want []", rec.Body.String())
	}
	// A pool applied again after deleting nothing: bad requests.
	for name, body := range map[string]string{
		"bad owner type": `{"owner_type":"channel","owner_id":"x"}`,
		"unknown owner":  `{"owner_type":"block","owner_id":"nope"}`,
	} {
		if rec := doRequest(t, mux, "POST", "/criteria-migration-review/apply", body); rec.Code < 400 {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := doRequest(t, mux, "DELETE", "/filler-pools/lp", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete pool: %d", rec.Code)
	}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
