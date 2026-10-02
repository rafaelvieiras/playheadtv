package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedCatalogItem inserts a media_source + catalog_item directly, bypassing
// the API, so scheduling-table tests have a valid catalog_item_id FK target
// without going through a probe/catalog-sync round trip.
func seedCatalogItem(t *testing.T, db storage.DB, id string) string {
	t.Helper()
	ctx := context.Background()
	msID := "ms-" + id
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'local_folder', 'seed', 'push', ?, ?)`,
		msID, DefaultTenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	itemID := "ci-" + id
	if err := db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		 VALUES (?, ?, ?, ?, 'movie', ?, 1000, ?)`,
		itemID, DefaultTenantID, msID, "ext-"+id, "Item "+id, now,
	); err != nil {
		t.Fatalf("seed catalog_item: %v", err)
	}
	return itemID
}

func TestBlocksCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Sitcom Night"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)

	rec = doRequest(t, mux, "GET", "/blocks", "")
	var list []domain.Block
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "PUT", "/blocks/"+block.ID, `{"name":"Sitcom Night HD"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.Block
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "Sitcom Night HD" {
		t.Errorf("Name = %q, want Sitcom Night HD", updated.Name)
	}

	rec = doRequest(t, mux, "DELETE", "/blocks/"+block.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestBlockNotFound(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "GET", "/blocks/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestBlockMissingName(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/blocks", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBlockItems(t *testing.T) {
	db, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Block A"}`)
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)

	item1 := seedCatalogItem(t, db, "1")
	item2 := seedCatalogItem(t, db, "2")

	rec = doRequest(t, mux, "POST", "/blocks/"+block.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, item1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add item 1: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var bi1 domain.BlockItem
	json.Unmarshal(rec.Body.Bytes(), &bi1)
	if bi1.Position != 0 {
		t.Errorf("first item position = %d, want 0", bi1.Position)
	}

	rec = doRequest(t, mux, "POST", "/blocks/"+block.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, item2))
	var bi2 domain.BlockItem
	json.Unmarshal(rec.Body.Bytes(), &bi2)
	if bi2.Position != 1 {
		t.Errorf("second item position = %d, want 1", bi2.Position)
	}

	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID+"/items", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list items: status = %d", rec.Code)
	}
	var items []domain.BlockItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 2 || items[0].Position != 0 || items[1].Position != 1 {
		t.Fatalf("unexpected items order: %+v", items)
	}

	rec = doRequest(t, mux, "DELETE", "/blocks/"+block.ID+"/items/"+bi1.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete item: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID+"/items", "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("items after delete = %d, want 1", len(items))
	}
}

func TestBlockItemInvalidCatalogItemFK(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Block A"}`)
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)

	rec = doRequest(t, mux, "POST", "/blocks/"+block.ID+"/items", `{"catalog_item_id":"does-not-exist"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// seedGenreBlockCatalogItem is seedCatalogItem plus a genres value, so
// dynamic block criteria tests have something to actually match against.
func seedGenreBlockCatalogItem(t *testing.T, db storage.DB, id, genres string) string {
	t.Helper()
	ctx := context.Background()
	msID := "ms-block-genre-" + id
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'local_folder', 'seed', 'push', ?, ?)`,
		msID, DefaultTenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	itemID := "ci-block-genre-" + id
	if err := db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, genres)
		 VALUES (?, ?, ?, ?, 'movie', ?, 1000, ?, ?)`,
		itemID, DefaultTenantID, msID, "ext-block-genre-"+id, "Item "+id, now, genres,
	); err != nil {
		t.Fatalf("seed catalog_item: %v", err)
	}
	// The genre field of a query reads the genre links, which a sync writes.
	if err := catalogsync.BackfillGenreLinks(ctx, db); err != nil {
		t.Fatalf("backfill genre links: %v", err)
	}
	return itemID
}

func TestBlockWithCriteriaPopulatesItems(t *testing.T) {
	db, mux := newTestDB(t)

	comedyItem := seedGenreBlockCatalogItem(t, db, "1", "Comedy")
	seedGenreBlockCatalogItem(t, db, "2", "Action") // shouldn't match

	rec := doRequest(t, mux, "POST", "/blocks",
		`{"name":"Comedy Channel","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)
	if block.Criteria == nil {
		t.Fatal("block.Criteria = nil, want set")
	}

	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID+"/items", "")
	var items []domain.BlockItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 || items[0].CatalogItemID != comedyItem {
		t.Fatalf("items = %+v, want exactly [%s]", items, comedyItem)
	}
}

