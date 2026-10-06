package materializer

import (
	"context"
	"fmt"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// breakTemplateColumns/breakStepColumns alias the columns whose DB name does
// not spell out to the struct field (see AGENTS.md, scan.go).
const breakTemplateColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, name, position, created_at"
const breakStepColumns = "id, break_template_id, position, step_type, filler_pool_id, selection_strategy AS selection, target_ms, max_fit_attempts"

// loadBreakTemplates reads the break templates scoped to the channel or to any
// of the given blocks, each with its steps in order.
func (m *Materializer) loadBreakTemplates(ctx context.Context, tenantID, channelID string, blockIDs map[string]struct{}) ([]domain.BreakTemplate, error) {
	query := "SELECT " + breakTemplateColumns + " FROM break_template WHERE tenant_id = ? AND ((scope_type = 'channel' AND scope_id = ?)"
	args := []any{tenantID, channelID}
	for blockID := range blockIDs {
		query += " OR (scope_type = 'block' AND scope_id = ?)"
		args = append(args, blockID)
	}
	query += ") ORDER BY created_at, id"
	var templates []domain.BreakTemplate
	if err := m.db.Query(ctx, &templates, query, args...); err != nil {
		return nil, fmt.Errorf("list break_template: %w", err)
	}
	if len(templates) == 0 {
		return nil, nil
	}
	ids := make([]any, len(templates))
	index := make(map[string]int, len(templates))
	for i, t := range templates {
		ids[i] = t.ID
		index[t.ID] = i
	}
	var steps []domain.BreakTemplateStep
	stepQuery := "SELECT " + breakStepColumns + " FROM break_template_step WHERE break_template_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ") ORDER BY position, rowid"
	if err := m.db.Query(ctx, &steps, stepQuery, ids...); err != nil {
		return nil, fmt.Errorf("list break_template_step: %w", err)
	}
	for _, st := range steps {
		i := index[st.BreakTemplateID]
		templates[i].Steps = append(templates[i].Steps, st)
	}
	return templates, nil
}

// maxFillStepParts bounds the items one 'fill' step of a break template airs
// in a single break, so a pool of near-zero-length items cannot turn one
// break into millions of entries. It is generous for real use (an hour of
// 4-second spots is 900 items). The looping fills' own cap (maxLoopEntries)
// counts every part a template emits, since each one is an entry.
const maxFillStepParts = 1000

// breakPart is one filler item of a break: a plain rule's break is one part, a
// template's break is the parts of all its steps in order.
type breakPart struct {
	item domain.FillerPoolItem
	dur  int64
}

// breakParts is the whole break to air at one boundary.
type breakParts []breakPart

// total is the length of the break in milliseconds.
func (b breakParts) total() int64 {
	var t int64
	for _, p := range b {
		t += p.dur
	}
	return t
}

// resolveFillerRuleAt picks the lowest-position rule of kind scoped exactly to
// (scope, scopeID), nil when there is none.
func resolveFillerRuleAt(rules []domain.FillerRule, kind domain.FillerKind, scope domain.FillerScope, scopeID string) *domain.FillerRule {
	var candidates []domain.FillerRule
	for _, r := range rules {
		if r.Kind == kind && r.Scope == scope && r.ScopeID == scopeID {
			candidates = append(candidates, r)
		}
	}
	return lowestPosition(candidates)
}

// resolveTemplateAt picks the lowest-position template of kind scoped exactly
// to (scope, scopeID) that has at least one step, nil when there is none.
func resolveTemplateAt(templates []domain.BreakTemplate, kind domain.FillerKind, scope domain.FillerScope, scopeID string) *domain.BreakTemplate {
	var best *domain.BreakTemplate
	for i := range templates {
		t := &templates[i]
		if t.Kind != kind || t.Scope != scope || t.ScopeID != scopeID || len(t.Steps) == 0 {
			continue
		}
		if best == nil || t.Position < best.Position {
			best = t
		}
	}
	return best
}

// resolveBreak decides what airs at a preroll, midroll or postroll boundary:
// a break template, or a plain filler rule (the one-step case). Precedence:
//
//  1. the block's own configuration beats the channel's: a block-scoped
//     template or rule of the kind settles it, and nothing at channel scope is
//     looked at;
//  2. within one scope level a template beats a plain rule of the same kind;
//  3. among several templates (or rules) at one level, the lowest position wins.
//
// At most one of the two results is non-nil; both are nil when nothing applies.
// The plain-rule path is exactly resolveFillerRule.
func resolveBreak(rules []domain.FillerRule, templates []domain.BreakTemplate, kind domain.FillerKind, blockID, channelID string) (*domain.FillerRule, *domain.BreakTemplate) {
	if blockID != "" {
		if t := resolveTemplateAt(templates, kind, domain.ScopeBlock, blockID); t != nil {
			return nil, t
		}
		if r := resolveFillerRuleAt(rules, kind, domain.ScopeBlock, blockID); r != nil {
			return r, nil
		}
	}
	if t := resolveTemplateAt(templates, kind, domain.ScopeChannel, channelID); t != nil {
		return nil, t
	}
	return resolveFillerRuleAt(rules, kind, domain.ScopeChannel, channelID), nil
}

