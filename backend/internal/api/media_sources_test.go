package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestProbeJellyfin(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()

	if !probeJellyfin(ok.URL, http.DefaultClient) {
		t.Error("probeJellyfin(reachable server) = false, want true")
	}
	if probeJellyfin("http://127.0.0.1:1", http.DefaultClient) {
		t.Error("probeJellyfin(unreachable) = true, want false")
	}

	notOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer notOK.Close()
	if probeJellyfin(notOK.URL, http.DefaultClient) {
		t.Error("probeJellyfin(500 response) = true, want false")
	}
}

func TestMediaSourceCreatePullMode(t *testing.T) {
	_, mux := newTestDB(t)

	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer jf.Close()

	body := fmt.Sprintf(`{"type":"jellyfin","name":"Home","base_url":%q,"credential":"secret-token"}`, jf.URL)
	rec := doRequest(t, mux, "POST", "/media-sources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ms domain.MediaSource
	if err := json.Unmarshal(rec.Body.Bytes(), &ms); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ms.SyncMode != domain.SyncPull {
		t.Errorf("SyncMode = %q, want pull", ms.SyncMode)
	}
	if ms.LastProbeOK == nil || !*ms.LastProbeOK {
		t.Errorf("LastProbeOK = %v, want true", ms.LastProbeOK)
	}
	if strings.Contains(rec.Body.String(), "secret-token") {
		t.Error("response leaked the raw credential")
	}
}

func TestMediaSourceCreatePushModeUnreachable(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"jellyfin","name":"Remote","base_url":"http://127.0.0.1:1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)
	if ms.SyncMode != domain.SyncPush {
		t.Errorf("SyncMode = %q, want push", ms.SyncMode)
	}
	if ms.LastProbeOK == nil || *ms.LastProbeOK {
		t.Errorf("LastProbeOK = %v, want false", ms.LastProbeOK)
	}
}

func TestMediaSourceCreateLocalFolder(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)
	if ms.SyncMode != domain.SyncPush {
		t.Errorf("SyncMode = %q, want push", ms.SyncMode)
	}
	if ms.LastProbeAt != nil {
		t.Errorf("LastProbeAt = %v, want nil (no probe attempted)", ms.LastProbeAt)
	}
}

