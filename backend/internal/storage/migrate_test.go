package storage

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
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

// TestMigration0010_TypedBlockItems proves the typed block item migration on a
// database that already holds manual blocks: every existing block_item becomes
// a 'catalog' item with its position, trim and catalog item intact, the new
// columns and tables work, a series item satisfies the CHECK and a malformed
// one does not.
func TestMigration0010_TypedBlockItems(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0010_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0010): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 't1', 'local_folder', 'Local', 'push', 1, 1)",
		"INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, search_text) VALUES ('ci1', 't1', 'ms1', 'ext1', 'movie', 'One', 60000, 1, 'one')",
		"INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, search_text) VALUES ('ci2', 't1', 'ms1', 'ext2', 'movie', 'Two', 90000, 1, 'two')",
		"INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c1', 't1', 1, 'Channel', 'UTC', 1, 1)",
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('b1', 't1', 'Manual', 1, 1)",
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('b2', 't1', 'Other', 1, 1)",
		"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES ('bi1', 'b1', 0, 'ci1', NULL)",
		"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES ('bi2', 'b1', 1, 'ci2', 45000)",
		"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES ('bi3', 'b2', 0, 'ci2', NULL)",
		"INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at) VALUES ('e1', 't1', 'c1', 'program', 'ci1', 1, 2, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0010): %v", err)
	}

	var rows []struct {
		ID             string
		BlockID        string
		Position       int
		ItemType       string
		CatalogItemID  *string
		TrimDurationMs *int64
		Spec           *string
	}
	if err := db.Query(ctx, &rows, "SELECT id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec FROM block_item ORDER BY id"); err != nil {
		t.Fatalf("query block_item: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("block_item rows = %d, want 3 (the rebuild must not lose rows)", len(rows))
	}
	for i, want := range []struct {
		id, block, catalog string
		pos                int
		trim               *int64
	}{
		{"bi1", "b1", "ci1", 0, nil},
		{"bi2", "b1", "ci2", 1, int64Ptr(45000)},
		{"bi3", "b2", "ci2", 0, nil},
	} {
		got := rows[i]
		if got.ID != want.id || got.BlockID != want.block || got.Position != want.pos || got.ItemType != "catalog" ||
			got.CatalogItemID == nil || *got.CatalogItemID != want.catalog || got.Spec != nil {
			t.Errorf("row %d = %+v, want %+v as a catalog item", i, got, want)
		}
		if (got.TrimDurationMs == nil) != (want.trim == nil) || (want.trim != nil && *got.TrimDurationMs != *want.trim) {
			t.Errorf("row %d trim = %v, want %v", i, got.TrimDurationMs, want.trim)
		}
	}

	var fk []int
	if err := db.Query(ctx, &fk, "PRAGMA foreign_keys"); err != nil || len(fk) != 1 || fk[0] != 1 {
		t.Fatalf("foreign_keys after migrations = %v (err %v), want [1]", fk, err)
	}

	// A series item: no catalog item, a spec. Unique (block_id, position) still holds.
	if err := db.Exec(ctx, `INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('bs1', 'b1', 2, 'series', NULL, '{"media_source_id":"ms1"}')`); err != nil {
		t.Fatalf("insert series block_item: %v", err)
	}
	if err := db.Exec(ctx, `INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('bs2', 'b1', 2, 'series', NULL, '{}')`); err == nil {
		t.Errorf("duplicate (block_id, position) was accepted")
	}
	// Malformed typed rows are rejected.
	for _, q := range []string{
		`INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('x1', 'b1', 5, 'series', NULL)`,
		`INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('x2', 'b1', 6, 'series', 'ci1', '{}')`,
		`INSERT INTO block_item (id, block_id, position, item_type) VALUES ('x3', 'b1', 7, 'catalog')`,
		`INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('x4', 'b1', 8, 'bogus', 'ci1')`,
	} {
		if err := db.Exec(ctx, q); err == nil {
			t.Errorf("malformed block_item accepted: %s", q)
		}
	}

	// epg_entry gained the series columns; existing rows read NULL.
	var cycles []*int
	if err := db.Query(ctx, &cycles, "SELECT series_cycle FROM epg_entry WHERE id = 'e1'"); err != nil || len(cycles) != 1 || cycles[0] != nil {
		t.Fatalf("epg_entry.series_cycle = %v (err %v), want [nil]", cycles, err)
	}

	// series_cursor cascades with its block item.
	if err := db.Exec(ctx, "INSERT INTO series_cursor (tenant_id, channel_id, block_item_id, origin_ms) VALUES ('t1', 'c1', 'bs1', 5)"); err != nil {
		t.Fatalf("insert series_cursor: %v", err)
	}
	if err := db.Exec(ctx, "DELETE FROM block_item WHERE id = 'bs1'"); err != nil {
		t.Fatalf("delete block_item: %v", err)
	}
	var cursors []int
	if err := db.Query(ctx, &cursors, "SELECT COUNT(*) FROM series_cursor"); err != nil || cursors[0] != 0 {
		t.Fatalf("series_cursor rows after item delete = %v (err %v), want 0", cursors, err)
	}

	// Deleting a block still cascades to its items.
	if err := db.Exec(ctx, "DELETE FROM block WHERE id = 'b2'"); err != nil {
		t.Fatalf("delete block: %v", err)
	}
	var left []int
	if err := db.Query(ctx, &left, "SELECT COUNT(*) FROM block_item WHERE block_id = 'b2'"); err != nil || left[0] != 0 {
		t.Fatalf("block_item rows of deleted block = %v (err %v), want 0", left, err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

// TestMigration0011_ExistingSeriesItemsKeepUntilSlotEnd proves the amount
// migration marks series items stored before amounts existed as "slot_end"
// (their behavior then), leaves catalog items without a spec, and does not
// touch a spec that already carries an amount.
func TestMigration0011_ExistingSeriesItemsKeepUntilSlotEnd(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0011_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0011): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at) VALUES ('ms1', 't1', 'local_folder', 'Local', 'push', 1, 1)",
		"INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at, search_text) VALUES ('ci1', 't1', 'ms1', 'ext1', 'movie', 'One', 60000, 1, 'one')",
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('b1', 't1', 'Manual', 1, 1)",
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id) VALUES ('bi1', 'b1', 0, 'catalog', 'ci1')",
		`INSERT INTO block_item (id, block_id, position, item_type, spec) VALUES ('bs1', 'b1', 1, 'series', '{"media_source_id":"ms1","series_title":"Show","playback_order":"shuffle","seed":7,"slice":null}')`,
		`INSERT INTO block_item (id, block_id, position, item_type, spec) VALUES ('bs2', 'b1', 2, 'series', '{"media_source_id":"ms1","series_title":"Show","playback_order":"original","seed":8,"amount":{"mode":"count","count":2}}')`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0011): %v", err)
	}

	var rows []struct {
		ID   string
		Spec *string
	}
	if err := db.Query(ctx, &rows, "SELECT id, spec FROM block_item ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		if r.Spec != nil {
			got[r.ID] = *r.Spec
		}
	}
	if _, ok := got["bi1"]; ok {
		t.Errorf("catalog item gained a spec: %s", got["bi1"])
	}
	var legacy struct {
		PlaybackOrder string `json:"playback_order"`
		Seed          int64  `json:"seed"`
		Amount        struct {
			Mode  string `json:"mode"`
			Count int    `json:"count"`
		} `json:"amount"`
	}
	if err := json.Unmarshal([]byte(got["bs1"]), &legacy); err != nil {
		t.Fatalf("legacy spec %q: %v", got["bs1"], err)
	}
	if legacy.Amount.Mode != "slot_end" || legacy.Amount.Count != 0 || legacy.PlaybackOrder != "shuffle" || legacy.Seed != 7 {
		t.Errorf("legacy series spec after migration = %s, want slot_end with the rest intact", got["bs1"])
	}
	if err := json.Unmarshal([]byte(got["bs2"]), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Amount.Mode != "count" || legacy.Amount.Count != 2 {
		t.Errorf("spec that already had an amount was rewritten: %s", got["bs2"])
	}
}

