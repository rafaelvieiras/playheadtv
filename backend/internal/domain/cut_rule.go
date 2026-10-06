package domain

// CutStrategyType names how a CutStrategy produces cut offsets.
type CutStrategyType string

const (
	// CutNone is an explicit "do not cut" chosen by the author.
	CutNone CutStrategyType = "none"
	// CutInterval cuts every EveryMs from the start of the item.
	CutInterval CutStrategyType = "interval"
	// CutPercentage cuts at each of Percentages of the item duration.
	CutPercentage CutStrategyType = "percentage"
	// CutOffsets cuts at the explicit OffsetsMs.
	CutOffsets CutStrategyType = "offsets"
	// CutPhased runs an ordered list of interval Phases.
	CutPhased CutStrategyType = "phased"
	// CutChapter cuts at chapter roles (Roles), snapped to chapter boundaries.
	CutChapter CutStrategyType = "chapter"
)

// ChapterRole is a position in the item a chapter strategy cuts at.
type ChapterRole string

const (
	// ChapterRoleMiddle cuts at the chapter boundary nearest the middle of
	// the item (the middle itself when no boundary is within the tolerance).
	ChapterRoleMiddle ChapterRole = "middle"
	// ChapterRoleAfterIntro cuts right after the intro chapter.
	ChapterRoleAfterIntro ChapterRole = "after_intro"
)

// Limits on a cut rule set, validated on write by the API and MCP.
const (
	MaxCutRules           = 20
	MaxCutRuleSetName     = 100
	MaxCutRuleName        = 100
	MaxCutPercentages     = 10
	MaxCutOffsets         = 20
	MaxCutPhases          = 5
	MaxCutBreaksLimit     = 50
	MinCutIntervalMs      = int64(60 * 1000)
	MaxCutIntervalMs      = int64(24 * 60 * 60 * 1000)
	MaxCutSnapToleranceMs = int64(30 * 60 * 1000)
)

// CutPhase is one stage of a phased cadence: cut every EveryMs from where the
// previous phase ended until this phase ends. Exactly one of UntilMs (an
// offset from the start of the item) and UntilBeforeEndMs (a distance from
// its end) ends a phase, except for the last phase, which runs to the end of
// the item and sets neither.
type CutPhase struct {
	EveryMs          int64 `json:"every_ms"`
	UntilMs          int64 `json:"until_ms,omitempty"`
	UntilBeforeEndMs int64 `json:"until_before_end_ms,omitempty"`
}

// CutStrategy says where the cuts of one item fall. Only the fields of its
// Type are used.
type CutStrategy struct {
	Type        CutStrategyType `json:"type"`
	EveryMs     int64           `json:"every_ms,omitempty"`
	Percentages []float64       `json:"percentages,omitempty"`
	OffsetsMs   []int64         `json:"offsets_ms,omitempty"`
	Phases      []CutPhase      `json:"phases,omitempty"`
	Roles       []ChapterRole   `json:"roles,omitempty"`
	// Fallback ('chapter' only) is the strategy used when the item has no
	// chapters or no role resolved.
	Fallback *CutFallback `json:"fallback,omitempty"`
}

// CutFallback is a CutStrategy that is not a chapter strategy and has no
// fallback of its own (a separate type so the strategy schema is not
// recursive).
type CutFallback struct {
	Type        CutStrategyType `json:"type"`
	EveryMs     int64           `json:"every_ms,omitempty"`
	Percentages []float64       `json:"percentages,omitempty"`
	OffsetsMs   []int64         `json:"offsets_ms,omitempty"`
	Phases      []CutPhase      `json:"phases,omitempty"`
}

// Strategy returns the fallback as the plain strategy it stands for.
func (f CutFallback) Strategy() CutStrategy {
	return CutStrategy{Type: f.Type, EveryMs: f.EveryMs, Percentages: f.Percentages, OffsetsMs: f.OffsetsMs, Phases: f.Phases}
}

// CutSelector says which items a CutRule applies to; every set field must
// match. The duration range is [MinDurationMs, MaxDurationMs): a zero bound is
// open.
type CutSelector struct {
	Kinds         []CatalogItemKind `json:"kinds,omitempty"`
	MinDurationMs int64             `json:"min_duration_ms,omitempty"`
	MaxDurationMs int64             `json:"max_duration_ms,omitempty"`
	// HasChapters, when set, requires the item to have (true) or lack (false)
	// at least two chapters, which is what gives a boundary to cut at.
	HasChapters *bool `json:"has_chapters,omitempty"`
}

// CutGuards bound the cuts of an item. A zero field takes its default
// (cutplan.DefaultGuards); a rule's guards override the set's field by field.
type CutGuards struct {
	// MinSegmentMs is the shortest stretch of the item between two cuts, or
	// between a cut and the start or end.
	MinSegmentMs int64 `json:"min_segment_ms,omitempty"`
	// MinFromStartMs and MinFromEndMs keep a cut away from the edges.
	MinFromStartMs int64 `json:"min_from_start_ms,omitempty"`
	MinFromEndMs   int64 `json:"min_from_end_ms,omitempty"`
	// MaxBreaks caps the cuts of one item (the earliest are kept).
	MaxBreaks int `json:"max_breaks,omitempty"`
}

// CutRule is one entry of an ordered rule list; the first rule whose selector
// matches an item decides its cuts.
type CutRule struct {
	Name     string      `json:"name,omitempty"`
	Match    CutSelector `json:"match,omitempty"`
	Strategy CutStrategy `json:"strategy"`
	// SnapToleranceMs moves each cut to the nearest chapter boundary within
	// this distance when the item has chapters; 0 leaves the cuts alone (the
	// 'chapter' strategy defaults to 5 minutes).
	SnapToleranceMs int64      `json:"snap_tolerance_ms,omitempty"`
	Guards          *CutGuards `json:"guards,omitempty"`
}

// CutConfig is the rule list of a rule set, the part cutplan evaluates.
type CutConfig struct {
	Rules []CutRule `json:"rules"`
	// Default decides the items no rule matches; without it they are not cut.
	Default *CutStrategy `json:"default,omitempty"`
	Guards  CutGuards    `json:"guards"`
}

// CutRuleSet is a CutConfig stored for a channel or a block, scoped like a
// FillerRule. Among the sets of one scope the lowest Position wins; a block's
// own set beats its channel's.
type CutRuleSet struct {
	ID        string      `json:"id"`
	TenantID  string      `json:"tenant_id"`
	Scope     FillerScope `json:"scope_type"`
	ScopeID   string      `json:"scope_id"`
	Name      string      `json:"name"`
	Position  int         `json:"position"`
	CreatedAt int64       `json:"created_at"`
	CutConfig
}
