package pullsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// newSyncer builds a Syncer backed by a plain, never-Start'ed
// materializer.Materializer — every test in this file only needs New's
// mat.Trigger() call inside SyncOne not to panic, not an actual
// materialization run.
func newSyncer(db *storage.SQLite) *Syncer {
	// No plugin host: these tests exercise the Jellyfin pull path.
	return New(db, testEncKey, materializer.New(db, time.Hour), nil)
}

var testEncKey = [32]byte{
	1, 2, 3, 4, 5, 6, 7, 8,
	9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24,
	25, 26, 27, 28, 29, 30, 31, 32,
}

const testTenantID = "tenant1"

func newTestDB(t *testing.T) *storage.SQLite {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "playheadtv.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		testTenantID, "Test Tenant", string(domain.TierSelfHosted), now,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return db
}

// seedJellyfinSource inserts a jellyfin+pull media_source pointing at
// baseURL, with credential (if non-empty) encrypted under testEncKey.
func seedJellyfinSource(t *testing.T, db *storage.SQLite, id, baseURL, credential string) domain.MediaSource {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var cipher []byte
	if credential != "" {
		sealed, err := crypto.Encrypt(testEncKey, []byte(credential))
		if err != nil {
			t.Fatalf("crypto.Encrypt: %v", err)
		}
		cipher = sealed
	}
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'jellyfin', 'Jellyfin', ?, ?, 'pull', ?, ?)`,
		id, testTenantID, baseURL, cipher, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	return domain.MediaSource{
		ID: id, TenantID: testTenantID, Type: domain.SourceJellyfin,
		BaseURL: &baseURL, CredentialCipher: cipher, SyncMode: domain.SyncPull,
	}
}

func TestSyncOneRejectsNonPullSource(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UnixMilli()
	if err := db.Exec(context.Background(),
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES ('nas', ?, 'local_folder', 'NAS', 'push', ?, ?)`,
		testTenantID, now, now,
	); err != nil {
		t.Fatalf("seed local_folder source: %v", err)
	}

	s := newSyncer(db)
	_, err := s.SyncOne(context.Background(), domain.MediaSource{
		ID: "nas", TenantID: testTenantID, Type: domain.SourceLocalFolder, SyncMode: domain.SyncPush,
	})
	if !errors.Is(err, ErrNotPullSource) {
		t.Errorf("err = %v, want ErrNotPullSource", err)
	}
}

func TestSyncOneUpsertAndDelete(t *testing.T) {
	db := newTestDB(t)

	itemsA := []jellyfinItem{
		{Id: "ext-1", Name: "Movie One", Type: "Movie", RunTimeTicks: 60_000_000_000},
		{Id: "ext-2", Name: "Episode One", Type: "Episode", RunTimeTicks: 15_000_000_000, SeriesName: strPtr("Show")},
	}
	itemsB := []jellyfinItem{itemsA[0]} // ext-2 "removed" from Jellyfin on the second sync

	current := &itemsA
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: *current, TotalRecordCount: len(*current)})
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")
	s := newSyncer(db)

	result, err := s.SyncOne(context.Background(), ms)
	if err != nil {
		t.Fatalf("SyncOne (1st): %v", err)
	}
	if result.ItemsUpserted != 2 || result.ItemsDeleted != 0 {
		t.Errorf("result = %+v, want 2 upserted / 0 deleted", result)
	}

	var items []domain.CatalogItem
	if err := db.Query(context.Background(), &items, "SELECT id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at FROM catalog_item WHERE media_source_id = ? ORDER BY external_id", ms.ID); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("catalog_item rows = %d, want 2", len(items))
	}
	if items[0].Kind != domain.CatalogMovie || items[0].DurationMs != 6_000_000 {
		t.Errorf("items[0] = %+v, want movie/6000000ms", items[0])
	}
	if items[1].Kind != domain.CatalogEpisode || items[1].SeriesTitle == nil || *items[1].SeriesTitle != "Show" {
		t.Errorf("items[1] = %+v, want episode/series \"Show\"", items[1])
	}

	current = &itemsB
	result, err = s.SyncOne(context.Background(), ms)
	if err != nil {
		t.Fatalf("SyncOne (2nd): %v", err)
	}
	if result.ItemsUpserted != 1 || result.ItemsDeleted != 1 {
		t.Errorf("result (2nd) = %+v, want 1 upserted / 1 deleted", result)
	}

	items = nil
	if err := db.Query(context.Background(), &items, "SELECT id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at FROM catalog_item WHERE media_source_id = ?", ms.ID); err != nil {
		t.Fatalf("query catalog_item (2nd): %v", err)
	}
	if len(items) != 1 || items[0].ExternalID != "ext-1" {
		t.Fatalf("catalog_item after 2nd sync = %+v, want only ext-1", items)
	}

	var refreshed domain.MediaSource
	if err := db.Query(context.Background(), &refreshed, "SELECT id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at FROM media_source WHERE id = ?", ms.ID); err != nil {
		t.Fatalf("query media_source: %v", err)
	}
	if refreshed.LastSyncedAt == nil {
		t.Error("LastSyncedAt = nil, want set after sync")
	}
}

