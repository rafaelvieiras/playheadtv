package materializer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// segs renders entries as "kind:item@offset" so a test reads the whole layout.
func segs(entries []domain.EpgEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = fmt.Sprintf("%s:%s@%d", e.Kind, e.CatalogItemID, e.ItemOffsetMs/minMs)
	}
	return strings.Join(parts, " ")
}

func TestNormalizeCuts(t *testing.T) {
	dur := 60 * minMs
	cases := []struct {
		name string
		in   []int64
		want []int64
	}{
		{"none", nil, nil},
		{"sorted and deduplicated", []int64{40 * minMs, 20 * minMs, 20 * minMs}, []int64{20 * minMs, 40 * minMs}},
		{"outside the item", []int64{-5, 0, dur, dur + minMs}, nil},
		{"too close to the ends", []int64{minSegmentMs - 1, dur - minSegmentMs + 1}, nil},
		{"too close to a neighbour", []int64{20 * minMs, 20*minMs + 10}, []int64{20 * minMs}},
	}
	for _, c := range cases {
		got := normalizeCuts(c.in, dur)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	many := make([]int64, 0, 100)
	for i := int64(1); i < 100; i++ {
		many = append(many, i*5000)
	}
	if got := normalizeCuts(many, 1000*5000); len(got) != maxCutsPerItem {
		t.Errorf("got %d cuts, want the cap of %d", len(got), maxCutsPerItem)
	}
}

func TestCuts_DefaultPlannerChangesNothing(t *testing.T) {
	base := runEndless(endlessInput(domain.FillerMidRoll), 5*time.Hour)
	with := endlessInput(domain.FillerMidRoll)
	with.CutPlanner = NoCuts()
	if got, want := segs(runEndless(with, 5*time.Hour)), segs(base); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	for _, e := range base {
		if e.ItemOffsetMs != 0 || e.SplitGroup != nil {
			t.Fatalf("entry %s carries segment data without a planner", e.CatalogItemID)
		}
	}
}

func TestCuts_ContinuousItemSplitsWithMidrollBetweenSegments(t *testing.T) {
	in := endlessInput(domain.FillerMidRoll)
	in.CutPlanner = FixedCuts(20*minMs, 40*minMs)
	entries := runEndless(in, 3*time.Hour)
	// m0 is 60 min: three 20 min segments and two 2 min breaks (64 min), then
	// the break between programs, then m1.
	want := "program:m0@0 midroll:mid@0 program:m0@20 midroll:mid@0 program:m0@40 midroll:mid@0 program:m1@0 midroll:mid@0 program:m1@20 midroll:mid@0 program:m1@40"
	if got := segs(entries); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	assertTight(t, entries)
	for i, e := range entries {
		if e.Kind == domain.EpgProgram {
			if e.EndAt-e.StartAt != 20*minMs {
				t.Errorf("segment %d lasts %d ms", i, e.EndAt-e.StartAt)
			}
			if e.SplitGroup == nil {
				t.Errorf("segment %d has no split group", i)
			}
		} else if e.SplitGroup != nil {
			t.Errorf("break %d carries a split group", i)
		}
	}
	// One group per airing, shared by its three segments, never by two airings.
	groups := map[string]int{}
	for _, e := range entries {
		if e.SplitGroup != nil {
			groups[*e.SplitGroup]++
		}
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2: %v", len(groups), groups)
	}
	for g, n := range groups {
		if n != 3 {
			t.Errorf("group %s has %d segments, want 3", g, n)
		}
	}
	if *entries[0].SplitGroup != entries[0].ID {
		t.Errorf("the group is the first segment's id")
	}
}

func TestCuts_NoMidrollMeansNoCut(t *testing.T) {
	in := endlessInput()
	in.CutPlanner = FixedCuts(20 * minMs)
	entries := runEndless(in, 3*time.Hour)
	if got, want := seq(entries), "m0 m1 m2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCuts_SpanTakenFromTheSlotAndUnitMustFit(t *testing.T) {
	in := endlessInput(domain.FillerMidRoll)
	in.CutPlanner = FixedCuts(30 * minMs)
	// m0 plus its inner break is 62 minutes: it does not fit in 61.
	if got := seq(runEndless(in, 61*time.Minute)); got != "" {
		t.Fatalf("got %q, want nothing", got)
	}
	if got := seq(runEndless(in, 62*time.Minute)); got != "m0 mid m0" {
		t.Fatalf("got %q", got)
	}
}

func TestCuts_ManualBlockSplitAndOverrunPolicy(t *testing.T) {
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		BlockItems:      map[string][]domain.BlockItem{"blk": {{ID: "b0", BlockID: "blk", CatalogItemID: "x"}, {ID: "b1", BlockID: "blk", CatalogItemID: "y"}}},
		CatalogDuration: map[string]int64{"x": 20 * minMs, "y": 20 * minMs, "mid": 2 * minMs},
		PoolItems:       map[string][]domain.FillerPoolItem{"mp": {{ID: "pm", FillerPoolID: "mp", CatalogItemID: "mid", Weight: 1}}},
		RepeatBlockIDs:  map[string]bool{},
		FillerRules: []domain.FillerRule{{
			ID: "r", Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: domain.FillerMidRoll, FillerPoolID: "mp", Selection: domain.SelectionSequential,
		}},
		CutPlanner: FixedCuts(10 * minMs),
	}
	for _, s := range []int64{0, 50} {
		in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{
			ID: fmt.Sprintf("s%d", s), BlockID: "blk", Recurrence: domain.RecurrenceOnce,
			StartAt: int64Ptr(t0.Add(time.Duration(s) * time.Minute).UnixMilli()), Active: true,
		})
	}
	got := expandChannel(in, t0, t0, t0.Add(3*time.Hour), newSelectionState())
	var first []domain.EpgEntry
	for _, e := range got {
		if e.StartAt < t0.Add(50*time.Minute).UnixMilli() {
			first = append(first, e)
		}
	}
	// x (10 + 2 + 10), the break between items (2), y (10 + 2 + 10): 46 min.
	want := "program:x@0 midroll:mid@0 program:x@10 midroll:mid@0 program:y@0 midroll:mid@0 program:y@10"
	if got := segs(first); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	assertTight(t, first)
	// A 40 minute slot holds x and its inner break (22) but not y (24 more).
	in.ScheduleItems[1].StartAt = int64Ptr(t0.Add(40 * time.Minute).UnixMilli())
	got = expandChannel(in, t0, t0, t0.Add(3*time.Hour), newSelectionState())
	var tight []domain.EpgEntry
	for _, e := range got {
		if e.StartAt < t0.Add(40*time.Minute).UnixMilli() {
			tight = append(tight, e)
			if e.EndAt > t0.Add(40*time.Minute).UnixMilli() {
				t.Fatalf("%s overran the next slot", e.CatalogItemID)
			}
		}
	}
	if got, want := segs(tight), "program:x@0 midroll:mid@0 program:x@10 midroll:mid@0"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestCuts_DeterministicRegeneration(t *testing.T) {
	in := endlessInput(domain.FillerMidRoll)
	in.CutPlanner = FixedCuts(15*minMs, 45*minMs)
	if a, b := segs(runEndless(in, 6*time.Hour)), segs(runEndless(in, 6*time.Hour)); a != b {
		t.Fatalf("two regenerations differ:\n%s\n%s", a, b)
	}
}

