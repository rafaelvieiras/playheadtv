package materializer

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const sec = int64(1000)

// gapPool builds a one-pool fixture: item names map to durations in seconds,
// in the order given.
func gapPool(names []string, secs []int64) (map[string][]domain.FillerPoolItem, map[string]int64) {
	pool := map[string][]domain.FillerPoolItem{"pool": {}}
	dur := map[string]int64{}
	for i, n := range names {
		pool["pool"] = append(pool["pool"], domain.FillerPoolItem{ID: "pi-" + n, FillerPoolID: "pool", CatalogItemID: n, Weight: 1})
		dur[n] = secs[i] * sec
	}
	return pool, dur
}

func runGap(rule *domain.FillerRule, pool map[string][]domain.FillerPoolItem, dur map[string]int64, residualSecs int64, state *selectionState) ([]domain.EpgEntry, time.Time, time.Time) {
	cursor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	target := cursor.Add(time.Duration(residualSecs) * time.Second)
	entries, end := fillGap(rule, pool, dur, cursor, target, domain.EpgTail, "t", "c", 0, nil, state, false)
	return entries, end, target
}

func TestFillGap_MaxFitAttempts(t *testing.T) {
	tests := []struct {
		name         string
		names        []string
		secs         []int64
		attempts     int
		residualSecs int64
		want         string
		wantLeftSecs int64
	}{
		{"one attempt gives up at the first miss", []string{"A", "B"}, []int64{30, 5}, 1, 12, "", 12},
		{"a second attempt finds the short item", []string{"A", "B"}, []int64{30, 5}, 2, 12, "B B", 2},
		{"the tail keeps going with small items", []string{"A", "B", "C"}, []int64{4, 10, 3}, 2, 20, "A B C", 3},
		{"attempts bound consecutive misses", []string{"L1", "L2", "L3", "S"}, []int64{50, 50, 50, 5}, 3, 12, "", 12},
		{"one more attempt reaches the short item", []string{"L1", "L2", "L3", "S"}, []int64{50, 50, 50, 5}, 4, 12, "S S", 2},
		{"attempts beyond the pool size are harmless", []string{"A", "B"}, []int64{30, 40}, 20, 12, "", 12},
		{"unset attempts behave as one", []string{"A", "B"}, []int64{30, 5}, 0, 12, "", 12},
		{"a non-positive duration counts as a miss, never loops", []string{"Z", "B"}, []int64{0, 5}, 2, 12, "B B", 2},
		{"a fitting first pick needs no extra attempt", []string{"A", "B"}, []int64{5, 30}, 1, 10, "A", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool, dur := gapPool(tc.names, tc.secs)
			rule := &domain.FillerRule{ID: "r", FillerPoolID: "pool", Selection: domain.SelectionSequential, MaxFitAttempts: tc.attempts}
			entries, end, target := runGap(rule, pool, dur, tc.residualSecs, newSelectionState())
			if got := seq(entries); got != tc.want {
				t.Fatalf("aired %q, want %q", got, tc.want)
			}
			if left := target.Sub(end); left != time.Duration(tc.wantLeftSecs)*time.Second {
				t.Fatalf("residual %v, want %ds", left, tc.wantLeftSecs)
			}
			for i := 1; i < len(entries); i++ {
				if entries[i-1].EndAt != entries[i].StartAt {
					t.Fatalf("entries are not back to back: %v", seq(entries))
				}
			}
		})
	}
}

func TestFillGap_SequentialRotationIsNotConsumedBySkippedItems(t *testing.T) {
	pool, dur := gapPool([]string{"A", "B", "C"}, []int64{30, 5, 5})
	rule := &domain.FillerRule{ID: "r", FillerPoolID: "pool", Selection: domain.SelectionSequential, MaxFitAttempts: 3}
	state := newSelectionState()

	// A does not fit and is skipped; B airs, so the rotation moves past B only.
	entries, _, _ := runGap(rule, pool, dur, 7, state)
	if got := seq(entries); got != "B" {
		t.Fatalf("first gap aired %q, want B", got)
	}
	if got := state.seqCursor["r"]; got != 2 {
		t.Fatalf("rotation at %d after airing B, want 2 (A was only skipped)", got)
	}
	// Next gap continues with C, not with the skipped A or a repeated B.
	entries, _, _ = runGap(rule, pool, dur, 7, state)
	if got := seq(entries); got != "C" {
		t.Fatalf("second gap aired %q, want C", got)
	}
	// A gap in which nothing fits leaves the rotation untouched.
	before := state.seqCursor["r"]
	entries, _, _ = runGap(rule, pool, dur, 3, state)
	if len(entries) != 0 || state.seqCursor["r"] != before {
		t.Fatalf("a gap nothing fits changed the rotation: %d -> %d, entries %q", before, state.seqCursor["r"], seq(entries))
	}
	// A gap that fits the long item airs it first: it never lost its turn.
	entries, _, _ = runGap(rule, pool, dur, 32, state)
	if got := seq(entries); !strings.HasPrefix(got, "A") {
		// state sits after C (index 3 -> A), so A is next in line.
		t.Fatalf("A was not next in the rotation: %q", got)
	}
}

