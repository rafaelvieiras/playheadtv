package materializer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

const ep22 = int64(22 * 60 * 1000)

// ep22d is the duration of n 22-minute episodes.
func ep22d(n int) time.Duration { return time.Duration(n*22) * time.Minute }

var (
	amtCount1    = domain.SeriesAmount{Mode: domain.AmountCount, Count: 1}
	amtSlotEnd   = domain.SeriesAmount{Mode: domain.AmountSlotEnd}
	amtSeriesEnd = domain.SeriesAmount{Mode: domain.AmountSeriesEnd}
)

func amtCount(n int) domain.SeriesAmount {
	return domain.SeriesAmount{Mode: domain.AmountCount, Count: n}
}

// seriesWalk builds the run of one series item: n episodes named prefix01..,
// each durMs long, walked in original order.
func seriesWalk(prefix string, n int, durMs int64, amount domain.SeriesAmount) *seriesRun {
	eps := make([]series.Episode, n)
	for i := range eps {
		eps[i] = series.Episode{
			ID:            fmt.Sprintf("%s%02d", prefix, i+1),
			DurationMs:    durMs,
			SeasonNumber:  i64(1),
			EpisodeNumber: i64(int64(i + 1)),
		}
	}
	spec := specOf(domain.OrderOriginal, 1)
	spec.Amount = &amount
	return newSeriesRun(spec, "chan1", eps, 0, nil, nil, 0)
}

func seriesBI(id, blockID string) domain.BlockItem {
	return domain.BlockItem{ID: id, BlockID: blockID, ItemType: domain.BlockItemSeries}
}

func at(entries []string, e domain.EpgEntry) []string {
	return append(entries, fmt.Sprintf("%s@%s", e.CatalogItemID, time.UnixMilli(e.StartAt).UTC().Format("15:04")))
}

func onceItem(id, blockID string, t time.Time) domain.ScheduleItem {
	ms := t.UnixMilli()
	return domain.ScheduleItem{ID: id, BlockID: blockID, Recurrence: domain.RecurrenceOnce, StartAt: &ms, Active: true}
}

// Scenario 1: a continuous channel whose block holds one series that airs
// until the end of the slot is a 24/7 single-series channel.
func TestAmount_ContinuousSingleSeriesUntilSlotEndIsAnEndlessSeriesChannel(t *testing.T) {
	run := seriesWalk("a", 3, ep22, amtSlotEnd)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems:  []domain.ScheduleItem{{ID: "cont", BlockID: "blk", Recurrence: domain.RecurrenceContinuous, Active: true}},
		RepeatBlockIDs: map[string]bool{"blk": true}, // a continuous item over a manual block wraps only when it repeats
		BlockItems: map[string][]domain.BlockItem{"blk": {
			seriesBI("sa", "blk"),
			{ID: "m", BlockID: "blk", CatalogItemID: "movie"}, // never reached
		}},
		CatalogDuration: map[string]int64{"movie": ep22},
		Series:          map[string]*seriesRun{"sa": run},
	}
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(24*time.Hour), newSelectionState())
	if len(entries) < 60 {
		t.Fatalf("got %d entries, want a full day of episodes", len(entries))
	}
	for i, e := range entries {
		if e.CatalogItemID == "movie" {
			t.Fatalf("entry %d is the item after the series, which must never air", i)
		}
		if e.RotationNext == nil || *e.RotationNext != 0 {
			t.Fatalf("entry %d rotation = %v, want the rotation to stay on the series (0)", i, e.RotationNext)
		}
		if want := fmt.Sprintf("a%02d", i%3+1); e.CatalogItemID != want {
			t.Fatalf("entry %d = %s, want %s", i, e.CatalogItemID, want)
		}
	}
}

