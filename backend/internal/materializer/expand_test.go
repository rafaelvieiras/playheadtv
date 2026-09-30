package materializer

import (
	"sort"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time.LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestWeeklyOccurrenceTimes_UTC(t *testing.T) {
	// Wednesday 2026-09-30 is day_of_week 3.
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	times, err := weeklyOccurrenceTimes(3, "20:00", time.UTC, scanFrom, windowEnd)
	if err != nil {
		t.Fatalf("weeklyOccurrenceTimes: %v", err)
	}
	if len(times) != 1 {
		t.Fatalf("expected exactly one occurrence, got %d: %v", len(times), times)
	}
	want := time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)
	if !times[0].Equal(want) {
		t.Errorf("got %v, want %v", times[0], want)
	}
}

func TestWeeklyOccurrenceTimes_NonUTCTimezone(t *testing.T) {
	loc := mustLoc(t, "America/Sao_Paulo") // UTC-3, no DST since 2019
	// Monday 2026-09-28, 20:00 local -> 2026-09-28T23:00:00Z.
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

	times, err := weeklyOccurrenceTimes(1, "20:00", loc, scanFrom, windowEnd)
	if err != nil {
		t.Fatalf("weeklyOccurrenceTimes: %v", err)
	}
	if len(times) != 1 {
		t.Fatalf("expected exactly one occurrence, got %d: %v", len(times), times)
	}
	want := time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC)
	if !times[0].Equal(want) {
		t.Errorf("got %v (loc %v), want %v UTC", times[0], times[0].Location(), want)
	}
	if times[0].Location() != time.UTC {
		t.Errorf("expected result to be in UTC, got %v", times[0].Location())
	}
}

func TestWeeklyOccurrenceTimes_OutsideWindowExcluded(t *testing.T) {
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	// day_of_week 3 (Wednesday) falls well outside this one-day window.
	times, err := weeklyOccurrenceTimes(3, "20:00", time.UTC, scanFrom, windowEnd)
	if err != nil {
		t.Fatalf("weeklyOccurrenceTimes: %v", err)
	}
	if len(times) != 0 {
		t.Errorf("expected no occurrences, got %v", times)
	}
}

func TestOnceOccurrenceTime(t *testing.T) {
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

	inside := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if _, ok := onceOccurrenceTime(inside.UnixMilli(), scanFrom, windowEnd); !ok {
		t.Errorf("expected instant inside window to be included")
	}

	beforeWindow := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	if _, ok := onceOccurrenceTime(beforeWindow.UnixMilli(), scanFrom, windowEnd); ok {
		t.Errorf("expected instant before scanFrom to be excluded")
	}

	atWindowEnd := windowEnd // exclusive upper bound
	if _, ok := onceOccurrenceTime(atWindowEnd.UnixMilli(), scanFrom, windowEnd); ok {
		t.Errorf("expected instant at windowEnd to be excluded (exclusive)")
	}

	atScanFrom := scanFrom // inclusive lower bound
	if _, ok := onceOccurrenceTime(atScanFrom.UnixMilli(), scanFrom, windowEnd); !ok {
		t.Errorf("expected instant at scanFrom to be included (inclusive)")
	}
}

func TestResolveFillerRule_PrefersBlockScopeOverChannel(t *testing.T) {
	rules := []domain.FillerRule{
		{ID: "channel-rule", Kind: domain.FillerPreRoll, Scope: domain.ScopeChannel, ScopeID: "ch1", Position: 0},
		{ID: "block-rule", Kind: domain.FillerPreRoll, Scope: domain.ScopeBlock, ScopeID: "blk1", Position: 5},
	}
	got := resolveFillerRule(rules, domain.FillerPreRoll, "blk1", "ch1")
	if got == nil || got.ID != "block-rule" {
		t.Fatalf("expected block-rule, got %+v", got)
	}
}