// TestSyncOneEpisodeInheritsSeriesGenre proves the full SyncOne path — not
// just mapJellyfinItem in isolation — resolves an episode's genre from its
// parent series via GET /Users then GET /Items/{seriesId}, and that the
// resulting catalog_item row carries both genres and production_year.
func TestSyncOneEpisodeInheritsSeriesGenre(t *testing.T) {
	db := newTestDB(t)

	items := []jellyfinItem{
		{
			Id: "movie-1", Name: "A Movie", Type: "Movie", RunTimeTicks: 65_316_370_000,
			Genres: []string{"Comedy"}, ProductionYear: int64Ptr(2019),
		},
		{
			Id: "ep-1", Name: "Pilot", Type: "Episode", RunTimeTicks: 12_000_000_000,
			SeriesName: strPtr("Some Show"), SeriesId: strPtr("series-1"),
			ProductionYear: int64Ptr(2021),
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users":
			json.NewEncoder(w).Encode([]jellyfinUser{{Id: "user-1"}})
		case r.URL.Path == "/Items/series-1":
			if r.URL.Query().Get("userId") != "user-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(struct {
				Genres []string `json:"Genres"`
			}{Genres: []string{"Comedy", "Romance"}})
		default:
			json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: len(items)})
		}
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")
	s := newSyncer(db)

	result, err := s.SyncOne(context.Background(), ms)
	if err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if result.ItemsUpserted != 2 {
		t.Fatalf("ItemsUpserted = %d, want 2", result.ItemsUpserted)
	}

	var catItems []domain.CatalogItem
	if err := db.Query(context.Background(), &catItems,
		"SELECT id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at, genres, production_year FROM catalog_item WHERE media_source_id = ? ORDER BY external_id", ms.ID,
	); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(catItems) != 2 {
		t.Fatalf("catalog_item rows = %d, want 2", len(catItems))
	}

	// ORDER BY external_id sorts "ep-1" before "movie-1".
	episode, movie := catItems[0], catItems[1]
	if movie.ExternalID != "movie-1" || movie.Genres != "Comedy" {
		t.Errorf("movie = %+v, want genres \"Comedy\"", movie)
	}
	if movie.ProductionYear == nil || *movie.ProductionYear != 2019 {
		t.Errorf("movie.ProductionYear = %v, want 2019", movie.ProductionYear)
	}
	if episode.ExternalID != "ep-1" || episode.Genres != "Comedy,Romance" {
		t.Errorf("episode = %+v, want genres \"Comedy,Romance\" inherited from series-1", episode)
	}
	if episode.ProductionYear == nil || *episode.ProductionYear != 2021 {
		t.Errorf("episode.ProductionYear = %v, want 2021", episode.ProductionYear)
	}
}

