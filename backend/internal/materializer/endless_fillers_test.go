package materializer

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const minMs = int64(time.Minute / time.Millisecond)

var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// endlessInput is a channel whose continuous item rotates through three
// one-hour movies, with filler pools pre (1 min), mid (2), post (3), tail (5)
// and fb (fallback, 1 min); only the rules named in kinds exist.
func endlessInput(kinds ...domain.FillerKind) channelInput {
	durations := map[string]int64{
		"m0": 60 * minMs, "m1": 60 * minMs, "m2": 60 * minMs, "news": 30 * minMs,
		"pre": 1 * minMs, "mid": 2 * minMs, "post": 3 * minMs, "tail": 5 * minMs, "fb": 1 * minMs,
	}
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{
			{ID: "cont", BlockID: "rot", Recurrence: domain.RecurrenceContinuous, Active: true},
		},
		BlockItems: map[string][]domain.BlockItem{
			"rot": {
				{ID: "b0", BlockID: "rot", Position: 0, CatalogItemID: "m0"},
				{ID: "b1", BlockID: "rot", Position: 1, CatalogItemID: "m1"},
				{ID: "b2", BlockID: "rot", Position: 2, CatalogItemID: "m2"},
			},
			"news": {{ID: "n0", BlockID: "news", CatalogItemID: "news"}},
		},
		CatalogDuration: durations,
		// A continuous item over a manual block wraps only when it repeats.
		RepeatBlockIDs: map[string]bool{"rot": true},
		PoolItems:      map[string][]domain.FillerPoolItem{},
	}
	for _, k := range kinds {
		pool := "pool-" + string(k)
		item := string(k)
		switch k {
		case domain.FillerPreRoll:
			item = "pre"
		case domain.FillerMidRoll:
			item = "mid"
		case domain.FillerPostRoll:
			item = "post"
		case domain.FillerTail:
			item = "tail"
		case domain.FillerFallback:
			item = "fb"
		}
		in.PoolItems[pool] = []domain.FillerPoolItem{{ID: "pi-" + item, FillerPoolID: pool, CatalogItemID: item, Weight: 1}}
		in.FillerRules = append(in.FillerRules, domain.FillerRule{
			ID: "rule-" + string(k), Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: k, FillerPoolID: pool, Selection: domain.SelectionSequential,
		})
	}
	return in
}

func runEndless(in channelInput, window time.Duration) []domain.EpgEntry {
	entries := expandChannel(in, t0, t0, t0.Add(window), newSelectionState())
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].StartAt < entries[j].StartAt })
	return entries
}

func seq(entries []domain.EpgEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.CatalogItemID
	}
	return strings.Join(parts, " ")
}

func assertTight(t *testing.T, entries []domain.EpgEntry) {
	t.Helper()
	for i := 1; i < len(entries); i++ {
		if entries[i-1].EndAt != entries[i].StartAt {
			t.Fatalf("gap or overlap between %s and %s: %s", entries[i-1].CatalogItemID, entries[i].CatalogItemID, seq(entries))
		}
	}
}

func TestEndless_ContinuousPreroll(t *testing.T) {
	entries := runEndless(endlessInput(domain.FillerPreRoll), 4*time.Hour)
	// Each program has its own preroll; the 4th unit (61 min) would end at
	// 4h04 and is not aired.
	if got, want := seq(entries), "pre m0 pre m1 pre m2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
	if entries[1].Kind != domain.EpgProgram || entries[0].Kind != domain.EpgPreRoll {
		t.Errorf("kinds wrong: %+v", entries[:2])
	}
}

func TestEndless_ContinuousMidrollBetweenPrograms(t *testing.T) {
	entries := runEndless(endlessInput(domain.FillerMidRoll), 5*time.Hour)
	// No midroll before the first program nor after the last that fits.
	if got, want := seq(entries), "m0 mid m1 mid m2 mid m0"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
}

func TestEndless_ContinuousPostroll(t *testing.T) {
	entries := runEndless(endlessInput(domain.FillerPostRoll), 4*time.Hour)
	if got, want := seq(entries), "m0 post m1 post m2 post"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
}

