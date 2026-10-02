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

func TestCatalogItemsSearchAccentCaseSeriesAndFilters(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	// Accented titles are deliberate test data for accent-insensitive search.
	syncBody := `{"items": [
		{"external_id":"e1","kind":"episode","title":"Piloto","series_title":"Série Dourada","duration_ms":1000},
		{"external_id":"e2","kind":"episode","title":"Episódio Dois","series_title":"Série Dourada","duration_ms":1000},
		{"external_id":"e3","kind":"episode","title":"episodio tres","series_title":"Outra","duration_ms":1000},
		{"external_id":"m1","kind":"movie","title":"Ação Máxima","duration_ms":1000}
	]}`
	if rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody); rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync: %d %s", rec.Code, rec.Body.String())
	}

	titles := func(path string) []string {
		t.Helper()
		rec := doRequest(t, mux, "GET", path, "")
		var items []domain.CatalogItem
		json.Unmarshal(rec.Body.Bytes(), &items)
		out := []string{}
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	// Both directions, ordered by title (SQLite byte order).
	eq(titles("/catalog-items?q=episodio"), "Episódio Dois", "episodio tres")
	eq(titles("/catalog-items?q=Epis%C3%B3dio"), "Episódio Dois", "episodio tres")
	eq(titles("/catalog-items?q=acao"), "Ação Máxima")
	eq(titles("/catalog-items?q=ACAO%20MAXIMA"), "Ação Máxima")
	// Series title match.
	eq(titles("/catalog-items?q=serie%20dourada"), "Episódio Dois", "Piloto")
	// Filters, limit and offset still compose.
	eq(titles("/catalog-items?q=serie&kind=movie"))
	eq(titles("/catalog-items?q=episodio&kind=episode&limit=1"), "Episódio Dois")
	eq(titles("/catalog-items?q=episodio&limit=1&offset=1"), "episodio tres")
	eq(titles("/catalog-items?q=serie&media_source_id="+ms.ID), "Episódio Dois", "Piloto")
	eq(titles("/catalog-items?q=serie&media_source_id=nope"))
}

func TestCatalogSyncPushAcceptsMetadataAndAPIReturnsIt(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	syncBody := `{"items": [{
		"external_id":"e1","kind":"episode","title":"Pilot","series_title":"Some Show","duration_ms":1000,
		"genres":"Comedy,Drama","production_year":2020,
		"season_number":1,"episode_number":3,"episode_number_end":4,"series_external_id":"s1","season_name":"Season 1",
		"overview":"First.","content_rating":"TV-14","community_rating":8.5,"imdb_id":"tt1",
		"width":1920,"height":1080,"video_codec":"h264","container":"mkv","bitrate":5000000,
		"image_primary_tag":"p","series_image_primary_tag":"sp",
		"studios":["Acme"],"tags":["cult"],"audio_languages":["eng"],"subtitle_languages":["por"],
		"cast":[{"name":"Ana","role":"Hero"}],"directors":[{"name":"Dana"}],
		"production_locations":["Brazil"],"chapters":[{"name":"Intro","start_ms":0}]
	},{"external_id":"m1","kind":"movie","title":"Plain","duration_ms":1000}]}`
	if rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody); rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync: %d %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?kind=episode", "")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("list = %d items, want 1", len(list))
	}
	ep := list[0]
	if ep["season_number"] != float64(1) || ep["episode_number_end"] != float64(4) || ep["community_rating"] != 8.5 || ep["genres"] != "Comedy,Drama" {
		t.Errorf("list scalars wrong: %v", ep)
	}
	if studios, _ := ep["studios"].([]any); len(studios) != 1 || studios[0] != "Acme" {
		t.Errorf("list studios = %v", ep["studios"])
	}
	for _, detailOnly := range []string{"cast", "directors", "production_locations", "chapters"} {
		if _, ok := ep[detailOnly]; ok {
			t.Errorf("list response carries detail-only field %q", detailOnly)
		}
	}

	rec = doRequest(t, mux, "GET", "/catalog-items/"+ep["id"].(string), "")
	var item domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &item)
	if len(item.Cast) != 1 || item.Cast[0].Role != "Hero" || len(item.Directors) != 1 || item.Directors[0].Name != "Dana" ||
		len(item.Locations) != 1 || len(item.Chapters) != 1 || len(item.AudioLanguages) != 1 || item.SubtitleLanguages[0] != "por" {
		t.Errorf("detail wrong: %+v", item)
	}

	// An item pushed without metadata still renders, with nulls and no lists.
	rec = doRequest(t, mux, "GET", "/catalog-items?kind=movie", "")
	var movies []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &movies)
	if _, has := movies[0]["studios"]; movies[0]["season_number"] != nil || has {
		t.Errorf("plain movie = %v, want null season_number and no studios", movies[0])
	}

	// Re-push without the lists clears the links.
	if rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync",
		`{"items":[{"external_id":"e1","kind":"episode","title":"Pilot","duration_ms":1000,"studios":["Other"]}]}`); rec.Code != http.StatusOK {
		t.Fatalf("re-push: %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/catalog-items/"+ep["id"].(string), "")
	item = domain.CatalogItem{}
	json.Unmarshal(rec.Body.Bytes(), &item)
	if len(item.Cast) != 0 || len(item.Studios) != 1 || item.Studios[0] != "Other" || item.SeasonNumber != nil {
		t.Errorf("after re-push: %+v", item)
	}
}
