package materializer

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// All the expansion tests below run on Monday 2026-09-28 (UTC) with "now" at
// 00:00, so an hour h of the day is the instant day0 + h.
var baseDay0 = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

func blAt(h, m int) int64 {
	return baseDay0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).UnixMilli()
}

func blMs(d time.Duration) int64 { return int64(d / time.Millisecond) }

// baseLayerInput is a channel whose base schedule is a continuous item over
// the repeating block "base" (items b0, b1, b2 of itemDur each) and whose
// pinned schedule is one "once" item per pinnedAt (each airing the single
// item "p0" of pinnedDur).
func baseLayerInput(itemDur, pinnedDur time.Duration, pinnedAt ...int64) channelInput {
	var pinned []domain.ScheduleItem
	for i, start := range pinnedAt {
		pinned = append(pinned, domain.ScheduleItem{
			ID: fmt.Sprintf("pin%d", i), ScheduleID: "pinned", BlockID: "p",
			Recurrence: domain.RecurrenceOnce, StartAt: int64Ptr(start), Active: true,
		})
	}
	return channelInput{
		ChannelID: "chan1", TenantID: "tenant1", Timezone: "UTC",
		ScheduleItems: pinned,
		HasBase:       true,
		BaseScheduleItems: []domain.ScheduleItem{{
			ID: "base-cont", ScheduleID: "base", BlockID: "base",
			Recurrence: domain.RecurrenceContinuous, Active: true,
		}},
		BlockItems: map[string][]domain.BlockItem{
			"p": {{ID: "pbi", BlockID: "p", CatalogItemID: "p0"}},
			"base": {
				{ID: "bb0", BlockID: "base", Position: 0, CatalogItemID: "b0"},
				{ID: "bb1", BlockID: "base", Position: 1, CatalogItemID: "b1"},
				{ID: "bb2", BlockID: "base", Position: 2, CatalogItemID: "b2"},
			},
		},
		RepeatBlockIDs: map[string]bool{"base": true},
		CatalogDuration: map[string]int64{
			"p0": blMs(pinnedDur), "b0": blMs(itemDur), "b1": blMs(itemDur), "b2": blMs(itemDur),
		},
	}
}

func blExpand(in channelInput, windowEnd time.Time) []domain.EpgEntry {
	out := expandChannel(in, baseDay0, baseDay0, windowEnd, newSelectionState())
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt < out[j].StartAt })
	return out
}

// shape renders an entry as "HH:MM-HH:MM kind item[+offset]" for comparisons.
func blShape(e domain.EpgEntry) string {
	f := func(ms int64) string { return time.UnixMilli(ms).UTC().Format("15:04:05") }
	s := fmt.Sprintf("%s-%s %s %s", f(e.StartAt), f(e.EndAt), e.Kind, e.CatalogItemID)
	if e.ItemOffsetMs > 0 {
		s += fmt.Sprintf("+%s", time.Duration(e.ItemOffsetMs)*time.Millisecond)
	}
	return s
}

func blShapes(entries []domain.EpgEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = blShape(e)
	}
	return out
}

func blAssertShapes(t *testing.T, got []domain.EpgEntry, want []string) {
	t.Helper()
	g := blShapes(got)
	if len(g) != len(want) {
		t.Fatalf("got %d entries, want %d\ngot:  %v\nwant: %v", len(g), len(want), g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q\ngot:  %v\nwant: %v", i, g[i], want[i], g, want)
		}
	}
}