func TestEndless_ContinuousAllKindsOrder(t *testing.T) {
	entries := runEndless(endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll), 4*time.Hour)
	// unit 1: 1+60+3 = 64; then 2 (mid) + 1 + 60 + 3 = 66 each: 64+66+66 = 196 min,
	// the next would end at 262 > 240.
	if got, want := seq(entries), "pre m0 post mid pre m1 post mid pre m2 post"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
	for _, e := range entries {
		if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "cont" {
			t.Errorf("entry %s not attributed to the continuous item", e.CatalogItemID)
		}
	}
}

// The tail only covers what is left when whole programs no longer fit; the
// fallback only what the tail leaves.
func TestEndless_ContinuousTailThenFallback(t *testing.T) {
	entries := runEndless(endlessInput(domain.FillerTail, domain.FillerFallback), 3*time.Hour+32*time.Minute)
	// 3h of programs, 32 min left: six 5-minute tail items (30 min), the last
	// 2 minutes go to the 1-minute fallback.
	if got, want := seq(entries), "m0 m1 m2 tail tail tail tail tail tail fb fb"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
	if entries[3].Kind != domain.EpgTail || entries[9].Kind != domain.EpgFallback {
		t.Errorf("kinds wrong: %v / %v", entries[3].Kind, entries[9].Kind)
	}
}

// A timed item after the continuous one keeps its nominal start; the span
// before it is programs with fillers, then tail for what is left.
func TestEndless_ContinuousWithTimedItemAfter(t *testing.T) {
	in := endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll, domain.FillerTail)
	timedStart := t0.Add(2*time.Hour + 30*time.Minute)
	ms := timedStart.UnixMilli()
	in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{ID: "timed", BlockID: "news", Recurrence: domain.RecurrenceOnce, StartAt: &ms, Active: true})
	entries := runEndless(in, 4*time.Hour)

	var beforeTimed []domain.EpgEntry
	for _, e := range entries {
		if e.StartAt < ms {
			beforeTimed = append(beforeTimed, e)
			if e.EndAt > ms {
				t.Fatalf("%s crosses the timed item's start", e.CatalogItemID)
			}
		}
	}
	// 64 + 66 = 130 min of programs, 20 min left before the timed item: four
	// tail items.
	if got, want := seq(beforeTimed), "pre m0 post mid pre m1 post tail tail tail tail"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, beforeTimed)
	// The timed block's own occurrence starts at its nominal time (with the
	// channel's preroll, as for any manual block).
	var firstTimed *domain.EpgEntry
	for i := range entries {
		if entries[i].SourceScheduleItemID != nil && *entries[i].SourceScheduleItemID == "timed" {
			firstTimed = &entries[i]
			break
		}
	}
	if firstTimed == nil || firstTimed.StartAt != ms {
		t.Fatalf("timed occurrence = %+v, want it at its nominal start", firstTimed)
	}
}

// After a timed item the continuous item resumes where its rotation left off.
func TestEndless_ResumesRotationAfterTimedItem(t *testing.T) {
	in := endlessInput(domain.FillerMidRoll)
	ms := t0.Add(2*time.Hour + 30*time.Minute).UnixMilli()
	in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{ID: "timed", BlockID: "news", Recurrence: domain.RecurrenceOnce, StartAt: &ms, Active: true})
	entries := runEndless(in, 5*time.Hour)
	// m0 mid m1 (122 min), 28 min left unfilled, news at 2h30, then the rotation
	// goes on with m2 (no midroll right after the timed item).
	if got, want := seq(entries), "m0 mid m1 news m2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A dynamic block under a timed item gets the same per-program behavior.
