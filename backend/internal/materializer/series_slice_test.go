package materializer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func (f *testFixture) addSlicedSeriesItem(t *testing.T, id, blockID string, position int, order, amountJSON, sliceJSON string, sliceSeed int64) {
	t.Helper()
	spec := fmt.Sprintf(`{"media_source_id":%q,"series_external_id":"ser-Show","series_title":"Show","playback_order":%q,"seed":42,"amount":%s,"slice":%s,"slice_seed":%d}`,
		f.sourceID, order, amountJSON, sliceJSON, sliceSeed)
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, ?, 'series', NULL, ?)",
		id, blockID, position, spec,
	); err != nil {
		t.Fatalf("seed series block_item %s: %v", id, err)
	}
}

// sliceLibrary seeds a series with a special (sp1), season 1 (a1..a3) and a
// season 2 with a numbering gap and a double file (b1, b2, b4, b56 = 5-6).
func sliceLibrary(t *testing.T, f *testFixture) {
	t.Helper()
	f.createEpisode(t, "sp1", "Show", 0, 1, epMs)
	for i := 1; i <= 3; i++ {
		f.createEpisode(t, fmt.Sprintf("a%d", i), "Show", 1, i, epMs)
	}
	f.createEpisode(t, "b1", "Show", 2, 1, epMs)
	f.createEpisode(t, "b2", "Show", 2, 2, epMs)
	f.createEpisode(t, "b4", "Show", 2, 4, epMs)
	f.createEpisode(t, "b56", "Show", 2, 5, epMs)
	if err := f.db.Exec(context.Background(), "UPDATE catalog_item SET episode_number_end = 6 WHERE id = 'b56'"); err != nil {
		t.Fatal(err)
	}
}

func slicedChannel(t *testing.T, order, amountJSON, sliceJSON string, sliceSeed int64, withMovie bool) *testFixture {
	t.Helper()
	f := newFixture(t)
	sliceLibrary(t, f)
	f.createCatalogItem(t, "movie", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSlicedSeriesItem(t, "sbi", "blk", 0, order, amountJSON, sliceJSON, sliceSeed)
	if withMovie {
		f.addBlockItem(t, "bmovie", "blk", 1, "movie")
	}
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	return f
}

func runSliced(t *testing.T, f *testFixture, window time.Duration) []string {
	t.Helper()
	if err := New(f.db, window).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return seriesIDs(queryEntries(t, f))
}

func TestSliceEndToEnd_SeasonSliceWithCountAmountAndRotation(t *testing.T) {
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"count","count":2}`, `{"seasons":[1],"exclude_specials":true}`, 0, true)
	got := runSliced(t, f, 5*time.Hour)
	want := []string{"a1", "a2", "movie", "a3", "a1", "movie", "a2", "a3", "movie", "a1"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSliceEndToEnd_SeriesEndRunsTheSlicedCycleOnly(t *testing.T) {
	// Specials stay by default, so season 0 plus the episode range 2..2.
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"series_end"}`, `{"episode_from":2,"episode_to":2}`, 0, true)
	got := runSliced(t, f, 4*time.Hour)
	want := []string{"a2", "b2", "movie", "a2", "b2", "movie", "a2", "b2"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSliceEndToEnd_MultiEpisodeFileCountsOnceAndGapsAreNotHoles(t *testing.T) {
	// Last 2 by position of season 2: b4 and the 5-6 file (not b2, not "episode 6").
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"slot_end"}`, `{"seasons":[2],"last":2}`, 0, false)
	got := runSliced(t, f, 3*time.Hour)
	want := []string{"b4", "b56", "b4", "b56", "b4", "b56"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSliceEndToEnd_EpisodeRangeKeepsADoubleFileOverlappingIt(t *testing.T) {
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"slot_end"}`, `{"episode_from":6}`, 0, false)
	got := runSliced(t, f, time.Hour)
	if want := []string{"b56", "b56"}; !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSliceEndToEnd_ShuffleStaysInsideTheSliceAndSampleIsStable(t *testing.T) {
	f := slicedChannel(t, domain.OrderShuffle, `{"mode":"slot_end"}`, `{"exclude_specials":true,"sample":3}`, 777, false)
	ctx := context.Background()
	first := runSliced(t, f, 6*time.Hour)
	if len(first) != 12 {
		t.Fatalf("got %d entries, want 12", len(first))
	}
	set := map[string]bool{}
	for _, id := range first {
		set[id] = true
	}
	if len(set) != 3 || set["sp1"] {
		t.Fatalf("sample aired %v, want exactly 3 distinct non-special episodes", set)
	}
	// A fresh materializer over the same stored spec samples the same episodes.
	if err := f.db.Exec(ctx, "DELETE FROM epg_entry"); err != nil {
		t.Fatal(err)
	}
	second := runSliced(t, f, 6*time.Hour)
	set2 := map[string]bool{}
	for _, id := range second {
		set2[id] = true
	}
	if fmt.Sprint(set) != fmt.Sprint(set2) {
		t.Fatalf("sample changed between runs: %v vs %v", set, set2)
	}
}

func TestSliceEndToEnd_SliceTracksTheLibrary(t *testing.T) {
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"slot_end"}`, `{"seasons":[1]}`, 0, false)
	if got := runSliced(t, f, 90*time.Minute); !equalStrings(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("got %v", got)
	}
	// A sync adds a season 1 episode and a season 3 one.
	f.createEpisode(t, "a4", "Show", 1, 4, epMs)
	f.createEpisode(t, "c1", "Show", 3, 1, epMs)
	if err := f.db.Exec(context.Background(), "DELETE FROM epg_entry"); err != nil {
		t.Fatal(err)
	}
	if got := runSliced(t, f, 2*time.Hour); !equalStrings(got, []string{"a1", "a2", "a3", "a4"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSliceEndToEnd_ASliceThatMatchesNothingIsSkipped(t *testing.T) {
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"count","count":1}`, `{"seasons":[9]}`, 0, true)
	got := runSliced(t, f, time.Hour)
	for _, id := range got {
		if id != "movie" {
			t.Fatalf("got %v, want only the movie after the empty slice", got)
		}
	}
	if len(got) == 0 {
		t.Fatal("nothing aired")
	}
}

func TestSliceEndToEnd_ItemWithoutSliceIsUnchanged(t *testing.T) {
	f := slicedChannel(t, domain.OrderOriginal, `{"mode":"slot_end"}`, `null`, 0, false)
	got := runSliced(t, f, 4*time.Hour)
	// Season 0 (sp1) is out: the block does not include specials by default.
	want := []string{"a1", "a2", "a3", "b1", "b2", "b4", "b56", "a1"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Midroll applies to a sliced walk exactly like to a whole series: one break
// between two consecutive episodes of a visit.
func TestSliceEndToEnd_MidrollBreaksBetweenSlicedEpisodes(t *testing.T) {
	f := newFixture(t)
	sliceLibrary(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSlicedSeriesItem(t, "sbi", "blk", 0, domain.OrderOriginal, `{"mode":"count","count":3}`, `{"seasons":[1]}`, 0)
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	if err := New(f.db, 3*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := layout(queryEntries(t, f))
	if want := "a1 [mid] a2 [mid] a3"; !strings.HasPrefix(got, want) {
		t.Fatalf("layout = %q, want it to start with %q", got, want)
	}
	for _, id := range seriesIDs(queryEntries(t, f)) {
		if id == "sp1" || id[0] == 'b' {
			t.Fatalf("an episode outside the slice aired: %s", id)
		}
	}
}