// TestMigration0013_ExistingBlocksKeepTheirBehavior proves the playback
// settings migration leaves what exists untouched: a dynamic and a manual
// block read back with the default title order, no interleave parameters and
// no seed, and the new column and table accept an interleaved block.
func TestMigration0013_ExistingBlocksKeepTheirBehavior(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0013_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0013): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		"INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c1', 't1', 1, 'Channel', 'UTC', 1, 1)",
		`INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', 't1', 'Dynamic', '{"version":1,"all":[{"field":"kind","op":"equals","value":"episode"}]}', 1, 1)`,
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('man', 't1', 'Manual', 1, 1)",
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0013): %v", err)
	}
	var rows []struct {
		ID             string
		PlaybackOrder  string
		InterleaveSpec *string
		PlaybackSeed   int64
	}
	if err := db.Query(ctx, &rows, "SELECT id, playback_order, interleave_spec, playback_seed FROM block ORDER BY id"); err != nil {
		t.Fatalf("query block: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("blocks = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.PlaybackOrder != "title" || r.InterleaveSpec != nil || r.PlaybackSeed != 0 {
			t.Errorf("block %s = %+v, want the title default with no interleave", r.ID, r)
		}
	}
	if err := db.Exec(ctx, `UPDATE block SET playback_order = 'interleaved', interleave_spec = '{"per_round":1}', playback_seed = 5 WHERE id = 'dyn'`); err != nil {
		t.Fatalf("set interleaved: %v", err)
	}
	if err := db.Exec(ctx, "INSERT INTO block_cursor (tenant_id, channel_id, block_id, origin_ms) VALUES ('t1', 'c1', 'dyn', 7)"); err != nil {
		t.Fatalf("insert block_cursor: %v", err)
	}
	if err := db.Exec(ctx, "INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at, series_round) VALUES ('e1', 't1', 'c1', 'program', 'x', 1, 2, 1, 3)"); err == nil {
		t.Error("epg_entry accepted an unknown catalog item (foreign key off?)")
	}
	// The cursor goes with its block.
	if err := db.Exec(ctx, "DELETE FROM block WHERE id = 'dyn'"); err != nil {
		t.Fatal(err)
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM block_cursor"); err != nil || n[0] != 0 {
		t.Fatalf("block_cursor after block delete = %v (%v), want 0", n, err)
	}
}