func TestEndless_DynamicTimedBlockPerProgramFillers(t *testing.T) {
	in := endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll, domain.FillerTail)
	in.ScheduleItems = nil
	start := t0.Add(10 * time.Minute)
	next := t0.Add(10*time.Minute + 3*time.Hour)
	s, n := start.UnixMilli(), next.UnixMilli()
	in.ScheduleItems = []domain.ScheduleItem{
		{ID: "dyn", BlockID: "rot", Recurrence: domain.RecurrenceOnce, StartAt: &s, Active: true},
		{ID: "after", BlockID: "news", Recurrence: domain.RecurrenceOnce, StartAt: &n, Active: true},
	}
	in.DynamicBlockIDs = map[string]bool{"rot": true}
	entries := runEndless(in, 6*time.Hour)
	var dyn []domain.EpgEntry
	for _, e := range entries {
		if e.SourceScheduleItemID != nil && *e.SourceScheduleItemID == "dyn" {
			dyn = append(dyn, e)
		}
	}
	// 64 + 66 + 66 = 196 > 180: two programs fit (130 min), 50 left for the tail.
	if got, want := seq(dyn), "pre m0 post mid pre m1 post tail tail tail tail tail tail tail tail tail tail"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, dyn)
	if dyn[len(dyn)-1].EndAt != n {
		t.Errorf("the span does not end exactly at the next occurrence")
	}
	// Preroll is no longer a single filler at the top of the occurrence.
	pre := 0
	for _, e := range dyn {
		if e.Kind == domain.EpgPreRoll {
			pre++
		}
	}
	if pre != 2 {
		t.Errorf("%d prerolls, want one per program (2)", pre)
	}
}

// No filler rules: both endless shapes behave as before (programs back to back).
func TestEndless_NoRulesUnchanged(t *testing.T) {
	entries := runEndless(endlessInput(), 3*time.Hour)
	if got, want := seq(entries), "m0 m1 m2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A continuous item over a block set to play once keeps its fillers per
// program and stops after the last program (no midroll after it).
func TestEndless_PlayOnceBlockWithFillers(t *testing.T) {
	in := endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll)
	in.RepeatBlockIDs = nil
	entries := runEndless(in, 6*time.Hour)
	if got, want := seq(entries), "pre m0 post mid pre m1 post mid pre m2 post"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A block-scoped rule wins over the channel's for the continuous item's block.
func TestEndless_BlockScopedRuleWins(t *testing.T) {
	in := endlessInput(domain.FillerPreRoll)
	in.CatalogDuration["blockpre"] = 2 * minMs
	in.PoolItems["pool-block"] = []domain.FillerPoolItem{{ID: "pi", FillerPoolID: "pool-block", CatalogItemID: "blockpre"}}
	in.FillerRules = append(in.FillerRules, domain.FillerRule{ID: "r-block", Scope: domain.ScopeBlock, ScopeID: "rot", Kind: domain.FillerPreRoll, FillerPoolID: "pool-block", Selection: domain.SelectionSequential})
	entries := runEndless(in, 3*time.Hour)
	if got, want := seq(entries), "blockpre m0 blockpre m1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Fillers and programs of near-zero length cannot spin forever: the fill is
// capped and returns promptly.
func TestEndless_ZeroProgressGuards(t *testing.T) {
	in := endlessInput(domain.FillerTail)
	in.BlockItems["rot"] = []domain.BlockItem{{ID: "b0", BlockID: "rot", CatalogItemID: "m0"}}
	in.CatalogDuration["m0"] = 50 * minMs
	in.CatalogDuration["tail"] = 1 // 1 ms
	began := time.Now()
	entries := runEndless(in, 1*time.Hour)
	if time.Since(began) > 10*time.Second {
		t.Fatalf("took %v", time.Since(began))
	}
	if len(entries) != 1+maxLoopEntries {
		t.Fatalf("got %d entries, want the program plus the capped tail (%d)", len(entries), 1+maxLoopEntries)
	}

	// A zero-length program airs nothing instead of looping.
	in = endlessInput(domain.FillerPreRoll)
	in.CatalogDuration["m0"], in.CatalogDuration["m1"], in.CatalogDuration["m2"] = 0, 0, 0
	if got := runEndless(in, 2*time.Hour); len(got) != 0 {
		t.Fatalf("zero-duration programs aired: %s", seq(got))
	}

	// A one-millisecond program is capped too.
	in = endlessInput()
	in.CatalogDuration["m0"], in.CatalogDuration["m1"], in.CatalogDuration["m2"] = 1, 1, 1
	began = time.Now()
	got := runEndless(in, 2*time.Hour)
	if time.Since(began) > 10*time.Second || len(got) > maxLoopEntries {
		t.Fatalf("took %v with %d entries", time.Since(began), len(got))
	}
}

func TestEndless_FillerRatioWarning(t *testing.T) {
	in := endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll)
	// 6 minutes of fillers against one-hour programs: fine.
	if w := in.fillerRatioWarnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %+v", w)
	}
	// Against a 10-minute program the 6 minutes exceed 50%.
	in.CatalogDuration["m1"] = 10 * minMs
	w := in.fillerRatioWarnings()
	if len(w) != 1 || w[0].Kind != WarningFillerRatio || w[0].BlockID != "rot" {
		t.Fatalf("warnings = %+v", w)
	}
	// A manual block under a timed item is not endless and gets none.
	s := t0.UnixMilli()
	in.ScheduleItems = []domain.ScheduleItem{{ID: "timed", BlockID: "rot", Recurrence: domain.RecurrenceOnce, StartAt: &s, Active: true}}
	if w := in.fillerRatioWarnings(); len(w) != 0 {
		t.Fatalf("manual timed block warned: %+v", w)
	}
	// ...but a dynamic one is.
	in.DynamicBlockIDs = map[string]bool{"rot": true}
	if w := in.fillerRatioWarnings(); len(w) != 1 {
		t.Fatalf("dynamic timed block: %+v", w)
	}
}

