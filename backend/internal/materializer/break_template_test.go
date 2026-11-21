package materializer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// withTemplate adds, to the endlessInput channel, pools A (a bumper), B (ad1 60s,
// ad2 90s, ad3 30s) and C (another bumper) and a break template of kind made of
// [single A] -> [fill 4 min from B] -> [single C].
func withTemplate(in channelInput, kind domain.FillerKind, scope domain.FillerScope, scopeID string, position int) channelInput {
	in.CatalogDuration["bumpA"] = 30 * sec
	in.CatalogDuration["bumpC"] = 20 * sec
	in.CatalogDuration["ad1"] = 60 * sec
	in.CatalogDuration["ad2"] = 90 * sec
	in.CatalogDuration["ad3"] = 30 * sec
	in.PoolItems["A"] = []domain.FillerPoolItem{{ID: "pa", FillerPoolID: "A", CatalogItemID: "bumpA", Weight: 1}}
	in.PoolItems["B"] = []domain.FillerPoolItem{
		{ID: "pb1", FillerPoolID: "B", CatalogItemID: "ad1", Weight: 1},
		{ID: "pb2", FillerPoolID: "B", CatalogItemID: "ad2", Weight: 1},
		{ID: "pb3", FillerPoolID: "B", CatalogItemID: "ad3", Weight: 1},
	}
	in.PoolItems["C"] = []domain.FillerPoolItem{{ID: "pc", FillerPoolID: "C", CatalogItemID: "bumpC", Weight: 1}}
	id := fmt.Sprintf("tmpl-%s-%s-%d", kind, scopeID, position)
	in.BreakTemplates = append(in.BreakTemplates, domain.BreakTemplate{
		ID: id, Scope: scope, ScopeID: scopeID, Kind: kind, Position: position,
		Steps: []domain.BreakTemplateStep{
			{ID: id + "-s1", StepType: domain.BreakStepSingle, FillerPoolID: "A", Selection: domain.SelectionSequential, MaxFitAttempts: 1},
			{ID: id + "-s2", StepType: domain.BreakStepFill, FillerPoolID: "B", Selection: domain.SelectionSequential, TargetMs: 4 * minMs, MaxFitAttempts: 1},
			{ID: id + "-s3", StepType: domain.BreakStepSingle, FillerPoolID: "C", Selection: domain.SelectionSequential, MaxFitAttempts: 1},
		},
	})
	return in
}

func TestBreakTemplate_ExpansionOrderMidroll(t *testing.T) {
	in := withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	entries := runEndless(in, 3*time.Hour+30*time.Minute)
	// A 4 minute fill from B in rotation order: ad1 ad2 ad3 ad1 = 240 s exactly.
	// The rotation goes on in the next break (ad2 ad3 ad1 = 180 s; the next ad2,
	// 90 s, no longer fits the 60 s left, and one attempt gives up).
	want := "m0 bumpA ad1 ad2 ad3 ad1 bumpC m1 bumpA ad2 ad3 ad1 bumpC m2"
	if got := seq(entries); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	assertTight(t, entries)
	for _, e := range entries {
		if e.CatalogItemID != "m0" && e.CatalogItemID != "m1" && e.CatalogItemID != "m2" && e.Kind != domain.EpgMidRoll {
			t.Errorf("%s is %s, want the midroll kind (hidden from the guide)", e.CatalogItemID, e.Kind)
		}
	}
}

func TestBreakTemplate_PrerollAndPostrollKinds(t *testing.T) {
	in := withTemplate(endlessInput(), domain.FillerPreRoll, domain.ScopeChannel, "chan1", 0)
	in = withTemplate(in, domain.FillerPostRoll, domain.ScopeChannel, "chan1", 0)
	entries := runEndless(in, 2*time.Hour)
	pre := "bumpA ad1 ad2 ad3 ad1 bumpC"
	if got, want := seq(entries), pre+" m0 "+pre; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i, e := range entries {
		want := domain.EpgPreRoll
		if i == 6 {
			want = domain.EpgProgram
		} else if i > 6 {
			want = domain.EpgPostRoll
		}
		if e.Kind != want {
			t.Errorf("entry %d (%s) kind %s, want %s", i, e.CatalogItemID, e.Kind, want)
		}
	}
	assertTight(t, entries)
}