// Without a base schedule nothing changes, even when base items happen to be
// present on the input: HasBase alone turns the layer on.
func TestBaseLayer_NoBaseIsUnchanged(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(2, 0))
	in.HasBase = false
	in.BaseScheduleItems = nil
	in.FillerRules = []domain.FillerRule{
		{ID: "fb", Kind: domain.FillerFallback, Scope: domain.ScopeChannel, ScopeID: "chan1", FillerPoolID: "pool", Selection: domain.SelectionSequential},
	}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "pi", FillerPoolID: "pool", CatalogItemID: "f0"}}}
	in.CatalogDuration["f0"] = blMs(30 * time.Minute)

	got := blExpand(in, baseDay0.Add(4*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-00:30:00 fallback f0",
		"00:30:00-01:00:00 fallback f0",
		"01:00:00-01:30:00 fallback f0",
		"01:30:00-02:00:00 fallback f0",
		"02:00:00-03:00:00 program p0",
		"03:00:00-03:30:00 fallback f0",
		"03:30:00-04:00:00 fallback f0",
	})
}

// The base fills the gaps between two pinned occurrences, and keeps the
// position it would have had anyway: hour h airs b[h mod 3] whatever the
// pinned layer covered before.
func TestBaseLayer_FillsGapBetweenPinnedOccurrences(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(2, 0), blAt(6, 0))
	got := blExpand(in, baseDay0.Add(10*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 program b0",
		"01:00:00-02:00:00 program b1",
		"02:00:00-03:00:00 program p0",
		"03:00:00-04:00:00 program b0",
		"04:00:00-05:00:00 program b1",
		"05:00:00-06:00:00 program b2",
		"06:00:00-07:00:00 program p0",
		"07:00:00-08:00:00 program b1", // not b0: the base ran underneath
		"08:00:00-09:00:00 program b2",
		"09:00:00-10:00:00 program b0",
	})
	for _, e := range got {
		wantSrc := "base-cont"
		if e.CatalogItemID == "p0" {
			wantSrc = "pin0"
			if e.StartAt == blAt(6, 0) {
				wantSrc = "pin1"
			}
		}
		if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != wantSrc {
			t.Errorf("%s: source = %v, want %s", blShape(e), e.SourceScheduleItemID, wantSrc)
		}
	}
}

// A pinned occurrence interrupting a base program: the base program is cut
// where the pinned one starts (which never starts late), and the base resumes
// inside the program it would be in by then.
func TestBaseLayer_PinnedInterruptsBaseProgramMidWay(t *testing.T) {
	// 90 minute base programs: b0 00:00-01:30, b1 01:30-03:00, b2 03:00-04:30...
	in := baseLayerInput(90*time.Minute, time.Hour, blAt(1, 0))
	got := blExpand(in, baseDay0.Add(6*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 program b0", // cut at the pinned start
		"01:00:00-02:00:00 program p0",
		"02:00:00-03:00:00 program b1+30m0s", // b1 began 01:30, resumed 30 min in
		"03:00:00-04:30:00 program b2",
		"04:30:00-06:00:00 program b0",
	})
	if got[0].StartAt != blAt(0, 0) || got[1].StartAt != blAt(1, 0) {
		t.Errorf("the pinned occurrence did not start on time: %v", blShapes(got))
	}
}

// A gap smaller than one base program shows a slice of the program in
// progress, cut at both ends.
func TestBaseLayer_GapSmallerThanBaseProgram(t *testing.T) {
	in := baseLayerInput(time.Hour, 30*time.Minute, blAt(1, 0), blAt(1, 50))
	got := blExpand(in, baseDay0.Add(3*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 program b0",
		"01:00:00-01:30:00 program p0",
		"01:30:00-01:50:00 program b1+30m0s",
		"01:50:00-02:20:00 program p0",
		"02:20:00-03:00:00 program b2+20m0s",
	})
}