func TestMediaSourceCreatePlexNeverProbes(t *testing.T) {
	_, mux := newTestDB(t)

	// Plex isn't implemented in v1 (domain.SourcePlex): even pointed at a
	// server that would answer Jellyfin's probe endpoint, it must stay push
	// without attempting that probe (Plex doesn't speak that API).
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer jf.Close()

	body := fmt.Sprintf(`{"type":"plex","name":"Plex Server","base_url":%q}`, jf.URL)
	rec := doRequest(t, mux, "POST", "/media-sources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)
	if ms.SyncMode != domain.SyncPush {
		t.Errorf("SyncMode = %q, want push", ms.SyncMode)
	}
	if ms.LastProbeAt != nil || ms.LastProbeOK != nil {
		t.Errorf("LastProbeAt/OK = %v/%v, want nil/nil (no probe attempted)", ms.LastProbeAt, ms.LastProbeOK)
	}
}

func TestMediaSourceRequiresBaseURLUnlessLocalFolder(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"jellyfin","name":"Missing base url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMediaSourceCRUDAndProbeRerunOnBaseURLChange(t *testing.T) {
	_, mux := newTestDB(t)

	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer jf.Close()

	body := fmt.Sprintf(`{"type":"jellyfin","name":"Home","base_url":%q}`, jf.URL)
	rec := doRequest(t, mux, "POST", "/media-sources", body)
	var created domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doRequest(t, mux, "GET", "/media-sources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}

	rec = doRequest(t, mux, "GET", "/media-sources/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	// Change base_url to an unreachable address: probe should re-run and flip to push.
	updateBody := `{"type":"jellyfin","name":"Home","base_url":"http://127.0.0.1:1"}`
	rec = doRequest(t, mux, "PUT", "/media-sources/"+created.ID, updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.SyncMode != domain.SyncPush {
		t.Errorf("SyncMode after base_url change = %q, want push", updated.SyncMode)
	}

	rec = doRequest(t, mux, "DELETE", "/media-sources/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	rec = doRequest(t, mux, "GET", "/media-sources/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
}

func TestCatalogSyncBlockedWhenPullMode(t *testing.T) {
	_, mux := newTestDB(t)

	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer jf.Close()

	body := fmt.Sprintf(`{"type":"jellyfin","name":"Home","base_url":%q}`, jf.URL)
	rec := doRequest(t, mux, "POST", "/media-sources", body)
	var created domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.SyncMode != domain.SyncPull {
		t.Fatalf("precondition: SyncMode = %q, want pull", created.SyncMode)
	}

	rec = doRequest(t, mux, "POST", "/media-sources/"+created.ID+"/catalog-sync", `{"items":[]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("catalog-sync on pull source: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

// TestMediaSourceCredentialEncryptedAtRest proves credential_cipher actually
// holds AES-256-GCM ciphertext, not the raw credential: it reads the column
// straight out of the database (bypassing the API, which never returns it —
// see the "response leaked the raw credential" check above) and confirms the
// bytes both differ from the plaintext and decrypt back to it under the same
// key tests register with (testEncKey).
func TestMediaSourceCredentialEncryptedAtRest(t *testing.T) {
	db, mux := newTestDB(t)

	const plaintext = "minha-api-key-secreta"
	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"jellyfin","name":"Home","base_url":"http://127.0.0.1:1","credential":"`+plaintext+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var ms domain.MediaSource
	if err := json.Unmarshal(rec.Body.Bytes(), &ms); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var row struct {
		CredentialCipher []byte
	}
	err := db.Query(context.Background(), &row,
		"SELECT credential_cipher FROM media_source WHERE id = ?", ms.ID,
	)
	if err != nil {
		t.Fatalf("query raw credential_cipher: %v", err)
	}

	if bytes.Equal(row.CredentialCipher, []byte(plaintext)) {
		t.Fatal("credential_cipher stored in plaintext")
	}

	decrypted, err := crypto.Decrypt(testEncKey, row.CredentialCipher)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != plaintext {
		t.Fatalf("Decrypt(credential_cipher) = %q, want %q", decrypted, plaintext)
	}
}

func TestPullSyncEndpointRejectsWrongMode(t *testing.T) {
	_, mux := newTestDB(t)

	// local_folder is push-only, never jellyfin — the endpoint must reject
	// both "wrong type" and "wrong mode" in one shot here.
	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/pull-sync", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("pull-sync on local_folder source: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPullSyncEndpointSuccess(t *testing.T) {
	_, mux := newTestDB(t)

	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			w.WriteHeader(http.StatusOK)
		case "/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"TotalRecordCount": 2,
				"Items": []map[string]any{
					{"Id": "ext-1", "Name": "Movie One", "Type": "Movie", "RunTimeTicks": 60_000_000_000},
					{"Id": "ext-2", "Name": "Episode One", "Type": "Episode", "RunTimeTicks": 15_000_000_000, "SeriesName": "Show"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer jf.Close()

	body := fmt.Sprintf(`{"type":"jellyfin","name":"Home","base_url":%q,"credential":"secret-token"}`, jf.URL)
	rec := doRequest(t, mux, "POST", "/media-sources", body)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)
	if ms.SyncMode != domain.SyncPull {
		t.Fatalf("precondition: SyncMode = %q, want pull", ms.SyncMode)
	}

	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/pull-sync", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pull-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			ItemsUpserted int `json:"items_upserted"`
			ItemsDeleted  int `json:"items_deleted"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Result.ItemsUpserted != 2 {
		t.Errorf("items_upserted = %d, want 2", resp.Result.ItemsUpserted)
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?media_source_id="+ms.ID, "")
	var items []domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 2 {
		t.Fatalf("catalog items after pull-sync = %d, want 2", len(items))
	}
}

func TestCatalogSyncUpsertAndDelete(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	syncBody := `{
		"items": [
			{"external_id":"ext-1","kind":"movie","title":"Movie One","duration_ms":6000000},
			{"external_id":"ext-2","kind":"episode","title":"Episode One","series_title":"Show","duration_ms":1500000}
		]
	}`
	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?media_source_id="+ms.ID, "")
	var items []domain.CatalogItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 2 {
		t.Fatalf("catalog items after sync = %d, want 2", len(items))
	}

	// Re-sync: update ext-1's title, delete ext-2.
	syncBody2 := `{
		"items": [
			{"external_id":"ext-1","kind":"movie","title":"Movie One Renamed","duration_ms":6000000}
		],
		"deleted_external_ids": ["ext-2"]
	}`
	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync", syncBody2)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog-sync 2: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/catalog-items?media_source_id="+ms.ID, "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("catalog items after re-sync = %d, want 1", len(items))
	}
	if items[0].Title != "Movie One Renamed" {
		t.Errorf("title = %q, want %q", items[0].Title, "Movie One Renamed")
	}
}

// TestCatalogSyncPushResolvesDynamicPools proves catalogSync (push) calls
// catalogsync.ResolveDynamicPools after every sync, same as pull's SyncOne:
// a pre-existing dynamic pool automatically drops an item once a push sync
// deletes it, with no manual re-resolve call. catalogSyncItem's push JSON
// doesn't carry genres/production_year (only pull's Jellyfin mapper does —
// see catalogsync.Item's doc comment), so the matching genre is set directly
// on the row after the initial push add, and the sync being exercised here is
// the one that removes it via deleted_external_ids.
func TestCatalogSyncPushResolvesDynamicPools(t *testing.T) {
	db, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
	var ms domain.MediaSource
	json.Unmarshal(rec.Body.Bytes(), &ms)

	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync",
		`{"items":[{"external_id":"ext-1","kind":"movie","title":"Comedy One","duration_ms":6000000}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial catalog-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := db.Exec(context.Background(), "UPDATE catalog_item SET genres = ? WHERE media_source_id = ? AND external_id = ?",
		"Comedy", ms.ID, "ext-1",
	); err != nil {
		t.Fatalf("set genres directly: %v", err)
	}
	if err := catalogsync.BackfillGenreLinks(context.Background(), db); err != nil {
		t.Fatalf("backfill genre links: %v", err)
	}

	rec = doRequest(t, mux, "POST", "/filler-pools", `{"name":"Comedy","collection_id":"`+newCollection(t, mux, `{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"},{"field":"genre","op":"equals","value":"Comedy"}]}`)+`"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	var items []domain.FillerPoolItem
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("pool items before delete sync = %d, want 1", len(items))
	}

	rec = doRequest(t, mux, "POST", "/media-sources/"+ms.ID+"/catalog-sync",
		`{"items":[],"deleted_external_ids":["ext-1"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete catalog-sync: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, mux, "GET", "/filler-pools/"+pool.ID+"/items", "")
	json.Unmarshal(rec.Body.Bytes(), &items)
	if len(items) != 0 {
		t.Fatalf("pool items after delete sync = %d, want 0 (dynamic pool should auto-update)", len(items))
	}
}
