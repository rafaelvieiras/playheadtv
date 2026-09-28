package catalogsync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

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
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES ('ms1', ?, 'local_folder', 'seed', 'push', ?, ?)`,
		testTenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}
	return db
}

// seedItem inserts one catalog_item row directly, bypassing Upsert, so tests
// can freely set genres/production_year/kind without going through Item's
// pull/push-shared shape.
func seedItem(t *testing.T, db storage.DB, id, kind, title, genres string, year *int64) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, genres, production_year)
		 VALUES (?, ?, 'ms1', ?, ?, ?, 1000, ?, ?, ?)`,
		id, testTenantID, "ext-"+id, kind, title, now, genres, year,
	); err != nil {
		t.Fatalf("seed catalog_item %s: %v", id, err)
	}
	return id
}

func seedPool(t *testing.T, db storage.DB, id, name string, criteria *string) string {
	t.Helper()
	ctx := context.Background()
	if err := db.Exec(ctx,
		"INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES (?, ?, ?, ?, ?)",
		id, testTenantID, name, criteria, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed filler_pool %s: %v", id, err)
	}
	return id
}

func poolItemCatalogIDs(t *testing.T, db storage.DB, poolID string) []string {
	t.Helper()
	var ids []string
	if err := db.Query(context.Background(), &ids,
		"SELECT catalog_item_id FROM filler_pool_item WHERE filler_pool_id = ? ORDER BY catalog_item_id", poolID,
	); err != nil {
		t.Fatalf("query filler_pool_item: %v", err)
	}
	return ids
}

func seedBlock(t *testing.T, db storage.DB, id, name string, criteria *string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := db.Exec(ctx,
		"INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, testTenantID, name, criteria, now, now,
	); err != nil {
		t.Fatalf("seed block %s: %v", id, err)
	}
	return id
}

func blockItemCatalogIDs(t *testing.T, db storage.DB, blockID string) []string {
	t.Helper()
	var ids []string
	if err := db.Query(context.Background(), &ids,
		"SELECT catalog_item_id FROM block_item WHERE block_id = ? ORDER BY position", blockID,
	); err != nil {
		t.Fatalf("query block_item: %v", err)
	}
	return ids
}

func strPtr(s string) *string { return &s }

func TestResolveDynamicPoolsGenreCriteria(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)
	seedItem(t, db, "c2", "movie", "Action One", "Action", nil)
	seedItem(t, db, "c3", "movie", "Comedy Two", "Comedy,Romance", nil)

	criteria := strPtr(`{"kind":"movie","genres":["Comedy"]}`)
	poolID := seedPool(t, db, "pool1", "Comedy", criteria)

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools: %v", err)
	}

	got := poolItemCatalogIDs(t, db, poolID)
	want := []string{"c1", "c3"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pool items = %v, want %v", got, want)
	}
}

// TestResolveDynamicPoolsGenreLanguageVariants verifies the documented
// behavior that no genre normalization happens: a Portuguese genre name
// ("Comédia") only matches when listed explicitly alongside the English one.
func TestResolveDynamicPoolsGenreLanguageVariants(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)
	seedItem(t, db, "c2", "movie", "Comedia Two", "Comédia", nil)

	poolOnlyEnglish := seedPool(t, db, "pool-en", "English only", strPtr(`{"kind":"movie","genres":["Comedy"]}`))
	poolBoth := seedPool(t, db, "pool-both", "Both spellings", strPtr(`{"kind":"movie","genres":["Comedy","Comédia"]}`))

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools: %v", err)
	}

	if got := poolItemCatalogIDs(t, db, poolOnlyEnglish); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("english-only pool items = %v, want [c1]", got)
	}
	if got := poolItemCatalogIDs(t, db, poolBoth); len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("both-spellings pool items = %v, want [c1 c2]", got)
	}
}

func TestResolveDynamicPoolsReflectsGenreRewrite(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)

	criteria := strPtr(`{"kind":"movie","genres":["Comedy"]}`)
	poolID := seedPool(t, db, "pool1", "Comedy", criteria)

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools (1st): %v", err)
	}
	if got := poolItemCatalogIDs(t, db, poolID); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("pool items (1st) = %v, want [c1]", got)
	}

	// c1's genres get rewritten (e.g. a resync where Jellyfin changed the
	// item's genre) so it no longer matches "Comedy".
	if err := db.Exec(context.Background(), "UPDATE catalog_item SET genres = ? WHERE id = ?", "Action", "c1"); err != nil {
		t.Fatalf("update genres: %v", err)
	}

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools (2nd): %v", err)
	}
	if got := poolItemCatalogIDs(t, db, poolID); len(got) != 0 {
		t.Fatalf("pool items (2nd) = %v, want empty after c1 stopped matching", got)
	}
}

func TestResolveDynamicPoolsYearRange(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Old Movie", "", int64Ptr(1985))
	seedItem(t, db, "c2", "movie", "90s Movie", "", int64Ptr(1995))
	seedItem(t, db, "c3", "movie", "New Movie", "", int64Ptr(2020))

	yearFrom, yearTo := 1990, 2000
	encoded, err := json.Marshal(poolCriteria{Kind: "movie", YearFrom: &yearFrom, YearTo: &yearTo})
	if err != nil {
		t.Fatalf("marshal criteria: %v", err)
	}
	criteria := string(encoded)
	poolID := seedPool(t, db, "pool1", "90s", &criteria)

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools: %v", err)
	}
	got := poolItemCatalogIDs(t, db, poolID)
	if len(got) != 1 || got[0] != "c2" {
		t.Fatalf("pool items = %v, want [c2]", got)
	}
}