// TestMigration0014_Collections proves the collection migration keeps what
// exists (blocks, pools, criteria items and the review table's rows), adds the
// nullable references, and enforces the safety nets: a collection in use
// cannot be deleted at the database level and a review row may now name a
// block item.
func TestMigration0014_Collections(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pre := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		if e.Name() >= "0014_" {
			continue
		}
		content, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pre[e.Name()] = &fstest.MapFile{Data: content}
	}
	if err := RunMigrations(ctx, db, pre); err != nil {
		t.Fatalf("RunMigrations (pre-0014): %v", err)
	}
	for _, q := range []string{
		"INSERT INTO tenant (id, name, tier, created_at) VALUES ('t1', 'Test', 'self_hosted', 1)",
		`INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', 't1', 'Dynamic', '{"version":1,"all":[{"field":"kind","op":"equals","value":"episode"}]}', 1, 1)`,
		"INSERT INTO block (id, tenant_id, name, created_at, updated_at) VALUES ('man', 't1', 'Manual', 1, 1)",
		`INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('ci', 'man', 0, 'criteria', NULL, '{"query":{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]},"playback_order":"title","seed":1,"amount":{"mode":"all"}}')`,
		`INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES ('pool', 't1', 'Pool', '{"kind":"movie"}', 1)`,
		`INSERT INTO criteria_migration_review (tenant_id, owner_type, owner_id, legacy_criteria, proposed_query, problem, before_count, after_count, only_before, only_after, created_at)
		 VALUES ('t1', 'filler_pool', 'pool', '{"kind":"movie"}', NULL, 'x', 3, 2, '[]', '[]', 1)`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations (0014): %v", err)
	}

	var rows []struct {
		ID           string
		Criteria     *string
		CollectionID *string
	}
	if err := db.Query(ctx, &rows, "SELECT id, criteria, collection_id FROM block ORDER BY id"); err != nil || len(rows) != 2 {
		t.Fatalf("blocks = %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.CollectionID != nil {
			t.Errorf("block %s has a collection right after the DDL migration: the data move is the startup step", r.ID)
		}
	}
	if rows[0].ID != "dyn" || rows[0].Criteria == nil {
		t.Errorf("the deprecated criteria column was not kept: %+v", rows[0])
	}
	var n []int
	if err := db.Query(ctx, &n, "SELECT COUNT(*) FROM criteria_migration_review WHERE owner_id = 'pool' AND before_count = 3"); err != nil || n[0] != 1 {
		t.Fatalf("review rows after the table rebuild = %v %v", n, err)
	}

	for _, q := range []string{
		"INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('c1', 't1', 'Same name', '{}', 1, 1)",
		"INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('c2', 't1', 'Same name', '{}', 1, 1)", // names are labels
		"UPDATE block SET collection_id = 'c1' WHERE id = 'dyn'",
		"UPDATE filler_pool SET collection_id = 'c1' WHERE id = 'pool'",
		`UPDATE block_item SET spec = json_set(spec, '$.collection_id', 'c1') WHERE id = 'ci'`,
		`INSERT INTO criteria_migration_review (tenant_id, owner_type, owner_id, legacy_criteria, proposed_query, problem, before_count, after_count, only_before, only_after, created_at)
		 VALUES ('t1', 'block_item', 'ci', '{}', NULL, 'x', 0, 0, '[]', '[]', 1)`,
	} {
		if err := db.Exec(ctx, q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	if err := db.Exec(ctx, "DELETE FROM collection WHERE id = 'c1'"); err == nil {
		t.Error("a collection referenced by a block was deleted")
	}
	if err := db.Exec(ctx, "UPDATE block SET collection_id = 'nope' WHERE id = 'man'"); err == nil {
		t.Error("a block accepted an unknown collection")
	}
	if err := db.Exec(ctx, `INSERT INTO criteria_migration_review (tenant_id, owner_type, owner_id, legacy_criteria, before_count, after_count, only_before, only_after, created_at)
		 VALUES ('t1', 'channel', 'x', '{}', 0, 0, '[]', '[]', 1)`); err == nil {
		t.Error("the review table accepted an unknown owner type")
	}
	// The expression index serves "which items use collection X".
	var plan []struct {
		Detail string
	}
	if err := db.Query(ctx, &plan, "EXPLAIN QUERY PLAN SELECT id FROM block_item WHERE item_type = 'criteria' AND json_extract(spec, '$.collection_id') = 'c1'"); err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 || !strings.Contains(plan[0].Detail, "idx_block_item_collection") {
		t.Errorf("usage lookup plan = %+v, want the expression index", plan)
	}
	// Deleting the tenant takes its collections with it.
	if err := db.Exec(ctx, "DELETE FROM tenant WHERE id = 't1'"); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
}