func TestFillGap_RandomAndWeightedNeverRetryARejectedItemInOneRound(t *testing.T) {
	// Four items too long and one that fits; with as many attempts as items
	// the fitting one must always be found, which needs draws without
	// replacement within a round.
	names := []string{"L1", "L2", "L3", "L4", "S"}
	secs := []int64{60, 60, 60, 60, 5}
	for _, sel := range []domain.SelectionStrategy{domain.SelectionRandom, domain.SelectionWeighted} {
		t.Run(string(sel), func(t *testing.T) {
			pool, dur := gapPool(names, secs)
			if sel == domain.SelectionWeighted {
				for i := range pool["pool"] {
					if pool["pool"][i].CatalogItemID != "S" {
						pool["pool"][i].Weight = 1000
					}
				}
			}
			rule := &domain.FillerRule{ID: "r", FillerPoolID: "pool", Selection: sel, MaxFitAttempts: 5}
			for i := 0; i < 300; i++ {
				entries, _, _ := runGap(rule, pool, dur, 6, newSelectionState())
				if got := seq(entries); got != "S" {
					t.Fatalf("trial %d aired %q, want S", i, got)
				}
			}
		})
	}
}

func TestFillGap_AttemptsRespectTheEntryCap(t *testing.T) {
	pool, dur := gapPool([]string{"L", "S"}, []int64{999999, 1})
	rule := &domain.FillerRule{ID: "r", FillerPoolID: "pool", Selection: domain.SelectionSequential, MaxFitAttempts: 5}
	// 100000 seconds of residual would take 100000 one-second items.
	entries, end, _ := runGap(rule, pool, dur, 100000, newSelectionState())
	if len(entries) != maxLoopEntries {
		t.Fatalf("emitted %d entries, want the cap %d", len(entries), maxLoopEntries)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(maxLoopEntries) * time.Second); !end.Equal(want) {
		t.Fatalf("cursor at %v, want %v", end, want)
	}
}

// slotInput is a channel with two once-items 100 minutes apart over a block
// of fixed content (manual: 40 + 35 minutes, once; or dynamic: looped), plus
// the filler rules named in rules.
func slotInput(dynamic bool, rules ...domain.FillerRule) channelInput {
	t1 := t0.Add(100 * time.Minute)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{
			{ID: "s1", BlockID: "blk", Recurrence: domain.RecurrenceOnce, StartAt: int64Ptr(t0.UnixMilli()), Active: true},
			{ID: "s2", BlockID: "blk", Recurrence: domain.RecurrenceOnce, StartAt: int64Ptr(t1.UnixMilli()), Active: true},
		},
		BlockItems: map[string][]domain.BlockItem{"blk": {
			{ID: "b0", BlockID: "blk", Position: 0, CatalogItemID: "p40"},
			{ID: "b1", BlockID: "blk", Position: 1, CatalogItemID: "p35"},
		}},
		CatalogDuration: map[string]int64{
			"p40": 40 * minMs, "p35": 35 * minMs,
			"t30": 30 * minMs, "t10": 10 * minMs, "fb": 1 * minMs,
		},
		DynamicBlockIDs: map[string]bool{"blk": dynamic},
		PoolItems: map[string][]domain.FillerPoolItem{
			"tailpool": {{ID: "x1", FillerPoolID: "tailpool", CatalogItemID: "t30", Weight: 1}, {ID: "x2", FillerPoolID: "tailpool", CatalogItemID: "t10", Weight: 1}},
			"fbpool":   {{ID: "f1", FillerPoolID: "fbpool", CatalogItemID: "fb", Weight: 1}},
		},
		FillerRules: rules,
	}
	return in
}

func tailRule(attempts int) domain.FillerRule {
	return domain.FillerRule{ID: "tail", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerTail, FillerPoolID: "tailpool", Selection: domain.SelectionSequential, MaxFitAttempts: attempts}
}

func fallbackRule() domain.FillerRule {
	return domain.FillerRule{ID: "fb", Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: domain.FillerFallback, FillerPoolID: "fbpool", Selection: domain.SelectionSequential}
}

func kindsOf(entries []domain.EpgEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = fmt.Sprintf("%s:%s", e.Kind, e.CatalogItemID)
	}
	return strings.Join(parts, " ")
}

