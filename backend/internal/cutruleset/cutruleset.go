// Package cutruleset validates and stores cut rule sets (see
// domain.CutRuleSet) and answers previews. The REST handlers and the MCP tools
// both go through it, so limits and error messages are the same on both
// surfaces. The rules themselves are evaluated by internal/cutplan.
package cutruleset

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutplan"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// setColumns aliases scope_type and config to the row struct's field names.
const setColumns = "id, tenant_id, scope_type AS scope, scope_id, name, position, config AS config_json, created_at"

// row is a cut_rule_set row; ConfigJSON is decoded into the domain type.
type row struct {
	ID         string
	TenantID   string
	Scope      string
	ScopeID    string
	Name       string
	Position   int
	ConfigJSON string
	CreatedAt  int64
}

// Input is a rule set as a client sends it, on create and on replace.
type Input struct {
	ScopeType string              `json:"scope_type" jsonschema:"one of: channel, block"`
	ScopeID   string              `json:"scope_id" jsonschema:"the Channel.ID or Block.ID this rule set applies to, per scope_type"`
	Name      string              `json:"name,omitempty" jsonschema:"optional display name"`
	Position  int                 `json:"position,omitempty" jsonschema:"ordering among rule sets of the same scope; the lowest wins"`
	Rules     []domain.CutRule    `json:"rules" jsonschema:"ordered rules (at most 20); the first whose match selector fits an item decides its cuts. A rule is {name?, match:{kinds?:[movie|episode|other], min_duration_ms?, max_duration_ms?, has_chapters?}, strategy:{type: none|interval|percentage|offsets|phased|chapter, every_ms?, percentages?, offsets_ms?, phases?:[{every_ms, until_ms?|until_before_end_ms?}], roles?:[middle|after_intro], fallback?}, snap_tolerance_ms?, guards?}"`
	Default   *domain.CutStrategy `json:"default,omitempty" jsonschema:"strategy for items no rule matches; without it those items are not cut"`
	Guards    domain.CutGuards    `json:"guards,omitempty" jsonschema:"guard rails for every rule: min_segment_ms, min_from_start_ms, min_from_end_ms (default 3 minutes each) and max_breaks (default 10, at most 50); 0 takes the default"`
}

// Config returns the evaluated part of the input.
func (in Input) Config() domain.CutConfig {
	rules := in.Rules
	if rules == nil {
		rules = []domain.CutRule{}
	}
	return domain.CutConfig{Rules: rules, Default: in.Default, Guards: in.Guards}
}

// Validate checks in against the model's limits.
func Validate(in Input) error {
	switch domain.FillerScope(in.ScopeType) {
	case domain.ScopeChannel, domain.ScopeBlock:
	default:
		return &cutplan.ValidationError{Msg: "scope_type must be one of channel, block"}
	}
	if in.ScopeID == "" {
		return &cutplan.ValidationError{Msg: "scope_id is required"}
	}
	if utf8.RuneCountInString(in.Name) > domain.MaxCutRuleSetName {
		return &cutplan.ValidationError{Msg: "name is too long"}
	}
	return cutplan.ValidateConfig(in.Config())
}

// IsValidation reports whether err is a validation problem (HTTP 400).
func IsValidation(err error) bool { return cutplan.IsValidation(err) }

func toDomain(r row) (domain.CutRuleSet, error) {
	set := domain.CutRuleSet{
		ID: r.ID, TenantID: r.TenantID, Scope: domain.FillerScope(r.Scope), ScopeID: r.ScopeID,
		Name: r.Name, Position: r.Position, CreatedAt: r.CreatedAt,
	}
	if err := json.Unmarshal([]byte(r.ConfigJSON), &set.CutConfig); err != nil {
		return domain.CutRuleSet{}, err
	}
	if set.Rules == nil {
		set.Rules = []domain.CutRule{}
	}
	return set, nil
}

func encode(in Input) (string, error) {
	b, err := json.Marshal(in.Config())
	return string(b), err
}