func TestResolveFillerRule_LowestPositionWins(t *testing.T) {
	rules := []domain.FillerRule{
		{ID: "second", Kind: domain.FillerMidRoll, Scope: domain.ScopeChannel, ScopeID: "ch1", Position: 3},
		{ID: "first", Kind: domain.FillerMidRoll, Scope: domain.ScopeChannel, ScopeID: "ch1", Position: 1},
	}
	got := resolveFillerRule(rules, domain.FillerMidRoll, "blk1", "ch1")
	if got == nil || got.ID != "first" {
		t.Fatalf("expected rule with lowest position, got %+v", got)
	}
}

func TestResolveFillerRule_NoMatch(t *testing.T) {
	rules := []domain.FillerRule{
		{ID: "x", Kind: domain.FillerPreRoll, Scope: domain.ScopeChannel, ScopeID: "other-channel", Position: 0},
	}
	if got := resolveFillerRule(rules, domain.FillerPreRoll, "blk1", "ch1"); got != nil {
		t.Fatalf("expected no match, got %+v", got)
	}
}

func TestSelectFillerItem_SequentialAdvancesAndWraps(t *testing.T) {
	rule := domain.FillerRule{ID: "r1", Selection: domain.SelectionSequential}
	pool := []domain.FillerPoolItem{
		{ID: "a", CatalogItemID: "cat-a"},
		{ID: "b", CatalogItemID: "cat-b"},
		{ID: "c", CatalogItemID: "cat-c"},
	}
	state := newSelectionState()

	var picks []string
	for i := 0; i < 7; i++ {
		item, ok := selectFillerItem(rule, pool, state)
		if !ok {
			t.Fatalf("selectFillerItem: expected ok=true")
		}
		picks = append(picks, item.ID)
	}
	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	for i, w := range want {
		if picks[i] != w {
			t.Errorf("pick[%d] = %q, want %q (full: %v)", i, picks[i], w, picks)
			break
		}
	}
}

func TestSelectFillerItem_SequentialIsPerRule(t *testing.T) {
	pool := []domain.FillerPoolItem{{ID: "a"}, {ID: "b"}}
	state := newSelectionState()
	r1 := domain.FillerRule{ID: "r1", Selection: domain.SelectionSequential}
	r2 := domain.FillerRule{ID: "r2", Selection: domain.SelectionSequential}

	first, _ := selectFillerItem(r1, pool, state)
	if first.ID != "a" {
		t.Fatalf("r1 first pick = %q, want a", first.ID)
	}
	// r2's cursor is independent of r1's.
	otherFirst, _ := selectFillerItem(r2, pool, state)
	if otherFirst.ID != "a" {
		t.Fatalf("r2 first pick = %q, want a (rules should have independent cursors)", otherFirst.ID)
	}
}

func TestSelectFillerItem_EmptyPool(t *testing.T) {
	rule := domain.FillerRule{ID: "r1", Selection: domain.SelectionSequential}
	_, ok := selectFillerItem(rule, nil, newSelectionState())
	if ok {
		t.Errorf("expected ok=false for empty pool")
	}
}

func TestSelectFillerItem_Weighted(t *testing.T) {
	rule := domain.FillerRule{ID: "r1", Selection: domain.SelectionWeighted}
	pool := []domain.FillerPoolItem{
		{ID: "heavy", Weight: 9},
		{ID: "light", Weight: 1},
	}
	state := newSelectionState()

	const trials = 20000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		item, ok := selectFillerItem(rule, pool, state)
		if !ok {
			t.Fatalf("selectFillerItem: expected ok=true")
		}
		counts[item.ID]++
	}

	heavyRatio := float64(counts["heavy"]) / float64(trials)
	if heavyRatio < 0.80 || heavyRatio > 0.98 {
		t.Errorf("heavy item picked %.3f of the time, want roughly 0.90 (counts=%v)", heavyRatio, counts)
	}
}

