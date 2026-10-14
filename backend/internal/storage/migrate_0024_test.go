package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// TestMigration0024_ChannelBaseSchedule proves a channel that predates the base
// schedule comes out of the migration with none, that the column references a
// real schedule and that a channel can not use one schedule for both layers.
func TestMigration0024_ChannelBaseSchedule(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0024_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0024): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES ('s1', 't1', 'Pinned', 1, 1)",
		"INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES ('s2', 't1', 'Base', 1, 1)",
		"INSERT INTO channel (id, tenant_id, number, name, timezone, schedule_id, created_at, updated_at) VALUES ('c1', 't1', 1, 'C', 'UTC', 's1', 1, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0024): %v", err)
	}
	var bases []*string
	if err := db.Query(ctx, &bases, "SELECT base_schedule_id FROM channel"); err != nil {
		t.Fatal(err)
	}
	if len(bases) != 1 || bases[0] != nil {
		t.Errorf("base_schedule_id = %v, want NULL for an existing channel", bases)
	}
	if err := db.Exec(ctx, "UPDATE channel SET base_schedule_id = 's2' WHERE id = 'c1'"); err != nil {
		t.Errorf("a different schedule as base rejected: %v", err)
	}
	if err := db.Exec(ctx, "UPDATE channel SET base_schedule_id = 's1' WHERE id = 'c1'"); err == nil {
		t.Error("the pinned schedule accepted as base")
	}
	if err := db.Exec(ctx, "UPDATE channel SET base_schedule_id = 'missing' WHERE id = 'c1'"); err == nil {
		t.Error("an unknown schedule accepted as base")
	}
}