// Create validates and stores a new rule set.
func Create(ctx context.Context, db storage.DB, tenantID string, in Input) (domain.CutRuleSet, error) {
	if err := Validate(in); err != nil {
		return domain.CutRuleSet{}, err
	}
	config, err := encode(in)
	if err != nil {
		return domain.CutRuleSet{}, err
	}
	id := uuid.NewString()
	if err := db.Exec(ctx,
		`INSERT INTO cut_rule_set (id, tenant_id, scope_type, scope_id, name, position, config, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tenantID, in.ScopeType, in.ScopeID, strings.TrimSpace(in.Name), in.Position, config, time.Now().UnixMilli(),
	); err != nil {
		return domain.CutRuleSet{}, err
	}
	return Get(ctx, db, tenantID, id)
}

// Replace validates in and replaces the whole rule set (a PUT). It reports
// sql.ErrNoRows for an unknown id.
func Replace(ctx context.Context, db storage.DB, tenantID, id string, in Input) (domain.CutRuleSet, error) {
	if _, err := Get(ctx, db, tenantID, id); err != nil {
		return domain.CutRuleSet{}, err
	}
	if err := Validate(in); err != nil {
		return domain.CutRuleSet{}, err
	}
	config, err := encode(in)
	if err != nil {
		return domain.CutRuleSet{}, err
	}
	if err := db.Exec(ctx,
		`UPDATE cut_rule_set SET scope_type = ?, scope_id = ?, name = ?, position = ?, config = ? WHERE tenant_id = ? AND id = ?`,
		in.ScopeType, in.ScopeID, strings.TrimSpace(in.Name), in.Position, config, tenantID, id,
	); err != nil {
		return domain.CutRuleSet{}, err
	}
	return Get(ctx, db, tenantID, id)
}

// Get returns one rule set; sql.ErrNoRows when unknown.
func Get(ctx context.Context, db storage.DB, tenantID, id string) (domain.CutRuleSet, error) {
	var r row
	if err := db.Query(ctx, &r, "SELECT "+setColumns+" FROM cut_rule_set WHERE tenant_id = ? AND id = ?", tenantID, id); err != nil {
		return domain.CutRuleSet{}, err
	}
	return toDomain(r)
}

// List returns every rule set of the tenant, oldest first.
func List(ctx context.Context, db storage.DB, tenantID string) ([]domain.CutRuleSet, error) {
	var rows []row
	if err := db.Query(ctx, &rows, "SELECT "+setColumns+" FROM cut_rule_set WHERE tenant_id = ? ORDER BY created_at, id", tenantID); err != nil {
		return nil, err
	}
	out := make([]domain.CutRuleSet, 0, len(rows))
	for _, r := range rows {
		set, err := toDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, nil
}

// Delete removes a rule set; sql.ErrNoRows when unknown.
func Delete(ctx context.Context, db storage.DB, tenantID, id string) error {
	if _, err := Get(ctx, db, tenantID, id); err != nil {
		return err
	}
	return db.Exec(ctx, "DELETE FROM cut_rule_set WHERE tenant_id = ? AND id = ?", tenantID, id)
}

// PreviewInput is a rule list plus a sample item to run it on.
type PreviewInput struct {
	Rules      []domain.CutRule        `json:"rules" jsonschema:"the rules to evaluate, as in create_cut_rule_set"`
	Default    *domain.CutStrategy     `json:"default,omitempty"`
	Guards     domain.CutGuards        `json:"guards,omitempty"`
	Kind       string                  `json:"kind" jsonschema:"the sample item's kind: movie, episode or other"`
	DurationMs int64                   `json:"duration_ms" jsonschema:"the sample item's duration in milliseconds"`
	Chapters   []domain.CatalogChapter `json:"chapters,omitempty" jsonschema:"optional sample chapters: {name, start_ms}"`
}

// PreviewResult is where the cuts fall for the sample item.
type PreviewResult struct {
	cutplan.Plan
	// SegmentsMs are the lengths of the pieces the cuts leave.
	SegmentsMs []int64 `json:"segments_ms"`
}

// Preview validates the rules and plans the sample item. It touches no storage.
func Preview(in PreviewInput) (PreviewResult, error) {
	switch domain.CatalogItemKind(in.Kind) {
	case domain.CatalogMovie, domain.CatalogEpisode, domain.CatalogOther:
	default:
		return PreviewResult{}, &cutplan.ValidationError{Msg: "kind must be one of movie, episode, other"}
	}
	const maxDuration = int64(7 * 24 * 60 * 60 * 1000)
	if in.DurationMs < 1 || in.DurationMs > maxDuration {
		return PreviewResult{}, &cutplan.ValidationError{Msg: "duration_ms must be between 1 and 604800000"}
	}
	if len(in.Chapters) > 500 {
		return PreviewResult{}, &cutplan.ValidationError{Msg: "chapters must hold at most 500 entries"}
	}
	cfg := Input{Rules: in.Rules, Default: in.Default, Guards: in.Guards}.Config()
	if err := cutplan.ValidateConfig(cfg); err != nil {
		return PreviewResult{}, err
	}
	plan := cutplan.PlanItem(cfg, cutplan.Item{Kind: domain.CatalogItemKind(in.Kind), DurationMs: in.DurationMs, Chapters: in.Chapters})
	return PreviewResult{Plan: plan, SegmentsMs: cutplan.Segments(plan.CutsMs, in.DurationMs)}, nil
}
