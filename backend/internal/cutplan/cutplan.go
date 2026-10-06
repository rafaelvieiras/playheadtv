// Package cutplan is the cut-point time model: given a rule list (see
// domain.CutConfig) and one catalog item, it decides where inside the item a
// break goes. It is pure (no storage, no clock, no randomness): the same item
// and the same rules always give the same cuts, so a regeneration of the guide
// never moves a break. docs/ARCHITECTURE.md "Cut points" has the semantics.
//
// The package is not wired into the materializer yet. Planner is the adapter
// the materializer will consume once an epg entry can start at an offset into
// its item.
package cutplan

import (
	"math"
	"sort"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// Defaults of the guard rails (a zero CutGuards field takes these).
const (
	DefaultMinSegmentMs   = int64(3 * 60 * 1000)
	DefaultMinFromStartMs = int64(3 * 60 * 1000)
	DefaultMinFromEndMs   = int64(3 * 60 * 1000)
	DefaultMaxBreaks      = 10

	// DefaultChapterToleranceMs is how far a chapter strategy may move a cut to
	// reach a chapter boundary when the rule sets no tolerance.
	DefaultChapterToleranceMs = int64(5 * 60 * 1000)
	// DefaultIntroMaxMs is the longest first chapter still taken for an intro
	// when no chapter is named like one.
	DefaultIntroMaxMs = int64(10 * 60 * 1000)

	// maxCandidates bounds the cuts one strategy generates before the guards
	// thin them out, so a long item with a short cadence stays cheap.
	maxCandidates = 10000
)

// DefaultGuards are the guard rails applied where a rule set sets none.
var DefaultGuards = domain.CutGuards{
	MinSegmentMs:   DefaultMinSegmentMs,
	MinFromStartMs: DefaultMinFromStartMs,
	MinFromEndMs:   DefaultMinFromEndMs,
	MaxBreaks:      DefaultMaxBreaks,
}

// builtinFallback is the last resort of a chapter strategy with no data and no
// fallback of its own: one break in the middle.
var builtinFallback = domain.CutStrategy{Type: domain.CutPercentage, Percentages: []float64{50}}

// Item is what the planner knows about one catalog item. Chapters is optional
// (nil for an item without chapter metadata) and need not be sorted.
type Item struct {
	Kind       domain.CatalogItemKind
	DurationMs int64
	Chapters   []domain.CatalogChapter
}

// Source says where a Plan's cuts came from.
type Source string

const (
	// SourceRule: the strategy of the rule that matched.
	SourceRule Source = "rule"
	// SourceDefault: no rule matched and the set's default strategy decided.
	SourceDefault Source = "default"
	// SourceFallback: a chapter strategy had no chapters (or none of its roles
	// resolved) and its fallback strategy decided.
	SourceFallback Source = "fallback"
	// SourceBuiltin: as SourceFallback, with no fallback configured: one cut in
	// the middle.
	SourceBuiltin Source = "builtin"
	// SourceNone: no rule matched and the set has no default, or the author
	// chose the 'none' strategy.
	SourceNone Source = "none"
)

// Plan is the outcome for one item.
type Plan struct {
	// CutsMs are the offsets into the item, strictly ascending, after the
	// guard rails. Empty (never nil) when the item is not cut.
	CutsMs []int64 `json:"cuts_ms"`
	// RuleIndex is the index of the matching rule, -1 when none matched.
	RuleIndex int `json:"rule_index"`
	// Source and Strategy name what produced the cuts.
	Source   Source                 `json:"source"`
	Strategy domain.CutStrategyType `json:"strategy"`
	// Notes explain what the planner skipped or fell back from, for previews
	// and logs.
	Notes []string `json:"notes"`
}

// PlanItem returns where item is cut under cfg. It does not validate cfg (see
// ValidateConfig); a malformed strategy yields no cuts instead of a panic.
func PlanItem(cfg domain.CutConfig, item Item) Plan {
	plan := Plan{CutsMs: []int64{}, RuleIndex: -1, Source: SourceNone, Strategy: domain.CutNone, Notes: []string{}}
	if item.DurationMs <= 0 {
		plan.Notes = append(plan.Notes, "the item has no positive duration")
		return plan
	}
	chapters := sortedChapters(item)
	boundaries := boundariesOf(chapters)

	var (
		strategy  domain.CutStrategy
		snap      int64
		ruleGuard *domain.CutGuards
	)
	if i := matchRule(cfg.Rules, item, len(boundaries) > 0); i >= 0 {
		r := cfg.Rules[i]
		plan.RuleIndex, plan.Source = i, SourceRule
		strategy, snap, ruleGuard = r.Strategy, r.SnapToleranceMs, r.Guards
	} else if cfg.Default != nil {
		plan.Source = SourceDefault
		strategy = *cfg.Default
	} else {
		plan.Notes = append(plan.Notes, "no rule matches this item and the set has no default")
		return plan
	}
	plan.Strategy = strategy.Type
	guards := mergeGuards(DefaultGuards, cfg.Guards, ruleGuard)

	raw, resolved := candidates(strategy, item.DurationMs, chapters, boundaries, snap, &plan)
	if !resolved {
		// A chapter strategy without data: fall back, never "no cut" for lack
		// of chapters.
		fb := builtinFallback
		plan.Source = SourceBuiltin
		if strategy.Fallback != nil {
			fb = strategy.Fallback.Strategy()
			plan.Source = SourceFallback
		}
		plan.Strategy = fb.Type
		raw, _ = candidates(fb, item.DurationMs, chapters, boundaries, snap, &plan)
	}
	if plan.Strategy != domain.CutNone && plan.Strategy != domain.CutChapter {
		raw = snapAll(raw, boundaries, snap)
	}
	plan.CutsMs = applyGuards(raw, item.DurationMs, guards)
	if len(raw) > 0 && len(plan.CutsMs) == 0 {
		plan.Notes = append(plan.Notes, "every cut was dropped by the guard rails (the item is too short)")
	}
	return plan
}

// candidates returns the raw cut offsets of one strategy. resolved is false
// only for a chapter strategy that found nothing to cut at for lack of
// chapter data.
func candidates(s domain.CutStrategy, durationMs int64, chapters []domain.CatalogChapter, boundaries []int64, snapMs int64, plan *Plan) (cuts []int64, resolved bool) {
	switch s.Type {
	case domain.CutNone:
		return nil, true
	case domain.CutInterval:
		return everyCuts(0, durationMs, s.EveryMs), true
	case domain.CutPercentage:
		for _, p := range s.Percentages {
			cuts = append(cuts, int64(math.Round(float64(durationMs)*p/100)))
		}
		return cuts, true
	case domain.CutOffsets:
		return append(cuts, s.OffsetsMs...), true
	case domain.CutPhased:
		return phasedCuts(s.Phases, durationMs), true
	case domain.CutChapter:
		if len(boundaries) == 0 {
			plan.Notes = append(plan.Notes, "the item has no chapters")
			return nil, false
		}
		tol := snapMs
		if tol <= 0 {
			tol = DefaultChapterToleranceMs
		}
		for _, role := range s.Roles {
			if c, ok := roleCut(role, durationMs, chapters, boundaries, tol); ok {
				cuts = append(cuts, c)
			} else {
				plan.Notes = append(plan.Notes, "role "+string(role)+" did not resolve")
			}
		}
		return cuts, len(cuts) > 0
	}
	return nil, true
}

// everyCuts returns start+every, start+2*every, ... below end.
func everyCuts(start, end, every int64) []int64 {
	if every <= 0 {
		return nil
	}
	var out []int64
	for t := start + every; t < end && len(out) < maxCandidates; t += every {
		out = append(out, t)
	}
	return out
}

// phasedCuts runs the phases in order. A phase starts where the previous one
// ended (the first at 0) and cuts every EveryMs until its end, which is its
// UntilMs, or the duration minus UntilBeforeEndMs; the last phase runs to the
// end of the item. A phase that ends before it starts (an item shorter than
// the phase boundary) is skipped.
func phasedCuts(phases []domain.CutPhase, durationMs int64) []int64 {
	var out []int64
	start := int64(0)
	for i, p := range phases {
		end := durationMs
		if i < len(phases)-1 {
			switch {
			case p.UntilMs > 0:
				end = p.UntilMs
			case p.UntilBeforeEndMs > 0:
				end = durationMs - p.UntilBeforeEndMs
			}
		}
		if end > durationMs {
			end = durationMs
		}
		if end <= start {
			continue
		}
		out = append(out, everyCuts(start, end, p.EveryMs)...)
		start = end
		if len(out) >= maxCandidates {
			break
		}
	}
	return out
}

// matchRule returns the index of the first rule whose selector matches, or -1.
func matchRule(rules []domain.CutRule, item Item, hasChapters bool) int {
	for i, r := range rules {
		if matches(r.Match, item, hasChapters) {
			return i
		}
	}
	return -1
}

func matches(sel domain.CutSelector, item Item, hasChapters bool) bool {
	if len(sel.Kinds) > 0 {
		found := false
		for _, k := range sel.Kinds {
			if k == item.Kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if sel.MinDurationMs > 0 && item.DurationMs < sel.MinDurationMs {
		return false
	}
	if sel.MaxDurationMs > 0 && item.DurationMs >= sel.MaxDurationMs {
		return false
	}
	if sel.HasChapters != nil && *sel.HasChapters != hasChapters {
		return false
	}
	return true
}

// sortedChapters returns the chapters that start inside the item, ordered by
// start, one per start.
func sortedChapters(item Item) []domain.CatalogChapter {
	var out []domain.CatalogChapter
	for _, c := range item.Chapters {
		if c.StartMs >= 0 && c.StartMs < item.DurationMs {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	uniq := out[:0]
	for i, c := range out {
		if i == 0 || c.StartMs != out[i-1].StartMs {
			uniq = append(uniq, c)
		}
	}
	return uniq
}

// boundariesOf returns the chapter starts after 0, the places a cut can snap
// to. An item has chapters when this is not empty, that is when it has at
// least two.
func boundariesOf(chapters []domain.CatalogChapter) []int64 {
	var out []int64
	for _, c := range chapters {
		if c.StartMs > 0 {
			out = append(out, c.StartMs)
		}
	}
	return out
}

// nearest returns the boundary closest to t (the earlier one on a tie) and
// whether it lies within tol.
func nearest(boundaries []int64, t, tol int64) (int64, bool) {
	best, bestDist := int64(0), int64(-1)
	for _, b := range boundaries {
		d := b - t
		if d < 0 {
			d = -d
		}
		if bestDist < 0 || d < bestDist {
			best, bestDist = b, d
		}
	}
	return best, bestDist >= 0 && bestDist <= tol
}

func snapAll(cuts, boundaries []int64, tol int64) []int64 {
	if tol <= 0 || len(boundaries) == 0 {
		return cuts
	}
	out := make([]int64, len(cuts))
	for i, c := range cuts {
		if b, ok := nearest(boundaries, c, tol); ok {
			c = b
		}
		out[i] = c
	}
	return out
}

// roleCut resolves one chapter role to a cut offset.
func roleCut(role domain.ChapterRole, durationMs int64, chapters []domain.CatalogChapter, boundaries []int64, tol int64) (int64, bool) {
	switch role {
	case domain.ChapterRoleMiddle:
		mid := durationMs / 2
		if b, ok := nearest(boundaries, mid, tol); ok {
			return b, true
		}
		return mid, true
	case domain.ChapterRoleAfterIntro:
		return introEnd(chapters, durationMs)
	}
	return 0, false
}

// introNames are the chapter names (lower case, substring match) taken for an
// intro.
var introNames = []string{"intro", "opening", "prologue", "cold open", "teaser"}

// introEnd is the start of the chapter after the intro. The intro is the first
// chapter named like one (see introNames); with none, the first chapter, but
// only when it ends within DefaultIntroMaxMs: a longer first chapter is the
// film's first act, not an intro.
func introEnd(chapters []domain.CatalogChapter, durationMs int64) (int64, bool) {
	for i, c := range chapters {
		name := strings.ToLower(c.Name)
		for _, n := range introNames {
			if strings.Contains(name, n) {
				if i+1 < len(chapters) && chapters[i+1].StartMs < durationMs {
					return chapters[i+1].StartMs, true
				}
				return 0, false
			}
		}
	}
	if len(chapters) >= 2 && chapters[1].StartMs <= DefaultIntroMaxMs && chapters[1].StartMs < durationMs {
		return chapters[1].StartMs, true
	}
	return 0, false
}

// mergeGuards layers the set's guards, then the rule's, over the defaults field
// by field; a zero field keeps the lower layer.
func mergeGuards(base, set domain.CutGuards, rule *domain.CutGuards) domain.CutGuards {
	apply := func(g domain.CutGuards) {
		if g.MinSegmentMs > 0 {
			base.MinSegmentMs = g.MinSegmentMs
		}
		if g.MinFromStartMs > 0 {
			base.MinFromStartMs = g.MinFromStartMs
		}
		if g.MinFromEndMs > 0 {
			base.MinFromEndMs = g.MinFromEndMs
		}
		if g.MaxBreaks > 0 {
			base.MaxBreaks = g.MaxBreaks
		}
	}
	apply(set)
	if rule != nil {
		apply(*rule)
	}
	return base
}

// applyGuards sorts and de-duplicates the cuts, then drops, in this order,
// those outside [MinFromStart, duration-MinFromEnd], those closer than
// MinSegment to the previous kept cut, a last cut that leaves a final segment
// shorter than MinSegment, and everything after the first MaxBreaks cuts.
func applyGuards(raw []int64, durationMs int64, g domain.CutGuards) []int64 {
	cuts := append([]int64(nil), raw...)
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })
	out := []int64{}
	prev := int64(0)
	for _, c := range cuts {
		if c <= 0 || c >= durationMs || c < g.MinFromStartMs || c > durationMs-g.MinFromEndMs {
			continue
		}
		if c-prev < g.MinSegmentMs || c == prev {
			continue
		}
		out = append(out, c)
		prev = c
	}
	for len(out) > 0 && durationMs-out[len(out)-1] < g.MinSegmentMs {
		out = out[:len(out)-1]
	}
	if g.MaxBreaks > 0 && len(out) > g.MaxBreaks {
		out = out[:g.MaxBreaks]
	}
	return out
}

// Segments turns cuts into the lengths of the pieces they leave (one more than
// there are cuts).
func Segments(cutsMs []int64, durationMs int64) []int64 {
	out := make([]int64, 0, len(cutsMs)+1)
	prev := int64(0)
	for _, c := range cutsMs {
		out = append(out, c-prev)
		prev = c
	}
	return append(out, durationMs-prev)
}
