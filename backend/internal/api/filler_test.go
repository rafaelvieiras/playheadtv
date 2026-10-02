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

func TestFillerPoolsCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Commercials"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	rec = doRequest(t, mux, "GET", "/filler-pools", "")
	var list []domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "PUT", "/filler-pools/"+pool.ID, `{"name":"Commercials Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "DELETE", "/filler-pools/"+pool.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestFillerPoolItems(t *testing.T) {
	db, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	item := seedCatalogItem(t, db, "filler-1")

	rec = doRequest(t, mux, "POST", "/filler-pools/"+pool.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q,"weight":3}`, item))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add item: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var poolItem domain.FillerPoolItem
	json.Unmarshal(rec.Body.Bytes(), &poolItem)
	if poolItem.Weight != 3 {
		t.Errorf("Weight = %d, want 3", poolItem.Weight)
	}

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var items []domain.FillerPoolItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}

	rec = doRequest(t, mux, "DELETE", "/filler-pools/"+pool.ID+"/items/"+poolItem.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete item: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 0 {
		t.Fatalf("items after delete = %d, want 0", len(items))
	}
}

func TestFillerRulesCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	body := fmt.Sprintf(`{"scope_type":"channel","scope_id":"chan-1","kind":"midroll","filler_pool_id":%q,"selection_strategy":"random"}`, pool.ID)
	rec = doRequest(t, mux, "POST", "/filler-rules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var rule domain.FillerRule
	json.Unmarshal(rec.Body.Bytes(), &rule)
	if rule.Scope != domain.ScopeChannel || rule.Selection != domain.SelectionRandom {
		t.Fatalf("unexpected rule: %+v", rule)
	}

	rec = doRequest(t, mux, "GET", "/filler-rules", "")
	var list []domain.FillerRule
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list length = %d, want 1", len(list))
	}

	rec = doRequest(t, mux, "GET", "/filler-rules/"+rule.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	updateBody := fmt.Sprintf(`{"scope_type":"block","scope_id":"block-1","kind":"tail","filler_pool_id":%q,"selection_strategy":"weighted"}`, pool.ID)
	rec = doRequest(t, mux, "PUT", "/filler-rules/"+rule.ID, updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.FillerRule
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Scope != domain.ScopeBlock || updated.Kind != domain.FillerTail {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	rec = doRequest(t, mux, "DELETE", "/filler-rules/"+rule.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/filler-rules/"+rule.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestFillerRuleInvalidKind(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	body := fmt.Sprintf(`{"scope_type":"channel","scope_id":"chan-1","kind":"bogus","filler_pool_id":%q,"selection_strategy":"random"}`, pool.ID)
	rec = doRequest(t, mux, "POST", "/filler-rules", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// seedGenreCatalogItem is seedCatalogItem plus a genres value, so dynamic
// filler_pool criteria tests have something to actually match against.
func seedGenreCatalogItem(t *testing.T, db storage.DB, id, genres string) string {
	t.Helper()
	ctx := context.Background()
	msID := "ms-genre-" + id
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'local_folder', 'seed', 'push', ?, ?)`,
		msID, DefaultTenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	itemID := "ci-genre-" + id
	if err := db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, genres)
		 VALUES (?, ?, ?, ?, 'movie', ?, 1000, ?, ?)`,
		itemID, DefaultTenantID, msID, "ext-genre-"+id, "Item "+id, now, genres,
	); err != nil {
		t.Fatalf("seed catalog_item: %v", err)
	}
	// The genre field of a query reads the genre links, which a sync writes.
	if err := catalogsync.BackfillGenreLinks(ctx, db); err != nil {
		t.Fatalf("backfill genre links: %v", err)
	}
	return itemID
}

func TestFillerPoolWithCriteriaPopulatesItems(t *testing.T) {
	db, mux := newTestDB(t)

	comedyItem := seedGenreCatalogItem(t, db, "1", "Comedy")
	seedGenreCatalogItem(t, db, "2", "Action") // shouldn't match

	rec := doRequest(t, mux, "POST", "/filler-pools",
		`{"name":"Comedy Block","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)
	if pool.Criteria == nil {
		t.Fatal("pool.Criteria = nil, want set")
	}

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var items []domain.FillerPoolItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 || items[0].CatalogItemID != comedyItem {
		t.Fatalf("items = %+v, want exactly [%s]", items, comedyItem)
	}
}

func TestFillerPoolWithCriteriaRejectsManualItemWrites(t *testing.T) {
	db, mux := newTestDB(t)
	item := seedGenreCatalogItem(t, db, "1", "Comedy")

	rec := doRequest(t, mux, "POST", "/filler-pools",
		`{"name":"Comedy Block","criteria":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	rec = doRequest(t, mux, "POST", "/filler-pools/"+pool.ID+"/items", fmt.Sprintf(`{"catalog_item_id":%q}`, item))
	if rec.Code != http.StatusConflict {
		t.Fatalf("add item on dynamic pool: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var items []domain.FillerPoolItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (only from criteria)", len(items))
	}
	rec = doRequest(t, mux, "DELETE", "/filler-pools/"+pool.ID+"/items/"+items[0].ID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete item on dynamic pool: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

func TestFillerPoolCriteriaValidation(t *testing.T) {
	db, mux := newTestDB(t)

	for name, criteria := range invalidCriteria {
		rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bad","criteria":`+criteria+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
	}
	var n []int
	if err := db.Query(context.Background(), &n, "SELECT COUNT(*) FROM filler_pool"); err != nil || n[0] != 0 {
		t.Errorf("pools after rejected writes = %v %v, want 0", n, err)
	}
}