func TestBreakTemplate_RotationStoredOnEveryPart(t *testing.T) {
	in := withTemplate(endlessInput(), domain.FillerPostRoll, domain.ScopeChannel, "chan1", 0)
	entries := runEndless(in, 2*time.Hour+30*time.Minute)
	// m0's rotation_next is 1, and so is every part of its postroll: a resume
	// reads the last row's value.
	for i, e := range entries[:7] {
		if e.RotationNext == nil || *e.RotationNext != 1 {
			t.Errorf("entry %d (%s) rotation_next = %v, want 1", i, e.CatalogItemID, e.RotationNext)
		}
	}
}

func fillRule(pool string, target int64, attempts int) domain.BreakTemplate {
	return domain.BreakTemplate{
		ID: "t", Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: domain.FillerMidRoll,
		Steps: []domain.BreakTemplateStep{{ID: "s", StepType: domain.BreakStepFill, FillerPoolID: pool, Selection: domain.SelectionSequential, TargetMs: target, MaxFitAttempts: attempts}},
	}
}

func TestBreakTemplate_FillStepTargetAndFitAttempts(t *testing.T) {
	tests := []struct {
		name     string
		secs     []int64
		target   int64
		attempts int
		want     string
	}{
		{"stops under the target when the next does not fit", []int64{30, 60}, 150, 1, "i0 i1 i0"},
		{"exact fill", []int64{30, 60}, 90, 1, "i0 i1"},
		{"a second attempt finds the short item", []int64{100, 30}, 70, 2, "i1 i1"},
		{"one attempt gives up at the first miss", []int64{100, 30}, 70, 1, ""},
		{"target below every item fills nothing", []int64{30}, 10, 5, ""},
		{"a non-positive item never fits and never loops", []int64{0, 30}, 60, 2, "i1 i1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names := make([]string, len(tc.secs))
			for i := range tc.secs {
				names[i] = fmt.Sprintf("i%d", i)
			}
			pool, dur := gapPool(names, tc.secs)
			src := &midrollSource{tmpl: ptrTemplate(fillRule("pool", tc.target*sec, tc.attempts)), pools: pool, durations: dur, state: newSelectionState()}
			var got []string
			total := int64(0)
			for _, p := range src.pickBreak(t0) {
				got = append(got, p.item.CatalogItemID)
				total += p.dur
			}
			if strings.Join(got, " ") != tc.want {
				t.Fatalf("got %q, want %q", strings.Join(got, " "), tc.want)
			}
			if total > tc.target*sec {
				t.Fatalf("break is %d ms, over the %d s target", total, tc.target)
			}
		})
	}
}

func ptrTemplate(t domain.BreakTemplate) *domain.BreakTemplate { return &t }

func TestBreakTemplate_SequentialRotationContinuesAcrossBreaks(t *testing.T) {
	// The fill step's rotation is per step: the second break starts where the
	// first stopped.
	pool, dur := gapPool([]string{"a", "b", "c"}, []int64{10, 10, 10})
	src := &midrollSource{tmpl: ptrTemplate(fillRule("pool", 20*sec, 1)), pools: pool, durations: dur, state: newSelectionState()}
	var got []string
	for i := 0; i < 3; i++ {
		for _, p := range src.pickBreak(t0) {
			got = append(got, p.item.CatalogItemID)
		}
	}
	if want := "a b c a b c"; strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
}