// What is left of a program after the pinned occurrence is not worth a cut
// when it is shorter than a minute: the fallback filler takes it, and the
// base goes on with its next program on time.
func TestBaseLayer_ShortRemainderGoesToFiller(t *testing.T) {
	in := baseLayerInput(time.Hour, 59*time.Minute+30*time.Second, blAt(1, 0))
	in.FillerRules = []domain.FillerRule{
		{ID: "fb", Kind: domain.FillerFallback, Scope: domain.ScopeChannel, ScopeID: "chan1", FillerPoolID: "pool", Selection: domain.SelectionSequential},
	}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "pi", FillerPoolID: "pool", CatalogItemID: "f0"}}}
	in.CatalogDuration["f0"] = blMs(20 * time.Second)

	got := blExpand(in, baseDay0.Add(3*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 program b0",
		"01:00:00-01:59:30 program p0",
		"01:59:30-01:59:50 fallback f0",
		"02:00:00-03:00:00 program b2",
	})
}

// The base's own preroll/postroll chain surrounds every program it airs in a
// gap, at the position it would have had anyway.
func TestBaseLayer_OwnFillerChain(t *testing.T) {
	// A unit is preroll 5 + program 50 + postroll 5: one per hour, b[h mod 3].
	in := baseLayerInput(50*time.Minute, time.Hour, blAt(2, 0))
	in.FillerRules = []domain.FillerRule{
		{ID: "pre", Kind: domain.FillerPreRoll, Scope: domain.ScopeBlock, ScopeID: "base", FillerPoolID: "pre", Selection: domain.SelectionSequential},
		{ID: "post", Kind: domain.FillerPostRoll, Scope: domain.ScopeBlock, ScopeID: "base", FillerPoolID: "post", Selection: domain.SelectionSequential},
	}
	in.PoolItems = map[string][]domain.FillerPoolItem{
		"pre":  {{ID: "pi1", FillerPoolID: "pre", CatalogItemID: "pre0"}},
		"post": {{ID: "pi2", FillerPoolID: "post", CatalogItemID: "post0"}},
	}
	in.CatalogDuration["pre0"] = blMs(5 * time.Minute)
	in.CatalogDuration["post0"] = blMs(5 * time.Minute)

	got := blExpand(in, baseDay0.Add(5*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-00:05:00 preroll pre0",
		"00:05:00-00:55:00 program b0",
		"00:55:00-01:00:00 postroll post0",
		"01:00:00-01:05:00 preroll pre0",
		"01:05:00-01:55:00 program b1",
		"01:55:00-02:00:00 postroll post0",
		"02:00:00-03:00:00 program p0",
		"03:00:00-03:05:00 preroll pre0",
		"03:05:00-03:55:00 program b0",
		"03:55:00-04:00:00 postroll post0",
		"04:00:00-04:05:00 preroll pre0",
		"04:05:00-04:55:00 program b1",
		"04:55:00-05:00:00 postroll post0",
	})
	// Every entry of the base keeps the base item as its source.
	for _, e := range got {
		if e.CatalogItemID == "p0" {
			continue
		}
		if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "base-cont" {
			t.Errorf("%s: source = %v, want base-cont", blShape(e), e.SourceScheduleItemID)
		}
	}
}

// A base schedule that is timed (no continuous item) airs only at its own
// times, and the channel fallback fills what neither layer covers.
func TestBaseLayer_TimedBaseAndFallbackForTheRest(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(4, 0))
	in.BaseScheduleItems = []domain.ScheduleItem{{
		ID: "base-once", ScheduleID: "base", BlockID: "base",
		Recurrence: domain.RecurrenceOnce, StartAt: int64Ptr(blAt(1, 0)), Active: true,
	}}
	in.RepeatBlockIDs = nil // the base block plays its list once
	in.FillerRules = []domain.FillerRule{
		{ID: "fb", Kind: domain.FillerFallback, Scope: domain.ScopeChannel, ScopeID: "chan1", FillerPoolID: "pool", Selection: domain.SelectionSequential},
	}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "pi", FillerPoolID: "pool", CatalogItemID: "f0"}}}
	in.CatalogDuration["f0"] = blMs(time.Hour)

	got := blExpand(in, baseDay0.Add(6*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 fallback f0",
		"01:00:00-02:00:00 program b0",
		"02:00:00-03:00:00 program b1",
		"03:00:00-04:00:00 program b2",
		"04:00:00-05:00:00 program p0",
		"05:00:00-06:00:00 fallback f0",
	})
}

