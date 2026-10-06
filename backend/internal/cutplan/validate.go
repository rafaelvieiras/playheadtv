package cutplan

import (
	"errors"
	"fmt"
	"math"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// ValidationError is a problem with a rule set the author sent (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// ValidateConfig checks a rule list against the model's bounds: every strategy
// is well formed, phases are ordered and do not overlap, selectors can match
// something, and no rule is shadowed by an earlier one. Messages name the rule
// ("rule 2: ...") so the editor can show them as they are.
func ValidateConfig(cfg domain.CutConfig) error {
	if len(cfg.Rules) > domain.MaxCutRules {
		return invalid("rules must hold at most %d rules", domain.MaxCutRules)
	}
	if err := validateGuards(cfg.Guards); err != nil {
		return invalid("guards: %s", err.Error())
	}
	if len(cfg.Rules) == 0 && cfg.Default == nil {
		return invalid("add at least one rule or a default strategy")
	}
	for i, r := range cfg.Rules {
		n := i + 1
		if len([]rune(r.Name)) > domain.MaxCutRuleName {
			return invalid("rule %d: name must be at most %d characters", n, domain.MaxCutRuleName)
		}
		if err := validateSelector(r.Match); err != nil {
			return invalid("rule %d: %s", n, err.Error())
		}
		if err := validateStrategy(r.Strategy, true); err != nil {
			return invalid("rule %d: %s", n, err.Error())
		}
		if r.SnapToleranceMs < 0 || r.SnapToleranceMs > domain.MaxCutSnapToleranceMs {
			return invalid("rule %d: snap_tolerance_ms must be between 0 and %d", n, domain.MaxCutSnapToleranceMs)
		}
		if r.Guards != nil {
			if err := validateGuards(*r.Guards); err != nil {
				return invalid("rule %d: guards: %s", n, err.Error())
			}
		}
		for j := 0; j < i; j++ {
			if covers(cfg.Rules[j].Match, r.Match) {
				return invalid("rule %d can never match: rule %d already matches every item it would", n, j+1)
			}
		}
	}
	if cfg.Default != nil {
		if err := validateStrategy(*cfg.Default, true); err != nil {
			return invalid("default: %s", err.Error())
		}
	}
	return nil
}

func validateGuards(g domain.CutGuards) error {
	const day = int64(24 * 60 * 60 * 1000)
	for name, v := range map[string]int64{"min_segment_ms": g.MinSegmentMs, "min_from_start_ms": g.MinFromStartMs, "min_from_end_ms": g.MinFromEndMs} {
		if v < 0 || v > day {
			return invalid("%s must be between 0 and %d", name, day)
		}
	}
	if g.MaxBreaks < 0 || g.MaxBreaks > domain.MaxCutBreaksLimit {
		return invalid("max_breaks must be between 0 and %d", domain.MaxCutBreaksLimit)
	}
	return nil
}

func validateSelector(s domain.CutSelector) error {
	seen := map[domain.CatalogItemKind]bool{}
	for _, k := range s.Kinds {
		switch k {
		case domain.CatalogMovie, domain.CatalogEpisode, domain.CatalogOther:
		default:
			return invalid("kinds must be among movie, episode, other")
		}
		if seen[k] {
			return invalid("kinds lists %s twice", k)
		}
		seen[k] = true
	}
	if s.MinDurationMs < 0 || s.MaxDurationMs < 0 {
		return invalid("duration bounds cannot be negative")
	}
	if s.MinDurationMs > 0 && s.MaxDurationMs > 0 && s.MinDurationMs >= s.MaxDurationMs {
		return invalid("min_duration_ms must be below max_duration_ms")
	}
	return nil
}

// covers reports whether every item matching b also matches a (so b, listed
// after a, could never be reached).
func covers(a, b domain.CutSelector) bool {
	if len(a.Kinds) > 0 {
		if len(b.Kinds) == 0 {
			return false
		}
		for _, k := range b.Kinds {
			found := false
			for _, ak := range a.Kinds {
				found = found || ak == k
			}
			if !found {
				return false
			}
		}
	}
	if a.MinDurationMs > b.MinDurationMs {
		return false
	}
	if a.MaxDurationMs > 0 && (b.MaxDurationMs == 0 || b.MaxDurationMs > a.MaxDurationMs) {
		return false
	}
	if a.HasChapters != nil && (b.HasChapters == nil || *a.HasChapters != *b.HasChapters) {
		return false
	}
	return true
}

// validateStrategy checks one strategy. top is false inside a chapter
// strategy's fallback, where 'chapter' is not allowed.
func validateStrategy(s domain.CutStrategy, top bool) error {
	has := func(set bool, field string) error {
		if set {
			return invalid("%s does not apply to a %s strategy", field, s.Type)
		}
		return nil
	}
	stray := func(every, pct, off, phases, roles, fb bool) error {
		for _, c := range []struct {
			set  bool
			name string
			bad  bool
		}{
			{s.EveryMs != 0, "every_ms", every}, {len(s.Percentages) > 0, "percentages", pct},
			{len(s.OffsetsMs) > 0, "offsets_ms", off}, {len(s.Phases) > 0, "phases", phases},
			{len(s.Roles) > 0, "roles", roles}, {s.Fallback != nil, "fallback", fb},
		} {
			if c.bad {
				if err := has(c.set, c.name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	switch s.Type {
	case domain.CutNone:
		return stray(true, true, true, true, true, true)
	case domain.CutInterval:
		if err := stray(false, true, true, true, true, true); err != nil {
			return err
		}
		return validEvery(s.EveryMs)
	case domain.CutPercentage:
		if err := stray(true, false, true, true, true, true); err != nil {
			return err
		}
		if len(s.Percentages) == 0 || len(s.Percentages) > domain.MaxCutPercentages {
			return invalid("percentages must hold 1 to %d values", domain.MaxCutPercentages)
		}
		for i, p := range s.Percentages {
			if math.IsNaN(p) || p <= 0 || p >= 100 {
				return invalid("percentages must be above 0 and below 100")
			}
			if i > 0 && p <= s.Percentages[i-1] {
				return invalid("percentages must be strictly ascending")
			}
		}
	case domain.CutOffsets:
		if err := stray(true, true, false, true, true, true); err != nil {
			return err
		}
		if len(s.OffsetsMs) == 0 || len(s.OffsetsMs) > domain.MaxCutOffsets {
			return invalid("offsets_ms must hold 1 to %d values", domain.MaxCutOffsets)
		}
		for i, o := range s.OffsetsMs {
			if o <= 0 {
				return invalid("offsets_ms must be positive")
			}
			if i > 0 && o <= s.OffsetsMs[i-1] {
				return invalid("offsets_ms must be strictly ascending")
			}
		}
	case domain.CutPhased:
		if err := stray(true, true, true, false, true, true); err != nil {
			return err
		}
		return validatePhases(s.Phases)
	case domain.CutChapter:
		if !top {
			return invalid("a fallback cannot be a chapter strategy")
		}
		if err := stray(true, true, true, true, false, false); err != nil {
			return err
		}
		if len(s.Roles) == 0 {
			return invalid("roles must hold at least one of middle, after_intro")
		}
		seen := map[domain.ChapterRole]bool{}
		for _, r := range s.Roles {
			switch r {
			case domain.ChapterRoleMiddle, domain.ChapterRoleAfterIntro:
			default:
				return invalid("roles must be among middle, after_intro")
			}
			if seen[r] {
				return invalid("roles lists %s twice", r)
			}
			seen[r] = true
		}
		if s.Fallback != nil {
			if err := validateStrategy(s.Fallback.Strategy(), false); err != nil {
				return invalid("fallback: %s", err.Error())
			}
		}
	default:
		return invalid("strategy type must be one of none, interval, percentage, offsets, phased, chapter")
	}
	return nil
}

func validEvery(every int64) error {
	if every < domain.MinCutIntervalMs || every > domain.MaxCutIntervalMs {
		return invalid("every_ms must be between %d and %d", domain.MinCutIntervalMs, domain.MaxCutIntervalMs)
	}
	return nil
}

// validatePhases checks a phased cadence: every phase but the last ends at an
// offset from the start (until_ms) or at a distance from the end
// (until_before_end_ms), the last one sets neither, and the ends are strictly
// ordered so no two phases overlap. All the bounded phases use the same kind of
// end: mixing offsets from the start with distances from the end would make
// the order depend on the item's duration.
func validatePhases(phases []domain.CutPhase) error {
	if len(phases) == 0 || len(phases) > domain.MaxCutPhases {
		return invalid("phases must hold 1 to %d phases", domain.MaxCutPhases)
	}
	var prevKind string
	var prevBound int64
	for i, p := range phases {
		n := i + 1
		if err := validEvery(p.EveryMs); err != nil {
			return invalid("phase %d: %s", n, err.Error())
		}
		last := i == len(phases)-1
		if p.UntilMs < 0 || p.UntilBeforeEndMs < 0 {
			return invalid("phase %d: a bound cannot be negative", n)
		}
		if p.UntilMs > 0 && p.UntilBeforeEndMs > 0 {
			return invalid("phase %d: set until_ms or until_before_end_ms, not both", n)
		}
		if last {
			if p.UntilMs > 0 || p.UntilBeforeEndMs > 0 {
				return invalid("phase %d: the last phase runs to the end of the item and takes no bound", n)
			}
			break
		}
		if p.UntilMs == 0 && p.UntilBeforeEndMs == 0 {
			return invalid("phase %d: set until_ms or until_before_end_ms (only the last phase is unbounded)", n)
		}
		kind, bound := "until_ms", p.UntilMs
		if p.UntilBeforeEndMs > 0 {
			kind, bound = "until_before_end_ms", p.UntilBeforeEndMs
		}
		if prevKind != "" && prevKind != kind {
			return invalid("phase %d: bounds must all be offsets from the start or all distances from the end", n)
		}
		if prevKind != "" {
			if kind == "until_ms" && bound <= prevBound {
				return invalid("phase %d overlaps phase %d: until_ms must be above %d", n, n-1, prevBound)
			}
			if kind == "until_before_end_ms" && bound >= prevBound {
				return invalid("phase %d overlaps phase %d: until_before_end_ms must be below %d", n, n-1, prevBound)
			}
		}
		prevKind, prevBound = kind, bound
	}
	return nil
}