func TestResolveBreak_PrecedenceAndScope(t *testing.T) {
	tmpl := func(id string, scope domain.FillerScope, scopeID string, pos int) domain.BreakTemplate {
		return domain.BreakTemplate{ID: id, Scope: scope, ScopeID: scopeID, Kind: domain.FillerMidRoll, Position: pos,
			Steps: []domain.BreakTemplateStep{{ID: id + "s", StepType: domain.BreakStepSingle}}}
	}
	rule := func(id string, scope domain.FillerScope, scopeID string, pos int) domain.FillerRule {
		return domain.FillerRule{ID: id, Scope: scope, ScopeID: scopeID, Kind: domain.FillerMidRoll, Position: pos}
	}
	tests := []struct {
		name  string
		rules []domain.FillerRule
		tmpls []domain.BreakTemplate
		want  string // "rule:<id>", "tmpl:<id>" or ""
	}{
		{"nothing", nil, nil, ""},
		{"plain rule only is unchanged", []domain.FillerRule{rule("r", domain.ScopeChannel, "c", 0)}, nil, "rule:r"},
		{"template only", nil, []domain.BreakTemplate{tmpl("t", domain.ScopeChannel, "c", 0)}, "tmpl:t"},
		{"template beats a plain rule at the same level", []domain.FillerRule{rule("r", domain.ScopeChannel, "c", 0)}, []domain.BreakTemplate{tmpl("t", domain.ScopeChannel, "c", 5)}, "tmpl:t"},
		{"block template beats channel template", nil, []domain.BreakTemplate{tmpl("tc", domain.ScopeChannel, "c", 0), tmpl("tb", domain.ScopeBlock, "b", 9)}, "tmpl:tb"},
		{"block rule beats a channel template", []domain.FillerRule{rule("rb", domain.ScopeBlock, "b", 0)}, []domain.BreakTemplate{tmpl("tc", domain.ScopeChannel, "c", 0)}, "rule:rb"},
		{"block template beats a block rule and a channel rule", []domain.FillerRule{rule("rb", domain.ScopeBlock, "b", 0), rule("rc", domain.ScopeChannel, "c", 0)}, []domain.BreakTemplate{tmpl("tb", domain.ScopeBlock, "b", 3)}, "tmpl:tb"},
		{"lowest position wins among templates", nil, []domain.BreakTemplate{tmpl("t2", domain.ScopeChannel, "c", 2), tmpl("t1", domain.ScopeChannel, "c", 1)}, "tmpl:t1"},
		{"lowest position wins among rules", []domain.FillerRule{rule("r2", domain.ScopeChannel, "c", 2), rule("r1", domain.ScopeChannel, "c", 1)}, nil, "rule:r1"},
		{"other block and other channel are ignored", []domain.FillerRule{rule("rx", domain.ScopeBlock, "x", 0)}, []domain.BreakTemplate{tmpl("tx", domain.ScopeBlock, "x", 0), tmpl("ty", domain.ScopeChannel, "y", 0)}, ""},
		{"another kind is ignored", nil, []domain.BreakTemplate{{ID: "tp", Scope: domain.ScopeChannel, ScopeID: "c", Kind: domain.FillerPreRoll, Steps: []domain.BreakTemplateStep{{ID: "x"}}}}, ""},
		{"a template with no steps is ignored", []domain.FillerRule{rule("r", domain.ScopeChannel, "c", 0)}, []domain.BreakTemplate{{ID: "e", Scope: domain.ScopeChannel, ScopeID: "c", Kind: domain.FillerMidRoll}}, "rule:r"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, tm := resolveBreak(tc.rules, tc.tmpls, domain.FillerMidRoll, "b", "c")
			got := ""
			switch {
			case r != nil && tm != nil:
				t.Fatal("both a rule and a template resolved")
			case r != nil:
				got = "rule:" + r.ID
			case tm != nil:
				got = "tmpl:" + tm.ID
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveBreak_PlainRulePathMatchesResolveFillerRule(t *testing.T) {
	rules := []domain.FillerRule{
		{ID: "a", Scope: domain.ScopeChannel, ScopeID: "c", Kind: domain.FillerPreRoll, Position: 2},
		{ID: "b", Scope: domain.ScopeChannel, ScopeID: "c", Kind: domain.FillerPreRoll, Position: 1},
		{ID: "d", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerPreRoll, Position: 7},
	}
	for _, blk := range []string{"", "blk", "other"} {
		old := resolveFillerRule(rules, domain.FillerPreRoll, blk, "c")
		got, tm := resolveBreak(rules, nil, domain.FillerPreRoll, blk, "c")
		if tm != nil || (old == nil) != (got == nil) || (old != nil && old.ID != got.ID) {
			t.Fatalf("block %q: resolveBreak %v, resolveFillerRule %v", blk, got, old)
		}
	}
}

func TestBreakTemplate_ScopeBlockWinsInExpansion(t *testing.T) {
	// The continuous item airs block "rot"; a channel template is overridden by
	// a block template whose single step is just bumper C.
	in := withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	in.BreakTemplates = append(in.BreakTemplates, domain.BreakTemplate{
		ID: "blk", Scope: domain.ScopeBlock, ScopeID: "rot", Kind: domain.FillerMidRoll,
		Steps: []domain.BreakTemplateStep{{ID: "blk-s", StepType: domain.BreakStepSingle, FillerPoolID: "C", Selection: domain.SelectionSequential, MaxFitAttempts: 1}},
	})
	if got, want := seq(runEndless(in, 2*time.Hour+30*time.Minute)), "m0 bumpC m1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBreakTemplate_TemplateBeatsPlainRuleInExpansion(t *testing.T) {
	in := withTemplate(endlessInput(domain.FillerMidRoll), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	if got := seq(runEndless(in, 2*time.Hour+30*time.Minute)); !strings.HasPrefix(got, "m0 bumpA ad1") || strings.Contains(got, " mid ") {
		t.Fatalf("the template did not replace the plain midroll: %q", got)
	}
}

func TestBreakTemplate_PlainRulesBehaveAsBefore(t *testing.T) {
	// A channel with only plain rules and an unrelated template of another
	// scope expands exactly like one with the plain rules alone.
	base := runEndless(endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll), 4*time.Hour)
	in := withTemplate(endlessInput(domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll), domain.FillerMidRoll, domain.ScopeBlock, "unused", 0)
	if got, want := seq(runEndless(in, 4*time.Hour)), seq(base); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBreakTemplate_UnitMustFitWholeBeforeLimit(t *testing.T) {
	// A break that makes the next program overrun the window is not aired:
	// no orphaned break parts at the end.
	in := withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	// m0 (60) + break (30+240+20 s = 4.83 min) + m1 (60) = 124.83 min.
	got := seq(runEndless(in, 124*time.Minute))
	if got != "m0" {
		t.Fatalf("got %q, want only m0", got)
	}
	got = seq(runEndless(in, 125*time.Minute))
	if !strings.HasSuffix(got, "bumpC m1") {
		t.Fatalf("got %q", got)
	}
}

func TestBreakTemplate_DeterministicRegeneration(t *testing.T) {
	in := withTemplate(endlessInput(domain.FillerPreRoll), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	a := seq(runEndless(in, 12*time.Hour))
	b := seq(runEndless(in, 12*time.Hour))
	if a != b {
		t.Fatalf("two regenerations differ:\n%s\n%s", a, b)
	}
}

// singleAdPool makes the fill pool rotation-insensitive: a regeneration
// restarts every sequential rotation (an accepted limitation), so a resumed
// suffix is only comparable to the full run when a pool has one item.
func singleAdPool(in channelInput) channelInput {
	in.PoolItems["B"] = in.PoolItems["B"][:1]
	return in
}

func TestBreakTemplate_ResumeAfterKeptHead(t *testing.T) {
	in := singleAdPool(withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0))
	full := runEndless(in, 8*time.Hour)
	// The head ends at the end of the first break (what extendKeptThroughBreak
	// keeps); the rotation resumes at m1 with the midroll aired.
	cut := 0
	for i, e := range full {
		if e.Kind == domain.EpgMidRoll {
			cut = i + 1
		} else if cut > 0 {
			break
		}
	}
	keptUntil := time.UnixMilli(full[cut].StartAt).UTC()
	resume := in
	resume.KeptUntil = keptUntil
	resume.ContinuousStart = 1
	resume.ContinuousStage = stageAfterMid
	resumed := runEndless(resume, 8*time.Hour)
	if got, want := seq(resumed), seq(full[cut:]); got != want {
		t.Fatalf("resumed generation differs from the full one:\n%s\n%s", got, want)
	}
}

func TestBreakTemplate_ResumeAfterKeptPostrollKeepsOwedMidAndPre(t *testing.T) {
	in := singleAdPool(withTemplate(endlessInput(domain.FillerMidRoll), domain.FillerPostRoll, domain.ScopeChannel, "chan1", 0))
	full := runEndless(in, 8*time.Hour)
	cut := 0
	for i, e := range full {
		if e.Kind == domain.EpgPostRoll {
			cut = i + 1
		} else if cut > 0 {
			break
		}
	}
	resume := in
	resume.KeptUntil = time.UnixMilli(full[cut].StartAt).UTC()
	resume.ContinuousStart = 1
	resume.ContinuousStage = stageAfterPost
	if got, want := seq(runEndless(resume, 8*time.Hour)), seq(full[cut:]); got != want {
		t.Fatalf("resumed generation differs from the full one:\n%s\n%s", got, want)
	}
}

func TestBreakTemplate_ManualBlockOrderAndOverrunPolicy(t *testing.T) {
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		BlockItems:      map[string][]domain.BlockItem{"blk": {{ID: "b0", BlockID: "blk", CatalogItemID: "x"}, {ID: "b1", BlockID: "blk", CatalogItemID: "y"}}},
		CatalogDuration: map[string]int64{"x": 20 * minMs, "y": 20 * minMs},
		PoolItems:       map[string][]domain.FillerPoolItem{},
		RepeatBlockIDs:  map[string]bool{},
	}
	for _, s := range []int64{0, 60} {
		in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{
			ID: fmt.Sprintf("s%d", s), BlockID: "blk", Recurrence: domain.RecurrenceOnce,
			StartAt: int64Ptr(t0.Add(time.Duration(s) * time.Minute).UnixMilli()), Active: true,
		})
	}
	for _, k := range []domain.FillerKind{domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll} {
		in = withTemplate(in, k, domain.ScopeBlock, "blk", 0)
	}
	got := expandChannel(in, t0, t0, t0.Add(3*time.Hour), newSelectionState())
	var first []domain.EpgEntry
	for _, e := range got {
		if e.StartAt < t0.Add(60*time.Minute).UnixMilli() {
			first = append(first, e)
		}
	}
	// Each template is [bumpA] [4 min of B] [bumpC]: 4.83 minutes. The slot holds
	// pre, x (20), mid, y (20), post = 49.7 minutes, all before the next slot.
	brk := func(kind string) string {
		parts := []string{"bumpA", "ad1", "ad2", "ad3", "ad1", "bumpC"}
		for i := range parts {
			parts[i] = kind + ":" + parts[i]
		}
		return strings.Join(parts, " ")
	}
	want := brk("preroll") + " program:x " + brk("midroll") + " program:y " + brk("postroll")
	if got := kinds(first); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if last := first[len(first)-1]; last.EndAt > t0.Add(60*time.Minute).UnixMilli() {
		t.Fatalf("first slot overran the next one")
	}
}

func TestBreakTemplate_ManualBlockPostrollReservedWhenSlotIsTight(t *testing.T) {
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		BlockItems:      map[string][]domain.BlockItem{"blk": {{ID: "b0", BlockID: "blk", CatalogItemID: "x"}, {ID: "b1", BlockID: "blk", CatalogItemID: "y"}}},
		CatalogDuration: map[string]int64{"x": 20 * minMs, "y": 20 * minMs},
		PoolItems:       map[string][]domain.FillerPoolItem{},
		RepeatBlockIDs:  map[string]bool{},
	}
	for _, s := range []int64{0, 30} {
		in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{
			ID: fmt.Sprintf("s%d", s), BlockID: "blk", Recurrence: domain.RecurrenceOnce,
			StartAt: int64Ptr(t0.Add(time.Duration(s) * time.Minute).UnixMilli()), Active: true,
		})
	}
	in = withTemplate(in, domain.FillerPostRoll, domain.ScopeChannel, "chan1", 0)
	got := expandChannel(in, t0, t0, t0.Add(3*time.Hour), newSelectionState())
	var slot []string
	var end int64
	for _, e := range got {
		if e.StartAt < t0.Add(30*time.Minute).UnixMilli() {
			slot = append(slot, string(e.Kind)+":"+e.CatalogItemID)
			end = e.EndAt
		}
	}
	// x (20) + the 4.83-minute postroll fit in 30 minutes; y does not, and is dropped.
	if want := "program:x postroll:bumpA postroll:ad1 postroll:ad2 postroll:ad3 postroll:ad1 postroll:bumpC"; strings.Join(slot, " ") != want {
		t.Fatalf("got %v", slot)
	}
	if end > t0.Add(30*time.Minute).UnixMilli() {
		t.Fatalf("the slot overran the next one")
	}
}

func TestBreakTemplate_EntryCapStaysBounded(t *testing.T) {
	// A pool of 1 ms items and a one-hour fill: each break is capped at
	// maxFillStepParts, and the looping fill still stops at its own cap.
	in := endlessInput()
	in.CatalogDuration["tiny"] = 1
	in.PoolItems["T"] = []domain.FillerPoolItem{{ID: "pt", FillerPoolID: "T", CatalogItemID: "tiny", Weight: 1}}
	in.BreakTemplates = []domain.BreakTemplate{{
		ID: "cap", Scope: domain.ScopeChannel, ScopeID: "chan1", Kind: domain.FillerMidRoll,
		Steps: []domain.BreakTemplateStep{{ID: "cap-s", StepType: domain.BreakStepFill, FillerPoolID: "T", Selection: domain.SelectionSequential, TargetMs: domain.MaxBreakTargetMs, MaxFitAttempts: 1}},
	}}
	began := time.Now()
	got := runEndless(in, 100*time.Hour)
	if time.Since(began) > 10*time.Second {
		t.Fatalf("took %v", time.Since(began))
	}
	// m0 then one break per boundary: 1000 parts each, until the 50000 cap.
	limit := maxLoopEntries + 3*domain.MaxBreakSteps*maxFillStepParts
	if len(got) < 1000 || len(got) > limit {
		t.Fatalf("%d entries, want between 1000 and %d", len(got), limit)
	}
	breakParts := 0
	for _, e := range got[1:] {
		if e.Kind == domain.EpgMidRoll {
			breakParts++
		} else {
			if breakParts != maxFillStepParts {
				t.Fatalf("a break had %d parts, want the %d cap", breakParts, maxFillStepParts)
			}
			breakParts = 0
		}
	}
}

func TestBreakTemplate_FillerRatioWarningCountsTheTemplate(t *testing.T) {
	// Plain rules: 6 minutes against one-hour programs, fine. A template of
	// [single mid 2 min][fill 40 min of pool B] can add 42 minutes: 70%.
	in := withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	in.BreakTemplates[0].Steps[1].TargetMs = 40 * minMs
	w := in.fillerRatioWarnings()
	if len(w) != 1 || w[0].Kind != WarningFillerRatio || w[0].BlockID != "rot" {
		t.Fatalf("warnings = %+v", w)
	}
	if !strings.Contains(w[0].Message, "40.8 min") {
		t.Fatalf("message does not count the template: %q", w[0].Message)
	}
	// A 4-minute fill (the default test template) stays below the limit.
	if w := withTemplate(endlessInput(), domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0).fillerRatioWarnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %+v", w)
	}
	// A fill step no pool item can fit adds nothing.
	in.BreakTemplates[0].Steps[1].TargetMs = 10 * sec
	in.BreakTemplates[0].Steps[0].FillerPoolID = "none"
	in.BreakTemplates[0].Steps[2].FillerPoolID = "none"
	if w := in.fillerRatioWarnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %+v", w)
	}
}

