package materializer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

// createMovie seeds a movie with the columns the criteria queries read.
func (f *testFixture) createMovie(t *testing.T, id, title string, year int, durationMs int64, premiere *int64) {
	t.Helper()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, production_year, premiere_date, synced_at, search_text)
		 VALUES (?, ?, ?, ?, 'movie', ?, ?, ?, ?, ?, ?)`,
		id, f.tenantID, f.sourceID, id, title, durationMs, year, premiere, time.Now().UnixMilli(), title+"\n",
	); err != nil {
		t.Fatalf("seed movie %s: %v", id, err)
	}
}

// addCriteriaBlockItem seeds a criteria block item with the given query (the
// JSON tree) and amount.
func (f *testFixture) addCriteriaBlockItem(t *testing.T, id, blockID string, position int, query, order, amount string) {
	t.Helper()
	spec := fmt.Sprintf(`{"query":%s,"playback_order":%q,"seed":42,"amount":%s}`, query, order, amount)
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, ?, 'criteria', NULL, ?)",
		id, blockID, position, spec,
	); err != nil {
		t.Fatalf("seed criteria block_item %s: %v", id, err)
	}
}

const (
	yearQuery  = `{"version":1,"all":[{"field":"year","op":"gte","value":1990},{"field":"year","op":"lte","value":1999}]}`
	countTwo   = `{"mode":"count","count":2}`
	allMatches = `{"mode":"all"}`
)

// nineties seeds five 1990s movies (ids n1..n5, titles chosen so title order
// differs from id order), a 1980s one and a zero-length 1990s one that must
// never be scheduled.
func nineties(t *testing.T, f *testFixture) {
	t.Helper()
	f.createMovie(t, "n1", "Echo", 1990, epMs, i64(5000))
	f.createMovie(t, "n2", "alpha", 1991, epMs, i64(1000)) // lowercase: title order is accent- and case-insensitive
	f.createMovie(t, "n3", "Delta", 1992, epMs, i64(4000))
	f.createMovie(t, "n4", "Bravo", 1993, epMs, i64(2000))
	f.createMovie(t, "n5", "Charlie", 1994, epMs, i64(3000))
	f.createMovie(t, "old", "Zulu", 1985, epMs, nil)
	f.createMovie(t, "zero", "Yankee", 1995, 0, nil)
}

func TestCriteriaEndToEnd_ManualBlockMixesFixedAndCriteriaItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createCatalogItem(t, "intro", epMs)
	f.createCatalogItem(t, "outro", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "b0", "blk", 0, "intro")
	f.addCriteriaBlockItem(t, "crit", "blk", 1, yearQuery, domain.OrderTitle, countTwo)
	f.addBlockItem(t, "b2", "blk", 2, "outro")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	base := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute)
	f.createOnceScheduleItem(t, "a", "sched1", "blk", base)
	f.createOnceScheduleItem(t, "b", "sched1", "blk", base.Add(4*time.Hour))

	mat := New(f.db, 10*time.Hour)
	for run := 0; run < 2; run++ { // a second run must reproduce the first
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		entries := queryEntries(t, f)
		// Title order (folded): alpha n2, Bravo n4, Charlie n5, Delta n3, Echo n1.
		// Two matches per visit, the cursor shared by the two occurrences.
		want := []string{"intro", "n2", "n4", "outro", "intro", "n5", "n3", "outro"}
		if got := seriesIDs(entries); !equalStrings(got, want) {
			t.Fatalf("run %d: entries = %v, want %v", run+1, got, want)
		}
		for i, e := range entries {
			isCrit := e.SourceBlockItemID != nil
			if isCrit != (e.CatalogItemID != "intro" && e.CatalogItemID != "outro") {
				t.Errorf("entry %d (%s): source_block_item_id = %v", i, e.CatalogItemID, e.SourceBlockItemID)
			}
			if isCrit && (*e.SourceBlockItemID != "crit" || e.SeriesCycle == nil || *e.SeriesCycle != 0) {
				t.Errorf("entry %d: bookkeeping %v %v", i, *e.SourceBlockItemID, e.SeriesCycle)
			}
			if e.Kind != domain.EpgProgram {
				t.Errorf("entry %d kind = %s", i, e.Kind)
			}
		}
	}
}

func TestCriteriaEndToEnd_AllMatchesAirsEveryMatchOnceThenMovesOn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createCatalogItem(t, "outro", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, domain.OrderTitle, allMatches)
	f.addBlockItem(t, "b1", "blk", 1, "outro")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "a", "sched1", "blk", time.Now().UTC().Add(10*time.Minute).Truncate(time.Minute))

	if err := New(f.db, 10*time.Hour).Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"n2", "n4", "n5", "n3", "n1", "outro"} // the zero-length and the 1985 movie never air
	if got := seriesIDs(queryEntries(t, f)); !equalStrings(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func TestCriteriaEndToEnd_PlaybackOrders(t *testing.T) {
	cases := []struct {
		order string
		want  []string
	}{
		{domain.OrderTitle, []string{"n2", "n4", "n5", "n3", "n1"}},
		{domain.OrderReleaseDate, []string{"n2", "n4", "n5", "n3", "n1"}}, // premiere 1000, 2000, 3000, 4000, 5000
	}
	for _, tc := range cases {
		t.Run(tc.order, func(t *testing.T) {
			f := newFixture(t)
			nineties(t, f)
			f.createChannel(t, "chan1", "UTC")
			f.createBlock(t, "blk")
			f.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, tc.order, allMatches)
			f.createSchedule(t, "sched1")
			f.assignChannelSchedule(t, "chan1", "sched1")
			f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
			if err := New(f.db, 5*time.Hour).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := seriesIDs(queryEntries(t, f))
			if len(got) < 10 || !equalStrings(got[:5], tc.want) || !equalStrings(got[5:10], tc.want) {
				t.Errorf("entries = %v, want %v repeated", got, tc.want)
			}
		})
	}
}

func TestCriteriaEndToEnd_ShuffleAirsEveryMatchOncePerCycleAndRandomStaysInTheMatchSet(t *testing.T) {
	f := newFixture(t)
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, domain.OrderShuffle, allMatches)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	if err := New(f.db, 8*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := seriesIDs(queryEntries(t, f))
	if len(got) < 15 {
		t.Fatalf("entries = %v", got)
	}
	for c := 0; c < 3; c++ {
		seen := map[string]bool{}
		for _, id := range got[c*5 : c*5+5] {
			if seen[id] {
				t.Fatalf("cycle %d repeats %s before every match aired: %v", c, id, got[c*5:c*5+5])
			}
			seen[id] = true
		}
		for _, id := range []string{"n1", "n2", "n3", "n4", "n5"} {
			if !seen[id] {
				t.Errorf("cycle %d misses %s: %v", c, id, got[c*5:c*5+5])
			}
		}
	}

	// random: only ever picks matches.
	g := newFixture(t)
	nineties(t, g)
	g.createChannel(t, "chan1", "UTC")
	g.createBlock(t, "blk")
	g.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, domain.OrderRandom, allMatches)
	g.createSchedule(t, "sched1")
	g.assignChannelSchedule(t, "chan1", "sched1")
	g.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	if err := New(g.db, 8*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	matches := map[string]bool{"n1": true, "n2": true, "n3": true, "n4": true, "n5": true}
	rnd := seriesIDs(queryEntries(t, g))
	if len(rnd) < 10 {
		t.Fatalf("random entries = %v", rnd)
	}
	for _, id := range rnd {
		if !matches[id] {
			t.Errorf("random picked %s, which does not match the query", id)
		}
	}
}

func TestCriteriaEndToEnd_FollowsTheCatalogAndSkipsAnEmptyQuery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createCatalogItem(t, "fixed", epMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	// The first criteria item matches nothing: it is skipped and the next item airs.
	f.addCriteriaBlockItem(t, "none", "blk", 0, `{"version":1,"all":[{"field":"year","op":"gt","value":2100}]}`, domain.OrderTitle, countTwo)
	f.addCriteriaBlockItem(t, "crit", "blk", 1, yearQuery, domain.OrderTitle, `{"mode":"count","count":1}`)
	f.addBlockItem(t, "b2", "blk", 2, "fixed")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

	mat := New(f.db, 3*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got := seriesIDs(queryEntries(t, f))
	if len(got) < 4 || !equalStrings(got[:4], []string{"n2", "fixed", "n4", "fixed"}) {
		t.Fatalf("entries = %v, want the empty item skipped and one match per visit", got)
	}

	// A new matching movie shows up in the next reset (the query is live).
	f.createMovie(t, "n0", "Aardvark", 1999, epMs, nil)
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatal(err)
	}
	got = seriesIDs(queryEntries(t, f))
	if len(got) < 2 || got[0] != "n0" {
		t.Fatalf("after reset entries = %v, want the new match (title order) first", got)
	}
}

func TestCriteriaEndToEnd_ResetRestartsAndPreviewWritesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, domain.OrderTitle, allMatches)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

	mat := New(f.db, 4*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Pretend 70 minutes passed: n2, n4 are history, n5 is on the air.
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
	live := func() []string {
		var out []string
		for _, e := range queryEntries(t, f) {
			if e.EndAt > now {
				out = append(out, e.CatalogItemID)
			}
		}
		return out
	}
	if l := live(); len(l) == 0 || l[0] != "n5" {
		t.Fatalf("setup: on the air = %v, want n5 first", l)
	}

	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	// A dry run writes nothing.
	var before []int
	f.db.Query(ctx, &before, "SELECT (SELECT COUNT(*) FROM epg_entry) + (SELECT COALESCE(SUM(origin_ms), 0) FROM series_cursor)")
	preview, err := mat.PreviewReset(ctx, f.tenantID, ch, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) == 0 || preview[0].CatalogItemID != "n2" {
		t.Fatalf("preview = %v, want it to start from the first match", seriesIDs(preview))
	}
	var after []int
	f.db.Query(ctx, &after, "SELECT (SELECT COUNT(*) FROM epg_entry) + (SELECT COALESCE(SUM(origin_ms), 0) FROM series_cursor)")
	if before[0] != after[0] {
		t.Errorf("preview changed stored rows: %v -> %v", before, after)
	}

	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatal(err)
	}
	if l := live(); len(l) < 2 || l[0] != "n2" || l[1] != "n4" {
		t.Fatalf("after reset = %v, want to restart at n2, n4", l)
	}
}

func TestCriteriaEndToEnd_TwoChannelsShuffleIndependently(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 12; i++ {
		f.createMovie(t, fmt.Sprintf("m%02d", i), fmt.Sprintf("Movie %02d", i), 1990, epMs, nil)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createChannelWithNumber(t, "chan2", 2, "UTC")
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, yearQuery, domain.OrderShuffle, allMatches)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.assignChannelSchedule(t, "chan2", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	if err := New(f.db, 8*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var a, b []string
	for _, e := range queryEntries(t, f) {
		a = append(a, e.CatalogItemID)
	}
	var es []domain.EpgEntry
	if err := f.db.Query(context.Background(), &es,
		"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = 'chan2' ORDER BY start_at"); err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		b = append(b, e.CatalogItemID)
	}
	if len(a) < 12 || len(b) < 12 || equalStrings(a[:12], b[:12]) {
		t.Errorf("two channels sharing a block shuffled alike: %v / %v", a, b)
	}
}

func TestCriteriaWalkSpec(t *testing.T) {
	count := criteriaWalkSpec(domain.CriteriaSpec{PlaybackOrder: domain.OrderTitle, Seed: 7, Amount: domain.CriteriaAmount{Mode: domain.CriteriaAmountCount, Count: 3}})
	if count.PlaybackOrder != domain.OrderTitle || count.Seed != 7 || count.Amount == nil || count.Amount.Mode != domain.AmountCount || count.Amount.Count != 3 {
		t.Errorf("count spec = %+v", count)
	}
	all := criteriaWalkSpec(domain.CriteriaSpec{PlaybackOrder: domain.OrderShuffle, Amount: domain.CriteriaAmount{Mode: domain.CriteriaAmountAll}})
	if all.Amount == nil || all.Amount.Mode != domain.AmountSeriesEnd {
		t.Errorf("all spec = %+v, want the rest of the cycle", all)
	}
}

func TestSortEpisodesTitleIsAccentAndCaseInsensitive(t *testing.T) {
	eps := []series.Episode{
		{ID: "1", Title: "Zed"},
		// Accented text below is deliberate test data for the accent-insensitive matching.
		{ID: "2", Title: "Éclair"},
		{ID: "3", Title: "eagle"},
		{ID: "5", Title: "Eagle"}, // same folded title: id breaks the tie
		{ID: "4", Title: "Apple"},
	}
	sortEpisodes(eps, domain.OrderTitle)
	var got []string
	for _, e := range eps {
		got = append(got, e.ID)
	}
	if want := []string{"4", "3", "5", "2", "1"}; !equalStrings(got, want) {
		t.Errorf("title order = %v, want %v", got, want)
	}
}