func TestExpand_DynamicBlockGetsTheTailRuleAndTheCascade(t *testing.T) {
	// Slot 1 is 100 minutes: the loop airs 40+35, the next 40 would end at
	// 115, so a 25 minute residual is left for the tail. The first
	// occurrence is the one examined; the second only exists as a boundary.
	window := 100 * time.Minute

	// One attempt: the 30 minute tail item does not fit, nothing airs, the
	// residual stays (no fallback rule).
	got := kindsOf(expandChannel(slotInput(true, tailRule(1)), t0, t0, t0.Add(window), newSelectionState()))
	if want := "program:p40 program:p35"; got != want {
		t.Fatalf("one attempt: %q, want %q", got, want)
	}

	// Two attempts: the 10 minute item fills 20 of the 25 minutes.
	got = kindsOf(expandChannel(slotInput(true, tailRule(2)), t0, t0, t0.Add(window), newSelectionState()))
	if want := "program:p40 program:p35 tail:t10 tail:t10"; got != want {
		t.Fatalf("two attempts: %q, want %q", got, want)
	}

	// With a channel fallback the last 5 minutes are covered too: items, tail,
	// fallback, in that order, with no gap.
	entries := expandChannel(slotInput(true, tailRule(2), fallbackRule()), t0, t0, t0.Add(window), newSelectionState())
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].StartAt < entries[j].StartAt })
	if want := "program:p40 program:p35 tail:t10 tail:t10 fallback:fb fallback:fb fallback:fb fallback:fb fallback:fb"; kindsOf(entries) != want {
		t.Fatalf("cascade: %q, want %q", kindsOf(entries), want)
	}
	assertTight(t, entries)
	for _, e := range entries {
		if e.Kind == domain.EpgTail && (e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "s1") {
			t.Fatalf("tail entry not attributed to its schedule item: %+v", e)
		}
	}
}

func TestExpand_ManualBlockTailUsesAttempts(t *testing.T) {
	// Manual "once" block: 40+35 minutes, 25 left. Same residual, same rule.
	in := slotInput(false, tailRule(2))
	in.RepeatBlockIDs = map[string]bool{}
	got := kindsOf(expandChannel(in, t0, t0, t0.Add(100*time.Minute), newSelectionState()))
	if want := "program:p40 program:p35 tail:t10 tail:t10"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpand_TailAttemptsAreDeterministicAcrossRegenerations(t *testing.T) {
	window := 100 * time.Minute
	run := func(keptUntil time.Time) []domain.EpgEntry {
		in := slotInput(true, tailRule(2), fallbackRule())
		in.KeptUntil = keptUntil
		out := expandChannel(in, t0, t0, t0.Add(window), newSelectionState())
		sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt < out[j].StartAt })
		return out
	}
	full := run(time.Time{})
	again := run(time.Time{})
	if kindsOf(full) != kindsOf(again) {
		t.Fatalf("two regenerations differ:\n%s\n%s", kindsOf(full), kindsOf(again))
	}
	// Resume in the middle of the tail: the kept head ends where the first
	// tail entry ends, and the rest must be what the full run holds there.
	var cut int
	for i, e := range full {
		if e.Kind == domain.EpgTail {
			cut = i + 1
			break
		}
	}
	resumed := run(time.UnixMilli(full[cut].StartAt).UTC())
	if got, want := kindsOf(resumed), kindsOf(full[cut:]); got != want {
		t.Fatalf("resumed generation differs from the full one after the kept head:\n%s\n%s", got, want)
	}
}

func TestExpand_UnfilledSlotResidualIsReportedOnlyWithoutFallback(t *testing.T) {
	collect := func(in channelInput) []residual {
		var found []residual
		in.Residuals = &found
		expandChannel(in, t0, t0, t0.Add(7*24*time.Hour), newSelectionState())
		return found
	}
	// No tail, no fallback: the 25 minutes of the first slot are reported
	// (the second occurrence has no next one, so it is not a slot).
	got := collect(slotInput(true))
	if len(got) != 1 || got[0].BlockID != "blk" || got[0].Length != 25*time.Minute {
		t.Fatalf("got %+v, want one 25 minute residual of blk", got)
	}
	// The tail cuts it to 5 minutes.
	got = collect(slotInput(true, tailRule(2)))
	if len(got) != 1 || got[0].Length != 5*time.Minute {
		t.Fatalf("with a tail: %+v, want one 5 minute residual", got)
	}
	// A channel fallback rule takes the remainder over: nothing to report.
	if got = collect(slotInput(true, tailRule(2), fallbackRule())); len(got) != 0 {
		t.Fatalf("with a fallback: %+v, want none", got)
	}
	if got = collect(slotInput(false, fallbackRule())); len(got) != 0 {
		t.Fatalf("manual block with a fallback: %+v, want none", got)
	}
	// Manual block, nothing configured: reported too.
	in := slotInput(false)
	in.RepeatBlockIDs = map[string]bool{}
	if got = collect(in); len(got) != 1 {
		t.Fatalf("manual block without rules: %+v, want one", got)
	}
}

func TestUnfilledSlotWarnings_OnePerBlockWithTheLongestResidual(t *testing.T) {
	in := slotInput(true)
	ws := in.unfilledSlotWarnings(t0)
	if len(ws) != 1 || ws[0].Kind != WarningUnfilledSlot || ws[0].BlockID != "blk" {
		t.Fatalf("got %+v", ws)
	}
	if !strings.Contains(ws[0].Message, "25.0 min") {
		t.Fatalf("message does not name the residual: %q", ws[0].Message)
	}
}