// End to end through the loader and the kept head: with every filler kind on
// a continuous channel, regenerating with any freeze margin never changes or
// tears the grid, and the stage a head ends in is resumed exactly.
func TestEndless_ContinuousRegenerationIsStable(t *testing.T) {
	for _, freeze := range []time.Duration{20 * time.Minute, 62 * time.Minute, 65 * time.Minute, 67 * time.Minute, 95 * time.Minute, 2 * time.Hour} {
		t.Run(freeze.String(), func(t *testing.T) {
			f := continuousSetup(t)
			ctx := context.Background()
			for id, d := range map[string]int64{"pre": 1 * minMs, "mid": 2 * minMs, "post": 3 * minMs, "tail": 5 * minMs} {
				f.createCatalogItem(t, id, d)
				f.createFillerPool(t, id+"pool")
				f.addFillerPoolItem(t, id+"pi", id+"pool", id)
			}
			f.createFillerRule(t, "r1", "channel", "chan1", "preroll", "prepool", "sequential")
			f.createFillerRule(t, "r2", "channel", "chan1", "midroll", "midpool", "sequential")
			f.createFillerRule(t, "r3", "channel", "chan1", "postroll", "postpool", "sequential")
			f.createFillerRule(t, "r4", "channel", "chan1", "tail", "tailpool", "sequential")

			mat := New(f.db, 6*time.Hour)
			mat.SetFreezeMargin(freeze)
			if err := mat.Run(ctx); err != nil {
				t.Fatal(err)
			}
			first := queryEntries(t, f)
			want := seq(first)
			if !strings.HasPrefix(want, "pre m0 post mid pre m1 post mid pre m2 post mid pre m0 post") {
				t.Fatalf("unexpected layout: %s", want)
			}
			for run := 0; run < 3; run++ {
				if err := mat.Run(ctx); err != nil {
					t.Fatal(err)
				}
				got := queryEntries(t, f)
				// Only the tail of the window may differ (it shifts with "now").
				gotSeq, wantSeq := seq(trimTail(got)), seq(trimTail(first))
				if gotSeq != wantSeq {
					t.Fatalf("run %d drifted:\n%s\nvs\n%s", run+2, gotSeq, wantSeq)
				}
				assertContiguous(t, got)
			}
		})
	}
}

// trimTail drops the trailing tail fillers (the leftover at the window's end,
// which legitimately moves as the window rolls forward).
func trimTail(entries []domain.EpgEntry) []domain.EpgEntry {
	end := len(entries)
	for end > 0 && entries[end-1].Kind == domain.EpgTail {
		end--
	}
	return entries[:end]
}