func TestResolveDynamicPoolsSkipsManualPools(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)
	poolID := seedPool(t, db, "pool1", "Manual", nil)

	// Manually add an item unrelated to any criteria — ResolveDynamicPools
	// must leave it untouched since criteria IS NULL for this pool.
	if err := db.Exec(context.Background(),
		"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, 1)",
		uuid.NewString(), poolID, "c1",
	); err != nil {
		t.Fatalf("seed filler_pool_item: %v", err)
	}

	if err := ResolveDynamicPools(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicPools: %v", err)
	}

	got := poolItemCatalogIDs(t, db, poolID)
	if len(got) != 1 || got[0] != "c1" {
		t.Fatalf("manual pool items = %v, want untouched [c1]", got)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestResolveDynamicBlocksGenreCriteria(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)
	seedItem(t, db, "c2", "movie", "Action One", "Action", nil)
	seedItem(t, db, "c3", "movie", "Comedy Two", "Comedy,Romance", nil)

	criteria := strPtr(`{"kind":"movie","genres":["Comedy"]}`)
	blockID := seedBlock(t, db, "block1", "Comedy", criteria)

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks: %v", err)
	}

	got := blockItemCatalogIDs(t, db, blockID)
	want := []string{"c1", "c3"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("block items = %v, want %v", got, want)
	}
}

func TestResolveDynamicBlocksReflectsGenreRewrite(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)

	criteria := strPtr(`{"kind":"movie","genres":["Comedy"]}`)
	blockID := seedBlock(t, db, "block1", "Comedy", criteria)

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks (1st): %v", err)
	}
	if got := blockItemCatalogIDs(t, db, blockID); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("block items (1st) = %v, want [c1]", got)
	}

	// c1's genres get rewritten (e.g. a resync where Jellyfin changed the
	// item's genre) so it no longer matches "Comedy".
	if err := db.Exec(context.Background(), "UPDATE catalog_item SET genres = ? WHERE id = ?", "Action", "c1"); err != nil {
		t.Fatalf("update genres: %v", err)
	}

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks (2nd): %v", err)
	}
	if got := blockItemCatalogIDs(t, db, blockID); len(got) != 0 {
		t.Fatalf("block items (2nd) = %v, want empty after c1 stopped matching", got)
	}
}

func TestResolveDynamicBlocksYearRange(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Old Movie", "", int64Ptr(1985))
	seedItem(t, db, "c2", "movie", "90s Movie", "", int64Ptr(1995))
	seedItem(t, db, "c3", "movie", "New Movie", "", int64Ptr(2020))

	yearFrom, yearTo := 1990, 2000
	encoded, err := json.Marshal(poolCriteria{Kind: "movie", YearFrom: &yearFrom, YearTo: &yearTo})
	if err != nil {
		t.Fatalf("marshal criteria: %v", err)
	}
	criteria := string(encoded)
	blockID := seedBlock(t, db, "block1", "90s", &criteria)

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks: %v", err)
	}
	got := blockItemCatalogIDs(t, db, blockID)
	if len(got) != 1 || got[0] != "c2" {
		t.Fatalf("block items = %v, want [c2]", got)
	}
}

func TestResolveDynamicBlocksSkipsManualBlocks(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c1", "movie", "Comedy One", "Comedy", nil)
	blockID := seedBlock(t, db, "block1", "Manual", nil)

	// Manually add an item unrelated to any criteria — ResolveDynamicBlocks
	// must leave it untouched since criteria IS NULL for this block.
	if err := db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES (?, ?, 0, ?, NULL)",
		uuid.NewString(), blockID, "c1",
	); err != nil {
		t.Fatalf("seed block_item: %v", err)
	}

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks: %v", err)
	}

	got := blockItemCatalogIDs(t, db, blockID)
	if len(got) != 1 || got[0] != "c1" {
		t.Fatalf("manual block items = %v, want untouched [c1]", got)
	}
}

// TestResolveDynamicBlocksPositionIsSequential proves block_item.position
// comes out 0..N-1 in the same order matchCriteria (ORDER BY title) returns,
// not just "some order" — the materializer's fillLoopingBlock walk depends
// on position being a stable, gap-free sequence to loop over.
func TestResolveDynamicBlocksPositionIsSequential(t *testing.T) {
	db := newTestDB(t)

	seedItem(t, db, "c3", "movie", "Charlie Comedy", "Comedy", nil)
	seedItem(t, db, "c1", "movie", "Alpha Comedy", "Comedy", nil)
	seedItem(t, db, "c2", "movie", "Bravo Comedy", "Comedy", nil)

	criteria := strPtr(`{"kind":"movie","genres":["Comedy"]}`)
	blockID := seedBlock(t, db, "block1", "Comedy", criteria)

	if err := ResolveDynamicBlocks(context.Background(), db, testTenantID); err != nil {
		t.Fatalf("ResolveDynamicBlocks: %v", err)
	}

	var rows []struct {
		Position      int
		CatalogItemID string
	}
	if err := db.Query(context.Background(), &rows,
		"SELECT position, catalog_item_id FROM block_item WHERE block_id = ? ORDER BY position", blockID,
	); err != nil {
		t.Fatalf("query block_item: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("block items = %d, want 3", len(rows))
	}
	wantOrder := []string{"c1", "c2", "c3"} // title order: Alpha, Bravo, Charlie
	for i, row := range rows {
		if row.Position != i {
			t.Errorf("row[%d].Position = %d, want %d", i, row.Position, i)
		}
		if row.CatalogItemID != wantOrder[i] {
			t.Errorf("row[%d].CatalogItemID = %q, want %q", i, row.CatalogItemID, wantOrder[i])
		}
	}
}
