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

func criteriaItemBody(query, order, amount string) string {
	body := `{"item_type":"criteria","criteria":{"query":` + query
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

	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(comedyQuery, "", `{"mode":"count","count":2}`))
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
	// The stored query is the canonical tree; the seed is never served.
	if !strings.Contains(jsonOf(c.Query), `"version":1`) || strings.Contains(rec.Body.String(), "seed") {
		t.Errorf("response = %s", rec.Body.String())
	}

	// "All matches" chosen explicitly, another order, listed with the live view.
	rec = doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(comedyQuery, "shuffle", `{"mode":"all"}`))
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
	dynamic := newBlock(t, mux, `{"name":"Dyn","criteria":`+comedyQuery+`}`)

	count := `{"mode":"count","count":1}`
	cases := []struct {
		name, block, body string
		want              int
	}{
		{"amount is required", blockID, criteriaItemBody(comedyQuery, "", ""), 400},
		{"all matches must be explicit", blockID, criteriaItemBody(comedyQuery, "", `{}`), 400},
		{"count without a number", blockID, criteriaItemBody(comedyQuery, "", `{"mode":"count"}`), 400},
		{"zero count", blockID, criteriaItemBody(comedyQuery, "", `{"mode":"count","count":0}`), 400},
		{"negative count", blockID, criteriaItemBody(comedyQuery, "", `{"count":-3}`), 400},
		{"count with all", blockID, criteriaItemBody(comedyQuery, "", `{"mode":"all","count":2}`), 400},
		{"series amount mode", blockID, criteriaItemBody(comedyQuery, "", `{"mode":"slot_end"}`), 400},
		{"series order", blockID, criteriaItemBody(comedyQuery, "original", count), 400},
		{"unknown order", blockID, criteriaItemBody(comedyQuery, "backwards", count), 400},
		{"no query", blockID, `{"item_type":"criteria","criteria":{"amount":` + count + `}}`, 400},
		{"legacy query", blockID, criteriaItemBody(`{"kind":"movie","genres":["Comedy"]}`, "", count), 400},
		{"invalid query", blockID, criteriaItemBody(`{"version":1,"all":[{"field":"year","op":"contains","value":"x"}]}`, "", count), 400},
		{"criteria missing", blockID, `{"item_type":"criteria"}`, 400},
		{"catalog id", blockID, `{"item_type":"criteria","catalog_item_id":"` + movie + `","criteria":{"query":` + comedyQuery + `,"amount":` + count + `}}`, 400},
		{"trim", blockID, `{"item_type":"criteria","trim_duration_ms":5,"criteria":{"query":` + comedyQuery + `,"amount":` + count + `}}`, 400},
		{"with series", blockID, fmt.Sprintf(`{"item_type":"criteria","series":{"media_source_id":%q,"series_title":"Show"},"criteria":{"query":%s,"amount":%s}}`, ms, comedyQuery, count), 400},
		{"criteria on a catalog item", blockID, `{"item_type":"catalog","catalog_item_id":"` + movie + `","criteria":{"query":` + comedyQuery + `,"amount":` + count + `}}`, 400},
		{"criteria on a series item", blockID, fmt.Sprintf(`{"item_type":"series","series":{"media_source_id":%q,"series_title":"Show"},"criteria":{"query":%s,"amount":%s}}`, ms, comedyQuery, count), 400},
		{"dynamic block", dynamic, criteriaItemBody(comedyQuery, "", count), 409},
		{"unknown block", "nope", criteriaItemBody(comedyQuery, "", count), 404},
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
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(comedyQuery, "title", `{"mode":"count","count":1}`))
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
	got, code, body = put(`{"query":` + horror + `}`)
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
	put(`{"query":` + comedyQuery + `}`)
	put(`{"playback_order":"release_date"}`) // unchanged order
	if origin() != 7 {
		t.Errorf("cursor origin = %d after edits that keep the order, want it untouched", origin())
	}
	put(`{"playback_order":"shuffle"}`)
	if origin() == 7 {
		t.Error("cursor origin was not moved by a playback order change")
	}

	for name, tc := range map[string]struct{ body string }{
		"series order":  {`{"playback_order":"original"}`},
		"unknown order": {`{"playback_order":"nope"}`},
		"bad amount":    {`{"amount":{"mode":"count","count":0}}`},
		"series amount": {`{"amount":{"mode":"slot_end"}}`},
		"legacy query":  {`{"query":{"kind":"movie","genres":["x"]}}`},
		"invalid query": {`{"query":{"version":1,"all":[]}}`},
		"trim":          {`{"trim_duration_ms":5}`},
		"nothing":       {`{}`},
	} {
		if _, code, body := put(tc.body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}

	// query is only valid on a criteria item; a catalog item takes none of the series/criteria fields.
	for name, body := range map[string]string{
		"query on a catalog item":  `{"query":` + comedyQuery + `}`,
		"amount on a catalog item": `{"amount":{"mode":"all"}}`,
		"order on a catalog item":  `{"playback_order":"title"}`,
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

	// Both still hold the legacy value and resolve as before.
	rec = doRequest(t, mux, "GET", "/blocks/lb", "")
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	if b.Criteria == nil || *b.Criteria != legacy {
		t.Fatalf("block criteria = %v", b.Criteria)
	}

	// Applying the proposal is a PUT with the tree; it clears the review entry.
	rec = doRequest(t, mux, "PUT", "/blocks/lb", `{"name":"Legacy block","criteria":`+jsonOf(review[0].ProposedQuery)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply proposal: %d %s", rec.Code, rec.Body.String())
	}
	mustDecode(t, rec.Body.Bytes(), &b)
	if b.Criteria == nil || catalogsync.IsLegacyCriteria(*b.Criteria) {
		t.Fatalf("block criteria after PUT = %v", b.Criteria)
	}
	if items := listItems(t, mux, "lb"); len(items) != 1 {
		t.Errorf("block items after applying = %d, want the exact match only", len(items))
	}
	rec = doRequest(t, mux, "GET", "/criteria-migration-review", "")
	mustDecode(t, rec.Body.Bytes(), &review)
	if len(review) != 1 || review[0].OwnerID != "lp" {
		t.Errorf("review after applying one = %+v", review)
	}

	// Deleting the owner drops its entry, and an empty review is [] not null.
	if rec := doRequest(t, mux, "DELETE", "/filler-pools/lp", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete pool: %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/criteria-migration-review", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty review = %s, want []", rec.Body.String())
	}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
