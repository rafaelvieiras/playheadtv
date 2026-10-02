package materializer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

const epMs = int64(30 * 60 * 1000)

func i64(v int64) *int64 { return &v }

// testEpisodes builds n episodes e1..en of one season each 30 minutes long,
// in a scrambled id order so ordering can never come from the ids.
func testEpisodes(n int) []series.Episode {
	out := make([]series.Episode, n)
	for i := 0; i < n; i++ {
		out[i] = series.Episode{
			ID:            fmt.Sprintf("e%02d", n-i), // descending ids
			Title:         fmt.Sprintf("Episode %d", n-i),
			DurationMs:    epMs,
			SeasonNumber:  i64(1),
			EpisodeNumber: i64(int64(n - i)),
		}
	}
	return out
}

func specOf(order string, seed int64) domain.SeriesSpec {
	return domain.SeriesSpec{MediaSourceID: "ms", SeriesTitle: "Show", PlaybackOrder: order, Seed: seed}
}

// drain takes n episodes from the run, consuming each as the materializer does.
func drain(r *seriesRun, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		ep, cycle, ok := r.next()
		if !ok {
			break
		}
		r.apply(ep.ID, cycle)
		out = append(out, ep.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSeriesOrder_OriginalIsSeasonThenEpisodeSpecialsFirstAndWraps(t *testing.T) {
	eps := []series.Episode{
		{ID: "s2e1", DurationMs: epMs, SeasonNumber: i64(2), EpisodeNumber: i64(1)},
		{ID: "s1e2", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(2)},
		{ID: "nonum", DurationMs: epMs, Title: "zzz"},
		{ID: "s0e1", DurationMs: epMs, SeasonNumber: i64(0), EpisodeNumber: i64(1)},
		{ID: "s1e1", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(1)},
	}
	r := newSeriesRun(specOf(domain.OrderOriginal, 1), "chan", eps, 0, nil, nil, 0)
	got := drain(r, 7)
	want := []string{"s0e1", "s1e1", "s1e2", "s2e1", "nonum", "s0e1", "s1e1"}
	if !equalStrings(got, want) {
		t.Errorf("original order = %v, want %v", got, want)
	}
}

func TestSeriesOrder_ReleaseDateByPremiereNullsLast(t *testing.T) {
	eps := []series.Episode{
		{ID: "late", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(1), PremiereDate: i64(3000)},
		{ID: "nodate", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(2)},
		// Aired first although it has the highest episode number: the order is by date, not numbering.
		{ID: "early", DurationMs: epMs, SeasonNumber: i64(2), EpisodeNumber: i64(9), PremiereDate: i64(1000)},
		{ID: "mid", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(3), PremiereDate: i64(2000)},
	}
	r := newSeriesRun(specOf(domain.OrderReleaseDate, 1), "chan", eps, 0, nil, nil, 0)
	got := drain(r, 5)
	want := []string{"early", "mid", "late", "nodate", "early"}
	if !equalStrings(got, want) {
		t.Errorf("release_date order = %v, want %v", got, want)
	}
}

func TestSeriesOrder_ShuffleEveryEpisodeOncePerCycleAndNeverOpensWithLast(t *testing.T) {
	for _, n := range []int{2, 3, 7, 25} {
		for seed := int64(1); seed <= 40; seed++ {
			r := newSeriesRun(specOf(domain.OrderShuffle, seed), "chan", testEpisodes(n), 0, nil, nil, 0)
			got := drain(r, n*6)
			for c := 0; c < 6; c++ {
				cycle := got[c*n : (c+1)*n]
				seen := map[string]bool{}
				for _, id := range cycle {
					if seen[id] {
						t.Fatalf("n=%d seed=%d cycle %d repeats %s before every episode played: %v", n, seed, c, id, cycle)
					}
					seen[id] = true
				}
				if c > 0 && cycle[0] == got[c*n-1] {
					t.Fatalf("n=%d seed=%d cycle %d opens with the previous cycle's last episode %s", n, seed, c, cycle[0])
				}
			}
		}
	}
}

func TestSeriesOrder_ShuffleReproducibleFromSeedAndVariesWithSeedAndChannel(t *testing.T) {
	order := func(seed int64, channel string) []string {
		return drain(newSeriesRun(specOf(domain.OrderShuffle, seed), channel, testEpisodes(12), 0, nil, nil, 0), 24)
	}
	if !equalStrings(order(7, "chan"), order(7, "chan")) {
		t.Error("same seed and channel must reproduce the same order")
	}
	if equalStrings(order(7, "chan"), order(8, "chan")) {
		t.Error("different seeds produced the same 24-episode order")
	}
	if equalStrings(order(7, "chanA"), order(7, "chanB")) {
		t.Error("two channels sharing a block must not shuffle identically")
	}
	// A cycle is a fresh shuffle, not a repeat of the previous one.
	o := order(7, "chan")
	if equalStrings(o[:12], o[12:]) {
		t.Error("second cycle repeats the first cycle's order")
	}
}

func TestSeriesOrder_ShuffleSingleEpisodeSeriesRepeats(t *testing.T) {
	r := newSeriesRun(specOf(domain.OrderShuffle, 3), "chan", testEpisodes(1), 0, nil, nil, 0)
	if got := drain(r, 3); !equalStrings(got, []string{"e01", "e01", "e01"}) {
		t.Errorf("single episode series = %v", got)
	}
}

// rebuild constructs a fresh run whose cursor is replayed from the rows the
// given run's consumed episodes would have left in epg_entry.
func historyOf(played []string, cycleLen int) []seriesRow {
	rows := make([]seriesRow, len(played))
	for i, id := range played {
		rows[i] = seriesRow{StartAt: int64(1000 + i), CatalogItemID: id, Cycle: i / cycleLen}
	}
	return rows
}

func TestSeriesOrder_CursorRebuildsFromPersistedRows(t *testing.T) {
	for _, order := range []string{domain.OrderOriginal, domain.OrderShuffle, domain.OrderReleaseDate, domain.OrderRandom} {
		eps := testEpisodes(6)
		full := drain(newSeriesRun(specOf(order, 5), "chan", eps, 0, nil, nil, 0), 20)
		for _, k := range []int{1, 5, 6, 7, 13} {
			r := newSeriesRun(specOf(order, 5), "chan", eps, 0, historyOf(full[:k], 6), nil, 0)
			rest := drain(r, 20-k)
			if !equalStrings(rest, full[k:]) {
				t.Errorf("%s: resuming after %d episodes gave %v, want %v", order, k, rest, full[k:])
			}
		}
	}
}

func TestSeriesOrder_ShuffleCatalogGainJoinsCurrentCycleOnceAndLossLeavesNoHole(t *testing.T) {
	eps := testEpisodes(8)
	base := drain(newSeriesRun(specOf(domain.OrderShuffle, 11), "chan", eps, 0, nil, nil, 0), 8)

	// Three played, then a new episode arrives and one unplayed one vanishes.
	played := base[:3]
	removed := base[5]
	var changed []series.Episode
	for _, e := range eps {
		if e.ID != removed {
			changed = append(changed, e)
		}
	}
	changed = append(changed, series.Episode{ID: "new", Title: "New", DurationMs: epMs, SeasonNumber: i64(1), EpisodeNumber: i64(99)})

	r := newSeriesRun(specOf(domain.OrderShuffle, 11), "chan", changed, 0, historyOf(played, 8), nil, 0)
	rest := drain(r, 6) // 8 - 3 played - 1 removed + 1 new = 5 left in the cycle, then a new cycle opens
	cycleRest := rest[:5]
	seen := map[string]bool{}
	for _, id := range played {
		seen[id] = true
	}
	for _, id := range cycleRest {
		if seen[id] {
			t.Fatalf("episode %s repeated inside the cycle: %v (played %v)", id, cycleRest, played)
		}
		if id == removed {
			t.Fatalf("removed episode %s still aired", id)
		}
		seen[id] = true
	}
	if !seen["new"] {
		t.Fatalf("the added episode did not join the current cycle: %v", cycleRest)
	}
	if len(seen) != 8 {
		t.Fatalf("cycle covered %d episodes, want 8 (3 played + 5 to go)", len(seen))
	}
	// The new episode's place is deterministic from seed and id: rebuilding gives the same order.
	again := drain(newSeriesRun(specOf(domain.OrderShuffle, 11), "chan", changed, 0, historyOf(played, 8), nil, 0), 6)
	if !equalStrings(rest, again) {
		t.Errorf("join position is not deterministic: %v vs %v", rest, again)
	}
	// After the cycle completes a new one starts, not opening with the last played.
	if rest[5] == cycleRest[4] {
		t.Errorf("new cycle opens with the previous cycle's last episode")
	}
}

func TestSeriesOrder_RandomIsDeterministicAndCoversEpisodes(t *testing.T) {
	a := drain(newSeriesRun(specOf(domain.OrderRandom, 9), "chan", testEpisodes(5), 0, nil, nil, 0), 200)
	b := drain(newSeriesRun(specOf(domain.OrderRandom, 9), "chan", testEpisodes(5), 0, nil, nil, 0), 200)
	if !equalStrings(a, b) {
		t.Error("random is not reproducible from its seed")
	}
	seen := map[string]bool{}
	for _, id := range a {
		seen[id] = true
	}
	if len(seen) != 5 {
		t.Errorf("200 random picks covered %d of 5 episodes", len(seen))
	}
}

func TestSeriesRun_CursorOriginIgnoresOlderRows(t *testing.T) {
	eps := testEpisodes(4)
	old := []seriesRow{{StartAt: 100, CatalogItemID: "e01", Cycle: 0}, {StartAt: 200, CatalogItemID: "e02", Cycle: 0}}
	r := newSeriesRun(specOf(domain.OrderOriginal, 1), "chan", eps, 500, old, nil, 0)
	if got := drain(r, 1); got[0] != "e01" {
		t.Errorf("rows before the origin counted: first episode %s, want e01", got[0])
	}
}

func TestSeriesRun_AdoptsStartedRowsButRecomputesFutureOnes(t *testing.T) {
	eps := testEpisodes(5) // original order would give e01 at every slot of a fresh run
	rows := []seriesRow{
		{StartAt: 1000, CatalogItemID: "e04", Cycle: 0}, // already started
		{StartAt: 9000, CatalogItemID: "e05", Cycle: 0}, // still to air
	}
	r := newSeriesRun(specOf(domain.OrderOriginal, 1), "chan", eps, 0, nil, rows, 5000)
	if slot, ok := r.plan(1000); !ok || slot.Episode.ID != "e04" {
		t.Errorf("started slot = %+v, want the persisted e04", slot)
	}
	if slot, ok := r.plan(9000); !ok || slot.Episode.ID != "e01" {
		t.Errorf("future slot = %+v, want a fresh pick (e01), not the persisted row", slot)
	}
	// An adopted episode that left the catalog is not forced back in.
	r = newSeriesRun(specOf(domain.OrderOriginal, 1), "chan", eps[:1], 0, nil, rows[:1], 5000)
	if slot, _ := r.plan(1000); slot.Episode.ID == "e04" {
		t.Error("adopted an episode that no longer exists")
	}
}

// ---- expansion ----

func seriesBlockInput(order string, seed int64, n int, blockItems []domain.BlockItem, sched []domain.ScheduleItem, extraDur map[string]int64) (channelInput, *seriesRun) {
	run := newSeriesRun(specOf(order, seed), "chan1", testEpisodes(n), 0, nil, nil, 0)
	return channelInput{
		ChannelID:       "chan1",
		TenantID:        "tenant1",
		Timezone:        "UTC",
		ScheduleItems:   sched,
		BlockItems:      map[string][]domain.BlockItem{"blk": blockItems},
		CatalogDuration: extraDur,
		Series:          map[string]*seriesRun{"sbi": run},
	}, run
}

func seriesItem() domain.BlockItem {
	return domain.BlockItem{ID: "sbi", BlockID: "blk", ItemType: domain.BlockItemSeries}
}

func TestExpandChannel_SeriesAirsUntilNextOccurrenceAndDropsWhatDoesNotFit(t *testing.T) {
	// Wed 20:00 block (movie 60m, then the series) and a Wed 22:15 one-off
	// block: the series fills 21:00-22:15 with two whole 30m episodes and the
	// third (22:00-22:30) does not fit and is dropped.
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true},
		{ID: "se2", BlockID: "movie", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("22:15"), Active: true},
	}
	in, _ := seriesBlockInput(domain.OrderOriginal, 1, 10,
		[]domain.BlockItem{
			{ID: "bi1", BlockID: "blk", CatalogItemID: "m1"},
			seriesItem(),
			{ID: "bi3", BlockID: "blk", CatalogItemID: "m1"}, // after the series: never airs
		}, sched, map[string]int64{"m1": 60 * 60 * 1000, "m2": 30 * 60 * 1000})
	in.BlockItems["movie"] = []domain.BlockItem{{ID: "bi9", BlockID: "movie", CatalogItemID: "m2"}}

	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC), newSelectionState())

	var got []string
	for _, e := range entries {
		got = append(got, fmt.Sprintf("%s@%s", e.CatalogItemID, time.UnixMilli(e.StartAt).UTC().Format("15:04")))
	}
	// e03 (22:00-22:30) would overrun the 22:15 occurrence: dropped, as is the
	// movie placed after the series in the block.
	want := []string{"m1@20:00", "e01@21:00", "e02@21:30", "m2@22:15"}
	if !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for _, e := range entries[1:3] {
		if e.SourceBlockItemID == nil || *e.SourceBlockItemID != "sbi" || e.SeriesCycle == nil || *e.SeriesCycle != 0 || e.Kind != domain.EpgProgram {
			t.Errorf("series entry lacks its source: %+v", e)
		}
	}
	if entries[0].SourceBlockItemID != nil {
		t.Errorf("catalog entry carries a series source: %+v", entries[0])
	}
}

