package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestCatalogItemsSearch(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	syncBody := `{
		"items": [
			{"external_id":"ext-1","kind":"movie","title":"The Matrix","duration_ms":8000000},
			{"external_id":"ext-2","kind":"episode","title":"The Office S01E01","series_title":"The Office","duration_ms":1300000},
			{"external_id":"ext-3","kind":"movie","title":"Matrix Reloaded","duration_ms":8200000}
		]
	}`
	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?q=matrix", "")
	var items []domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 2 {
		t.Fatalf("q=matrix results = %d, want 2", len(items))
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?kind=episode", "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("kind=episode results = %d, want 1", len(items))
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?limit=1", "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("limit=1 results = %d, want 1", len(items))
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?limit=abc", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=abc: status = %d, want 400", rec.Code)
	}
}

func TestGetCatalogItem(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	syncBody := `{"items": [{"external_id":"ext-1","kind":"movie","title":"The Matrix","duration_ms":8000000}]}`
	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?media_source_id="+ms.ID, "")
	var items []domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("precondition: catalog items = %d, want 1", len(items))
	}

	rec = doRequest(t, mux, "GET", "/catalog-items/"+items[0].ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var item domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &item)
	if item.ExternalID != "ext-1" {
		t.Errorf("ExternalID = %q, want ext-1", item.ExternalID)
	}

	rec = doRequest(t, mux, "GET", "/catalog-items/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", rec.Code)
	}
}
