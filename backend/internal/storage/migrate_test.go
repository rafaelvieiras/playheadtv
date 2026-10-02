package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// preMigration006FS is every migrations.FS file except 0006, used to
// reconstruct "an existing self-hosted install, one migration behind" —
// TestMigration0006_MigratesScheduleEntryData seeds schedule_entry rows
// against this schema, then applies 0006 for real to prove the data
// migration (not just the DDL) works against real pre-existing rows, which
// TestSQLiteMigrateAndRoundTrip's always-empty-database run never exercises.
func preMigration006FS(t *testing.T) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() >= "0006_" { // 0006 and every later migration
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = &fstest.MapFile{Data: content}
	}
	return out
}

// TestMigration0006_MigratesScheduleEntryData proves issue #7's Schedule/
// Playout split migration (0006) correctly carries forward pre-existing
// schedule_entry rows — the one migration in this codebase that transforms
// data, not just DDL, so this can't just be "does RunMigrations return an
// error" (see splitStatements' own known-limitation comment: each statement
// commits independently, so a partial failure midway is a real risk this
// test guards against).
func TestMigration0006_MigratesScheduleEntryData(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "playheadtv.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := RunMigrations(ctx, db, preMigration006FS(t)); err != nil {
		t.Fatalf("RunMigrations (pre-0006): %v", err)
	}

	now := int64(1700000000000)
	seed := func(query string, args ...any) {
		t.Helper()
		if err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed("INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', ?)", now)
	seed("INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 't1', 'local_folder', 'Local', 'push', ?, ?)", now, now)
	seed("INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at) VALUES ('ci1', 't1', 'ms1', 'ext1', 'movie', 'Movie', 60000, ?)", now)
	// c1 has two schedule_entry rows (proving both carry over under the same
	// auto-created Schedule); c2 has none (proving a channel that never used
	// scheduling is left with a NULL schedule_id, not an empty Schedule).
	seed("INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c1', 't1', 1, 'Channel 1', 'UTC', ?, ?)", now, now)
	seed("INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c2', 't1', 2, 'Channel 2', 'UTC', ?, ?)", now, now)
	seed("INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('b1', 't1', 'Block 1', ?, ?)", now, now)
	seed(`INSERT INTO schedule_entry (id, tenant_id, channel_id, block_id, recurrence_type, day_of_week, start_time_local, active, created_at, updated_at)
	      VALUES ('se1', 't1', 'c1', 'b1', 'weekly', 1, '08:00', 1, ?, ?)`, now, now)
	seed(`INSERT INTO schedule_entry (id, tenant_id, channel_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
	      VALUES ('se2', 't1', 'c1', 'b1', 'once', ?, 1, ?, ?)`, now+1000, now, now)
	seed(`INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_entry_id, generated_at)
	      VALUES ('e1', 't1', 'c1', 'program', 'ci1', ?, ?, 'se1', ?)`, now, now+60000, now)
	seed(`INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at)
	      VALUES ('e2', 't1', 'c1', 'fallback', 'ci1', ?, ?, ?)`, now, now+60000, now)

	// The actual upgrade: apply every migration up to and including 0006
	// against a DB that already has 0001-0005 recorded as applied.
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0006): %v", err)
	}

	var c1ScheduleID []*string
	if err := db.Query(ctx, &c1ScheduleID, "SELECT schedule_id FROM channel WHERE id = 'c1'"); err != nil {
		t.Fatalf("query c1.schedule_id: %v", err)
	}
	if len(c1ScheduleID) != 1 || c1ScheduleID[0] == nil || *c1ScheduleID[0] != "migrated-c1" {
		t.Fatalf("c1.schedule_id = %v, want [migrated-c1]", c1ScheduleID)
	}

	var c2ScheduleID []*string
	if err := db.Query(ctx, &c2ScheduleID, "SELECT schedule_id FROM channel WHERE id = 'c2'"); err != nil {
		t.Fatalf("query c2.schedule_id: %v", err)
	}
	if len(c2ScheduleID) != 1 || c2ScheduleID[0] != nil {
		t.Fatalf("c2.schedule_id = %v, want [nil] (never had a schedule_entry)", c2ScheduleID)
	}

	var scheduleNames []string
	if err := db.Query(ctx, &scheduleNames, "SELECT name FROM schedule WHERE id = 'migrated-c1'"); err != nil {
		t.Fatalf("query schedule: %v", err)
	}
	if len(scheduleNames) != 1 || scheduleNames[0] != "Channel 1 schedule" {
		t.Fatalf("schedule names = %v, want [Channel 1 schedule]", scheduleNames)
	}

	var itemIDs []string
	if err := db.Query(ctx, &itemIDs, "SELECT id FROM schedule_item WHERE schedule_id = 'migrated-c1' ORDER BY id"); err != nil {
		t.Fatalf("query schedule_item: %v", err)
	}
	if len(itemIDs) != 2 || itemIDs[0] != "se1" || itemIDs[1] != "se2" {
		t.Fatalf("schedule_item ids = %v, want [se1 se2] (same ids as the old schedule_entry rows)", itemIDs)
	}

	// epg_entry survived the table rebuild with its data intact and its FK
	// repointed at schedule_item.
	var epgSourceIDs []*string
	if err := db.Query(ctx, &epgSourceIDs, "SELECT source_schedule_item_id FROM epg_entry ORDER BY id"); err != nil {
		t.Fatalf("query epg_entry.source_schedule_item_id: %v", err)
	}
	if len(epgSourceIDs) != 2 {
		t.Fatalf("epg_entry rows = %d, want 2 (rebuild must not lose rows)", len(epgSourceIDs))
	}
	if epgSourceIDs[0] == nil || *epgSourceIDs[0] != "se1" {
		t.Errorf("e1.source_schedule_item_id = %v, want se1", epgSourceIDs[0])
	}
	if epgSourceIDs[1] != nil {
		t.Errorf("e2.source_schedule_item_id = %v, want nil (fallback entry)", epgSourceIDs[1])
	}

	// schedule_entry itself must be gone.
	var scheduleEntryCount []int
	err = db.Query(ctx, &scheduleEntryCount, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schedule_entry'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if len(scheduleEntryCount) != 1 || scheduleEntryCount[0] != 0 {
		t.Fatalf("schedule_entry table still exists after migration")
	}

	// 0007 (rebuilt schedule_item with the 'continuous' recurrence): the
	// rebuild kept FK enforcement on and the new type is accepted.
	var fk []int
	if err := db.Query(ctx, &fk, "PRAGMA foreign_keys"); err != nil || len(fk) != 1 || fk[0] != 1 {
		t.Fatalf("foreign_keys after migrations = %v (err %v), want [1]", fk, err)
	}
	if err := db.Exec(ctx, `INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, active, created_at, updated_at)
	      VALUES ('sc1', 't1', 'migrated-c1', 'b1', 'continuous', 1, 1, 1)`); err != nil {
		t.Fatalf("insert continuous schedule_item: %v", err)
	}

	// Running the full migration set again must still be a no-op.
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (second run): %v", err)
	}
}

