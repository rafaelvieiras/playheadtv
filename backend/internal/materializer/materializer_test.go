package materializer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// testFixture is the common scaffolding every integration test in this file
// needs: a real, temp-file SQLite database with the schema applied, one
// tenant, one media_source, and a handful of catalog_item rows to reference
// from block_item/filler_pool_item.
type testFixture struct {
	db       *storage.SQLite
	tenantID string
	sourceID string
}

func newFixture(t *testing.T) *testFixture {
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
	tenantID := "tenant1"
	if err := db.Exec(ctx, "INSERT INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		tenantID, "Test Tenant", string(domain.TierSelfHosted), now,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	sourceID := "source1"
	if err := db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		 VALUES (?, ?, 'local_folder', 'Local', 'push', ?, ?)`,
		sourceID, tenantID, now, now,
	); err != nil {
		t.Fatalf("seed media_source: %v", err)
	}

	return &testFixture{db: db, tenantID: tenantID, sourceID: sourceID}
}

func (f *testFixture) createCatalogItem(t *testing.T, id string, durationMs int64) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		 VALUES (?, ?, ?, ?, 'movie', ?, ?, ?)`,
		id, f.tenantID, f.sourceID, id, id, durationMs, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed catalog_item %s: %v", id, err)
	}
}

func (f *testFixture) createChannel(t *testing.T, id string, timezone string) {
	t.Helper()
	f.createChannelWithNumber(t, id, 1, timezone)
}

func (f *testFixture) createChannelWithNumber(t *testing.T, id string, number int, timezone string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, f.tenantID, number, "Channel "+id, timezone, now, now,
	); err != nil {
		t.Fatalf("seed channel %s: %v", id, err)
	}
}

// createSchedule + assignChannelSchedule are separate steps (matching the
// real Schedule/Playout split, issue #7) rather than one combined helper, so
// a test that assigns the SAME schedule to two channels (proving the loop
// cursor fix) doesn't need to duplicate schedule creation.
func (f *testFixture) createSchedule(t *testing.T, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO schedule (id, tenant_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		id, f.tenantID, "Schedule "+id, now, now,
	); err != nil {
		t.Fatalf("seed schedule %s: %v", id, err)
	}
}

func (f *testFixture) assignChannelSchedule(t *testing.T, channelID, scheduleID string) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		"UPDATE channel SET schedule_id = ? WHERE id = ?", scheduleID, channelID,
	); err != nil {
		t.Fatalf("assign schedule %s to channel %s: %v", scheduleID, channelID, err)
	}
}

// createBlock seeds a manual block that plays its list once (list_mode
// "once"), the behavior most tests were written against; a test of repetition
// switches it with setListMode (createContinuousScheduleItem does it, since a
// continuous item rotates through its list).
func (f *testFixture) createBlock(t *testing.T, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block (id, tenant_id, name, list_mode, created_at, updated_at) VALUES (?, ?, ?, 'once', ?, ?)",
		id, f.tenantID, "Block "+id, now, now,
	); err != nil {
		t.Fatalf("seed block %s: %v", id, err)
	}
}

func (f *testFixture) setListMode(t *testing.T, blockID, mode string) {
	t.Helper()
	if err := f.db.Exec(context.Background(), "UPDATE block SET list_mode = ? WHERE id = ?", mode, blockID); err != nil {
		t.Fatalf("set list_mode of block %s: %v", blockID, err)
	}
}

// createDynamicBlock seeds a block with non-NULL criteria — the materializer
// only loops a block's items (fillLoopingBlock) instead of airing them once
// when block.criteria IS NOT NULL (see materializeChannel's dynIDs query);
// the actual criteria content is never read by the materializer itself
// (catalogsync.ResolveDynamicBlocks owns that), so any non-NULL value works
// here.
func (f *testFixture) createDynamicBlock(t *testing.T, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, f.tenantID, "Block "+id, "{}", now, now,
	); err != nil {
		t.Fatalf("seed dynamic block %s: %v", id, err)
	}
}

