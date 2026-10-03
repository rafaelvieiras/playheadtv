package materializer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const hourMs = int64(60 * 60 * 1000)

// repeatInput is a channel with one timed occurrence of block "blk" at 12:00
// and, when nextAt is non-zero, another block ("news") at nextAt.
func repeatInput(items []domain.BlockItem, repeat bool, nextAt time.Time, dur map[string]int64) (channelInput, time.Time) {
	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	sched := []domain.ScheduleItem{onceItem("slot", "blk", start)}
	blocks := map[string][]domain.BlockItem{"blk": items}
	if !nextAt.IsZero() {
		sched = append(sched, onceItem("next", "news", nextAt))
		blocks["news"] = []domain.BlockItem{{ID: "n", BlockID: "news", CatalogItemID: "news"}}
		dur["news"] = hourMs
	}
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems:   sched,
		BlockItems:      blocks,
		CatalogDuration: dur,
		Series:          map[string]*seriesRun{},
	}
	if repeat {
		in.RepeatBlockIDs = map[string]bool{"blk": true}
	}
	return in, start
}

func catalogBI(id, catalogID string) domain.BlockItem {
	return domain.BlockItem{ID: id, BlockID: "blk", CatalogItemID: catalogID}
}

func expandFrom(in channelInput, from time.Time, window time.Duration) []domain.EpgEntry {
	return expandChannel(in, from, from, from.Add(window), newSelectionState())
}

func TestRepeat_TimedItemLoopsTheListUntilTheNextTimedItem(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "a"), catalogBI("b2", "b")}, true, time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC), map[string]int64{"a": hourMs, "b": hourMs})
	entries := expandFrom(in, start, 8*time.Hour)
	var got []string
	for _, e := range entries {
		got = at(got, e)
	}
	want := []string{"a@12:00", "b@13:00", "a@14:00", "b@15:00", "a@16:00", "news@17:00"}
	if !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRepeat_ItemThatDoesNotFitEndsTheLapAndTailFillsTheResidual(t *testing.T) {
	// A 90-minute and a 60-minute item in a 5h slot: 12:00 (90m), 13:30 (60m),
	// 14:30 (90m), 16:00 (60m) ends 17:00 exactly; make the slot 4h45m so the
	// next 90-minute item does not fit and the tail rule fills what is left.
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "long"), catalogBI("b2", "short")}, true, time.Date(2026, 9, 30, 16, 45, 0, 0, time.UTC), map[string]int64{"long": 90 * 60 * 1000, "short": hourMs, "spot": 5 * 60 * 1000})
	in.FillerRules = []domain.FillerRule{{ID: "tail", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerTail, FillerPoolID: "pool", Selection: domain.SelectionSequential}}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "p1", FillerPoolID: "pool", CatalogItemID: "spot"}}}
	entries := expandFrom(in, start, 8*time.Hour)
	var programs []string
	var tail int
	for _, e := range entries {
		switch e.Kind {
		case domain.EpgProgram:
			if e.CatalogItemID != "news" {
				programs = at(programs, e)
			}
		case domain.EpgTail:
			tail++
		}
	}
	want := []string{"long@12:00", "short@13:30", "long@14:30"}
	if !equalStrings(programs, want) {
		t.Errorf("programs = %v, want %v", programs, want)
	}
	// 16:00..16:45 is left after the third item: nine 5-minute spots.
	if tail != 9 {
		t.Errorf("%d tail clips, want 9", tail)
	}
}

func TestRepeat_WithoutNextOccurrenceFillsToTheWindowEnd(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "a"), catalogBI("b2", "b")}, true, time.Time{}, map[string]int64{"a": hourMs, "b": hourMs})
	entries := expandFrom(in, start, 5*time.Hour)
	if len(entries) != 5 {
		t.Fatalf("%d entries, want 5 one-hour programs in a 5h window: %+v", len(entries), entries)
	}
	for i, e := range entries {
		if want := []string{"a", "b"}[i%2]; e.CatalogItemID != want {
			t.Errorf("entry %d = %s, want %s", i, e.CatalogItemID, want)
		}
	}
}