func TestSelectFillerItem_RandomStaysInBounds(t *testing.T) {
	rule := domain.FillerRule{ID: "r1", Selection: domain.SelectionRandom}
	pool := []domain.FillerPoolItem{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	state := newSelectionState()
	valid := map[string]bool{"a": true, "b": true, "c": true}

	for i := 0; i < 500; i++ {
		item, ok := selectFillerItem(rule, pool, state)
		if !ok {
			t.Fatalf("selectFillerItem: expected ok=true")
		}
		if !valid[item.ID] {
			t.Fatalf("selectFillerItem returned out-of-pool item %q", item.ID)
		}
	}
}

func TestFillGap_ItemsThatFitAreInserted(t *testing.T) {
	rule := &domain.FillerRule{ID: "r1", FillerPoolID: "pool1", Selection: domain.SelectionSequential}
	pool := map[string][]domain.FillerPoolItem{
		"pool1": {{ID: "a", CatalogItemID: "cat-a"}},
	}
	catalogDuration := map[string]int64{"cat-a": 10_000} // 10s
	cursor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	target := cursor.Add(25 * time.Second) // room for 2 full items, 5s residual

	entries, end := fillGap(rule, pool, catalogDuration, cursor, target, domain.EpgFallback, "tenant1", "chan1", 1000, nil, newSelectionState())
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].StartAt != cursor.UnixMilli() || entries[0].EndAt != cursor.Add(10*time.Second).UnixMilli() {
		t.Errorf("unexpected first entry timing: %+v", entries[0])
	}
	wantEnd := cursor.Add(20 * time.Second)
	if !end.Equal(wantEnd) {
		t.Errorf("end cursor = %v, want %v (5s residual left unfilled)", end, wantEnd)
	}
	for _, e := range entries {
		if e.TenantID != "tenant1" || e.ChannelID != "chan1" || e.Kind != domain.EpgFallback {
			t.Errorf("unexpected entry metadata: %+v", e)
		}
		if e.SourceScheduleItemID != nil {
			t.Errorf("expected nil SourceScheduleItemID for a gap-fill entry, got %v", *e.SourceScheduleItemID)
		}
	}
}

func TestFillGap_ItemThatDoesNotFitLeavesResidual(t *testing.T) {
	rule := &domain.FillerRule{ID: "r1", FillerPoolID: "pool1", Selection: domain.SelectionSequential}
	pool := map[string][]domain.FillerPoolItem{
		"pool1": {{ID: "a", CatalogItemID: "cat-a"}},
	}
	catalogDuration := map[string]int64{"cat-a": 30_000} // 30s, bigger than target
	cursor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	target := cursor.Add(10 * time.Second)

	entries, end := fillGap(rule, pool, catalogDuration, cursor, target, domain.EpgTail, "tenant1", "chan1", 1000, nil, newSelectionState())
	if len(entries) != 0 {
		t.Fatalf("expected no entries when the only item doesn't fit, got %+v", entries)
	}
	if !end.Equal(cursor) {
		t.Errorf("end cursor = %v, want unchanged %v", end, cursor)
	}
}

func TestFillGap_NilRuleOrEmptyPoolIsNoop(t *testing.T) {
	cursor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	target := cursor.Add(time.Hour)

	entries, end := fillGap(nil, nil, nil, cursor, target, domain.EpgFallback, "t", "c", 0, nil, newSelectionState())
	if len(entries) != 0 || !end.Equal(cursor) {
		t.Errorf("nil rule: expected no-op, got entries=%v end=%v", entries, end)
	}

	rule := &domain.FillerRule{ID: "r1", FillerPoolID: "pool1"}
	entries, end = fillGap(rule, map[string][]domain.FillerPoolItem{}, nil, cursor, target, domain.EpgFallback, "t", "c", 0, nil, newSelectionState())
	if len(entries) != 0 || !end.Equal(cursor) {
		t.Errorf("empty pool: expected no-op, got entries=%v end=%v", entries, end)
	}
}