// Regenerating the full window from the same state gives the same grid, and a
// pinned schedule with no occurrence at all leaves the whole window to the
// base.
func TestBaseLayer_DeterministicAndEmptyPinned(t *testing.T) {
	in := baseLayerInput(90*time.Minute, time.Hour, blAt(1, 0), blAt(5, 0))
	end := baseDay0.Add(8 * time.Hour)
	first := blShapes(blExpand(in, end))
	for i := 0; i < 3; i++ {
		if again := blShapes(blExpand(in, end)); fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatalf("regeneration %d differs\nfirst: %v\nagain: %v", i, first, again)
		}
	}

	empty := baseLayerInput(time.Hour, time.Hour)
	got := blExpand(empty, baseDay0.Add(4*time.Hour))
	blAssertShapes(t, got, []string{
		"00:00:00-01:00:00 program b0",
		"01:00:00-02:00:00 program b1",
		"02:00:00-03:00:00 program b2",
		"03:00:00-04:00:00 program b0",
	})
}

// With a base schedule the pinned schedule's continuous item does not air in
// the gaps: the base owns them.
func TestBaseLayer_PinnedContinuousDoesNotAir(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(2, 0))
	in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{
		ID: "pin-cont", ScheduleID: "pinned", BlockID: "pc",
		Recurrence: domain.RecurrenceContinuous, Active: true,
	})
	in.BlockItems["pc"] = []domain.BlockItem{{ID: "pcbi", BlockID: "pc", CatalogItemID: "pc0"}}
	in.CatalogDuration["pc0"] = blMs(time.Hour)
	in.RepeatBlockIDs["pc"] = true

	for _, e := range blExpand(in, baseDay0.Add(5*time.Hour)) {
		if e.CatalogItemID == "pc0" {
			t.Fatalf("the pinned continuous item aired under a base schedule: %s", blShape(e))
		}
	}
}

func blResiduals(in channelInput) []residual {
	var found []residual
	in.Residuals = &found
	blExpand(in, baseDay0.Add(24*time.Hour))
	return found
}

// A slot's leftover that the base layer airs is not an unfilled slot, even
// with no channel fallback rule.
func TestBaseLayer_CoveredSlotLeftoverIsNotAResidual(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(4, 0), blAt(8, 0), blAt(12, 0))
	if got := blResiduals(in); len(got) != 0 {
		t.Fatalf("base covers the gaps, got residuals %+v", got)
	}
	// The same channel without the base layer still reports them.
	in.HasBase = false
	in.BaseScheduleItems = nil
	if got := blResiduals(in); len(got) == 0 {
		t.Fatalf("no base: want residuals for the unfilled slots")
	}
}

// What the base layer itself leaves empty is still reported, and a channel
// fallback rule silences it.
func TestBaseLayer_BaseHoleIsStillAResidual(t *testing.T) {
	in := baseLayerInput(time.Hour, time.Hour, blAt(4, 0), blAt(8, 0), blAt(12, 0))
	in.BaseScheduleItems = []domain.ScheduleItem{{
		ID: "base-once", ScheduleID: "base", BlockID: "base",
		Recurrence: domain.RecurrenceOnce, StartAt: int64Ptr(blAt(1, 0)), Active: true,
	}}
	in.RepeatBlockIDs = nil
	got := blResiduals(in)
	if len(got) == 0 {
		t.Fatalf("base leaves holes before the next pinned item: want a residual")
	}
	for _, r := range got {
		if r.BlockID != "p" {
			t.Errorf("residual %+v: want block p", r)
		}
	}
	in.FillerRules = []domain.FillerRule{
		{ID: "fb", Kind: domain.FillerFallback, Scope: domain.ScopeChannel, ScopeID: "chan1", FillerPoolID: "pool", Selection: domain.SelectionSequential},
	}
	in.PoolItems = map[string][]domain.FillerPoolItem{"pool": {{ID: "pi", FillerPoolID: "pool", CatalogItemID: "f0"}}}
	in.CatalogDuration["f0"] = blMs(time.Hour)
	if got := blResiduals(in); len(got) != 0 {
		t.Fatalf("with a fallback rule: %+v, want none", got)
	}
}

