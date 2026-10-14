package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// TestMigration0025_Watermark proves the watermark table comes out of the
// migration empty (every existing channel and block has none), keeps one row
// per owner, and refuses values outside the documented modes and bounds.
func TestMigration0025_Watermark(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)"); err != nil {
		t.Fatal(err)
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM watermark"); err != nil || n[0] != 0 {
		t.Fatalf("watermark rows after migration = %v, %v", n, err)
	}
	ins := func(owner, mode, pos string, opacity, size int) error {
		return db.Exec(ctx, `INSERT INTO watermark (tenant_id, owner_type, owner_id, mode, position, opacity_pct, size_pct, created_at, updated_at)
			VALUES ('t1', 'channel', ?, ?, ?, ?, ?, 1, 1)`, owner, mode, pos, opacity, size)
	}
	if err := ins("c1", "override", "top_left", 50, 10); err != nil {
		t.Fatalf("valid row rejected: %v", err)
	}
	for name, err := range map[string]error{
		"second row for one owner": ins("c1", "disable", "top_left", 50, 10),
		"unknown mode":             ins("c2", "always", "top_left", 50, 10),
		"unknown position":         ins("c3", "override", "center", 50, 10),
		"opacity above 100":        ins("c4", "override", "top_left", 101, 10),
		"size below the minimum":   ins("c5", "override", "top_left", 50, 1),
		"size above the maximum":   ins("c6", "override", "top_left", 50, 41),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// image_id is unique across owners (it is the URL of the bytes).
	if err := db.Exec(ctx, "UPDATE watermark SET image_id = 'i1' WHERE owner_id = 'c1'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, `INSERT INTO watermark (tenant_id, owner_type, owner_id, mode, image_id, created_at, updated_at)
		VALUES ('t1', 'block', 'b1', 'override', 'i1', 1, 1)`); err == nil {
		t.Error("a duplicate image_id accepted")
	}
}
