package materializer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

// ilEps returns n episodes of one series: ids "<group><nn>", natural order by
// episode number (the slice is deliberately reversed so sorting is exercised).
func ilEps(group string, n int) []series.Episode {
	out := make([]series.Episode, 0, n)
	for i := n; i >= 1; i-- {
		out = append(out, series.Episode{
			ID:            fmt.Sprintf("%s%02d", group, i),
			Title:         fmt.Sprintf("%s episode %d", group, i),
			DurationMs:    epMs,
			SeasonNumber:  i64(1),
			EpisodeNumber: i64(int64(i)),
			GroupKey:      "s:" + group,
			GroupTitle:    "Series " + group,
		})
	}
	return out
}

// ilMovie is an item with no series: a one-item series of its own.
func ilMovie(id, title string) series.Episode {
	return series.Episode{ID: id, Title: title, DurationMs: epMs, GroupKey: "i:" + id, GroupTitle: title}
}

func ilSpec(il domain.Interleave, seed int64) domain.SeriesSpec {
	return domain.SeriesSpec{PlaybackOrder: domain.OrderInterleaved, Seed: seed, Interleave: &il}
}

func ilConcat(parts ...[]series.Episode) []series.Episode {
	var out []series.Episode
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// drainRows takes n slots from the run like the materializer does and also
// returns the rows it would have persisted.
func drainRows(r *seriesRun, n int) ([]string, []seriesRow) {
	var ids []string
	var rows []seriesRow
	for i := 0; i < n; i++ {
		ep, cycle, round, ok := r.nextRound()
		if !ok {
			break
		}
		r.applyRound(ep.ID, cycle, round)
		ids = append(ids, ep.ID)
		rows = append(rows, seriesRow{StartAt: int64(1000 + i), CatalogItemID: ep.ID, Cycle: cycle, Round: round})
	}
	return ids, rows
}

func ilDrain(r *seriesRun, n int) []string { ids, _ := drainRows(r, n); return ids }

func TestInterleave_RestartShortSeriesLoopOnTheirOwn(t *testing.T) {
	// A has 5 episodes, B 2, C is a movie (a one-item series); series sorted by
	// title: "Series A", "Series B", "Zeta movie".
	eps := ilConcat(ilEps("a", 5), ilEps("b", 2), []series.Episode{ilMovie("m1", "Zeta movie")})
	r := newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0)
	got := ilDrain(r, 15)
	// Every round visits every series: B and the movie restart by themselves
	// whenever they run out, while A is still on its first pass.
	want := []string{"a01", "b01", "m1", "a02", "b02", "m1", "a03", "b01", "m1", "a04", "b02", "m1", "a05", "b01", "m1"}
	if !equalStrings(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	// A then restarts too, in the cycle it records for itself (round 5 is its
	// second pass, and B's third: B restarts every two rounds).
	_, rows := drainRows(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0), 18)
	if r15, r16 := rows[15], rows[16]; r15.CatalogItemID != "a01" || r15.Cycle != 1 || r15.Round != 5 || r16.CatalogItemID != "b02" || r16.Cycle != 2 || r16.Round != 5 {
		t.Fatalf("rows 15/16 = %+v %+v", r15, r16)
	}
}