func TestExpandChannel_SeriesLastOccurrenceFillsTheWindowAndStopsThere(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true},
	}
	in, run := seriesBlockInput(domain.OrderOriginal, 1, 3, []domain.BlockItem{seriesItem()}, sched, nil)
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, windowEnd, newSelectionState())
	// 20:00 to 23:00 = six 30m episodes over a 3-episode series: two cycles.
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 6: %+v", len(entries), entries)
	}
	cycles := []int{*entries[0].SeriesCycle, *entries[3].SeriesCycle}
	if cycles[0] != 0 || cycles[1] != 1 {
		t.Errorf("cycles = %v, want [0 1]", cycles)
	}
	if last := entries[len(entries)-1]; last.EndAt != windowEnd.UnixMilli() {
		t.Errorf("last episode ends %v, want exactly the window end", time.UnixMilli(last.EndAt))
	}
	if run.emitted != 6 {
		t.Errorf("run.emitted = %d, want 6", run.emitted)
	}
}

func TestExpandChannel_SeriesSecondOccurrenceContinuesTheCursor(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true},
		{ID: "se2", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(4), StartTimeLocal: strPtr("20:00"), Active: true},
		{ID: "se3", BlockID: "stop", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("21:00"), Active: true},
		{ID: "se4", BlockID: "stop", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(4), StartTimeLocal: strPtr("21:00"), Active: true},
	}
	in, _ := seriesBlockInput(domain.OrderOriginal, 1, 10, []domain.BlockItem{seriesItem()}, sched, map[string]int64{"x": epMs})
	in.BlockItems["stop"] = []domain.BlockItem{{ID: "bx", BlockID: "stop", CatalogItemID: "x"}}
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), newSelectionState())
	var got []string
	for _, e := range entries {
		if e.SourceBlockItemID != nil {
			got = append(got, e.CatalogItemID)
		}
	}
	// Wednesday's hour takes e01, e02; Thursday's continues with e03, e04.
	want := []string{"e01", "e02", "e03", "e04"}
	if !equalStrings(got, want) {
		t.Errorf("episodes across occurrences = %v, want %v", got, want)
	}
}

