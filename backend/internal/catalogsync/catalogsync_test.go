package catalogsync

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestDeleteRemovesOrphanFillerPoolItem proves Delete cleans up
// filler_pool_item rows referencing the catalog_item being deleted — without
// it, the DELETE FROM catalog_item would fail on the FK constraint (no
// ON DELETE CASCADE from filler_pool_item, migrations/0001_init.sql).
func TestDeleteRemovesOrphanFillerPoolItem(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	itemID := seedItem(t, db, "c1", "movie", "Movie One", "", nil)
	poolID := seedPool(t, db, "pool1", "Manual", nil)
	if err := db.Exec(ctx,
		"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, 1)",
		uuid.NewString(), poolID, itemID,
	); err != nil {
		t.Fatalf("seed filler_pool_item: %v", err)
	}

	if err := Delete(ctx, db, "ms1", "ext-"+itemID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := poolItemCatalogIDs(t, db, poolID); len(got) != 0 {
		t.Fatalf("filler_pool_item after Delete = %v, want empty (orphan cleaned up)", got)
	}
	var remaining []string
	if err := db.Query(ctx, &remaining, "SELECT id FROM catalog_item WHERE id = ?", itemID); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("catalog_item %s still present after Delete", itemID)
	}
}

// TestDeleteRemovesOrphanBlockItem is TestDeleteRemovesOrphanFillerPoolItem's
// twin for block_item, which has the exact same no-ON-DELETE-CASCADE FK
// (migrations/0001_init.sql) — and, since dynamic blocks now populate
// block_item from the catalog the same way dynamic pools populate
// filler_pool_item, this is no longer a rare hand-curation edge case either.
func TestDeleteRemovesOrphanBlockItem(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	itemID := seedItem(t, db, "c1", "movie", "Movie One", "", nil)
	blockID := seedBlock(t, db, "block1", "Manual", nil)
	if err := db.Exec(ctx,
		"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES (?, ?, 0, ?, NULL)",
		uuid.NewString(), blockID, itemID,
	); err != nil {
		t.Fatalf("seed block_item: %v", err)
	}

	if err := Delete(ctx, db, "ms1", "ext-"+itemID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := blockItemCatalogIDs(t, db, blockID); len(got) != 0 {
		t.Fatalf("block_item after Delete = %v, want empty (orphan cleaned up)", got)
	}
	var remaining []string
	if err := db.Query(ctx, &remaining, "SELECT id FROM catalog_item WHERE id = ?", itemID); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("catalog_item %s still present after Delete", itemID)
	}
}