// Scenario 2: a timed 10:00-12:00 slot with a block of several single-episode
// series airs one episode of each per visit; the cursors are shared, so the
// next day's visit takes the next episode of each.
func TestAmount_TimedSlotOfSingleEpisodeSeriesAirsOneEachPerVisit(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	runs := map[string]*seriesRun{}
	var items []domain.BlockItem
	for _, n := range names {
		runs["s"+n] = seriesWalk(n, 5, ep22, amtCount1)
		items = append(items, seriesBI("s"+n, "blk"))
	}
	wed := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	thu := wed.AddDate(0, 0, 1)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{
			onceItem("w", "blk", wed), onceItem("wStop", "stop", wed.Add(2*time.Hour)),
			onceItem("th", "blk", thu), onceItem("thStop", "stop", thu.Add(2*time.Hour)),
		},
		BlockItems: map[string][]domain.BlockItem{
			"blk":  items,
			"stop": {{ID: "x", BlockID: "stop", CatalogItemID: "movie"}},
		},
		CatalogDuration: map[string]int64{"movie": ep22},
		Series:          runs,
	}
	from := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(72*time.Hour), newSelectionState())
	var got []string
	for _, e := range entries {
		if e.CatalogItemID != "movie" {
			got = at(got, e)
		}
	}
	want := []string{
		"a01@10:00", "b01@10:22", "c01@10:44", "d01@11:06", // Wednesday
		"a02@10:00", "b02@10:22", "c02@10:44", "d02@11:06", // Thursday: the next episode of each
	}
	if !equalStrings(got, want) {
		t.Fatalf("episodes = %v, want %v", got, want)
	}
	for _, e := range entries {
		if e.EndAt > wed.Add(2*time.Hour).UnixMilli() && e.StartAt < wed.Add(2*time.Hour).UnixMilli() {
			t.Errorf("entry %s crosses the slot end", e.CatalogItemID)
		}
	}
}

// Scenario 3: 17:00-19:00 with a play-once block A x2, B x1, C x1 and a
// fallback filler covering what the ~22 minute episodes leave.
func TestAmount_TimedSlotPlayOnceBlockWithCountsAndFallbackFiller(t *testing.T) {
	runs := map[string]*seriesRun{
		"sa": seriesWalk("a", 6, ep22, amtCount(2)),
		"sb": seriesWalk("b", 6, ep22, amtCount(1)),
		"sc": seriesWalk("c", 6, ep22, amtCount(1)),
	}
	start := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	slotEnd := start.Add(2 * time.Hour)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{onceItem("slot", "blk", start), onceItem("next", "news", slotEnd)},
		BlockItems: map[string][]domain.BlockItem{
			"blk":  {seriesBI("sa", "blk"), seriesBI("sb", "blk"), seriesBI("sc", "blk")},
			"news": {{ID: "n", BlockID: "news", CatalogItemID: "news"}},
		},
		FillerRules: []domain.FillerRule{{ID: "fr", Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: domain.FillerFallback, FillerPoolID: "pool", Selection: domain.SelectionSequential}},
		PoolItems:   map[string][]domain.FillerPoolItem{"pool": {{ID: "p1", FillerPoolID: "pool", CatalogItemID: "spot"}}},
		// 2-minute spots: 88 minutes of episodes leave 32 minutes, exactly 16 spots.
		CatalogDuration: map[string]int64{"spot": 2 * 60 * 1000, "news": ep22},
		Series:          runs,
	}
	from := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(48*time.Hour), newSelectionState())

	var programs []string
	cursor := start.UnixMilli()
	for _, e := range entries {
		if e.StartAt < start.UnixMilli() || e.StartAt >= slotEnd.UnixMilli() {
			continue
		}
		if e.StartAt != cursor {
			t.Fatalf("hole before %s at %s", e.CatalogItemID, time.UnixMilli(e.StartAt).UTC().Format("15:04"))
		}
		cursor = e.EndAt
		if e.EndAt > slotEnd.UnixMilli() {
			t.Errorf("%s runs past the slot end", e.CatalogItemID)
		}
		if e.Kind == domain.EpgFallback {
			if e.CatalogItemID != "spot" {
				t.Errorf("fallback entry %s, want the spot", e.CatalogItemID)
			}
			continue
		}
		programs = at(programs, e)
	}
	want := []string{"a01@17:00", "a02@17:22", "b01@17:44", "c01@18:06"}
	if !equalStrings(programs, want) {
		t.Errorf("programs = %v, want %v", programs, want)
	}
	if cursor != slotEnd.UnixMilli() {
		t.Errorf("coverage ends %s, want exactly the slot end 19:00", time.UnixMilli(cursor).UTC().Format("15:04"))
	}
	var next *domain.EpgEntry
	for i := range entries {
		if entries[i].CatalogItemID == "news" {
			next = &entries[i]
			break
		}
	}
	if next == nil || next.StartAt != slotEnd.UnixMilli() {
		t.Errorf("the 19:00 occurrence did not start on time: %+v", next)
	}
}