// End to end through the loader and the database: a channel with a pinned
// weekly item and a base schedule with a continuous item. The base airs around
// the pinned occurrence, resumes at its natural position, and a second full
// regeneration produces the same grid.
func TestMaterializer_BaseScheduleEndToEnd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	min := int64(60 * 1000)
	for _, id := range []string{"m0", "m1", "m2"} {
		f.createCatalogItem(t, id, 2*min)
	}
	f.createCatalogItem(t, "timed", 5*min)
	f.createChannel(t, "chan1", "UTC")
	f.createDynamicBlock(t, "rot")
	for i, id := range []string{"m0", "m1", "m2"} {
		f.addBlockItem(t, "bi"+id, "rot", i, id)
	}
	f.createBlock(t, "timedblock")
	f.addBlockItem(t, "bt", "timedblock", 0, "timed")

	f.createSchedule(t, "pinned")
	f.assignChannelSchedule(t, "chan1", "pinned")
	dow, hhmm, occ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "wk", "pinned", "timedblock", dow, hhmm)

	f.createSchedule(t, "base")
	f.createContinuousScheduleItem(t, "cont", "base", "rot")
	if err := f.db.Exec(ctx, "UPDATE channel SET base_schedule_id = 'base' WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}

	mat := New(f.db, 30*time.Minute)
	check := func() []domain.EpgEntry {
		t.Helper()
		if err := mat.Run(ctx); err != nil {
			t.Fatalf("Run: %v", err)
		}
		entries := queryEntries(t, f)
		var origin int64 = -1
		timed := 0
		for _, e := range entries {
			if e.CatalogItemID == "timed" {
				timed++
				if e.StartAt != occ.UnixMilli() {
					t.Errorf("pinned occurrence at %d, want %d", e.StartAt, occ.UnixMilli())
				}
				continue
			}
			if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "cont" {
				t.Errorf("unexpected entry %+v", e)
				continue
			}
			if e.StartAt >= occ.UnixMilli() && e.StartAt < occ.UnixMilli()+5*min {
				t.Errorf("base entry %s overlaps the pinned occurrence", blShape(e))
			}
			// Natural position: 2 minute programs back to back from the first
			// base start, one per slot whatever the pinned layer covered.
			if origin < 0 {
				origin = e.StartAt - e.ItemOffsetMs
			}
			slot := (e.StartAt - e.ItemOffsetMs - origin) / (2 * min)
			if want := fmt.Sprintf("m%d", slot%3); e.CatalogItemID != want {
				t.Errorf("base entry %s at slot %d, want %s", blShape(e), slot, want)
			}
		}
		if timed != 1 {
			t.Fatalf("pinned entries = %d, want 1: %v", timed, blShapes(entries))
		}
		if len(entries) < 5 {
			t.Fatalf("too few entries: %v", blShapes(entries))
		}
		return entries
	}
	first := check()
	var after int
	for _, e := range first {
		if e.StartAt >= occ.UnixMilli()+5*min {
			after++
		}
	}
	if after == 0 {
		t.Fatalf("the base did not continue after the pinned occurrence: %v", blShapes(first))
	}
	second := check()
	if fmt.Sprint(blShapes(first)) != fmt.Sprint(blShapes(second)) {
		t.Errorf("a second full regeneration changed the grid\nfirst:  %v\nsecond: %v", blShapes(first), blShapes(second))
	}
}