func TestExpandChannel_SeriesWithoutEpisodesIsSkippedAndNextItemAirs(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true},
	}
	in, _ := seriesBlockInput(domain.OrderOriginal, 1, 0,
		[]domain.BlockItem{seriesItem(), {ID: "bi2", BlockID: "blk", CatalogItemID: "m1"}}, sched, map[string]int64{"m1": epMs})
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), newSelectionState())
	if len(entries) != 1 || entries[0].CatalogItemID != "m1" {
		t.Fatalf("entries = %+v, want only m1", entries)
	}
}

func TestExpandChannel_SeriesSkipsSlotsBeforeTheCursorOrigin(t *testing.T) {
	// Occurrence starts 20:00; the cursor came to exist at 21:10, so the
	// slots ending at or before it (20:00-20:30, 20:30-21:00 and 21:00-21:30
	// does not: it ends after) are skipped without consuming episodes.
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("20:00"), Active: true},
	}
	in, run := seriesBlockInput(domain.OrderOriginal, 1, 10, []domain.BlockItem{seriesItem()}, sched, nil)
	run.originMs = time.Date(2026, 9, 30, 21, 10, 0, 0, time.UTC).UnixMilli()
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, time.Date(2026, 9, 30, 22, 30, 0, 0, time.UTC), newSelectionState())
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	if entries[0].CatalogItemID != "e01" || time.UnixMilli(entries[0].StartAt).UTC().Format("15:04") != "21:00" {
		t.Errorf("first entry = %s at %s, want e01 at 21:00 (the slot in progress when the cursor began)",
			entries[0].CatalogItemID, time.UnixMilli(entries[0].StartAt).UTC().Format("15:04"))
	}
}

