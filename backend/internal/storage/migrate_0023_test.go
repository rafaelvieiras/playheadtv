package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// TestMigration0023_ExistingBlocksExcludeSpecials proves a block that predates
// the include_specials option comes out of the migration with it off, and that
// only 0 and 1 are accepted.
func TestMigration0023_ExistingBlocksExcludeSpecials(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0023_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0023): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('manual', 't1', 'Manual', 1, 1)",
		`INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', 't1', 'Dyn', '{"version":1,"all":[]}', 1, 1)`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0023): %v", err)
	}
	var vals []int
	if err := db.Query(ctx, &vals, "SELECT include_specials FROM block"); err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 || vals[0] != 0 || vals[1] != 0 {
		t.Errorf("include_specials = %v, want 0 for every existing block", vals)
	}
	if err := db.Exec(ctx, "UPDATE block SET include_specials = 1 WHERE id = 'manual'"); err != nil {
		t.Errorf("1 rejected: %v", err)
	}
	if err := db.Exec(ctx, "UPDATE block SET include_specials = 2 WHERE id = 'manual'"); err == nil {
		t.Error("include_specials accepted 2")
	}
}