func TestCuts_EntryCapStaysBounded(t *testing.T) {
	in := endlessInput(domain.FillerMidRoll)
	in.CatalogDuration["m0"] = 100 * 1000 * 20
	in.CutPlanner = FixedCuts(func() []int64 {
		var c []int64
		for i := int64(1); i <= 200; i++ {
			c = append(c, i*10000)
		}
		return c
	}()...)
	entries := runEndless(in, 20*24*time.Hour)
	if len(entries) > maxLoopEntries+maxCutsPerItem*4 {
		t.Fatalf("%d entries exceed the cap", len(entries))
	}
}

func TestCuts_ResumeAfterKeptHead(t *testing.T) {
	f := continuousSetup(t)
	f.createCatalogItem(t, "mid", 3*60*1000)
	f.createFillerPool(t, "midpool")
	f.addFillerPoolItem(t, "midpi", "midpool", "mid")
	f.createFillerRule(t, "midrule", "channel", "chan1", "midroll", "midpool", "sequential")
	ctx := context.Background()
	newMat := func() *Materializer {
		mat := New(f.db, 4*time.Hour)
		mat.SetFreezeMargin(30 * time.Minute)
		mat.SetCutPlanner(FixedCuts(20*minMs, 40*minMs))
		return mat
	}
	if err := newMat().Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := queryEntries(t, f)
	// m0: three 20 min segments and two 3 min breaks (66 min). The freeze
	// margin (30 min) falls inside it: the second segment starts at minute 23
	// and the second inner break at 43.
	if len(first) < 8 {
		t.Fatalf("entries: %s", segs(first))
	}
	if err := newMat().Run(ctx); err != nil {
		t.Fatal(err)
	}
	second := queryEntries(t, f)
	assertTight(t, second)
	// The whole first airing of m0, third segment included, was kept as it was.
	for i := 0; i < 5; i++ {
		if second[i].ID != first[i].ID {
			t.Fatalf("entry %d (%s) was regenerated inside a kept split item", i, segs(second[i:i+1]))
		}
	}
	if got, want := segs(second[:8]), "program:m0@0 midroll:mid@0 program:m0@20 midroll:mid@0 program:m0@40 midroll:mid@0 program:m1@0 midroll:mid@0"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// No airing is left without its first segment, and offsets always grow
	// inside a group.
	var prevGroup string
	var prevOffset int64
	for _, e := range second {
		if e.Kind != domain.EpgProgram {
			continue
		}
		if e.SplitGroup != nil && *e.SplitGroup == prevGroup && e.ItemOffsetMs <= prevOffset {
			t.Fatalf("offset does not grow inside a group: %s", segs(second))
		}
		if e.SplitGroup != nil {
			prevGroup, prevOffset = *e.SplitGroup, e.ItemOffsetMs
		}
	}
}

// recordingCuts remembers what the planner was asked about.
type recordingCuts struct{ asked []CutItem }

func (r *recordingCuts) PlanCuts(item CutItem) []int64 {
	r.asked = append(r.asked, item)
	return nil
}

func TestCuts_PlannerIsToldWhereTheItemAirs(t *testing.T) {
	rec := &recordingCuts{}
	in := endlessInput(domain.FillerMidRoll)
	in.CutPlanner = rec
	runEndless(in, 3*time.Hour)
	if len(rec.asked) == 0 {
		t.Fatal("the planner was never asked")
	}
	first := rec.asked[0]
	if first.CatalogItemID != "m0" || first.DurationMs != 60*minMs || first.ChannelID != "chan1" || first.BlockID != "rot" {
		t.Fatalf("asked %+v, want m0 / 60 min / chan1 / rot", first)
	}
}

// fixedCuts cuts every item at the same offsets.
type fixedCuts []int64

func (f fixedCuts) PlanCuts(CutItem) []int64 { return f }

// FixedCuts returns a planner that cuts every item at the given offsets (those
// outside an item's length are dropped by the normalization).
func FixedCuts(offsetsMs ...int64) CutPlanner { return fixedCuts(offsetsMs) }