// TestExpandChannel_OverlapPushesSecondOccurrenceLater builds two weekly
// occurrences whose nominal starts are only 30 minutes apart, but whose first
// block runs a full hour — the second occurrence must be pushed to start
// right when the first one's content (plus any filler) actually ends, never
// overlapping it.
// TestExpandChannel_OverrunDropsWholeItemAndRespectsNextStart is issue #10's
// core guarantee in its starkest form: a single 60-minute item can't be
// trimmed (no client-side support for cutting mid-item), so when it alone
// doesn't fit the 30-minute gap to the next occurrence, it is dropped
// entirely rather than delaying that next occurrence by even one second.
func TestExpandChannel_OverrunDropsWholeItemAndRespectsNextStart(t *testing.T) {
	channelID := "chan1"
	tenantID := "tenant1"

	// Both occurrences use block "long" (60 min) so we can predict the
	// overrun precisely; a real schedule would normally use different
	// blocks, but nothing in the algorithm assumes that.
	scheduleItems := []domain.ScheduleItem{
		{
			ID: "se1", ScheduleID: "sched1", BlockID: "long",
			Recurrence: domain.RecurrenceWeekly,
			DayOfWeek:  intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true,
		},
		{
			ID: "se2", ScheduleID: "sched1", BlockID: "long",
			Recurrence: domain.RecurrenceWeekly,
			DayOfWeek:  intPtr(3), StartTimeLocal: strPtr("20:30"), Active: true,
		},
	}

	in := channelInput{
		ChannelID:     channelID,
		TenantID:      tenantID,
		Timezone:      "UTC",
		ScheduleItems: scheduleItems,
		BlockItems: map[string][]domain.BlockItem{
			"long": {{ID: "bi1", BlockID: "long", Position: 0, CatalogItemID: "cat-movie"}},
		},
		CatalogDuration: map[string]int64{"cat-movie": int64(60 * time.Minute / time.Millisecond)},
	}

	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) // Monday
	// Far enough past 20:30 + 60 min that the second occurrence's own item
	// fits fine — only the first occurrence's item is expected to be
	// dropped, for not fitting *its* 30-minute gap to the second occurrence.
	windowEnd := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	now := scanFrom

	entries := expandChannel(in, now, scanFrom, windowEnd, newSelectionState())
	if len(entries) != 1 {
		t.Fatalf("expected 1 program entry (the 20:30 occurrence's item, dropped by the 20:00 one not fitting), got %d: %+v", len(entries), entries)
	}

	want := time.Date(2026, 9, 30, 20, 30, 0, 0, time.UTC).UnixMilli()
	if entries[0].StartAt != want {
		t.Fatalf("second occurrence StartAt = %d, want exactly its nominal start %d (never delayed)", entries[0].StartAt, want)
	}
}

// TestExpandChannel_OverrunDropsTrailingItemsOnly proves items that DO fit
// still air, and only the trailing ones that don't are dropped.
func TestExpandChannel_OverrunDropsTrailingItemsOnly(t *testing.T) {
	channelID := "chan1"
	tenantID := "tenant1"

	scheduleItems := []domain.ScheduleItem{
		{
			ID: "se1", ScheduleID: "sched1", BlockID: "three-items",
			Recurrence: domain.RecurrenceOnce,
			StartAt:    int64Ptr(time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC).UnixMilli()),
			Active:     true,
		},
		{
			ID: "se2", ScheduleID: "sched1", BlockID: "three-items",
			Recurrence: domain.RecurrenceOnce,
			StartAt:    int64Ptr(time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC).UnixMilli()),
			Active:     true,
		},
	}

	// Three 25-minute items: the first two (50 min) fit in the 60-minute
	// gap, the third (which would end at 75 min) does not.
	in := channelInput{
		ChannelID:     channelID,
		TenantID:      tenantID,
		Timezone:      "UTC",
		ScheduleItems: scheduleItems,
		BlockItems: map[string][]domain.BlockItem{
			"three-items": {
				{ID: "bi1", BlockID: "three-items", Position: 0, CatalogItemID: "cat-a"},
				{ID: "bi2", BlockID: "three-items", Position: 1, CatalogItemID: "cat-b"},
				{ID: "bi3", BlockID: "three-items", Position: 2, CatalogItemID: "cat-c"},
			},
		},
		CatalogDuration: map[string]int64{
			"cat-a": int64(25 * time.Minute / time.Millisecond),
			"cat-b": int64(25 * time.Minute / time.Millisecond),
			"cat-c": int64(25 * time.Minute / time.Millisecond),
		},
	}

	scanFrom := time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC)
	// Well past the second occurrence's own (unconstrained, it's last in the
	// window) full item list, so only the first occurrence's cutoff is under
	// test here.
	windowEnd := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	now := scanFrom

	boundary := time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC).UnixMilli()
	all := expandChannel(in, now, scanFrom, windowEnd, newSelectionState())
	var entries []domain.EpgEntry
	for _, e := range all {
		if e.StartAt < boundary {
			entries = append(entries, e)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 program entries before 18:00 (cat-a, cat-b; cat-c dropped), got %d: %+v", len(entries), entries)
	}
	if entries[0].CatalogItemID != "cat-a" || entries[1].CatalogItemID != "cat-b" {
		t.Fatalf("entries = %+v, want cat-a then cat-b (cat-c dropped, it doesn't fit)", entries)
	}
	wantSecondEnd := time.Date(2026, 9, 28, 17, 50, 0, 0, time.UTC).UnixMilli()
	if entries[1].EndAt != wantSecondEnd {
		t.Fatalf("second item EndAt = %d, want %d", entries[1].EndAt, wantSecondEnd)
	}
}