func TestBlockWithCriteriaRejectsManualItemWrites(t *testing.T) {
	db, mux := newTestDB(t)
	item := seedGenreBlockCatalogItem(t, db, "1", "Comedy")

	rec := doRequest(t, mux, "POST", "/blocks",
		`{"name":"Comedy Channel","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)

	rec = doRequest(t, mux, "POST", "/blocks/"+block.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, item))
	if rec.Code != http.StatusConflict {
		t.Fatalf("add item on dynamic block: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/blocks/"+block.ID+"/items", "")
	var items []domain.BlockItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (only from criteria)", len(items))
	}
	rec = doRequest(t, mux, "DELETE", "/blocks/"+block.ID+"/items/"+items[0].ID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete item on dynamic block: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

func TestBlockCriteriaValidation(t *testing.T) {
	db, mux := newTestDB(t)

	for name, criteria := range invalidCriteria {
		rec := doRequest(t, mux, "POST", "/blocks", `{"name":"Bad","criteria":`+criteria+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
		rec = doRequest(t, mux, "PUT", "/blocks/does-not-matter", `{"name":"Bad","criteria":`+criteria+`}`)
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
			t.Errorf("%s: PUT accepted invalid criteria", name)
		}
	}
	var n []int
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM block"); err != nil || n[0] != 0 {
		t.Errorf("blocks after rejected writes = %v %v, want 0", n, err)
	}
}

// invalidCriteria are criteria values every write endpoint must reject with a
// 400: the legacy shape, unknown fields/operators, bad values, empty groups.
var invalidCriteria = map[string]string{
	"legacy shape":   `{"kind":"movie","genres":["Comedy"]}`,
	"legacy empty":   `{"kind":"movie"}`,
	"no version":     `{"all":[{"field":"kind","op":"equals","value":"movie"}]}`,
	"unknown field":  `{"version":1,"all":[{"field":"bogus","op":"equals","value":"x"}]}`,
	"bad operator":   `{"version":1,"all":[{"field":"year","op":"contains","value":"x"}]}`,
	"bad kind value": `{"version":1,"all":[{"field":"kind","op":"equals","value":"bogus"}]}`,
	"empty group":    `{"version":1,"all":[]}`,
	"not an object":  `"movie"`,
	"mixed group":    `{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}],"any":[{"field":"kind","op":"equals","value":"movie"}]}`,
}

// TestBlockUpdatePutRevertsToManual proves updateBlock's PUT-is-full-replace
// semantics: omitting criteria on a previously dynamic block reverts it to
// manual (block_item stays as last resolved, but new writes are now allowed).
func TestBlockUpdatePutRevertsToManual(t *testing.T) {
	db, mux := newTestDB(t)
	item := seedGenreBlockCatalogItem(t, db, "1", "Comedy")

	rec := doRequest(t, mux, "POST", "/blocks",
		`{"name":"Comedy Channel","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	var block domain.Block
	json.Unmarshal(rec.Body.Bytes(), &block)

	rec = doRequest(t, mux, "PUT", "/blocks/"+block.ID, `{"name":"Comedy Channel"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.Block
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Criteria != nil {
		t.Fatalf("updated.Criteria = %v, want nil after PUT omitted it", *updated.Criteria)
	}

	// Now manual writes should be allowed again.
	rec = doRequest(t, mux, "POST", "/blocks/"+block.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, item))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add item after revert to manual: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