// TestMigration0009_KeepsExistingCatalogRows proves the catalog metadata
// migration upgrades a database already holding catalog_item rows: the rows
// survive untouched with NULL metadata, and the new link tables work.
func TestMigration0009_KeepsExistingCatalogRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0009_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0009): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 't1', 'local_folder', 'Local', 'push', 1, 1)",
		"INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, genres, search_text) VALUES ('ci1', 't1', 'ms1', 'ext1', 'movie', 'Old Movie', 60000, 1, 'Comedy', 'old movie')",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0009): %v", err)
	}

	type row struct {
		Title        string
		Genres       string
		SeasonNumber *int64
		Overview     *string
	}
	var rows []row
	if err := db.Query(ctx, &rows, "SELECT title, genres, season_number, overview FROM catalog_item WHERE id = 'ci1'"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "Old Movie" || rows[0].Genres != "Comedy" || rows[0].SeasonNumber != nil || rows[0].Overview != nil {
		t.Fatalf("existing row after migration = %+v", rows)
	}

	// The link tables accept rows and cascade with the item.
	for _, q := range []string{
		"INSERT INTO studio (tenant_id, id, name, name_norm) VALUES ('t1', 's1', 'Acme', 'acme')",
		"INSERT INTO catalog_item_studio (tenant_id, catalog_item_id, studio_id) VALUES ('t1', 'ci1', 's1')",
		"DELETE FROM catalog_item WHERE id = 'ci1'",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM catalog_item_studio"); err != nil || n[0] != 0 {
		t.Fatalf("catalog_item_studio after item delete = %v (%v), want 0", n, err)
	}
}