// TestExpandChannel_OverrunReservesPostrollBeforeNextStart is the exact
// scenario issue #10 was closed on: a block at 17:00 whose programming would
// otherwise run past 17:55, plus a 5-minute postroll, with another
// occurrence at 18:00. Programming must stop by 17:55 so the postroll can
// run in full and still land the next occurrence exactly on time.
func TestExpandChannel_OverrunReservesPostrollBeforeNextStart(t *testing.T) {
	channelID := "chan1"
	tenantID := "tenant1"

	scheduleItems := []domain.ScheduleItem{
		{
			ID: "se1", ScheduleID: "sched1", BlockID: "long",
			Recurrence: domain.RecurrenceOnce,
			StartAt:    int64Ptr(time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC).UnixMilli()),
			Active:     true,
		},
		{
			ID: "se2", ScheduleID: "sched1", BlockID: "next",
			Recurrence: domain.RecurrenceOnce,
			StartAt:    int64Ptr(time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC).UnixMilli()),
			Active:     true,
		},
	}

	in := channelInput{
		ChannelID:     channelID,
		TenantID:      tenantID,
		Timezone:      "UTC",
		ScheduleItems: scheduleItems,
		BlockItems: map[string][]domain.BlockItem{
			// cat-show-1 (55 min) fits exactly up to the 17:55 cutoff;
			// cat-show-2 (35 min) would end at 18:30 and is dropped.
			"long": {
				{ID: "bi1", BlockID: "long", Position: 0, CatalogItemID: "cat-show-1"},
				{ID: "bi2", BlockID: "long", Position: 1, CatalogItemID: "cat-show-2"},
			},
			"next": {{ID: "bi3", BlockID: "next", Position: 0, CatalogItemID: "cat-next"}},
		},
		FillerRules: []domain.FillerRule{
			{ID: "fr1", Kind: domain.FillerPostRoll, Scope: domain.ScopeBlock, ScopeID: "long", FillerPoolID: "pool1", Selection: domain.SelectionSequential},
		},
		PoolItems: map[string][]domain.FillerPoolItem{
			"pool1": {{ID: "pi1", FillerPoolID: "pool1", CatalogItemID: "cat-post"}},
		},
		CatalogDuration: map[string]int64{
			"cat-show-1": int64(55 * time.Minute / time.Millisecond),
			"cat-show-2": int64(35 * time.Minute / time.Millisecond),
			"cat-post":   int64(5 * time.Minute / time.Millisecond),
			"cat-next":   int64(30 * time.Minute / time.Millisecond),
		},
	}

	scanFrom := time.Date(2026, 9, 28, 17, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 28, 18, 30, 0, 0, time.UTC)
	now := scanFrom

	entries := expandChannel(in, now, scanFrom, windowEnd, newSelectionState())

	var program, postroll, next *domain.EpgEntry
	for i := range entries {
		switch {
		case entries[i].Kind == domain.EpgProgram && entries[i].CatalogItemID == "cat-show-1":
			program = &entries[i]
		case entries[i].CatalogItemID == "cat-show-2":
			t.Fatalf("cat-show-2 should have been dropped (doesn't fit before the 17:55 cutoff), got %+v", entries[i])
		case entries[i].Kind == domain.EpgPostRoll:
			postroll = &entries[i]
		case entries[i].CatalogItemID == "cat-next":
			next = &entries[i]
		}
	}
	if program == nil || postroll == nil || next == nil {
		t.Fatalf("expected a program, a postroll and the next occurrence's item, got %+v", entries)
	}

	wantProgramEnd := time.Date(2026, 9, 28, 17, 55, 0, 0, time.UTC).UnixMilli()
	if program.EndAt != wantProgramEnd {
		t.Fatalf("program EndAt = %d, want 17:55 (%d) so the 5-minute postroll still fits before 18:00", program.EndAt, wantProgramEnd)
	}
	if postroll.StartAt != program.EndAt {
		t.Fatalf("postroll StartAt = %d, want it to start exactly when the program stops (%d)", postroll.StartAt, program.EndAt)
	}
	wantNextStart := time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC).UnixMilli()
	if postroll.EndAt != wantNextStart {
		t.Fatalf("postroll EndAt = %d, want exactly 18:00 (%d)", postroll.EndAt, wantNextStart)
	}
	if next.StartAt != wantNextStart {
		t.Fatalf("next occurrence StartAt = %d, want exactly its nominal start %d (never delayed)", next.StartAt, wantNextStart)
	}
}

