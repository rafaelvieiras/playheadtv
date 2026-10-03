package materializer

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Slots before the cursor origin and entries already over emit nothing that
// airs, so they must not use up the cap: with 1-second episodes a day of
// lookback is far more than maxSeriesEntries.
func TestExpandChannel_SkippedAndPastSlotsDoNotCountTowardsTheCap(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "cont", BlockID: "blk", Recurrence: domain.RecurrenceContinuous, Active: true},
	}
	in, run := seriesBlockInput(domain.OrderOriginal, 1, 1, []domain.BlockItem{seriesItem()}, sched, nil)
	run.episodes[0].DurationMs = 1000
	origin := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	run.originMs = origin.UnixMilli()
	run.nowMs = origin.UnixMilli()
	scanFrom := origin.Add(-24 * time.Hour)
	entries := expandChannel(in, scanFrom, scanFrom, origin.Add(time.Hour), newSelectionState())
	var future int
	for _, e := range entries {
		if e.EndAt > origin.UnixMilli() {
			future++
		}
	}
	if future != 3600 {
		t.Errorf("%d entries air after the origin, want 3600 (a day of skipped slots must not use the cap)", future)
	}
}

func TestExpandChannel_PastEntriesDoNotCountTowardsTheCap(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "cont", BlockID: "blk", Recurrence: domain.RecurrenceContinuous, Active: true},
	}
	in, run := seriesBlockInput(domain.OrderOriginal, 1, 1, []domain.BlockItem{seriesItem()}, sched, nil)
	run.episodes[0].DurationMs = 1000
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	run.nowMs = now.UnixMilli() // origin stays 0: the past entries are real, just over
	scanFrom := now.Add(-24 * time.Hour)
	entries := expandChannel(in, scanFrom, scanFrom, now.Add(time.Hour), newSelectionState())
	var future int
	for _, e := range entries {
		if e.EndAt > now.UnixMilli() {
			future++
		}
	}
	if future != 3600 {
		t.Errorf("%d entries air after now, want 3600", future)
	}
}

// A capped expansion is logged, and writing ~20k rows completes well inside
// the run budget (batched transaction), leaving exactly the capped entries.
func TestSeriesEndToEnd_CappedExpansionIsLoggedAndWritesInTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 30000 one-second episodes in one series, set-based.
	if err := f.db.Exec(ctx,
		`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < 30000)
		 INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, series_external_id, season_number, episode_number, duration_ms, synced_at, search_text)
		 SELECT 'e' || printf('%05d', i), ?, ?, 'e' || i, 'episode', 'Title ' || i, 'Show', 'ser-Show', 1, i, 1000, 0, 'x'
		   FROM seq`, f.tenantID, f.sourceID); err != nil {
		t.Fatal(err)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesBlockItem(t, "sbi", "blk", 0, domain.OrderOriginal)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

	buf := captureLog(t)
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	mat := New(f.db, 48*time.Hour)
	if err := mat.Run(runCtx); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	entries := queryEntries(t, f)
	if len(entries) != maxSeriesEntries {
		t.Fatalf("%d entries written, want the cap %d (log: %s)", len(entries), maxSeriesEntries, buf.String())
	}
	if !strings.Contains(buf.String(), "series cap hit: block item sbi on channel chan1") {
		t.Errorf("no cap warning in log: %q", buf.String())
	}
	t.Logf("capped run wrote %d rows in %s", len(entries), took)
}

func TestWarnings_CriteriaItemWithBrokenQueryIsReportedAndLogged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.createCatalogItem(t, "intro", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "b0", "blk", 0, "intro")
	// Not a valid condition tree: evaluation fails.
	f.addCriteriaBlockItem(t, "crit", "blk", 1, `{"version":1,"all":[{"field":"nope","op":"eq","value":1}]}`, domain.OrderTitle, allMatches)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

	mat := New(f.db, 5*time.Hour)
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	warnings, err := mat.Warnings(ctx, f.tenantID, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Kind != WarningCriteriaError || warnings[0].BlockID != "blk" || warnings[0].BlockItemID != "crit" {
		t.Fatalf("warnings = %+v", warnings)
	}
	if !strings.Contains(buf.String(), "ERROR channel chan1: block blk item crit") {
		t.Errorf("no error log with ids: %q", buf.String())
	}

	// The preview carries the same warning.
	_, pw, err := mat.PreviewResetWithWarnings(ctx, f.tenantID, ch, 0, 0)
	if err != nil || len(pw) != 1 {
		t.Fatalf("preview warnings = %+v (%v)", pw, err)
	}

	// A healthy channel has none, as an empty (non-nil) list.
	if err := f.db.Exec(ctx, "DELETE FROM block_item WHERE id = 'crit'"); err != nil {
		t.Fatal(err)
	}
	if warnings, err = mat.Warnings(ctx, f.tenantID, ch); err != nil || warnings == nil || len(warnings) != 0 {
		t.Fatalf("healthy channel warnings = %#v (%v)", warnings, err)
	}
}
