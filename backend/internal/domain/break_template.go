package domain

// BreakStepType says how one step of a BreakTemplate draws from its pool.
type BreakStepType string

const (
	// BreakStepSingle airs exactly one item of the pool.
	BreakStepSingle BreakStepType = "single"
	// BreakStepFill airs whole items of the pool until TargetMs is filled.
	BreakStepFill BreakStepType = "fill"
)

// Limits on a break template, validated on write by the API and MCP. They keep
// a full-window regeneration bounded: a template is expanded at every program
// boundary of an endless sequence.
const (
	MaxBreakSteps        = 10
	MaxBreakTargetMs     = int64(60 * 60 * 1000)
	MaxBreakTemplateName = 100
)

// BreakTemplateStep is one step of a BreakTemplate, aired in Position order.
type BreakTemplateStep struct {
	ID              string            `json:"id"`
	BreakTemplateID string            `json:"break_template_id"`
	Position        int               `json:"position"`
	StepType        BreakStepType     `json:"step_type"`
	FillerPoolID    string            `json:"filler_pool_id"`
	Selection       SelectionStrategy `json:"selection_strategy"`
	// TargetMs ('fill' steps) is the length the step fills with whole items; it
	// is a fixed length chosen by the author, never derived from the slot. 0 for
	// a 'single' step.
	TargetMs int64 `json:"target_ms"`
	// MaxFitAttempts ('fill' steps): see FillerRule.MaxFitAttempts.
	MaxFitAttempts int `json:"max_fit_attempts"`
}

// BreakTemplate replaces the single item a preroll, midroll or postroll rule
// inserts at a program boundary with an ordered list of steps. It is scoped
// like a FillerRule (channel or block) and Kind names the boundary it attaches
// to. The entries it emits keep that kind (preroll/midroll/postroll), so the
// guide still hides them.
type BreakTemplate struct {
	ID       string      `json:"id"`
	TenantID string      `json:"tenant_id"`
	Scope    FillerScope `json:"scope_type"`
	ScopeID  string      `json:"scope_id"`
	Kind     FillerKind  `json:"kind"`
	Name     string      `json:"name"`
	// Position orders templates sharing Scope+Kind: the lowest wins.
	Position  int                 `json:"position"`
	CreatedAt int64               `json:"created_at"`
	Steps     []BreakTemplateStep `json:"steps"`
}