func BenchmarkBreakTemplateRegeneration(b *testing.B) {
	// A large pool and a long window: a midroll template of single + 4 minute
	// fill + single between three-minute programs, over a week of one channel.
	in := endlessInput()
	in.BlockItems["rot"] = []domain.BlockItem{
		{ID: "b0", BlockID: "rot", Position: 0, CatalogItemID: "m0"},
		{ID: "b1", BlockID: "rot", Position: 1, CatalogItemID: "m1"},
	}
	in.CatalogDuration["m0"], in.CatalogDuration["m1"] = 3*minMs, 3*minMs
	in = withTemplate(in, domain.FillerMidRoll, domain.ScopeChannel, "chan1", 0)
	in.PoolItems["B"] = nil
	for i := 0; i < 20000; i++ {
		id := fmt.Sprintf("ad%d", i)
		in.CatalogDuration[id] = (15 + int64(i%7)*15) * sec
		in.PoolItems["B"] = append(in.PoolItems["B"], domain.FillerPoolItem{ID: "p" + id, FillerPoolID: "B", CatalogItemID: id, Weight: 1 + i%5})
	}
	for _, sel := range []domain.SelectionStrategy{domain.SelectionSequential, domain.SelectionRandom, domain.SelectionWeighted} {
		in.BreakTemplates[0].Steps[1].Selection = sel
		b.Run(string(sel), func(b *testing.B) {
			var n int
			for i := 0; i < b.N; i++ {
				n = len(expandChannel(in, t0, t0, t0.Add(7*24*time.Hour), newSelectionState()))
			}
			b.ReportMetric(float64(n), "entries")
		})
	}
}