func TestRepeat_PlayOnceKeepsTheListToASinglePass(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "a"), catalogBI("b2", "b")}, false, time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC), map[string]int64{"a": hourMs, "b": hourMs})
	entries := expandFrom(in, start, 8*time.Hour)
	var got []string
	for _, e := range entries {
		got = at(got, e)
	}
	want := []string{"a@12:00", "b@13:00", "news@17:00"}
	if !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRepeat_MidrollGoesBetweenLaps(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "a"), catalogBI("b2", "b")}, true, time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC), map[string]int64{"a": hourMs, "b": hourMs, "ad": 10 * 60 * 1000})
	in.FillerRules = []domain.FillerRule{{ID: "mid", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerMidRoll, FillerPoolID: "pool", Selection: domain.SelectionSequential}}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "p1", FillerPoolID: "pool", CatalogItemID: "ad"}}}
	entries := expandFrom(in, start, 8*time.Hour)
	var got []string
	for _, e := range entries {
		got = at(got, e)
	}
	// 12:00 a, 13:00 ad, 13:10 b, 14:10 ad, 14:20 a would end 15:20: it does
	// not fit, so the block ends after the break that did.
	want := []string{"a@12:00", "ad@13:00", "b@13:10", "ad@14:10", "news@15:00"}
	if !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRepeat_ZeroProgressListDoesNotSpin(t *testing.T) {
	// An empty series, a series item with no run at all and a zero-length
	// catalog item: nothing can air, and the block must simply end.
	items := []domain.BlockItem{seriesBI("sa", "blk"), seriesBI("ghost", "blk"), catalogBI("b1", "zero")}
	in, start := repeatInput(items, true, time.Time{}, map[string]int64{"zero": 0})
	in.Series = map[string]*seriesRun{"sa": seriesWalk("a", 0, ep22, amtCount(1))}
	done := make(chan []domain.EpgEntry, 1)
	go func() { done <- expandFrom(in, start, 48*time.Hour) }()
	select {
	case entries := <-done:
		if len(entries) != 0 {
			t.Errorf("%d entries, want none: %+v", len(entries), entries)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("expansion of a list that airs nothing did not finish")
	}
}

func TestRepeat_EmptyBlockDoesNotSpin(t *testing.T) {
	in, start := repeatInput(nil, true, time.Time{}, map[string]int64{})
	if entries := expandFrom(in, start, 48*time.Hour); len(entries) != 0 {
		t.Errorf("%d entries from an empty block, want none", len(entries))
	}
}

func TestRepeat_ZeroLengthItemsAreSkippedNotLooped(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "zero"), catalogBI("b2", "a")}, true, time.Time{}, map[string]int64{"zero": 0, "a": hourMs})
	entries := expandFrom(in, start, 3*time.Hour)
	if len(entries) != 3 {
		t.Fatalf("%d entries, want 3: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.CatalogItemID != "a" {
			t.Errorf("entry %s aired, want only a", e.CatalogItemID)
		}
	}
}

func TestRepeat_VeryShortItemsHitTheEntryCap(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "blip")}, true, time.Time{}, map[string]int64{"blip": 1})
	entries := expandFrom(in, start, time.Hour)
	if len(entries) != maxRepeatEntries {
		t.Errorf("%d entries, want the cap %d", len(entries), maxRepeatEntries)
	}
}

func TestRepeat_SeriesItemsInsideARepeatingList(t *testing.T) {
	// One episode of the series per lap, then a 30-minute movie; the series
	// cursor walks on across laps and starts its cycle again after the third.
	items := []domain.BlockItem{seriesBI("sa", "blk"), catalogBI("b1", "movie")}
	in, start := repeatInput(items, true, time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC), map[string]int64{"movie": 30 * 60 * 1000})
	in.Series = map[string]*seriesRun{"sa": seriesWalk("a", 3, ep22, amtCount(1))}
	entries := expandFrom(in, start, 8*time.Hour)
	var got []string
	for _, e := range entries {
		got = at(got, e)
	}
	want := []string{"a01@12:00", "movie@12:22", "a02@12:52", "movie@13:14", "a03@13:44", "movie@14:06", "a01@14:36", "news@15:00"}
	// The last movie (14:58-15:28) does not fit before 15:00: the lap ends.
	if len(got) != 8 || !equalStrings(got[:7], want[:7]) || !strings.HasPrefix(got[7], "news@15:00") {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRepeat_SlotEndSeriesInARepeatingListBehavesAsBefore(t *testing.T) {
	items := []domain.BlockItem{seriesBI("sa", "blk"), catalogBI("b1", "movie")}
	in, start := repeatInput(items, true, time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC), map[string]int64{"movie": hourMs})
	in.Series = map[string]*seriesRun{"sa": seriesWalk("a", 20, ep22, amtSlotEnd)}
	entries := expandFrom(in, start, 8*time.Hour)
	for _, e := range entries {
		if e.CatalogItemID == "movie" {
			t.Errorf("the item after a slot_end series aired: %+v", e)
		}
	}
	if last := entries[len(entries)-1]; last.CatalogItemID != "news" || last.StartAt != time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("last entry = %+v, want news at 14:00", last)
	}
}

func TestRepeat_PostrollReservedBeforeTheNextOccurrence(t *testing.T) {
	in, start := repeatInput([]domain.BlockItem{catalogBI("b1", "a")}, true, time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC), map[string]int64{"a": hourMs, "sign": 10 * 60 * 1000})
	in.FillerRules = []domain.FillerRule{{ID: "post", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerPostRoll, FillerPoolID: "pool", Selection: domain.SelectionSequential}}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "p1", FillerPoolID: "pool", CatalogItemID: "sign"}}}
	entries := expandFrom(in, start, 8*time.Hour)
	var got []string
	for _, e := range entries {
		got = at(got, e)
	}
	// 12:00 and 13:00 fit before 14:50, the third (14:00-15:00) would eat the
	// sign-off's room; the postroll then runs into the next occurrence.
	want := []string{"a@12:00", "a@13:00", "sign@14:00", "news@15:00"}
	if len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[3] != want[3] {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// listModeSetup seeds a channel driven by one continuous item over the manual
// block "rot" of three one-hour movies.
func listModeSetup(t *testing.T, mode string) *testFixture {
	t.Helper()
	f := newFixture(t)
	for _, id := range []string{"m0", "m1", "m2"} {
		f.createCatalogItem(t, id, hourMs)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "rot")
	for i, id := range []string{"m0", "m1", "m2"} {
		f.addBlockItem(t, "bi"+id, "rot", i, id)
	}
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "rot")
	f.setListMode(t, "rot", mode) // after the helper, which makes it repeat
	return f
}

