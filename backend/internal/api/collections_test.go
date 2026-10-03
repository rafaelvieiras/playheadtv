package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const horrorQuery = `{"version":1,"all":[{"field":"genre","op":"equals","value":"Horror"}]}`

func getCollectionJSON(t *testing.T, mux http.Handler, id string) (domain.Collection, int) {
	t.Helper()
	rec := doRequestAny(t, mux, "GET", "/collections/"+id, "")
	var c domain.Collection
	if rec.Code == http.StatusOK {
		mustDecode(t, rec.Body.Bytes(), &c)
	}
	return c, rec.Code
}

func TestCollectionCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "GET", "/collections", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %s, want []", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "POST", "/collections", `{"name":"  Comedies ","description":"funny","query":`+comedyQuery+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var c domain.Collection
	mustDecode(t, rec.Body.Bytes(), &c)
	if c.ID == "" || c.Name != "Comedies" || c.Description == nil || *c.Description != "funny" || c.UsageCount != 0 || c.CreatedAt == 0 {
		t.Fatalf("created = %+v", c)
	}
	if !strings.Contains(jsonOf(c.Tree), `"version":1`) || strings.Contains(rec.Body.String(), `"tenant_id":"default"`) == false {
		t.Errorf("body = %s", rec.Body.String())
	}

	// Validation: the same strict parsing as before, plus the name.
	for name, q := range invalidCriteria {
		if rec := doRequest(t, mux, "POST", "/collections", `{"name":"Bad","query":`+q+`}`); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	for name, body := range map[string]string{
		"no name":      `{"query":` + comedyQuery + `}`,
		"blank name":   `{"name":"  ","query":` + comedyQuery + `}`,
		"no query":     `{"name":"X"}`,
		"null query":   `{"name":"X","query":null}`,
		"not json":     `nope`,
		"unknown body": `{"name":"X","query":` + comedyQuery + `,"criteria":1}`,
	} {
		rec := doRequest(t, mux, "POST", "/collections", body)
		if name == "unknown body" {
			if rec.Code != http.StatusCreated { // unknown fields are ignored like everywhere else
				t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}

	// PUT is a full replace.
	rec = doRequest(t, mux, "PUT", "/collections/"+c.ID, `{"name":"Horrors","query":`+horrorQuery+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var u domain.Collection
	mustDecode(t, rec.Body.Bytes(), &u)
	if u.Name != "Horrors" || u.Description != nil || !strings.Contains(jsonOf(u.Tree), "Horror") || u.UpdatedAt < c.UpdatedAt {
		t.Errorf("updated = %+v", u)
	}
	if rec := doRequest(t, mux, "PUT", "/collections/"+c.ID, `{"name":"X","query":{"version":1,"all":[]}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid update: %d", rec.Code)
	}
	if got, code := getCollectionJSON(t, mux, c.ID); code != 200 || got.Name != "Horrors" {
		t.Errorf("get = %+v %d", got, code)
	}

	for _, method := range []string{"GET", "PUT", "DELETE"} {
		body := ""
		if method == "PUT" {
			body = `{"name":"X","query":` + comedyQuery + `}`
		}
		if rec := doRequest(t, mux, method, "/collections/nope", body); rec.Code != http.StatusNotFound {
			t.Errorf("%s unknown: %d", method, rec.Code)
		}
	}
	if rec := doRequest(t, mux, "DELETE", "/collections/"+c.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete unused: %d %s", rec.Code, rec.Body.String())
	}
	if _, code := getCollectionJSON(t, mux, c.ID); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}

func TestCollectionUsageAndDeleteWhileInUse(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c", "Comedy")
	col := newCollection(t, mux, comedyQuery)

	blockID := newBlock(t, mux, `{"name":"Dyn block","collection_id":"`+col+`"}`)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Dyn pool","collection_id":"`+col+`"}`)
	var pool domain.FillerPool
	mustDecode(t, rec.Body.Bytes(), &pool)
	manual := newBlock(t, mux, `{"name":"Manual"}`)
	rec = doRequest(t, mux, "POST", "/blocks/"+manual+"/items", criteriaItemBody(col, "", `{"mode":"all"}`))
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)

	rec = doRequest(t, mux, "GET", "/collections/"+col+"/usage", "")
	var u domain.CollectionUsage
	mustDecode(t, rec.Body.Bytes(), &u)
	if rec.Code != 200 || len(u.Blocks) != 1 || u.Blocks[0].ID != blockID || u.Blocks[0].Name != "Dyn block" ||
		len(u.FillerPools) != 1 || u.FillerPools[0].ID != pool.ID ||
		len(u.BlockItems) != 1 || u.BlockItems[0].ID != item.ID || u.BlockItems[0].BlockName != "Manual" || u.BlockItems[0].BlockID != manual {
		t.Fatalf("usage = %d %s", rec.Code, rec.Body.String())
	}
	if c, _ := getCollectionJSON(t, mux, col); c.UsageCount != 3 {
		t.Errorf("usage_count = %d, want 3", c.UsageCount)
	}

	rec = doRequest(t, mux, "DELETE", "/collections/"+col, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete in use: %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string                 `json:"error"`
		Usage domain.CollectionUsage `json:"usage"`
	}
	mustDecode(t, rec.Body.Bytes(), &body)
	if !strings.Contains(body.Error, "1 block, 1 filler pool, 1 criteria item") || body.Usage.Count() != 3 || body.Usage.Blocks[0].Name != "Dyn block" {
		t.Errorf("409 body = %s", rec.Body.String())
	}
	if _, code := getCollectionJSON(t, mux, col); code != 200 {
		t.Fatal("the collection was deleted despite the 409")
	}

	// Free the users one by one: still 409 until the last one is gone.
	if rec := doRequest(t, mux, "PUT", "/blocks/"+blockID, `{"name":"Dyn block"}`); rec.Code != 200 {
		t.Fatalf("revert block: %d", rec.Code)
	}
	if rec := doRequest(t, mux, "DELETE", "/filler-pools/"+pool.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete pool: %d", rec.Code)
	}
	if rec := doRequest(t, mux, "DELETE", "/collections/"+col, ""); rec.Code != http.StatusConflict {
		t.Fatalf("delete with one item left: %d", rec.Code)
	}
	if rec := doRequest(t, mux, "DELETE", "/blocks/"+manual+"/items/"+item.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete item: %d", rec.Code)
	}
	if rec := doRequest(t, mux, "DELETE", "/collections/"+col, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete free collection: %d %s", rec.Code, rec.Body.String())
	}
}

// Editing a collection applies to every user at once: dynamic blocks and
// pools are re-resolved and a criteria item's live view follows.
func TestEditingASharedCollectionUpdatesEveryUser(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c1", "Comedy")
	seedGenreBlockCatalogItem(t, db, "c2", "Comedy")
	seedGenreBlockCatalogItem(t, db, "h1", "Horror")
	col := newCollection(t, mux, comedyQuery)

	blockID := newBlock(t, mux, `{"name":"B","collection_id":"`+col+`"}`)
	other := newBlock(t, mux, `{"name":"B2","collection_id":"`+col+`"}`)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"P","collection_id":"`+col+`"}`)
	var pool domain.FillerPool
	mustDecode(t, rec.Body.Bytes(), &pool)
	manual := newBlock(t, mux, `{"name":"M"}`)
	rec = doRequest(t, mux, "POST", "/blocks/"+manual+"/items", criteriaItemBody(col, "", `{"mode":"all"}`))
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	if n := len(listItems(t, mux, blockID)); n != 2 {
		t.Fatalf("block items = %d, want 2", n)
	}

	if rec := doRequest(t, mux, "PUT", "/collections/"+col, `{"name":"Now horror","query":`+horrorQuery+`}`); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{blockID, other} {
		if items := listItems(t, mux, id); len(items) != 1 || items[0].CatalogItemID != "ci-block-genre-h1" {
			t.Errorf("block %s items after edit = %+v", id, items)
		}
	}
	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var pi []domain.FillerPoolItem
	mustDecode(t, rec.Body.Bytes(), &pi)
	if len(pi) != 1 || pi[0].CatalogItemID != "ci-block-genre-h1" {
		t.Errorf("pool items after edit = %+v", pi)
	}
	if view := listItems(t, mux, manual)[0].Criteria; view.MatchCount != 1 || view.CollectionName == nil || *view.CollectionName != "Now horror" {
		t.Errorf("criteria view after edit = %+v", view)
	}
	// The block response names the collection.
	rec = doRequest(t, mux, "GET", "/blocks/"+blockID, "")
	var b domain.Block
	mustDecode(t, rec.Body.Bytes(), &b)
	if b.CollectionName == nil || *b.CollectionName != "Now horror" {
		t.Errorf("block = %+v", b)
	}
}

func TestCollectionOfAnotherTenantIsUnreachable(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES ('other', 'Other', 'self_hosted', 1)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, `INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('foreign', 'other', 'Theirs', ?, 1, 1)`, mustCanon(t, comedyQuery)); err != nil {
		t.Fatal(err)
	}
	mine := newCollection(t, mux, comedyQuery)
	blockID := newBlock(t, mux, `{"name":"B"}`)

	if rec := doRequest(t, mux, "GET", "/collections", ""); strings.Contains(rec.Body.String(), "foreign") || !strings.Contains(rec.Body.String(), mine) {
		t.Errorf("list leaks: %s", rec.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/collections/foreign", ""},
		{"PUT", "/collections/foreign", `{"name":"Mine now","query":` + comedyQuery + `}`},
		{"DELETE", "/collections/foreign", ""},
		{"GET", "/collections/foreign/usage", ""},
		{"GET", "/collections/foreign/preview", ""},
		{"POST", "/catalog-items/preview-criteria", `{"collection_id":"foreign"}`},
	} {
		if rec := doRequest(t, mux, c.method, c.path, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s, want 404", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	for _, c := range []struct{ name, method, path, body string }{
		{"create block", "POST", "/blocks", `{"name":"X","collection_id":"foreign"}`},
		{"update block", "PUT", "/blocks/" + blockID, `{"name":"X","collection_id":"foreign"}`},
		{"create pool", "POST", "/filler-pools", `{"name":"X","collection_id":"foreign"}`},
		{"criteria item", "POST", "/blocks/" + blockID + "/items", criteriaItemBody("foreign", "", `{"mode":"all"}`)},
	} {
		if rec := doRequest(t, mux, c.method, c.path, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", c.name, rec.Code, rec.Body.String())
		}
	}
	rec := doRequest(t, mux, "POST", "/blocks/"+blockID+"/items", criteriaItemBody(mine, "", `{"mode":"all"}`))
	var item domain.BlockItem
	mustDecode(t, rec.Body.Bytes(), &item)
	if rec := doRequest(t, mux, "PUT", "/blocks/"+blockID+"/items/"+item.ID, `{"collection_id":"foreign"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("update item to a foreign collection: %d %s", rec.Code, rec.Body.String())
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM collection WHERE tenant_id = 'other' AND name = 'Theirs'"); err != nil || n[0] != 1 {
		t.Errorf("foreign collection was touched: %v %v", n, err)
	}
}

func mustCanon(t *testing.T, q string) string {
	t.Helper()
	parsed, err := catalogsync.ParseQuery([]byte(q))
	if err != nil {
		t.Fatal(err)
	}
	return parsed.JSON()
}

func TestPreviewCollectionByID(t *testing.T) {
	db, mux := newTestDB(t)
	seedGenreBlockCatalogItem(t, db, "c1", "Comedy")
	seedGenreBlockCatalogItem(t, db, "c2", "Comedy")
	seedGenreBlockCatalogItem(t, db, "h1", "Horror")
	col := newCollection(t, mux, comedyQuery)

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/collections/" + col + "/preview", ""},
		{"POST", "/catalog-items/preview-criteria", `{"collection_id":"` + col + `"}`},
		{"POST", "/catalog-items/preview-criteria", comedyQuery},
	} {
		rec := doRequest(t, mux, c.method, c.path, c.body)
		var res catalogsync.CriteriaPreview
		mustDecode(t, rec.Body.Bytes(), &res)
		if rec.Code != 200 || res.MatchCount != 2 || len(res.Sample) != 2 {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestInlineCriteriaIsRejectedWithAPointerToCollections(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"X","criteria":`+comedyQuery+`}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "collection_id") {
		t.Errorf("block: %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, mux, "POST", "/filler-pools", `{"name":"X","criteria":`+comedyQuery+`}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "collection_id") {
		t.Errorf("pool: %d %s", rec.Code, rec.Body.String())
	}
	// Explicit null is the same as absent: a manual block/pool.
	if rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Y","criteria":null,"collection_id":null}`); rec.Code != http.StatusCreated {
		t.Errorf("null criteria: %d %s", rec.Code, rec.Body.String())
	}
}
