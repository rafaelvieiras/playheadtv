package materializer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// entryRows renders every epg_entry of the channel without the columns that
// legitimately differ between two runs (the row id and generated_at), so two
// materializations can be compared for identity.
func entryRows(t *testing.T, f *testFixture) []string {
	t.Helper()
	var out []string
	for _, e := range queryEntries(t, f) {
		ptr := func(s *string) string {
			if s == nil {
				return "-"
			}
			return *s
		}
		num := func(n *int) string {
			if n == nil {
				return "-"
			}
			return fmt.Sprint(*n)
		}
		out = append(out, fmt.Sprintf("%s|%s|%d|%d|%s|%s|%s|%s|%s", e.Kind, e.CatalogItemID, e.StartAt, e.EndAt,
			ptr(e.SourceScheduleItemID), num(e.RotationNext), ptr(e.SourceBlockItemID), num(e.SeriesCycle), num(e.SeriesRound)))
	}
	return out
}

// TestCollectionMigrationLeavesTheMaterializedGridIdentical materializes a
// channel that uses every kind of owner the migration moves (a dynamic block
// in title order, a dynamic block in the interleaved order, a dynamic filler
// pool feeding a preroll rule, and a criteria item inside a manual block) in
// the state before the migration, migrates, materializes again from scratch,
// and compares the epg_entry rows.
func TestCollectionMigrationLeavesTheMaterializedGridIdentical(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	const dur = int64(10 * 60 * 1000)
	f.seedSeries(t, "Aaa", 6, dur)
	f.seedSeries(t, "Bbb", 2, dur)
	f.seedSeries(t, "Ccc", 3, dur)
	f.createCatalogItem(t, "intro", epMs)
	f.createChannel(t, "chan1", "UTC")

	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := f.db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// The state a 0013 database is in: queries live in the criteria columns.
	exec("INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('dyn', ?, 'Nineties', ?, ?, ?)", f.tenantID, yearQuery, now, now)
	f.createInterleavedBlock(t, "inter", epsQuery, domain.OrderInterleaved, `{"per_round":2,"on_exhaustion":"restart","series_order":"title"}`, 7)
	exec("INSERT INTO filler_pool (id, tenant_id, name, criteria, created_at) VALUES ('pool', ?, 'Nineties pool', ?, ?)", f.tenantID, yearQuery, now)
	f.createFillerRule(t, "rule", "block", "dyn", "preroll", "pool", "sequential")
	f.createBlock(t, "man")
	f.addBlockItem(t, "b0", "man", 0, "intro")
	f.addCriteriaBlockItem(t, "crit", "man", 1, yearQuery, domain.OrderShuffle, countTwo)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	base := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute)
	f.createOnceScheduleItem(t, "s1", "sched1", "man", base)
	f.createOnceScheduleItem(t, "s2", "sched1", "dyn", base.Add(1*time.Hour))
	f.createOnceScheduleItem(t, "s3", "sched1", "inter", base.Add(3*time.Hour))

	mat := New(f.db, 10*time.Hour)
	if err := catalogsync.ResolveDynamic(ctx, f.db, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	before := entryRows(t, f)
	if len(before) < 20 {
		t.Fatalf("only %d entries materialized before the migration: %v", len(before), before)
	}

	report, err := catalogsync.MigrateToCollections(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	// 'dyn', 'pool' and 'crit' share the nineties query, 'inter' has its own.
	if report.Assigned != 4 || report.Created != 2 || report.Held != 0 {
		t.Fatalf("report = %+v, want 4 owners on 2 collections", report)
	}
	var withoutCollection []int
	if err := f.db.Query(ctx, &withoutCollection, `SELECT (SELECT COUNT(*) FROM block WHERE criteria IS NOT NULL AND collection_id IS NULL)
		+ (SELECT COUNT(*) FROM filler_pool WHERE criteria IS NOT NULL AND collection_id IS NULL)`); err != nil || withoutCollection[0] != 0 {
		t.Fatalf("owners left without a collection: %v %v", withoutCollection, err)
	}
	// From here the deprecated columns must not matter: break them.
	exec("UPDATE block SET criteria = '{\"stale\":true}' WHERE collection_id IS NOT NULL")
	exec("UPDATE filler_pool SET criteria = '{\"stale\":true}' WHERE collection_id IS NOT NULL")
	exec(`UPDATE block_item SET spec = json_set(spec, '$.query', json('{"stale":true}')) WHERE item_type = 'criteria'`)

	if err := catalogsync.ResolveDynamic(ctx, f.db, f.tenantID); err != nil {
		t.Fatal(err)
	}
	exec("DELETE FROM epg_entry")
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	after := entryRows(t, f)
	if len(after) != len(before) {
		t.Fatalf("entries after = %d, before = %d", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("entry %d differs after the migration:\n before %s\n after  %s", i, before[i], after[i])
		}
	}

	// And a second regeneration on top of the migrated state stays identical.
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	again := entryRows(t, f)
	if fmt.Sprint(again) != fmt.Sprint(before) {
		t.Fatalf("a second run after the migration changed the grid")
	}
}

// A criteria item evaluates its collection, and a collection edit changes
// what the item airs on the next materialization.
func TestCriteriaItemFollowsItsCollection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	now := time.Now().UnixMilli()
	if err := f.db.Exec(ctx, "INSERT INTO collection (id, tenant_id, name, query, created_at, updated_at) VALUES ('col', ?, 'Nineties', ?, ?, ?)", f.tenantID, yearQuery, now, now); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(ctx,
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('crit', 'blk', 0, 'criteria', NULL, ?)",
		`{"collection_id":"col","playback_order":"title","seed":1,"amount":{"mode":"all"}}`); err != nil {
		t.Fatal(err)
	}
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "a", "sched1", "blk", time.Now().UTC().Add(10*time.Minute).Truncate(time.Minute))

	mat := New(f.db, 10*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := seriesIDs(queryEntries(t, f)), []string{"n2", "n4", "n5", "n3", "n1"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}

	// The collection is edited to the 1980s: the item airs the old movie.
	if err := f.db.Exec(ctx, "UPDATE collection SET query = ? WHERE id = 'col'", `{"version":1,"all":[{"field":"year","op":"lt","value":1990}]}`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(ctx, "DELETE FROM epg_entry"); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := seriesIDs(queryEntries(t, f)), []string{"old"}; !equalStrings(got, want) {
		t.Fatalf("entries after the edit = %v, want %v", got, want)
	}

	// A collection that vanished leaves a warning, not a crash.
	if err := f.db.Exec(ctx, "UPDATE block_item SET spec = ? WHERE id = 'crit'", `{"collection_id":"gone","playback_order":"title","seed":1,"amount":{"mode":"all"}}`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(ctx, "DELETE FROM epg_entry"); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT id, tenant_id, number, name, timezone, schedule_id, created_at, updated_at FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := mat.PreviewResetWithWarnings(ctx, f.tenantID, ch, 0, 0)
	if err != nil || len(warnings) != 1 || warnings[0].Kind != WarningCriteriaError {
		t.Fatalf("warnings = %+v %v", warnings, err)
	}
}