func TestExpandChannel_SeriesCapStopsRunawayExpansion(t *testing.T) {
	sched := []domain.ScheduleItem{
		{ID: "se1", BlockID: "blk", Recurrence: domain.RecurrenceWeekly, DayOfWeek: intPtr(3), StartTimeLocal: strPtr("00:00"), Active: true},
	}
	in, run := seriesBlockInput(domain.OrderOriginal, 1, 1, []domain.BlockItem{seriesItem()}, sched, nil)
	run.episodes[0].DurationMs = 1 // one millisecond: the window holds far more than the cap
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), newSelectionState())
	if len(entries) != maxSeriesEntries {
		t.Errorf("got %d entries, want the cap %d", len(entries), maxSeriesEntries)
	}
}

// ---- end to end through the database ----

func (f *testFixture) createEpisode(t *testing.T, id, seriesTitle string, season, episode int, durationMs int64) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, series_external_id, season_number, episode_number, duration_ms, synced_at, search_text)
		 VALUES (?, ?, ?, ?, 'episode', ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, f.tenantID, f.sourceID, id, "Title "+id, seriesTitle, "ser-"+seriesTitle, season, episode, durationMs, time.Now().UnixMilli(), id+"\n"+seriesTitle,
	); err != nil {
		t.Fatalf("seed episode %s: %v", id, err)
	}
}