// The end of the slot wins over the amount: episodes that would not end
// before it are not scheduled, and the items after the cut never air.
func TestAmount_SlotEndWinsOverCount(t *testing.T) {
	runs := map[string]*seriesRun{
		"sa": seriesWalk("a", 20, ep22, amtCount(10)),
		"sb": seriesWalk("b", 20, ep22, amtCount(1)),
	}
	start := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	slotEnd := start.Add(2 * time.Hour)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{onceItem("slot", "blk", start), onceItem("next", "news", slotEnd)},
		BlockItems: map[string][]domain.BlockItem{
			"blk":  {seriesBI("sa", "blk"), seriesBI("sb", "blk")},
			"news": {{ID: "n", BlockID: "news", CatalogItemID: "news"}},
		},
		CatalogDuration: map[string]int64{"news": ep22},
		Series:          runs,
	}
	from := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(48*time.Hour), newSelectionState())
	n := 0
	for _, e := range entries {
		if e.CatalogItemID == "news" {
			continue
		}
		n++
		if e.CatalogItemID[0] == 'b' {
			t.Errorf("series B aired although A's visit was cut by the slot end")
		}
		if e.EndAt > slotEnd.UnixMilli() {
			t.Errorf("%s ends after the slot end", e.CatalogItemID)
		}
	}
	if n != 5 { // 5 x 22 = 110 minutes; the sixth would end at 19:02
		t.Errorf("aired %d episodes, want 5", n)
	}
}

// A play-once manual block with no following occurrence is not cut: counts are
// honored in full and a later item airs.
func TestAmount_ManualBlockWithoutNextOccurrenceHonorsCounts(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 5, ep22, amtCount(3))}
	start := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{onceItem("slot", "blk", start)},
		BlockItems: map[string][]domain.BlockItem{
			"blk": {seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}},
		},
		CatalogDuration: map[string]int64{"movie": ep22},
		Series:          runs,
	}
	from := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(48*time.Hour), newSelectionState())
	got := seriesIDs(entries)
	if want := []string{"a01", "a02", "a03", "movie"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func continuousInput(items []domain.BlockItem, runs map[string]*seriesRun) channelInput {
	return channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems:   []domain.ScheduleItem{{ID: "cont", BlockID: "blk", Recurrence: domain.RecurrenceContinuous, Active: true}},
		RepeatBlockIDs:  map[string]bool{"blk": true},
		BlockItems:      map[string][]domain.BlockItem{"blk": items},
		CatalogDuration: map[string]int64{"movie": ep22},
		Series:          runs,
	}
}

func rotations(entries []domain.EpgEntry) []int {
	var out []int
	for _, e := range entries {
		out = append(out, *e.RotationNext)
	}
	return out
}

func equalInts(a, b []int) bool {
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

func TestAmount_ContinuousRotationMovesOnAfterEachVisit(t *testing.T) {
	runs := map[string]*seriesRun{
		"sa": seriesWalk("a", 6, ep22, amtCount(2)),
		"sb": seriesWalk("b", 6, ep22, amtCount(1)),
	}
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), seriesBI("sb", "blk")}, runs)
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(7)), newSelectionState())
	want := []string{"a01", "a02", "b01", "a03", "a04", "b02", "a05"}
	if got := seriesIDs(entries); !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	// A visit's last entry stores the next index (unwrapped), the others their own.
	if got, want := rotations(entries), []int{0, 1, 2, 0, 1, 2, 0}; !equalInts(got, want) {
		t.Fatalf("rotations = %v, want %v", got, want)
	}
}