func TestRepeat_ContinuousItemLoopsAndKeepsItsPositionAcrossRuns(t *testing.T) {
	f := listModeSetup(t, domain.ListModeRepeat)
	mat := New(f.db, 8*time.Hour)
	if err := mat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := queryEntries(t, f)
	if len(first) != 8 {
		t.Fatalf("%d entries in an 8h window, want 8", len(first))
	}
	assertRotation(t, first, 0)
	// A second run keeps the head and resumes the rotation from the persisted
	// position: the sequence stays unbroken.
	if err := mat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := queryEntries(t, f)
	assertRotation(t, second, 0)
	if len(second) < 8 {
		t.Errorf("%d entries after the second run, want at least 8", len(second))
	}
}

func TestRepeat_ContinuousPlayOnceAirsTheListThenStopsAcrossRuns(t *testing.T) {
	f := listModeSetup(t, domain.ListModeOnce)
	mat := New(f.db, 8*time.Hour)
	for run := 0; run < 2; run++ {
		if err := mat.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		entries := queryEntries(t, f)
		if len(entries) != 3 {
			t.Fatalf("run %d: %d entries, want the list once (3): %+v", run, len(entries), entries)
		}
		assertRotation(t, entries, 0)
		last := entries[len(entries)-1]
		if last.RotationNext == nil || *last.RotationNext != 3 {
			t.Errorf("run %d: last rotation_next = %v, want 3 (the list is done)", run, last.RotationNext)
		}
	}
}

func TestRepeat_TimedRegenerationIsStable(t *testing.T) {
	f := newFixture(t)
	f.createCatalogItem(t, "a", hourMs)
	f.createCatalogItem(t, "b", hourMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.setListMode(t, "blk", domain.ListModeRepeat)
	f.addBlockItem(t, "bi1", "blk", 0, "a")
	f.addBlockItem(t, "bi2", "blk", 1, "b")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	// A once occurrence a little in the past: the loop is already running.
	start := time.Now().Add(-90 * time.Minute).Truncate(time.Minute)
	if err := f.db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
		 VALUES ('once', ?, 'sched1', 'blk', 'once', ?, 1, 1, 1)`, f.tenantID, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	mat := New(f.db, 6*time.Hour)
	var runs [][]domain.EpgEntry
	for i := 0; i < 2; i++ {
		if err := mat.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, queryEntries(t, f))
	}
	if len(runs[0]) < 6 {
		t.Fatalf("%d entries, want the list looping through the window", len(runs[0]))
	}
	// 90 minutes in, the second item (b) is on the air.
	for i, e := range runs[0] {
		if want := []string{"b", "a"}[i%2]; e.CatalogItemID != want {
			t.Errorf("entry %d = %s, want %s", i, e.CatalogItemID, want)
		}
	}
	if len(runs[0]) != len(runs[1]) {
		t.Fatalf("regeneration changed the entry count: %d then %d", len(runs[0]), len(runs[1]))
	}
	for i := range runs[0] {
		if runs[0][i].CatalogItemID != runs[1][i].CatalogItemID || runs[0][i].StartAt != runs[1][i].StartAt {
			t.Errorf("entry %d moved on regeneration: %+v then %+v", i, runs[0][i], runs[1][i])
		}
	}
}

func TestRepeat_WarnsAboutARepeatingBlockThatCannotAir(t *testing.T) {
	f := newFixture(t)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "empty")
	f.setListMode(t, "empty", domain.ListModeRepeat)
	f.createBlock(t, "onceEmpty")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	start := time.Now().Add(time.Hour).UnixMilli()
	for _, b := range []string{"empty", "onceEmpty"} {
		if err := f.db.Exec(context.Background(),
			`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
			 VALUES (?, ?, 'sched1', ?, 'once', ?, 1, 1, 1)`, "si-"+b, f.tenantID, b, start); err != nil {
			t.Fatal(err)
		}
	}
	mat := New(f.db, 6*time.Hour)
	var ch domain.Channel
	if err := f.db.Query(context.Background(), &ch, "SELECT id, tenant_id, number, name, timezone, schedule_id, created_at, updated_at FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	warnings, err := mat.Warnings(context.Background(), f.tenantID, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Kind != WarningRepeatEmpty || warnings[0].BlockID != "empty" {
		t.Errorf("warnings = %+v, want one repeat-empty for the repeating block only", warnings)
	}
}