func (f *testFixture) addSeriesBlockItem(t *testing.T, id, blockID string, position int, order string) {
	t.Helper()
	spec := fmt.Sprintf(`{"media_source_id":%q,"series_external_id":"ser-Show","series_title":"Show","playback_order":%q,"seed":42}`, f.sourceID, order)
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, ?, 'series', NULL, ?)",
		id, blockID, position, spec,
	); err != nil {
		t.Fatalf("seed series block_item %s: %v", id, err)
	}
}

func (f *testFixture) createOnceScheduleItem(t *testing.T, id, scheduleID, blockID string, startAt time.Time) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, start_at, active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'once', ?, 1, ?, ?)`,
		id, f.tenantID, scheduleID, blockID, startAt.UnixMilli(), now, now,
	); err != nil {
		t.Fatalf("seed once schedule_item %s: %v", id, err)
	}
}

// seriesContinuousSetup: one channel, a continuous item over a manual block
// holding just the series (5 half-hour episodes, ids s1..s5 in order).
func seriesContinuousSetup(t *testing.T, order string) *testFixture {
	t.Helper()
	f := newFixture(t)
	for i := 1; i <= 5; i++ {
		f.createEpisode(t, fmt.Sprintf("s%d", i), "Show", 1, i, epMs)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesBlockItem(t, "sbi", "blk", 0, order)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	return f
}

func seriesIDs(entries []domain.EpgEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.CatalogItemID)
	}
	return out
}

func TestSeriesEndToEnd_ContinuousAirsEpisodesInOrderAndRegenerationIsStable(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderOriginal)
	ctx := context.Background()
	mat := New(f.db, 5*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := queryEntries(t, f)
	if len(first) != 10 {
		t.Fatalf("got %d entries, want 10 half-hours in 5h: %v", len(first), seriesIDs(first))
	}
	want := []string{"s1", "s2", "s3", "s4", "s5", "s1", "s2", "s3", "s4", "s5"}
	if got := seriesIDs(first); !equalStrings(got, want) {
		t.Fatalf("episodes = %v, want %v", got, want)
	}
	for _, e := range first {
		if e.SourceBlockItemID == nil || *e.SourceBlockItemID != "sbi" || e.SeriesCycle == nil || e.RotationNext == nil || *e.RotationNext != 0 {
			t.Fatalf("entry without series bookkeeping: %+v", e)
		}
	}
	if *first[5].SeriesCycle != 1 {
		t.Errorf("sixth episode cycle = %d, want 1", *first[5].SeriesCycle)
	}

	// Regenerating (same instant, within the freeze margin) changes nothing.
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	second := queryEntries(t, f)
	if !equalStrings(seriesIDs(second), want) {
		t.Fatalf("after re-run episodes = %v, want %v", seriesIDs(second), want)
	}
	for i := range second {
		if second[i].StartAt != first[i].StartAt {
			t.Errorf("entry %d moved: %d -> %d", i, first[i].StartAt, second[i].StartAt)
		}
	}
	var origins []int64
	if err := f.db.Query(ctx, &origins, "SELECT origin_ms FROM series_cursor WHERE channel_id = 'chan1' AND block_item_id = 'sbi'"); err != nil || len(origins) != 1 {
		t.Fatalf("series_cursor = %v (err %v), want one row", origins, err)
	}
}

func TestSeriesEndToEnd_ShuffleResumesAcrossFreezeWithoutReshuffling(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderShuffle)
	ctx := context.Background()
	mat := New(f.db, 6*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := seriesIDs(queryEntries(t, f))
	if len(first) != 12 {
		t.Fatalf("got %d entries, want 12", len(first))
	}
	// Two cycles of five, each a permutation; a re-run reproduces them exactly
	// even after a catalog sync adds an episode in the future part.
	f.createEpisode(t, "s6", "Show", 1, 6, epMs)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	second := seriesIDs(queryEntries(t, f))
	// The frozen head (the first hour: two episodes) is untouched.
	if !equalStrings(second[:2], first[:2]) {
		t.Errorf("frozen head changed: %v -> %v", first[:2], second[:2])
	}
	// The added episode shows up in the current cycle, once, and no episode repeats inside it.
	cycle := second[:6]
	seen := map[string]int{}
	for _, id := range cycle {
		seen[id]++
	}
	if len(seen) != 6 {
		t.Errorf("first cycle after the sync = %v, want six distinct episodes (the new one joined)", cycle)
	}
}

func TestSeriesEndToEnd_ResetRestartsFromTheFirstEpisode(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderOriginal)
	ctx := context.Background()
	mat := New(f.db, 4*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Pretend 70 minutes passed: s1, s2 are history, s3 is on the air.
	shift := int64(70 * 60 * 1000)
	if err := f.db.Exec(ctx, "UPDATE epg_entry SET start_at = start_at - ?, end_at = end_at - ?", shift, shift); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(ctx, "UPDATE series_cursor SET origin_ms = origin_ms - ?", shift); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	var live []domain.EpgEntry
	for _, e := range queryEntries(t, f) {
		if e.EndAt > now {
			live = append(live, e)
		}
	}
	if len(live) == 0 || live[0].CatalogItemID != "s3" {
		t.Fatalf("setup: on the air = %v, want s3 first", seriesIDs(live))
	}

	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatalf("ResetChannel: %v", err)
	}
	var after []domain.EpgEntry
	for _, e := range queryEntries(t, f) {
		if e.EndAt > now {
			after = append(after, e)
		}
	}
	if len(after) < 2 || after[0].CatalogItemID != "s1" || after[1].CatalogItemID != "s2" {
		t.Fatalf("after reset episodes = %v, want to restart at s1, s2", seriesIDs(after))
	}
}

func TestSeriesEndToEnd_TimedOccurrencesShareTheCursorAndRerunIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 1; i <= 8; i++ {
		f.createEpisode(t, fmt.Sprintf("s%d", i), "Show", 1, i, epMs)
	}
	f.createCatalogItem(t, "stop", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesBlockItem(t, "sbi", "blk", 0, domain.OrderOriginal)
	f.createBlock(t, "stopblk")
	f.addBlockItem(t, "bstop", "stopblk", 0, "stop")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	// Series 1h each time: two episodes before the stop block at +1h / +3h.
	base := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute)
	f.createOnceScheduleItem(t, "a", "sched1", "blk", base)
	f.createOnceScheduleItem(t, "stop1", "sched1", "stopblk", base.Add(time.Hour))
	f.createOnceScheduleItem(t, "b", "sched1", "blk", base.Add(2*time.Hour))
	f.createOnceScheduleItem(t, "stop2", "sched1", "stopblk", base.Add(3*time.Hour))

	mat := New(f.db, 6*time.Hour)
	for i := 0; i < 2; i++ { // the second run must reproduce the first
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range queryEntries(t, f) {
			if e.SourceBlockItemID != nil {
				got = append(got, e.CatalogItemID)
			}
		}
		if want := []string{"s1", "s2", "s3", "s4"}; !equalStrings(got, want) {
			t.Fatalf("run %d: series episodes = %v, want %v", i+1, got, want)
		}
	}
}

func TestSeriesEndToEnd_SlotsBeforeTheItemExistedAreSkipped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 1; i <= 8; i++ {
		f.createEpisode(t, fmt.Sprintf("s%d", i), "Show", 1, i, epMs)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesBlockItem(t, "sbi", "blk", 0, domain.OrderOriginal)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	// The occurrence began 70 minutes ago: slots ending before "now" belong to
	// a time the series item did not exist yet and must not use up episodes.
	f.createOnceScheduleItem(t, "a", "sched1", "blk", time.Now().UTC().Add(-70*time.Minute))

	mat := New(f.db, 3*time.Hour)
	for i := 0; i < 2; i++ {
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		entries := queryEntries(t, f)
		if len(entries) == 0 || entries[0].CatalogItemID != "s1" {
			t.Fatalf("run %d: first episode = %v, want s1", i+1, seriesIDs(entries))
		}
		if entries[0].StartAt > time.Now().UnixMilli() || entries[0].EndAt <= time.Now().UnixMilli() {
			t.Errorf("run %d: first episode is not the one on the air: %v-%v", i+1, time.UnixMilli(entries[0].StartAt), time.UnixMilli(entries[0].EndAt))
		}
	}
}

func TestSeriesEndToEnd_PreviewResetWritesNothing(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderOriginal)
	ctx := context.Background()
	mat := New(f.db, 3*time.Hour)
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	entries, err := mat.PreviewReset(ctx, f.tenantID, ch, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 || entries[0].CatalogItemID != "s1" {
		t.Fatalf("preview = %v, want 6 entries from s1", seriesIDs(entries))
	}
	var counts []int
	f.db.Query(ctx, &counts, "SELECT (SELECT COUNT(*) FROM epg_entry) + (SELECT COUNT(*) FROM series_cursor)")
	if len(counts) != 1 || counts[0] != 0 {
		t.Errorf("preview wrote rows: %v", counts)
	}
}

func TestSeriesEndToEnd_TwoChannelsSharingABlockHaveIndependentShuffles(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderShuffle)
	ctx := context.Background()
	f.createChannelWithNumber(t, "chan2", 2, "UTC")
	f.assignChannelSchedule(t, "chan2", "sched1")
	mat := New(f.db, 5*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var a, b []string
	for _, e := range queryEntries(t, f) {
		a = append(a, e.CatalogItemID)
	}
	var second []domain.EpgEntry
	f.db.Query(ctx, &second, "SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = 'chan2' ORDER BY start_at")
	for _, e := range second {
		b = append(b, e.CatalogItemID)
	}
	if len(a) != 10 || len(b) != 10 {
		t.Fatalf("entries per channel = %d, %d, want 10 each", len(a), len(b))
	}
	if equalStrings(a, b) {
		t.Errorf("both channels shuffled identically: %v", a)
	}
}
