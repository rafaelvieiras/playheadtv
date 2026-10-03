package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// TestMigration0015_ExistingManualBlocksRepeat proves the list mode migration
// switches every existing block to repeat and lists, for review, exactly the
// manual blocks that have items and a timed (weekly or once) schedule item.
func TestMigration0015_ExistingManualBlocksRepeat(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0015_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0015): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms', 't1', 'local_folder', 'src', 'push', 1, 1)",
		"INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at) VALUES ('ci', 't1', 'ms', 'x', 'movie', 'M', 1000, 1)",
		"INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES ('s1', 't1', 'Sched', 1, 1)",
		// manual with items and a weekly item: reviewed
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('timed', 't1', 'Timed', 1, 1)",
		// manual with a one-off item: reviewed
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('onceblk', 't1', 'Once', 1, 1)",
		// continuous only: repeat, not reviewed (a continuous item already rotated)
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('cont', 't1', 'Cont', 1, 1)",
		// timed but empty: not reviewed (nothing to repeat)
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('empty', 't1', 'Empty', 1, 1)",
		// dynamic with a timed item: not reviewed
		`INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', 't1', 'Dyn', '{"version":1,"all":[]}', 1, 1)`,
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('i1', 'timed', 0, 'catalog', 'ci')",
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('i2', 'onceblk', 0, 'catalog', 'ci')",
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('i3', 'cont', 0, 'catalog', 'ci')",
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('i4', 'dyn', 0, 'catalog', 'ci')",
		"INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, active, created_at, updated_at) VALUES ('w', 't1', 's1', 'timed', 'weekly', 1, '17:00', 1, 1, 1)",
		"INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at) VALUES ('o', 't1', 's1', 'onceblk', 'once', 5, 1, 1, 1)",
		"INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, active, created_at, updated_at) VALUES ('c', 't1', 's1', 'cont', 'continuous', 1, 1, 1)",
		"INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, active, created_at, updated_at) VALUES ('we', 't1', 's1', 'empty', 'weekly', 2, '10:00', 1, 1, 1)",
		"INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, active, created_at, updated_at) VALUES ('wd', 't1', 's1', 'dyn', 'weekly', 3, '10:00', 1, 1, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0015): %v", err)
	}
	var modes []string
	if err := db.Query(ctx, &modes, "SELECT list_mode FROM block"); err != nil {
		t.Fatalf("query list_mode: %v", err)
	}
	if len(modes) != 5 {
		t.Fatalf("blocks = %d, want 5", len(modes))
	}
	for _, m := range modes {
		if m != "repeat" {
			t.Errorf("list_mode = %q, want every existing block migrated to repeat", m)
		}
	}
	var reviewed []string
	if err := db.Query(ctx, &reviewed, "SELECT block_id FROM block_list_mode_review ORDER BY block_id"); err != nil {
		t.Fatalf("query review: %v", err)
	}
	if len(reviewed) != 2 || reviewed[0] != "onceblk" || reviewed[1] != "timed" {
		t.Errorf("review = %v, want [onceblk timed]", reviewed)
	}
	if err := db.Exec(ctx, "UPDATE block SET list_mode = 'sometimes' WHERE id = 'timed'"); err == nil {
		t.Error("list_mode accepted an unknown value")
	}
	// The review row goes with its block.
	if err := db.Exec(ctx, "DELETE FROM schedule_item WHERE block_id = 'timed'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "DELETE FROM block WHERE id = 'timed'"); err != nil {
		t.Fatal(err)
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM block_list_mode_review WHERE block_id = 'timed'"); err != nil || n[0] != 0 {
		t.Fatalf("review rows of a deleted block = %v (%v), want 0", n, err)
	}
}