func (f *testFixture) addBlockItem(t *testing.T, id, blockID string, position int, catalogItemID string) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, catalog_item_id) VALUES (?, ?, ?, ?)",
		id, blockID, position, catalogItemID,
	); err != nil {
		t.Fatalf("seed block_item %s: %v", id, err)
	}
}

// createWeeklyScheduleItem seeds a schedule_item under scheduleID — callers
// createSchedule + assignChannelSchedule it to whichever channel(s) should
// air it first.
func (f *testFixture) createWeeklyScheduleItem(t *testing.T, id, scheduleID, blockID string, dayOfWeek int, startTimeLocal string) {
	t.Helper()
	f.createMultiDayScheduleItem(t, id, scheduleID, blockID, domain.Weekdays{dayOfWeek}, startTimeLocal)
}

// createMultiDayScheduleItem seeds a weekly schedule_item airing on every
// listed weekday.
func (f *testFixture) createMultiDayScheduleItem(t *testing.T, id, scheduleID, blockID string, days domain.Weekdays, startTimeLocal string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, days_of_week, start_time_local, active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'weekly', ?, ?, 1, ?, ?)`,
		id, f.tenantID, scheduleID, blockID, days, startTimeLocal, now, now,
	); err != nil {
		t.Fatalf("seed schedule_item %s: %v", id, err)
	}
}

func (f *testFixture) createFillerPool(t *testing.T, id string) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO filler_pool (id, tenant_id, name, created_at) VALUES (?, ?, ?, ?)",
		id, f.tenantID, "Pool "+id, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed filler_pool %s: %v", id, err)
	}
}

func (f *testFixture) addFillerPoolItem(t *testing.T, id, poolID, catalogItemID string) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, 1)",
		id, poolID, catalogItemID,
	); err != nil {
		t.Fatalf("seed filler_pool_item %s: %v", id, err)
	}
}

func (f *testFixture) createFillerRule(t *testing.T, id, scopeType, scopeID, kind, poolID, selection string) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO filler_rule (id, tenant_id, scope_type, scope_id, kind, filler_pool_id, selection_strategy, position, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		id, f.tenantID, scopeType, scopeID, kind, poolID, selection, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed filler_rule %s: %v", id, err)
	}
}

// nearFutureWeeklySlot returns a (dayOfWeek, "HH:MM") pair, in UTC, that
// lands a few minutes from now — comfortably inside a short materialization
// window without the test needing to wait for it, and with second-level
// precision truncated away exactly like schedule_entry.start_time_local
// always is.
func nearFutureWeeklySlot() (dayOfWeek int, startTimeLocal string, occurrence time.Time) {
	occ := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Minute)
	return int(occ.Weekday()), occ.Format("15:04"), occ
}

func TestMaterializer_EndToEnd_WeeklyScheduleProducesEpgEntries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.createCatalogItem(t, "item1", 5*60*1000) // 5 min
	f.createCatalogItem(t, "item2", 5*60*1000) // 5 min
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "block1")
	f.addBlockItem(t, "bi1", "block1", 0, "item1")
	f.addBlockItem(t, "bi2", "block1", 1, "item2")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")

	dayOfWeek, startTimeLocal, occ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "se1", "sched1", "block1", dayOfWeek, startTimeLocal)

	mat := New(f.db, 2*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var entries []domain.EpgEntry
	if err := f.db.Query(ctx, &entries,
		"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = ? ORDER BY start_at", "chan1",
	); err != nil {
		t.Fatalf("query epg_entry: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 epg_entry rows, got %d: %+v", len(entries), entries)
	}

	if entries[0].Kind != domain.EpgProgram || entries[0].CatalogItemID != "item1" {
		t.Errorf("entries[0] = %+v, want kind=program catalog_item_id=item1", entries[0])
	}
	if entries[0].StartAt != occ.UnixMilli() {
		t.Errorf("entries[0].StartAt = %d, want %d", entries[0].StartAt, occ.UnixMilli())
	}
	wantMid := occ.Add(5 * time.Minute).UnixMilli()
	if entries[0].EndAt != wantMid {
		t.Errorf("entries[0].EndAt = %d, want %d", entries[0].EndAt, wantMid)
	}
	if entries[1].Kind != domain.EpgProgram || entries[1].CatalogItemID != "item2" {
		t.Errorf("entries[1] = %+v, want kind=program catalog_item_id=item2", entries[1])
	}
	if entries[1].StartAt != wantMid {
		t.Errorf("entries[1].StartAt = %d, want %d", entries[1].StartAt, wantMid)
	}
	if entries[0].SourceScheduleItemID == nil || *entries[0].SourceScheduleItemID != "se1" {
		t.Errorf("entries[0].SourceScheduleItemID = %v, want se1", entries[0].SourceScheduleItemID)
	}

	var rev domain.EpgRevision
	if err := f.db.Query(ctx, &rev, "SELECT tenant_id, revision, valid_until, updated_at FROM epg_revision WHERE tenant_id = ?", f.tenantID); err != nil {
		t.Fatalf("query epg_revision: %v", err)
	}
	if rev.Revision != 1 {
		t.Errorf("epg_revision.revision after first Run = %d, want 1", rev.Revision)
	}

	// Idempotency: running again with the same underlying state must
	// reproduce the same two program entries (not duplicate them), while
	// still bumping the revision counter.
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	var entriesAfter []domain.EpgEntry
	if err := f.db.Query(ctx, &entriesAfter,
		"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = ? ORDER BY start_at", "chan1",
	); err != nil {
		t.Fatalf("query epg_entry after second run: %v", err)
	}
	if len(entriesAfter) != 2 {
		t.Fatalf("expected 2 epg_entry rows after second Run, got %d: %+v", len(entriesAfter), entriesAfter)
	}
	if entriesAfter[0].CatalogItemID != "item1" || entriesAfter[1].CatalogItemID != "item2" {
		t.Errorf("entries changed shape across idempotent Run calls: %+v", entriesAfter)
	}

	if err := f.db.Query(ctx, &rev, "SELECT tenant_id, revision, valid_until, updated_at FROM epg_revision WHERE tenant_id = ?", f.tenantID); err != nil {
		t.Fatalf("query epg_revision after second run: %v", err)
	}
	if rev.Revision != 2 {
		t.Errorf("epg_revision.revision after second Run = %d, want 2", rev.Revision)
	}
}

// TestMaterializer_EndToEnd_DynamicBlockLoopCursorIsPerChannel is the
// regression test for issue #7's point 2: two channels assigned the SAME
// Schedule (and therefore airing the same dynamic block) must each start
// their loop at the block's first item, not share one rotation cursor within
// a single Run() — a real user with "channels may not have the same content
// playing at the same time" (ErsatzTV's own guarantee, quoted in the issue)
// would otherwise see the 2nd channel start mid-rotation depending purely on
// which channel the materializer happened to process first.
func TestMaterializer_EndToEnd_DynamicBlockLoopCursorIsPerChannel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.createCatalogItem(t, "item-a", 5*60*1000)  // 5 min
	f.createCatalogItem(t, "item-b", 20*60*1000) // 20 min
	f.createDynamicBlock(t, "dynblock")
	f.addBlockItem(t, "bi1", "dynblock", 0, "item-a")
	f.addBlockItem(t, "bi2", "dynblock", 1, "item-b")

	f.createSchedule(t, "sched1")
	dayOfWeek, startTimeLocal, _ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "se1", "sched1", "dynblock", dayOfWeek, startTimeLocal)

	f.createChannelWithNumber(t, "chanA", 1, "UTC")
	f.createChannelWithNumber(t, "chanB", 2, "UTC")
	f.assignChannelSchedule(t, "chanA", "sched1")
	f.assignChannelSchedule(t, "chanB", "sched1")

	// A 15-minute window leaves just enough room, after the occurrence
	// starts, for item-a (5 min) to fit but not item-a+item-b (25 min) — so
	// each channel should independently produce exactly one program entry,
	// item-a. With the pre-fix shared-by-BlockID cursor, whichever channel
	// the materializer processes 2nd would instead see item-b (index 1),
	// since the 1st channel's loop would have already advanced the shared
	// cursor past index 0.
	mat := New(f.db, 15*time.Minute)
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, chanID := range []string{"chanA", "chanB"} {
		var entries []domain.EpgEntry
		if err := f.db.Query(ctx, &entries,
			"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = ? ORDER BY start_at", chanID,
		); err != nil {
			t.Fatalf("query epg_entry for %s: %v", chanID, err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s: expected exactly 1 program entry, got %d: %+v", chanID, len(entries), entries)
		}
		if entries[0].CatalogItemID != "item-a" {
			t.Errorf("%s: entries[0].CatalogItemID = %q, want item-a (loop cursor leaked across channels)", chanID, entries[0].CatalogItemID)
		}
	}
}

func TestMaterializer_EndToEnd_PrerollAppearsBeforeFirstProgram(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.createCatalogItem(t, "item1", 10*60*1000) // 10 min
	f.createCatalogItem(t, "bumper1", 15*1000)  // 15s
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "block1")
	f.addBlockItem(t, "bi1", "block1", 0, "item1")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")

	f.createFillerPool(t, "pool1")
	f.addFillerPoolItem(t, "pi1", "pool1", "bumper1")
	f.createFillerRule(t, "fr1", "channel", "chan1", "preroll", "pool1", "sequential")

	dayOfWeek, startTimeLocal, occ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "se1", "sched1", "block1", dayOfWeek, startTimeLocal)

	mat := New(f.db, 2*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var entries []domain.EpgEntry
	if err := f.db.Query(ctx, &entries,
		"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = ? ORDER BY start_at", "chan1",
	); err != nil {
		t.Fatalf("query epg_entry: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 epg_entry rows (preroll + program), got %d: %+v", len(entries), entries)
	}
	if entries[0].Kind != domain.EpgPreRoll || entries[0].CatalogItemID != "bumper1" {
		t.Fatalf("entries[0] = %+v, want kind=preroll catalog_item_id=bumper1", entries[0])
	}
	if entries[1].Kind != domain.EpgProgram || entries[1].CatalogItemID != "item1" {
		t.Fatalf("entries[1] = %+v, want kind=program catalog_item_id=item1", entries[1])
	}
	if entries[0].EndAt != entries[1].StartAt {
		t.Errorf("preroll should end exactly when the program starts: preroll.EndAt=%d program.StartAt=%d", entries[0].EndAt, entries[1].StartAt)
	}
	// Expansion starts at the occurrence's nominal start (the preroll is part
	// of the occurrence's own airtime), so the preroll airs first and the
	// program is pushed back by its duration.
	if entries[0].StartAt != occ.UnixMilli() {
		t.Errorf("preroll.StartAt = %d, want %d (the occurrence's nominal start)", entries[0].StartAt, occ.UnixMilli())
	}
	wantProgramStart := occ.Add(15 * time.Second).UnixMilli()
	if entries[1].StartAt != wantProgramStart {
		t.Errorf("program.StartAt = %d, want %d (pushed back by the preroll's duration)", entries[1].StartAt, wantProgramStart)
	}
}

// epgEntryColumnsForTest mirrors internal/api/epg.go's epgEntryColumns; kept
// as its own copy since this package must not import internal/api.
const epgEntryColumnsForTest = "id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at, rotation_next, source_block_item_id, series_cycle, series_round"