func TestAmount_ContinuousOneItemBlockOfOneEpisodeVisitsWalksTheSeries(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 3, ep22, amtCount1)}
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk")}, runs)
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(5)), newSelectionState())
	if got, want := seriesIDs(entries), []string{"a01", "a02", "a03", "a01", "a02"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i, e := range entries {
		if *e.RotationNext != 1 {
			t.Errorf("entry %d rotation = %d, want 1 (visit complete, distinct from the item's own index 0)", i, *e.RotationNext)
		}
	}
}

func TestAmount_ContinuousSeriesEndFinishesTheCycleThenRotates(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 3, ep22, amtSeriesEnd)}
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}}, runs)
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(8)), newSelectionState())
	want := []string{"a01", "a02", "a03", "movie", "a01", "a02", "a03", "movie"}
	if got := seriesIDs(entries); !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if *entries[4].SeriesCycle != 1 {
		t.Errorf("second visit cycle = %d, want a new cycle (1)", *entries[4].SeriesCycle)
	}
}

func TestAmount_SeriesEndAirsOnlyTheRestOfTheCycleInProgress(t *testing.T) {
	eps := seriesWalk("a", 4, ep22, amtSeriesEnd).episodes
	spec := specOf(domain.OrderOriginal, 1)
	spec.Amount = &amtSeriesEnd
	history := []seriesRow{{StartAt: 1000, CatalogItemID: "a01"}, {StartAt: 2000, CatalogItemID: "a02"}}
	run := newSeriesRun(spec, "chan1", eps, 0, history, nil, 0)
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}}, map[string]*seriesRun{"sa": run})
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(4)), newSelectionState())
	if got, want := seriesIDs(entries), []string{"a03", "a04", "movie", "a01"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v (the cycle had a01, a02 aired already)", got, want)
	}
}

func TestAmount_ManualBlockSeriesEndThenNextItem(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 3, ep22, amtSeriesEnd)}
	start := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{onceItem("slot", "blk", start)},
		BlockItems: map[string][]domain.BlockItem{
			"blk": {seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}},
		},
		CatalogDuration: map[string]int64{"movie": ep22},
		Series:          runs,
	}
	from := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(24*time.Hour), newSelectionState())
	if got, want := seriesIDs(entries), []string{"a01", "a02", "a03", "movie"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestAmount_RandomSeriesEndAirsAsManyEpisodesAsTheSeriesHolds(t *testing.T) {
	spec := specOf(domain.OrderRandom, 7)
	spec.Amount = &amtSeriesEnd
	run := newSeriesRun(spec, "chan1", seriesWalk("a", 4, ep22, amtSeriesEnd).episodes, 0, nil, nil, 0)
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}}, map[string]*seriesRun{"sa": run})
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(6)), newSelectionState())
	ids := seriesIDs(entries)
	if ids[4] != "movie" || ids[3] == "movie" {
		t.Fatalf("entries = %v, want four random episodes and then the movie", ids)
	}
}

func TestAmount_ContinuousResumesACountVisitTheKeptHeadLeftMidway(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 6, ep22, amtCount(3))}
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}}, runs)
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	in.KeptUntil = from
	in.ContinuousVisitDone = 2 // the kept head holds the visit's first two episodes
	entries := expandChannel(in, from, from, from.Add(ep22d(3)), newSelectionState())
	if got, want := seriesIDs(entries), []string{"a01", "movie", "a02"}; !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v (one episode completes the visit)", got, want)
	}
}

