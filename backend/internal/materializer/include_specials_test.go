package materializer

import (
	"context"
	"sort"
	"testing"
	"time"
)

// specialsLibrary seeds: sp1 (season 0 of a series), a1 and a2 (season 1), ns
// (a series episode with no season number), loose0 (an episode with season 0
// but no series) and mov (a movie).
func specialsLibrary(t *testing.T, f *testFixture) {
	t.Helper()
	ctx := context.Background()
	f.createEpisode(t, "sp1", "Show", 0, 1, epMs)
	f.createEpisode(t, "a1", "Show", 1, 1, epMs)
	f.createEpisode(t, "a2", "Show", 1, 2, epMs)
	f.createEpisode(t, "ns", "Show", 1, 3, epMs)
	f.createEpisode(t, "loose0", "Loose", 0, 1, epMs)
	f.createCatalogItem(t, "mov", epMs)
	for _, q := range []string{
		"UPDATE catalog_item SET season_number = NULL WHERE id = 'ns'",
		"UPDATE catalog_item SET series_title = NULL, series_external_id = NULL WHERE id = 'loose0'",
	} {
		if err := f.db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	f.createChannel(t, "chan1", "UTC")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
}

func setIncludeSpecials(t *testing.T, f *testFixture, blockID string, on bool) {
	t.Helper()
	v := 0
	if on {
		v = 1
	}
	if err := f.db.Exec(context.Background(), "UPDATE block SET include_specials = ? WHERE id = ?", v, blockID); err != nil {
		t.Fatal(err)
	}
}

// airedSet regenerates the guide and returns the distinct catalog items on it.
func airedSet(t *testing.T, f *testFixture, window time.Duration) []string {
	t.Helper()
	if err := f.db.Exec(context.Background(), "DELETE FROM epg_entry"); err != nil {
		t.Fatal(err)
	}
	if err := New(f.db, window).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range seriesIDs(queryEntries(t, f)) {
		seen[id] = true
	}
	var out []string
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestIncludeSpecials_SeriesItem(t *testing.T) {
	f := newFixture(t)
	specialsLibrary(t, f)
	f.createBlock(t, "blk")
	f.addSeriesBlockItem(t, "sbi", "blk", 0, "original")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	if got := airedSet(t, f, 6*time.Hour); !equalStrings(got, []string{"a1", "a2", "ns"}) {
		t.Errorf("default: %v", got)
	}
	setIncludeSpecials(t, f, "blk", true)
	if got := airedSet(t, f, 6*time.Hour); !equalStrings(got, []string{"a1", "a2", "ns", "sp1"}) {
		t.Errorf("included: %v", got)
	}
}

func TestIncludeSpecials_SeriesItemExplicitSeasonZeroWins(t *testing.T) {
	f := slicedChannel(t, "original", `{"mode":"slot_end"}`, `{"seasons":[0]}`, 0, false)
	if got := runSliced(t, f, 2*time.Hour); !equalStrings(got, []string{"sp1", "sp1", "sp1", "sp1"}) {
		t.Errorf("seasons [0] with the option off: %v", got)
	}
	// A range that merely covers season 0 does not name it.
	f = slicedChannel(t, "original", `{"mode":"slot_end"}`, `{"season_to":1}`, 0, false)
	if got := runSliced(t, f, 90*time.Minute); !equalStrings(got, []string{"a1", "a2", "a3"}) {
		t.Errorf("season_to 1: %v", got)
	}
}

func TestIncludeSpecials_CriteriaItem(t *testing.T) {
	f := newFixture(t)
	specialsLibrary(t, f)
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, epsQuery, "title", allMatches)
	f.addBlockItem(t, "bmov", "blk", 1, "mov")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	// The series special goes; the loose season 0 episode (no series), the
	// episode without a season and the movie stay.
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "mov", "ns"}) {
		t.Errorf("default: %v", got)
	}
	setIncludeSpecials(t, f, "blk", true)
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "mov", "ns", "sp1"}) {
		t.Errorf("included: %v", got)
	}
}

func TestIncludeSpecials_DynamicBlockByTitle(t *testing.T) {
	f := newFixture(t)
	specialsLibrary(t, f)
	f.createDynamicBlock(t, "dyn")
	for i, id := range []string{"a1", "a2", "loose0", "mov", "ns", "sp1"} { // resolved membership
		f.addBlockItem(t, "bi-"+id, "dyn", i, id)
	}
	f.createContinuousScheduleItem(t, "cont", "sched1", "dyn")
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "mov", "ns"}) {
		t.Errorf("default: %v", got)
	}
	setIncludeSpecials(t, f, "dyn", true)
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "mov", "ns", "sp1"}) {
		t.Errorf("included: %v", got)
	}
}

func TestIncludeSpecials_DynamicBlockInterleaved(t *testing.T) {
	f := newFixture(t)
	specialsLibrary(t, f)
	f.createInterleavedBlock(t, "dyn", epsQuery, "interleaved", `{"per_round":1,"on_exhaustion":"restart","series_order":"title"}`, 7)
	f.createContinuousScheduleItem(t, "cont", "sched1", "dyn")
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "ns"}) {
		t.Errorf("default: %v", got)
	}
	setIncludeSpecials(t, f, "dyn", true)
	if got := airedSet(t, f, 8*time.Hour); !equalStrings(got, []string{"a1", "a2", "loose0", "ns", "sp1"}) {
		t.Errorf("included: %v", got)
	}
}