func TestExpandChannel_PrerollAppearsBeforeFirstProgram(t *testing.T) {
	channelID := "chan1"
	tenantID := "tenant1"

	scheduleItems := []domain.ScheduleItem{
		{
			ID: "se1", ScheduleID: "sched1", BlockID: "blk1",
			Recurrence: domain.RecurrenceOnce,
			StartAt:    int64Ptr(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).UnixMilli()),
			Active:     true,
		},
	}

	in := channelInput{
		ChannelID:       channelID,
		TenantID:        tenantID,
		Timezone:        "UTC",
		ScheduleItems:   scheduleItems,
		BlockItems: map[string][]domain.BlockItem{
			"blk1": {{ID: "bi1", BlockID: "blk1", Position: 0, CatalogItemID: "cat-show"}},
		},
		FillerRules: []domain.FillerRule{
			{ID: "fr1", Kind: domain.FillerPreRoll, Scope: domain.ScopeChannel, ScopeID: channelID, FillerPoolID: "pool1", Selection: domain.SelectionSequential},
		},
		PoolItems: map[string][]domain.FillerPoolItem{
			"pool1": {{ID: "pi1", FillerPoolID: "pool1", CatalogItemID: "cat-bumper"}},
		},
		CatalogDuration: map[string]int64{
			"cat-show":   int64(30 * time.Minute / time.Millisecond),
			"cat-bumper": int64(15 * time.Second / time.Millisecond),
		},
	}

	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	now := scanFrom

	entries := expandChannel(in, now, scanFrom, windowEnd, newSelectionState())

	var preroll, program *domain.EpgEntry
	for i := range entries {
		switch entries[i].Kind {
		case domain.EpgPreRoll:
			preroll = &entries[i]
		case domain.EpgProgram:
			program = &entries[i]
		}
	}
	if preroll == nil {
		t.Fatalf("expected a preroll entry, got %+v", entries)
	}
	if program == nil {
		t.Fatalf("expected a program entry, got %+v", entries)
	}
	if preroll.StartAt >= program.StartAt {
		t.Errorf("preroll StartAt %d should be before program StartAt %d", preroll.StartAt, program.StartAt)
	}
	if preroll.EndAt != program.StartAt {
		t.Errorf("preroll should end exactly when the program starts: preroll.EndAt=%d program.StartAt=%d", preroll.EndAt, program.StartAt)
	}
	if preroll.CatalogItemID != "cat-bumper" {
		t.Errorf("preroll CatalogItemID = %q, want cat-bumper", preroll.CatalogItemID)
	}
}