func TestAmount_EmptySeriesInCountBlockIsSkipped(t *testing.T) {
	runs := map[string]*seriesRun{"sa": seriesWalk("a", 0, ep22, amtCount(2))}
	in := continuousInput([]domain.BlockItem{seriesBI("sa", "blk"), {ID: "m", BlockID: "blk", CatalogItemID: "movie"}}, runs)
	from := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	entries := expandChannel(in, from, from, from.Add(ep22d(2)), newSelectionState())
	if got, want := seriesIDs(entries), []string{"movie", "movie"}; !equalStrings(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// ---- end to end through the database ----

func (f *testFixture) addSeriesBlockItemWithAmount(t *testing.T, id, blockID string, position int, amountJSON string) {
	t.Helper()
	spec := fmt.Sprintf(`{"media_source_id":%q,"series_external_id":"ser-Show","series_title":"Show","playback_order":"original","seed":42,"amount":%s}`, f.sourceID, amountJSON)
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, ?, 'series', NULL, ?)",
		id, blockID, position, spec,
	); err != nil {
		t.Fatalf("seed series block_item %s: %v", id, err)
	}
}

// A kept head that ends midway through a count visit resumes it: regenerating
// reproduces the exact sequence a single uninterrupted expansion produced.
func TestAmountEndToEnd_RegenerationResumesACountVisit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		f.createEpisode(t, fmt.Sprintf("s%d", i), "Show", 1, i, epMs)
	}
	f.createCatalogItem(t, "movie", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesBlockItemWithAmount(t, "sbi", "blk", 0, `{"mode":"count","count":3}`)
	f.addBlockItem(t, "bmovie", "blk", 1, "movie")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

	mat := New(f.db, 5*time.Hour)
	mat.SetFreezeMargin(45 * time.Minute) // keeps two 30m episodes: the head ends inside the first visit
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := seriesIDs(queryEntries(t, f))
	want := []string{"s1", "s2", "s3", "movie", "s4", "s5", "s1", "movie", "s2", "s3"}
	if !equalStrings(first, want) {
		t.Fatalf("first run = %v, want %v", first, want)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if second := seriesIDs(queryEntries(t, f)); !equalStrings(second, want) {
		t.Fatalf("after regeneration = %v, want the same %v", second, want)
	}
}

// Series items stored before amounts existed (no amount in the spec) keep
// airing until the end of the slot.
func TestAmountEndToEnd_LegacySeriesSpecWithoutAmountAirsUntilTheSlotEnd(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderOriginal) // seeded through addSeriesBlockItem: no amount
	f.createCatalogItem(t, "movie", epMs)
	f.addBlockItem(t, "bmovie", "blk", 1, "movie")
	mat := New(f.db, 3*time.Hour)
	if err := mat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range seriesIDs(queryEntries(t, f)) {
		if id == "movie" {
			t.Fatalf("the item after a legacy series item aired")
		}
	}
}

func TestAmountEndToEnd_ExplicitSlotEndAndCountOneDiffer(t *testing.T) {
	for amount, wantMovie := range map[string]bool{`{"mode":"slot_end"}`: false, `{"mode":"count","count":1}`: true} {
		f := newFixture(t)
		for i := 1; i <= 5; i++ {
			f.createEpisode(t, fmt.Sprintf("s%d", i), "Show", 1, i, epMs)
		}
		f.createCatalogItem(t, "movie", epMs)
		f.createChannel(t, "chan1", "UTC")
		f.createBlock(t, "blk")
		f.addSeriesBlockItemWithAmount(t, "sbi", "blk", 0, amount)
		f.addBlockItem(t, "bmovie", "blk", 1, "movie")
		f.createSchedule(t, "sched1")
		f.assignChannelSchedule(t, "chan1", "sched1")
		f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
		if err := New(f.db, 3*time.Hour).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		aired := false
		for _, id := range seriesIDs(queryEntries(t, f)) {
			aired = aired || id == "movie"
		}
		if aired != wantMovie {
			t.Errorf("amount %s: movie aired = %v, want %v", amount, aired, wantMovie)
		}
	}
}
