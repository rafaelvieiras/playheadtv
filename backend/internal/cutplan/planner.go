package cutplan

import (
	"sort"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// Planner adapts a stored rule set to the cut planner contract the
// materializer consumes once an epg entry can start at an offset into its item
// (item_offset_ms): given an item and the duration it airs for, return the
// ordered cut offsets, empty for no cut.
//
//	CutOffsets(item domain.CatalogItem, durationMs int64) []int64
//
// durationMs is the effective duration of the occurrence (after a block's
// trim), not necessarily item.DurationMs; offsets are in milliseconds from the
// start of what airs, strictly ascending. Chapters are read from
// item.Chapters and media segments from item.Segments, which the catalog list
// queries leave empty: a caller that wants chapter- or segment-aware cuts loads
// the item detail first (without them the planner takes the no-data path of the
// fallback chain, never an error).
//
// The method is a pure function of its inputs, so planning the same occurrence
// in two regenerations gives identical offsets.
type Planner struct {
	Config domain.CutConfig
}

// NewPlanner returns the Planner of a stored rule set.
func NewPlanner(set domain.CutRuleSet) Planner { return Planner{Config: set.CutConfig} }

// CutOffsets implements the planner contract described on Planner.
func (p Planner) CutOffsets(item domain.CatalogItem, durationMs int64) []int64 {
	return p.Plan(Item{Kind: item.Kind, DurationMs: durationMs, Chapters: item.Chapters, Segments: item.Segments}).CutsMs
}

// UsesSegments reports whether any rule of cfg can read an item's media
// segments: a chapter strategy with the after_intro role, as a rule or as the
// default. A caller that loads segments from storage skips the read otherwise.
func UsesSegments(cfg domain.CutConfig) bool {
	afterIntro := func(s *domain.CutStrategy) bool {
		if s == nil || s.Type != domain.CutChapter {
			return false
		}
		for _, r := range s.Roles {
			if r == domain.ChapterRoleAfterIntro {
				return true
			}
		}
		return false
	}
	if afterIntro(cfg.Default) {
		return true
	}
	for i := range cfg.Rules {
		if afterIntro(&cfg.Rules[i].Strategy) {
			return true
		}
	}
	return false
}

// Plan is CutOffsets with the explanation (rule, source, notes) kept.
func (p Planner) Plan(item Item) Plan { return PlanItem(p.Config, item) }

// PickSet returns the rule set that governs an item airing in blockID of
// channelID: a block's own set beats its channel's, and among the sets of one
// scope the lowest Position wins (ties by creation time, then id). nil when
// neither scope has a set.
func PickSet(sets []domain.CutRuleSet, channelID, blockID string) *domain.CutRuleSet {
	pick := func(scope domain.FillerScope, id string) *domain.CutRuleSet {
		var cand []domain.CutRuleSet
		for _, s := range sets {
			if s.Scope == scope && s.ScopeID == id && id != "" {
				cand = append(cand, s)
			}
		}
		if len(cand) == 0 {
			return nil
		}
		sort.SliceStable(cand, func(i, j int) bool {
			a, b := cand[i], cand[j]
			if a.Position != b.Position {
				return a.Position < b.Position
			}
			if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt < b.CreatedAt
			}
			return a.ID < b.ID
		})
		return &cand[0]
	}
	if s := pick(domain.ScopeBlock, blockID); s != nil {
		return s
	}
	return pick(domain.ScopeChannel, channelID)
}