func TestInterleave_DropSeriesLeaveForGoodAndTheCycleThenStartsOver(t *testing.T) {
	eps := ilConcat(ilEps("a", 5), ilEps("b", 2), []series.Episode{ilMovie("m1", "Zeta movie")})
	r := newSeriesRun(ilSpec(domain.Interleave{OnExhaustion: domain.OnExhaustionDrop}, 1), "chan", eps, 0, nil, nil, 0)
	got := ilDrain(r, 12)
	// Round 1: a01 b01 m1; round 2: a02 b02; then B and the movie have left
	// and A plays on alone; every series is done after a05, so the list ends
	// and (the block looping) everything starts over.
	want := []string{"a01", "b01", "m1", "a02", "b02", "a03", "a04", "a05", "a01", "b01", "m1", "a02"}
	if !equalStrings(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestInterleave_PerRoundAirsThatManyEpisodesPerTurn(t *testing.T) {
	eps := ilConcat(ilEps("a", 5), ilEps("b", 3))
	r := newSeriesRun(ilSpec(domain.Interleave{PerRound: 2}, 1), "chan", eps, 0, nil, nil, 0)
	got := ilDrain(r, 10)
	// Turns of two; B runs out after b03 and restarts within its turn.
	want := []string{"a01", "a02", "b01", "b02", "a03", "a04", "b03", "b01", "a05", "a01"}
	if !equalStrings(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	r = newSeriesRun(ilSpec(domain.Interleave{PerRound: 2, OnExhaustion: domain.OnExhaustionDrop}, 1), "chan", eps, 0, nil, nil, 0)
	got = ilDrain(r, 10)
	// Drop: B leaves after b03 (its second turn has one episode), A finishes
	// alone, then the cycle restarts.
	want = []string{"a01", "a02", "b01", "b02", "a03", "a04", "b03", "a05", "a01", "a02"}
	if !equalStrings(got, want) {
		t.Fatalf("drop: got  %v\nwant %v", got, want)
	}
}

func TestInterleave_EpisodesFollowNaturalOrderSpecialsFirstNullsLast(t *testing.T) {
	mk := func(id string, season, ep *int64) series.Episode {
		return series.Episode{ID: id, Title: id, DurationMs: epMs, SeasonNumber: season, EpisodeNumber: ep, GroupKey: "s:x", GroupTitle: "X"}
	}
	eps := []series.Episode{mk("none", nil, nil), mk("s2e1", i64(2), i64(1)), mk("sp", i64(0), i64(1)), mk("s1e2", i64(1), i64(2)), mk("s1e1", i64(1), i64(1))}
	got := ilDrain(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0), 5)
	want := []string{"sp", "s1e1", "s1e2", "s2e1", "none"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestInterleave_SeriesOrderTitleReleaseDateShuffleRandom(t *testing.T) {
	mk := func(group, title string, premiere *int64) []series.Episode {
		e := ilEps(group, 2)
		for i := range e {
			e[i].GroupTitle = title
			e[i].PremiereDate = premiere
		}
		return e
	}
	// Titles sort c < d < e; premieres: e oldest, c middle, d none (last).
	eps := ilConcat(mk("c", "Cartoon", i64(200)), mk("d", "Duck", nil), mk("e", "Eagle", i64(100)))
	firstRound := func(il domain.Interleave, channel string) []string {
		return ilDrain(newSeriesRun(ilSpec(il, 7), channel, eps, 0, nil, nil, 0), 3)
	}
	if got, want := firstRound(domain.Interleave{SeriesOrder: domain.OrderTitle}, "chan"), []string{"c01", "d01", "e01"}; !equalStrings(got, want) {
		t.Errorf("title: %v", got)
	}
	if got, want := firstRound(domain.Interleave{SeriesOrder: domain.OrderReleaseDate}, "chan"), []string{"e01", "c01", "d01"}; !equalStrings(got, want) {
		t.Errorf("release_date: %v", got)
	}

	// shuffle: the same sequence every round of a cycle, reproducible from seed
	// and channel, different across channels (for at least one of a few).
	seqOf := func(il domain.Interleave, channel string, n int) []string {
		return ilDrain(newSeriesRun(ilSpec(il, 7), channel, eps, 0, nil, nil, 0), n)
	}
	sh := seqOf(domain.Interleave{SeriesOrder: domain.OrderShuffle}, "chan", 6)
	groupOf := func(ids []string) string {
		var b strings.Builder
		for _, id := range ids {
			b.WriteString(id[:1])
		}
		return b.String()
	}
	if groupOf(sh[:3]) != groupOf(sh[3:6]) {
		t.Errorf("shuffle changed between rounds: %v", sh)
	}
	if again := seqOf(domain.Interleave{SeriesOrder: domain.OrderShuffle}, "chan", 6); !equalStrings(sh, again) {
		t.Errorf("shuffle not reproducible: %v vs %v", sh, again)
	}
	differs := false
	for _, ch := range []string{"chan2", "chan3", "chan4", "chan5", "chan6"} {
		if groupOf(seqOf(domain.Interleave{SeriesOrder: domain.OrderShuffle}, ch, 3)) != groupOf(sh[:3]) {
			differs = true
		}
	}
	if !differs {
		t.Error("shuffle identical on every channel")
	}

	// random: reproducible, covers every series each round, and varies between
	// rounds for some round. With two episodes per series there are two rounds
	// per cycle, so look over several cycles.
	long := ilConcat(ilEps("c", 20), ilEps("d", 20), ilEps("e", 20))
	for i := range long {
		long[i].GroupTitle = strings.ToUpper(long[i].GroupKey)
	}
	rnd := ilDrain(newSeriesRun(ilSpec(domain.Interleave{SeriesOrder: domain.OrderRandom}, 7), "chan", long, 0, nil, nil, 0), 60)
	if again := ilDrain(newSeriesRun(ilSpec(domain.Interleave{SeriesOrder: domain.OrderRandom}, 7), "chan", long, 0, nil, nil, 0), 60); !equalStrings(rnd, again) {
		t.Error("random not reproducible")
	}
	rounds := map[string]bool{}
	for i := 0; i+3 <= 60; i += 3 {
		g := groupOf(rnd[i : i+3])
		if len(map[rune]bool{rune(g[0]): true, rune(g[1]): true, rune(g[2]): true}) != 3 {
			t.Fatalf("round %d does not cover every series: %v", i/3, rnd[i:i+3])
		}
		rounds[g] = true
	}
	if len(rounds) < 2 {
		t.Errorf("random series order never changed: %v", rnd)
	}
}

func TestInterleave_RandomDoesNotRepeatASeriesAcrossARoundBoundary(t *testing.T) {
	long := ilConcat(ilEps("c", 30), ilEps("d", 30), ilEps("e", 30))
	ids := ilDrain(newSeriesRun(ilSpec(domain.Interleave{SeriesOrder: domain.OrderRandom}, 3), "chan", long, 0, nil, nil, 0), 90)
	for i := 1; i < len(ids); i++ {
		if ids[i][:1] == ids[i-1][:1] {
			t.Fatalf("series repeated back to back at %d: %v", i, ids[i-1:i+1])
		}
	}
}

func TestInterleave_CursorRebuildsFromPersistedRowsAtEveryPoint(t *testing.T) {
	eps := ilConcat(ilEps("a", 6), ilEps("b", 3), ilEps("c", 4), []series.Episode{ilMovie("m1", "Movie")})
	for _, il := range []domain.Interleave{
		{PerRound: 1}, {PerRound: 2, SeriesOrder: domain.OrderShuffle}, {PerRound: 1, SeriesOrder: domain.OrderRandom}, {PerRound: 3, OnExhaustion: domain.OnExhaustionDrop},
	} {
		full, rows := drainRows(newSeriesRun(ilSpec(il, 9), "chan", eps, 0, nil, nil, 0), 40)
		for k := 1; k < 40; k++ {
			r := newSeriesRun(ilSpec(il, 9), "chan", eps, 0, rows[:k], nil, 0)
			if rest := ilDrain(r, 40-k); !equalStrings(rest, full[k:]) {
				t.Fatalf("%+v: resuming after %d: %v, want %v", il, k, rest, full[k:])
			}
		}
	}
}

func TestInterleave_CatalogChangeMidRun(t *testing.T) {
	eps := ilConcat(ilEps("a", 6), ilEps("c", 6))
	_, rows := drainRows(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0), 4) // a01 c01 a02 c02

	// A new series B appears (it sorts between A and C): it joins when its
	// turn in the round comes up, here the next one, and nothing is replayed.
	withB := ilConcat(eps, ilEps("b", 2))
	got := ilDrain(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", withB, 0, rows, nil, 0), 6)
	want := []string{"a03", "b01", "c03", "a04", "b02", "c04"}
	if !equalStrings(got, want) {
		t.Errorf("series gained: got %v want %v", got, want)
	}

	// Series C is removed: it leaves without a hole, A carries on.
	onlyA := ilEps("a", 6)
	got = ilDrain(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", onlyA, 0, rows, nil, 0), 3)
	if want := []string{"a03", "a04", "a05"}; !equalStrings(got, want) {
		t.Errorf("series removed: got %v want %v", got, want)
	}

	// An episode of A removed while played ones stay counted: no repeat, no hole.
	var noA3 []series.Episode
	for _, e := range eps {
		if e.ID != "a03" {
			noA3 = append(noA3, e)
		}
	}
	got = ilDrain(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", noA3, 0, rows, nil, 0), 4)
	if want := []string{"a04", "c03", "a05", "c04"}; !equalStrings(got, want) {
		t.Errorf("episode removed: got %v want %v", got, want)
	}
}

func TestInterleave_EmptyCatalogHasNothingToPlay(t *testing.T) {
	r := newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", nil, 0, nil, nil, 0)
	if !r.empty() {
		t.Fatal("run over no episodes is not empty")
	}
	if _, _, _, ok := r.nextRound(); ok {
		t.Fatal("nextRound on an empty run reported an episode")
	}
}

func TestInterleave_DropCutsACountVisitAtTheEndOfTheListRestartNeverEnds(t *testing.T) {
	eps := ilConcat(ilEps("a", 3), ilEps("b", 1)) // a cycle is 4 episodes
	visit := func(on string) ([]string, bool) {
		il := domain.Interleave{OnExhaustion: on}
		run := newSeriesRun(ilSpec(il, 1), "chan", eps, 0, nil, nil, 0)
		run.amount = domain.SeriesAmount{Mode: domain.AmountCount, Count: 6}
		entries, _, completed := emitSeries(run, "blk", seriesVisit{Amount: run.amount}, time.UnixMilli(0), time.UnixMilli(0).Add(48*time.Hour), true, domain.EpgProgram, "t", "chan", 0, nil)
		return seriesIDs(entries), completed
	}
	got, done := visit(domain.OnExhaustionRestart)
	if want := []string{"a01", "b01", "a02", "b01", "a03", "b01"}; !equalStrings(got, want) || !done {
		t.Errorf("restart: %v done=%v, want %v", got, done, want)
	}
	got, done = visit(domain.OnExhaustionDrop)
	if want := []string{"a01", "b01", "a02", "a03"}; !equalStrings(got, want) || !done {
		t.Errorf("drop: %v done=%v, want the visit to end with the list: %v", got, done, want)
	}
}

// ---- end to end: a dynamic block over one long series plus short ones ----

const epsQuery = `{"version":1,"all":[{"field":"kind","op":"equals","value":"episode"}]}`

func (f *testFixture) createInterleavedBlock(t *testing.T, id, criteria, order, interleave string, seed int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	var spec any
	if interleave != "" {
		spec = interleave
	}
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block (id, tenant_id, name, criteria, playback_order, interleave_spec, playback_seed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, f.tenantID, "Block "+id, criteria, order, spec, seed, now, now,
	); err != nil {
		t.Fatalf("seed interleaved block %s: %v", id, err)
	}
}

func (f *testFixture) seedSeries(t *testing.T, group string, n int, durationMs int64) {
	t.Helper()
	for i := 1; i <= n; i++ {
		f.createEpisode(t, fmt.Sprintf("%s%03d", group, i), group, 1, i, durationMs)
	}
}

// cartoonsSetup is the reported case: every cartoon in one dynamic block, where
// the first series by title has 153 episodes. Episodes last 10 minutes so a
// 48h window holds a full playback cycle (169 episodes).
func cartoonsSetup(t *testing.T, order, interleave string) *testFixture {
	t.Helper()
	f := newFixture(t)
	const dur = int64(10 * 60 * 1000)
	f.seedSeries(t, "Aaa", 153, dur)
	f.seedSeries(t, "Bbb", 3, dur)
	f.seedSeries(t, "Ccc", 5, dur)
	f.seedSeries(t, "Ddd", 8, dur)
	f.createChannel(t, "chan1", "UTC")
	f.createInterleavedBlock(t, "cartoons", epsQuery, order, interleave, 42)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "cartoons")
	return f
}

func prefixes(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[:3]
	}
	return out
}

func TestInterleaveEndToEnd_ReportedCaseRestartShortSeriesLoopWhileTheLongOneContinues(t *testing.T) {
	f := cartoonsSetup(t, domain.OrderInterleaved, `{"per_round":1,"on_exhaustion":"restart","series_order":"title"}`)
	ctx := context.Background()
	mat := New(f.db, 48*time.Hour)
	for run := 0; run < 2; run++ { // a second run must reproduce the first
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		entries := queryEntries(t, f)
		ids := seriesIDs(entries)
		assertContiguous(t, entries)
		if len(ids) < 280 {
			t.Fatalf("window holds only %d entries", len(ids))
		}
		// 10-minute episodes in a 48h window: 288 entries = 72 full rounds of
		// the four series, none of them ever leaving the rotation.
		for i, e := range entries {
			round, turn := i/4, i%4
			var want string
			var wantCycle int
			switch turn {
			case 0:
				want, wantCycle = fmt.Sprintf("Aaa%03d", round%153+1), round/153
			case 1:
				want, wantCycle = fmt.Sprintf("Bbb%03d", round%3+1), round/3
			case 2:
				want, wantCycle = fmt.Sprintf("Ccc%03d", round%5+1), round/5
			default:
				want, wantCycle = fmt.Sprintf("Ddd%03d", round%8+1), round/8
			}
			if e.CatalogItemID != want || *e.SeriesCycle != wantCycle || *e.SeriesRound != round {
				t.Fatalf("run %d entry %d = %s cycle %d round %d, want %s cycle %d round %d", run+1, i, e.CatalogItemID, *e.SeriesCycle, *e.SeriesRound, want, wantCycle, round)
			}
		}
	}
}

func TestInterleaveEndToEnd_ReportedCaseDropSeriesLeaveAndTheLongOneEndsAlone(t *testing.T) {
	for _, on := range []string{domain.OnExhaustionDrop} {
		t.Run(on, func(t *testing.T) {
			f := cartoonsSetup(t, domain.OrderInterleaved, fmt.Sprintf(`{"per_round":1,"on_exhaustion":%q,"series_order":"title"}`, on))
			ctx := context.Background()
			mat := New(f.db, 48*time.Hour)
			for run := 0; run < 2; run++ { // a second run must reproduce the first
				if err := mat.Run(ctx); err != nil {
					t.Fatal(err)
				}
				entries := queryEntries(t, f)
				ids := seriesIDs(entries)
				assertContiguous(t, entries)
				for _, e := range entries {
					if e.SourceBlockItemID == nil || *e.SourceBlockItemID != "cartoons" || e.SeriesCycle == nil || e.SeriesRound == nil || e.Kind != domain.EpgProgram {
						t.Fatalf("entry %s bookkeeping: %+v", e.CatalogItemID, e)
					}
				}
				p := prefixes(ids)
				// The first rounds visit every series in turn, not 20 of the first.
				wantStart := []string{"Aaa", "Bbb", "Ccc", "Ddd", "Aaa", "Bbb", "Ccc", "Ddd", "Aaa", "Bbb", "Ccc", "Ddd", "Aaa", "Ccc", "Ddd", "Aaa", "Ccc", "Ddd", "Aaa", "Ddd"}
				if !equalStrings(p[:len(wantStart)], wantStart) {
					t.Fatalf("run %d: first entries %v, want %v", run+1, p[:len(wantStart)], wantStart)
				}
				// Within a series the episodes air in order.
				if ids[0] != "Aaa001" || ids[4] != "Aaa002" || ids[1] != "Bbb001" || ids[5] != "Bbb002" {
					t.Fatalf("episode order: %v", ids[:8])
				}
				// Once the small series ran out, A plays alone to its last episode.
				// Round 9..153 is A alone: 20 entries done above; cycle length 169.
				if len(ids) < 169+4 {
					t.Fatalf("window holds only %d entries", len(ids))
				}
				for i := 0; i < 169; i++ {
					if cycle := *entries[i].SeriesCycle; cycle != 0 {
						t.Fatalf("entry %d in cycle %d, want 0", i, cycle)
					}
				}
				if ids[168] != "Aaa153" || *entries[169].SeriesCycle != 1 {
					t.Fatalf("cycle end: %s then cycle %d", ids[168], *entries[169].SeriesCycle)
				}
				// Every series restarts from its first episode and rounds begin again.
				if got, want := ids[169:173], []string{"Aaa001", "Bbb001", "Ccc001", "Ddd001"}; !equalStrings(got, want) {
					t.Fatalf("after the cycle: %v, want %v", got, want)
				}
				// Every episode aired exactly once per cycle.
				seen := map[string]int{}
				for _, id := range ids[:169] {
					seen[id]++
				}
				if len(seen) != 169 {
					t.Fatalf("%d distinct episodes in a cycle of 169", len(seen))
				}
			}
		})
	}
}

func TestInterleaveEndToEnd_DynamicBlockWithDefaultOrderIsUnchanged(t *testing.T) {
	for _, order := range []string{"", domain.OrderTitle} { // "": a row from before the setting (column default)
		f := newFixture(t)
		const dur = int64(10 * 60 * 1000)
		f.seedSeries(t, "Aaa", 30, dur)
		f.seedSeries(t, "Bbb", 3, dur)
		f.createChannel(t, "chan1", "UTC")
		if order == "" {
			now := time.Now().UnixMilli()
			if err := f.db.Exec(context.Background(),
				"INSERT INTO block (id, tenant_id, name, criteria, created_at, updated_at) VALUES ('cartoons', ?, 'Cartoons', ?, ?, ?)",
				f.tenantID, epsQuery, now, now); err != nil {
				t.Fatal(err)
			}
		} else {
			f.createInterleavedBlock(t, "cartoons", epsQuery, order, "", 0)
		}
		// The block's items are owned by the catalog resolver: by title, looped.
		var ids []string
		if err := f.db.Query(context.Background(), &ids, "SELECT id FROM catalog_item ORDER BY title, id"); err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			f.addBlockItem(t, fmt.Sprintf("bi%03d", i), "cartoons", i, id)
		}
		f.createSchedule(t, "sched1")
		f.assignChannelSchedule(t, "chan1", "sched1")
		f.createContinuousScheduleItem(t, "cont", "sched1", "cartoons")
		if err := New(f.db, 8*time.Hour).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		entries := queryEntries(t, f)
		got := seriesIDs(entries)
		if len(got) < 20 || !equalStrings(got[:20], ids[:20]) {
			t.Fatalf("order %q: dynamic block no longer plays the title list: %v", order, got[:20])
		}
		for _, e := range entries {
			if e.SourceBlockItemID != nil || e.SeriesRound != nil {
				t.Fatalf("order %q: a default dynamic block recorded walk bookkeeping: %+v", order, e)
			}
		}
	}
}

func TestInterleaveEndToEnd_ResetRestartsAndTwoChannelsShuffleDifferently(t *testing.T) {
	f := cartoonsSetup(t, domain.OrderInterleaved, `{"per_round":1,"on_exhaustion":"restart","series_order":"shuffle"}`)
	f.createChannelWithNumber(t, "chan2", 2, "UTC")
	f.assignChannelSchedule(t, "chan2", "sched1")
	ctx := context.Background()
	mat := New(f.db, 4*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	roundOf := func(ch string) []string {
		var ids []string
		if err := f.db.Query(ctx, &ids, "SELECT catalog_item_id FROM epg_entry WHERE channel_id = ? ORDER BY start_at LIMIT 4", ch); err != nil {
			t.Fatal(err)
		}
		return prefixes(ids)
	}
	c1, c2 := roundOf("chan1"), roundOf("chan2")
	if equalStrings(c1, c2) {
		// Seeds are per channel; if two happen to collide on a 4-series shuffle,
		// the later rounds still must not be the same sequence of series.
		t.Logf("first rounds coincide: %v", c1)
	}
	var all1, all2 []string
	if err := f.db.Query(ctx, &all1, "SELECT catalog_item_id FROM epg_entry WHERE channel_id = 'chan1' ORDER BY start_at LIMIT 12"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Query(ctx, &all2, "SELECT catalog_item_id FROM epg_entry WHERE channel_id = 'chan2' ORDER BY start_at LIMIT 12"); err != nil {
		t.Fatal(err)
	}
	if equalStrings(all1, all2) {
		t.Errorf("two channels aired the same interleaved sequence: %v", all1)
	}
	if got := append(append([]string{}, c1...), c1...); !equalStrings(prefixes(all1[:8]), got) {
		t.Errorf("shuffle is not fixed within a cycle: %v", prefixes(all1[:8]))
	}

	// Pretend 70 minutes passed; the reset must restart from the first
	// episode of every series, whatever was on the air.
	shift := int64(70 * 60 * 1000)
	if err := f.db.Exec(ctx, "UPDATE epg_entry SET start_at = start_at - ?, end_at = end_at - ?", shift, shift); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(ctx, "UPDATE block_cursor SET origin_ms = origin_ms - ?", shift); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatalf("ResetChannel: %v", err)
	}
	now := time.Now().UnixMilli()
	var after []string
	if err := f.db.Query(ctx, &after, "SELECT catalog_item_id FROM epg_entry WHERE channel_id = 'chan1' AND end_at > ? ORDER BY start_at LIMIT 8", now); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(prefixes(after[:4]), c1) || after[0][3:] != "001" {
		t.Fatalf("after reset: %v, want the first round again (%v) from episode 001", after, c1)
	}
	for _, id := range after[:4] {
		if id[3:] != "001" {
			t.Fatalf("after reset the first round is not every series' first episode: %v", after[:4])
		}
	}
}

func TestInterleaveEndToEnd_CatalogChangeMidRunKeepsTheGridAndNewSeriesJoin(t *testing.T) {
	f := cartoonsSetup(t, domain.OrderInterleaved, `{"per_round":1,"on_exhaustion":"restart","series_order":"title"}`)
	ctx := context.Background()
	mat := New(f.db, 48*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	before := seriesIDs(queryEntries(t, f))

	// A new series sorting between Bbb and Ccc appears; Ddd stops being
	// schedulable (zero-length items are left out of every walk, which is what a
	// removal does to the walk too; deleting rows would trip the epg_entry
	// foreign key of what already aired).
	f.seedSeries(t, "Bcc", 4, int64(10*60*1000))
	if err := f.db.Exec(ctx, "UPDATE catalog_item SET duration_ms = 0 WHERE series_title = 'Ddd'"); err != nil {
		t.Fatal(err)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	after := seriesIDs(queryEntries(t, f))
	// The frozen head (one hour: six 10-minute episodes) is untouched.
	if !equalStrings(after[:6], before[:6]) {
		t.Fatalf("frozen head changed: %v vs %v", after[:6], before[:6])
	}
	p := prefixes(after)
	joined, dropped := false, true
	for i, s := range p {
		if s == "Bcc" {
			joined = true
			if i < 6 {
				t.Errorf("new series inside the frozen head at %d", i)
			}
		}
		if s == "Ddd" && i >= 6 {
			dropped = false
		}
	}
	if !joined {
		t.Error("the new series never joined the rounds")
	}
	if !dropped {
		t.Error("a removed series still airs after the frozen head")
	}
	assertContiguous(t, queryEntries(t, f))
	// No episode plays twice within the cycle in progress.
	seen := map[string]bool{}
	entries := queryEntries(t, f)
	for _, e := range entries {
		if *e.SeriesCycle != 0 {
			break
		}
		if seen[e.CatalogItemID] {
			t.Fatalf("%s aired twice in one cycle", e.CatalogItemID)
		}
		seen[e.CatalogItemID] = true
	}
}

func TestInterleaveEndToEnd_ManualBlockCriteriaItemDropEndsTheVisitAtTheListEnd(t *testing.T) {
	for _, on := range []string{domain.OnExhaustionRestart, domain.OnExhaustionDrop} {
		f := newFixture(t)
		const dur = int64(10 * 60 * 1000)
		f.seedSeries(t, "Aaa", 3, dur)
		f.seedSeries(t, "Bbb", 1, dur)
		f.createCatalogItem(t, "outro", dur)
		f.createChannel(t, "chan1", "UTC")
		f.createBlock(t, "blk")
		spec := fmt.Sprintf(`{"query":%s,"playback_order":"interleaved","seed":42,"amount":{"mode":"count","count":6},"interleave":{"per_round":1,"on_exhaustion":%q,"series_order":"title"}}`, epsQuery, on)
		if err := f.db.Exec(context.Background(),
			"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES ('crit', 'blk', 0, 'criteria', NULL, ?)", spec); err != nil {
			t.Fatal(err)
		}
		f.addBlockItem(t, "b1", "blk", 1, "outro")
		f.createSchedule(t, "sched1")
		f.assignChannelSchedule(t, "chan1", "sched1")
		f.createOnceScheduleItem(t, "a", "sched1", "blk", time.Now().UTC().Add(10*time.Minute).Truncate(time.Minute))
		for run := 0; run < 2; run++ {
			if err := New(f.db, 6*time.Hour).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := seriesIDs(queryEntries(t, f))
			// restart: B loops while A plays out; the visit ends (count 6)
			// without a list end ever being reached.
			want := []string{"Aaa001", "Bbb001", "Aaa002", "Bbb001", "Aaa003", "Bbb001", "outro"}
			if on == domain.OnExhaustionDrop {
				want = []string{"Aaa001", "Bbb001", "Aaa002", "Aaa003", "outro"}
			}
			if !equalStrings(got, want) {
				t.Fatalf("%s run %d: %v, want %v", on, run+1, got, want)
			}
		}
	}
}

func TestInterleaveEndToEnd_PreviewResetMatchesAndWritesNothing(t *testing.T) {
	f := cartoonsSetup(t, domain.OrderInterleaved, `{"per_round":2,"on_exhaustion":"restart","series_order":"title"}`)
	ctx := context.Background()
	mat := New(f.db, 6*time.Hour)
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	preview, err := mat.PreviewReset(ctx, f.tenantID, ch, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(queryEntries(t, f)); n != 0 {
		t.Fatalf("preview wrote %d entries", n)
	}
	var cursors []int64
	if err := f.db.Query(ctx, &cursors, "SELECT origin_ms FROM block_cursor"); err != nil || len(cursors) != 0 {
		t.Fatalf("preview wrote a cursor: %v %v", cursors, err)
	}
	want := []string{"Aaa001", "Aaa002", "Bbb001", "Bbb002", "Ccc001", "Ccc002", "Ddd001", "Ddd002", "Aaa003", "Aaa004"}
	if got := seriesIDs(preview); len(got) < len(want) || !equalStrings(got[:len(want)], want) {
		t.Fatalf("preview = %v, want it to start %v", got, want)
	}
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatal(err)
	}
	real := seriesIDs(queryEntries(t, f))
	if got := seriesIDs(preview); !equalStrings(got[:30], real[:30]) {
		t.Fatalf("preview and reset differ:\n%v\n%v", got[:30], real[:30])
	}
}

// A "series end" visit of a restart walk lasts until every series has been
// through a pass: the short ones loop meanwhile, the visit ends with the
// longest one.
func TestInterleave_RestartSeriesEndVisitEndsWhenEverySeriesCompletedAPass(t *testing.T) {
	eps := ilConcat(ilEps("a", 3), ilEps("b", 1))
	run := newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0)
	run.amount = domain.SeriesAmount{Mode: domain.AmountSeriesEnd}
	limit := time.UnixMilli(0).Add(48 * time.Hour)
	entries, end, completed := emitSeries(run, "blk", seriesVisit{Amount: run.amount}, time.UnixMilli(0), limit, true, domain.EpgProgram, "t", "chan", 0, nil)
	if want := []string{"a01", "b01", "a02", "b01", "a03"}; !equalStrings(seriesIDs(entries), want) || !completed {
		t.Fatalf("first visit %v completed=%v, want %v", seriesIDs(entries), completed, want)
	}
	// The next visit is another pass of everything.
	entries, _, completed = emitSeries(run, "blk", seriesVisit{Amount: run.amount}, end, limit, true, domain.EpgProgram, "t", "chan", 0, nil)
	if want := []string{"b01", "a01", "b01", "a02", "b01", "a03"}; !equalStrings(seriesIDs(entries), want) || !completed {
		t.Fatalf("second visit %v completed=%v, want %v", seriesIDs(entries), completed, want)
	}
}

// The boundary a series end visit depends on is rebuilt from rows like the
// rest of the cursor.
func TestInterleave_RestartBoundaryRebuildsFromRows(t *testing.T) {
	eps := ilConcat(ilEps("a", 3), ilEps("b", 2), ilEps("c", 1))
	il := domain.Interleave{PerRound: 2}
	_, rows := drainRows(newSeriesRun(ilSpec(il, 5), "chan", eps, 0, nil, nil, 0), 40)
	live := newSeriesRun(ilSpec(il, 5), "chan", eps, 0, nil, nil, 0)
	for k := 1; k <= 40; k++ {
		ep, cycle, round, _ := live.nextRound()
		live.applyRound(ep.ID, cycle, round)
		rebuilt := newSeriesRun(ilSpec(il, 5), "chan", eps, 0, rows[:k], nil, 0)
		if rebuilt.cycleDone() != live.cycleDone() {
			t.Fatalf("after %d rows: rebuilt boundary %v, live %v", k, rebuilt.cycleDone(), live.cycleDone())
		}
	}
}

// Rows an older version wrote (a global cycle shared by every series, the
// round restarting with it) keep working: each series reads its own pass off
// them and the walk continues from the last row.
func TestInterleave_RestartReadsRowsOfTheGlobalCycleModel(t *testing.T) {
	eps := ilConcat(ilEps("a", 2), ilEps("b", 1))
	old := []seriesRow{
		{StartAt: 1, CatalogItemID: "a01", Cycle: 0, Round: 0},
		{StartAt: 2, CatalogItemID: "b01", Cycle: 0, Round: 0},
		{StartAt: 3, CatalogItemID: "a02", Cycle: 0, Round: 1},
		{StartAt: 4, CatalogItemID: "a01", Cycle: 1, Round: 0},
		{StartAt: 5, CatalogItemID: "b01", Cycle: 1, Round: 0},
	}
	got := ilDrain(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, old, nil, 0), 4)
	if want := []string{"a02", "b01", "a01", "b01"}; !equalStrings(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Switching a block from restart to drop mid-run: the per-series cycles the
// restart rows carry read as stale or newer global cycles, so the walk stays
// valid (it may start the rotation over once).
func TestInterleave_DropReadsRowsWrittenUnderRestart(t *testing.T) {
	eps := ilConcat(ilEps("a", 4), ilEps("b", 1))
	_, rows := drainRows(newSeriesRun(ilSpec(domain.Interleave{}, 1), "chan", eps, 0, nil, nil, 0), 6)
	drop := ilSpec(domain.Interleave{OnExhaustion: domain.OnExhaustionDrop}, 1)
	got := ilDrain(newSeriesRun(drop, "chan", eps, 0, rows, nil, 0), 10)
	if len(got) != 10 {
		t.Fatalf("walk stopped: %v", got)
	}
}