// templateStepRule is the filler rule a step stands for: the step's pool,
// strategy and fit attempts, keyed by the step id so a "sequential" rotation is
// per step.
func templateStepRule(s domain.BreakTemplateStep) *domain.FillerRule {
	return &domain.FillerRule{ID: s.ID, FillerPoolID: s.FillerPoolID, Selection: s.Selection, MaxFitAttempts: s.MaxFitAttempts}
}

// stepSource is the per-step state a template keeps while one materialization
// walks it: the rule and, for a fill step, its picker (built once, since
// reading the pool's shortest item costs a pass over it).
type stepSource struct {
	step   domain.BreakTemplateStep
	rule   *domain.FillerRule
	picker *fitPicker
}

// pickBreak draws the break to air at the next boundary: every step of the
// template in order, or the one item of the plain rule. A nil result airs
// nothing (no rule, empty pools).
//
// A 'single' step airs one item exactly as a plain rule does (an empty pool or
// a non-positive duration skips the step). A 'fill' step airs whole items until
// its target is filled, through the same fit logic as the tail and fallback
// fills, so max_fit_attempts applies; what no candidate can fill stays unfilled
// (the break is just shorter), and the block's tail absorbs the difference
// at the end of the slot.
func (m *midrollSource) pickBreak() breakParts {
	if m == nil {
		return nil
	}
	if m.tmpl == nil {
		item, dur, ok := pickFiller(m.rule, m.pools, m.durations, m.state)
		if !ok {
			return nil
		}
		return breakParts{{item: item, dur: dur}}
	}
	if m.steps == nil {
		m.steps = make([]*stepSource, len(m.tmpl.Steps))
		for i, s := range m.tmpl.Steps {
			m.steps[i] = &stepSource{step: s, rule: templateStepRule(s)}
		}
	}
	var parts breakParts
	for _, ss := range m.steps {
		switch ss.step.StepType {
		case domain.BreakStepFill:
			pool := m.pools[ss.step.FillerPoolID]
			if len(pool) == 0 {
				continue
			}
			if ss.picker == nil {
				ss.picker = newFitPicker(ss.rule, pool, m.durations, m.state)
			}
			remaining := ss.step.TargetMs
			for n := 0; n < maxFillStepParts; n++ {
				item, dur, ok := ss.picker.pick(remaining)
				if !ok {
					break
				}
				parts = append(parts, breakPart{item: item, dur: dur})
				remaining -= dur
			}
		default: // BreakStepSingle
			if item, dur, ok := pickFiller(ss.rule, m.pools, m.durations, m.state); ok {
				parts = append(parts, breakPart{item: item, dur: dur})
			}
		}
	}
	return parts
}

// worstCase is the longest break the source can air, for the filler-ratio
// warning: the longest pool item for a plain rule; for a template the sum over
// its steps of the longest item (single) or the target (fill, when some item of
// the pool fits it).
func (m *midrollSource) worstCase() int64 {
	if m == nil {
		return 0
	}
	longest := func(poolID string, limit int64) int64 {
		var l int64
		for _, it := range m.pools[poolID] {
			if d := m.durations[it.CatalogItemID]; d > l && (limit == 0 || d <= limit) {
				l = d
			}
		}
		return l
	}
	if m.tmpl == nil {
		if m.rule == nil {
			return 0
		}
		return longest(m.rule.FillerPoolID, 0)
	}
	var sum int64
	for _, s := range m.tmpl.Steps {
		if s.StepType == domain.BreakStepFill {
			if longest(s.FillerPoolID, s.TargetMs) > 0 {
				sum += s.TargetMs
			}
			continue
		}
		sum += longest(s.FillerPoolID, 0)
	}
	return sum
}

// breakSource resolves the break source of one boundary kind for a block.
func (in channelInput) breakSource(kind domain.FillerKind, blockID string, state *selectionState) *midrollSource {
	rule, tmpl := resolveBreak(in.FillerRules, in.BreakTemplates, kind, blockID, in.ChannelID)
	return &midrollSource{rule: rule, tmpl: tmpl, pools: in.PoolItems, durations: in.CatalogDuration, state: state}
}