// --- through the loader and the kept head ---

func (f *testFixture) createBreakTemplate(t *testing.T, id, scopeType, scopeID, kind string, position int, steps ...[]any) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.Exec(ctx,
		`INSERT INTO break_template (id, tenant_id, scope_type, scope_id, kind, name, position, created_at) VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		id, f.tenantID, scopeType, scopeID, kind, position, time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed break_template %s: %v", id, err)
	}
	for i, s := range steps {
		// step: type, pool, selection, targetMs, attempts
		if err := f.db.Exec(ctx,
			`INSERT INTO break_template_step (id, break_template_id, position, step_type, filler_pool_id, selection_strategy, target_ms, max_fit_attempts)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("%s-s%d", id, i), id, i, s[0], s[1], s[2], s[3], s[4],
		); err != nil {
			t.Fatalf("seed break_template_step: %v", err)
		}
	}
}

// A midroll template of two single steps between the continuous item's movies:
// a kept head that ends between its two parts keeps the whole break, and the
// rotation carries on at the next movie.
func TestBreakTemplate_KeptHeadEndingInsideABreakKeepsTheWholeBreak(t *testing.T) {
	f := continuousSetup(t)
	f.createCatalogItem(t, "bumpA", 60*1000)
	f.createCatalogItem(t, "bumpB", 60*1000)
	f.createFillerPool(t, "pa")
	f.createFillerPool(t, "pb")
	f.addFillerPoolItem(t, "pia", "pa", "bumpA")
	f.addFillerPoolItem(t, "pib", "pb", "bumpB")
	f.createBreakTemplate(t, "bt", "channel", "chan1", "midroll", 0,
		[]any{"single", "pa", "sequential", 0, 1},
		[]any{"single", "pb", "sequential", 0, 1},
	)
	ctx := context.Background()
	mat := New(f.db, 6*time.Hour)
	// 1 minute 30 s into the break: the head would end after its first part.
	mat.SetFreezeMargin(time.Hour + 30*time.Second)
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	first := queryEntries(t, f)
	if len(first) < 4 || first[1].Kind != domain.EpgMidRoll || first[2].Kind != domain.EpgMidRoll {
		t.Fatalf("run 1 did not lay out a two-part break: %v", kinds(first))
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	second := queryEntries(t, f)
	for i := 0; i < 3; i++ {
		if second[i].ID != first[i].ID {
			t.Errorf("entry %d (%s) was regenerated although it belongs to the kept break", i, second[i].CatalogItemID)
		}
	}
	// The first break is complete and in order, then the rotation resumes at m1.
	if got := seq(second[:4]); got != "m0 bumpA bumpB m1" {
		t.Fatalf("got %q, want the whole break then m1", got)
	}
	// Every later break is whole too (never a lone first part).
	for i := 3; i < len(second); i++ {
		if second[i].CatalogItemID == "bumpA" && (i+1 >= len(second) || second[i+1].CatalogItemID != "bumpB") {
			// the window may end right after a break's first part only if the
			// unit does not fit, which the loop never airs
			t.Errorf("entry %d: bumpA without its bumpB", i)
		}
		if i > 0 && second[i].StartAt != second[i-1].EndAt {
			t.Errorf("entry %d is not contiguous", i)
		}
	}
}

func kinds(entries []domain.EpgEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = string(e.Kind) + ":" + e.CatalogItemID
	}
	return strings.Join(parts, " ")
}