// TestExpandChannel_DynamicBlockLoopsAsProgram proves a dynamic block
// (DynamicBlockIDs[blockID] == true) loops its item list continuously to
// fill the entire window between one schedule_entry occurrence and the
// next, tagged as domain.EpgProgram (never fallback/tail) — the behavior
// that makes a "genre channel" real programming instead of filler.
func TestExpandChannel_DynamicBlockLoopsAsProgram(t *testing.T) {
	channelID := "chan1"
	tenantID := "tenant1"
	blockID := "dyn-block"

	// Two weekly occurrences of the same dynamic block, a day apart, so the
	// loop must fill a whole day's worth of programming per occurrence.
	scheduleItems := []domain.ScheduleItem{
		{
			ID: "se1", ScheduleID: "sched1", BlockID: blockID,
			Recurrence: domain.RecurrenceWeekly,
			DayOfWeek:  intPtr(1), StartTimeLocal: strPtr("00:00"), Active: true,
		},
		{
			ID: "se2", ScheduleID: "sched1", BlockID: blockID,
			Recurrence: domain.RecurrenceWeekly,
			DayOfWeek:  intPtr(2), StartTimeLocal: strPtr("00:00"), Active: true,
		},
	}

	items := []domain.BlockItem{
		{ID: "bi1", BlockID: blockID, Position: 0, CatalogItemID: "cat-a"},
		{ID: "bi2", BlockID: blockID, Position: 1, CatalogItemID: "cat-b"},
		{ID: "bi3", BlockID: blockID, Position: 2, CatalogItemID: "cat-c"},
	}

	in := channelInput{
		ChannelID:       channelID,
		TenantID:        tenantID,
		Timezone:        "UTC",
		ScheduleItems:   scheduleItems,
		BlockItems:      map[string][]domain.BlockItem{blockID: items},
		DynamicBlockIDs: map[string]bool{blockID: true},
		CatalogDuration: map[string]int64{
			"cat-a": int64(10 * time.Minute / time.Millisecond),
			"cat-b": int64(20 * time.Minute / time.Millisecond),
			"cat-c": int64(30 * time.Minute / time.Millisecond),
		},
	}

	// Monday 2026-09-28 00:00 UTC is day_of_week 1; window covers both
	// occurrences (Monday and Tuesday) plus a bit of Wednesday so the second
	// occurrence's loop has somewhere to end.
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	now := scanFrom

	entries := expandChannel(in, now, scanFrom, windowEnd, newSelectionState())
	if len(entries) == 0 {
		t.Fatal("expected entries, got none")
	}

	// (a) every entry for this dynamic block is kind=program.
	for _, e := range entries {
		if e.Kind != domain.EpgProgram {
			t.Errorf("entry %+v: Kind = %q, want %q (dynamic block must never emit fallback/tail)", e, e.Kind, domain.EpgProgram)
		}
	}

	// Sort by StartAt to walk the timeline in order.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].StartAt < entries[j].StartAt })

	// (b) the item list actually repeats: with 3 items (10+20+30=60min) per
	// lap and a full day (1440min) to fill in the first occurrence, we
	// expect the cat-a/cat-b/cat-c cycle to appear more than once.
	var catalogSeq []string
	for _, e := range entries {
		catalogSeq = append(catalogSeq, e.CatalogItemID)
	}
	wantCycle := []string{"cat-a", "cat-b", "cat-c"}
	cycles := 0
	for i := 0; i+3 <= len(catalogSeq); i += 3 {
		if catalogSeq[i] == wantCycle[0] && catalogSeq[i+1] == wantCycle[1] && catalogSeq[i+2] == wantCycle[2] {
			cycles++
		}
	}
	if cycles < 2 {
		t.Fatalf("expected the 3-item cycle to repeat at least twice, got sequence %v", catalogSeq)
	}

	// (c) no overlap and no gap beyond the expected end-of-window residual:
	// each entry's EndAt must equal the next entry's StartAt.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].EndAt != entries[i].StartAt {
			t.Errorf("gap/overlap between entries[%d] (end=%d) and entries[%d] (start=%d)",
				i-1, entries[i-1].EndAt, i, entries[i].StartAt)
		}
	}
}

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }
func int64Ptr(v int64) *int64 { return &v }