func TestSyncOneCredentialDecryptedCorrectly(t *testing.T) {
	db := newTestDB(t)

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		if gotHeader != `MediaBrowser Token="secret-token"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(jellyfinItemsResponse{})
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "secret-token")
	s := newSyncer(db)
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if gotHeader != `MediaBrowser Token="secret-token"` {
		t.Errorf("Authorization received = %q, want %q", gotHeader, `MediaBrowser Token="secret-token"`)
	}
}

func TestSyncOneRejectedKeyIsReportedAsSuch(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "revoked")
	_, err := newSyncer(db).SyncOne(context.Background(), ms)
	if !errors.Is(err, ErrJellyfinAuth) {
		t.Fatalf("SyncOne error = %v, want ErrJellyfinAuth", err)
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error %q does not name the API key", err)
	}
}

func TestSyncOneNetworkErrorDoesNotPanic(t *testing.T) {
	db := newTestDB(t)
	ms := seedJellyfinSource(t, db, "src1", "http://127.0.0.1:1", "token")
	s := newSyncer(db)
	if _, err := s.SyncOne(context.Background(), ms); err == nil {
		t.Error("SyncOne against a closed port succeeded, want error")
	}
}

// TestSyncOneResolvesDynamicPools proves SyncOne calls
// catalogsync.ResolveDynamicPools after every sync: a pre-existing dynamic
// pool picks up a newly-synced matching item, and later drops it once that
// item disappears from Jellyfin — all without any manual re-resolve call.
func TestSyncOneResolvesDynamicPools(t *testing.T) {
	db := newTestDB(t)

	items := []jellyfinItem{
		{Id: "ext-1", Name: "Comedy One", Type: "Movie", RunTimeTicks: 65_316_370_000, Genres: []string{"Comedy"}},
		{Id: "ext-2", Name: "Action One", Type: "Movie", RunTimeTicks: 65_316_370_000, Genres: []string{"Action"}},
	}
	current := &items
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: *current, TotalRecordCount: len(*current)})
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")

	criteria := `{"kind":"movie","genres":["Comedy"]}`
	poolID := "pool1"
	if err := db.Exec(context.Background(),
		"INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES (?, ?, ?, ?, ?)",
		poolID, testTenantID, "Comedy", criteria, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed filler_pool: %v", err)
	}

	s := newSyncer(db)
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne (1st): %v", err)
	}

	var poolItems []string
	if err := db.Query(context.Background(), &poolItems,
		"SELECT catalog_item_id FROM filler_pool_item WHERE filler_pool_id = ?", poolID,
	); err != nil {
		t.Fatalf("query filler_pool_item (1st): %v", err)
	}
	if len(poolItems) != 1 {
		t.Fatalf("pool items (1st) = %d, want 1 (only the comedy movie)", len(poolItems))
	}

	// ext-1 (the comedy movie) disappears from Jellyfin on the next sync.
	remaining := []jellyfinItem{items[1]}
	current = &remaining
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne (2nd): %v", err)
	}

	poolItems = nil
	if err := db.Query(context.Background(), &poolItems,
		"SELECT catalog_item_id FROM filler_pool_item WHERE filler_pool_id = ?", poolID,
	); err != nil {
		t.Fatalf("query filler_pool_item (2nd): %v", err)
	}
	if len(poolItems) != 0 {
		t.Fatalf("pool items (2nd) = %d, want 0 (dynamic pool should auto-drop the removed item)", len(poolItems))
	}
}

func TestRunIsolatesPerSourceFailures(t *testing.T) {
	db := newTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jellyfinItemsResponse{
			Items:            []jellyfinItem{{Id: "ok-1", Name: "OK Movie", Type: "Movie", RunTimeTicks: 65_316_370_000}},
			TotalRecordCount: 1,
		})
	}))
	defer srv.Close()

	seedJellyfinSource(t, db, "good", srv.URL, "token")
	seedJellyfinSource(t, db, "bad", "http://127.0.0.1:1", "token")

	s := newSyncer(db)
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var items []domain.CatalogItem
	if err := db.Query(context.Background(), &items, "SELECT id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at FROM catalog_item WHERE media_source_id = ?", "good"); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("catalog_item for good source = %d rows, want 1 (bad source's failure shouldn't block it)", len(items))
	}
}
