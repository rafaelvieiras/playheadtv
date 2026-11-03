package catalogsync

import (
	"context"
	"testing"
	"time"

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

// TestDeleteRemovesTheGuideEntryThatReferencedTheItem reproduces the failure
// every sync of a source hit while a dropped item was still scheduled: the
// materialized guide holds a plain FK to catalog_item, so the delete failed with
// "FOREIGN KEY constraint failed" and the dead item stayed in the catalog for
// good. The guide is derived data — the sync rebuilds it right after — so its
// rows for that item go with it.
func TestDeleteRemovesTheGuideEntryThatReferencedTheItem(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	itemID := seedItem(t, db, "c1", "movie", "Movie One", "", nil)
	if err := db.Exec(ctx,
		"INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('ch1', ?, 1, 'Channel', 'UTC', ?, ?)",
		testTenantID, now, now,
	); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := db.Exec(ctx,
		`INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at)
		 VALUES ('e1', ?, 'ch1', 'program', ?, ?, ?, ?)`,
		testTenantID, itemID, now, now+60000, now,
	); err != nil {
		t.Fatalf("seed epg_entry: %v", err)
	}

	if err := Delete(ctx, db, "ms1", "ext-"+itemID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var entries []string
	if err := db.Query(ctx, &entries, "SELECT id FROM epg_entry WHERE catalog_item_id = ?", itemID); err != nil {
		t.Fatalf("query epg_entry: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("epg_entry after Delete = %v, want none (the item is gone)", entries)
	}
	var remaining []string
	if err := db.Query(ctx, &remaining, "SELECT id FROM catalog_item WHERE id = ?", itemID); err != nil {
		t.Fatalf("query catalog_item: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("catalog_item %s still present after Delete", itemID)
	}
}
