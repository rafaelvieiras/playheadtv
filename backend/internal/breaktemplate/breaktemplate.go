// Package breaktemplate validates and stores break templates (see
// domain.BreakTemplate). The REST handlers and the MCP tools both go through
// it, so the limits and the error messages are the same on both surfaces.
package breaktemplate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Columns alias scope_type/selection_strategy to the struct field names the
// scan matches (see AGENTS.md, scan.go).
const (
	templateColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, name, position, created_at"
	stepColumns     = "id, break_template_id, position, step_type, filler_pool_id, selection_strategy AS selection, target_ms, max_fit_attempts"
)

// StepInput is one step of a template as a client sends it.
type StepInput struct {
	StepType          string `json:"step_type" jsonschema:"single (one item from the pool) or fill (whole items until target_ms is filled)"`
	FillerPoolID      string `json:"filler_pool_id" jsonschema:"the filler pool this step draws from"`
	SelectionStrategy string `json:"selection_strategy" jsonschema:"one of: sequential, random, weighted"`
	TargetMs          int64  `json:"target_ms,omitempty" jsonschema:"fill steps only: the length to fill, in milliseconds (1 to 3600000)"`
	MaxFitAttempts    int    `json:"max_fit_attempts,omitempty" jsonschema:"fill steps only: how many candidates in a row may be rejected for not fitting before the fill gives up (1 to 20, default 1)"`
}

// Input is a template as a client sends it, on create and on replace.
type Input struct {
	ScopeType string      `json:"scope_type" jsonschema:"one of: channel, block"`
	ScopeID   string      `json:"scope_id" jsonschema:"the Channel.ID or Block.ID this template applies to, per scope_type"`
	Kind      string      `json:"kind" jsonschema:"the boundary it replaces the single filler item of: preroll, midroll or postroll"`
	Name      string      `json:"name,omitempty" jsonschema:"optional display name"`
	Position  int         `json:"position,omitempty" jsonschema:"ordering among templates of the same kind and scope; the lowest wins"`
	Steps     []StepInput `json:"steps" jsonschema:"the ordered steps (1 to 10)"`
}

// ValidationError is a problem with the client's input (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// Validate checks in against the model's limits and that every pool exists.
func Validate(ctx context.Context, db storage.DB, tenantID string, in Input) error {
	switch domain.FillerScope(in.ScopeType) {
	case domain.ScopeChannel, domain.ScopeBlock:
	default:
		return invalid("scope_type must be one of channel, block")
	}
	if in.ScopeID == "" {
		return invalid("scope_id is required")
	}
	switch domain.FillerKind(in.Kind) {
	case domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll:
	default:
		return invalid("kind must be one of preroll, midroll, postroll")
	}
	if utf8.RuneCountInString(in.Name) > domain.MaxBreakTemplateName {
		return invalid("name must be at most %d characters", domain.MaxBreakTemplateName)
	}
	if len(in.Steps) == 0 {
		return invalid("steps must hold at least one step")
	}
	if len(in.Steps) > domain.MaxBreakSteps {
		return invalid("steps must hold at most %d steps", domain.MaxBreakSteps)
	}
	for i, s := range in.Steps {
		n := i + 1
		switch domain.BreakStepType(s.StepType) {
		case domain.BreakStepSingle, domain.BreakStepFill:
		default:
			return invalid("step %d: step_type must be one of single, fill", n)
		}
		if s.FillerPoolID == "" {
			return invalid("step %d: filler_pool_id is required", n)
		}
		switch domain.SelectionStrategy(s.SelectionStrategy) {
		case domain.SelectionSequential, domain.SelectionRandom, domain.SelectionWeighted:
		default:
			return invalid("step %d: selection_strategy must be one of sequential, random, weighted", n)
		}
		if domain.BreakStepType(s.StepType) == domain.BreakStepFill {
			if s.TargetMs < 1 || s.TargetMs > domain.MaxBreakTargetMs {
				return invalid("step %d: target_ms must be between 1 and %d", n, domain.MaxBreakTargetMs)
			}
			if s.MaxFitAttempts != 0 && (s.MaxFitAttempts < 1 || s.MaxFitAttempts > domain.MaxFitAttemptsLimit) {
				return invalid("step %d: max_fit_attempts must be between 1 and %d", n, domain.MaxFitAttemptsLimit)
			}
		} else if s.TargetMs != 0 || s.MaxFitAttempts != 0 {
			return invalid("step %d: target_ms and max_fit_attempts only apply to fill steps", n)
		}
		var ids []string
		if err := db.Query(ctx, &ids, "SELECT id FROM filler_pool WHERE tenant_id = ? AND id = ?", tenantID, s.FillerPoolID); err != nil {
			return err
		}
		if len(ids) == 0 {
			return invalid("step %d: filler pool %s does not exist", n, s.FillerPoolID)
		}
	}
	return nil
}

func insertSteps(ctx context.Context, tx storage.DB, templateID string, steps []StepInput) error {
	for i, s := range steps {
		maxFit := s.MaxFitAttempts
		if maxFit == 0 {
			maxFit = 1
		}
		if err := tx.Exec(ctx,
			`INSERT INTO break_template_step (id, break_template_id, position, step_type, filler_pool_id, selection_strategy, target_ms, max_fit_attempts)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), templateID, i, s.StepType, s.FillerPoolID, s.SelectionStrategy, s.TargetMs, maxFit,
		); err != nil {
			return err
		}
	}
	return nil
}

// Create validates and stores a new template and returns it with its steps.
func Create(ctx context.Context, db storage.DB, tenantID string, in Input) (domain.BreakTemplate, error) {
	if err := Validate(ctx, db, tenantID, in); err != nil {
		return domain.BreakTemplate{}, err
	}
	id := uuid.NewString()
	err := db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx,
			`INSERT INTO break_template (id, tenant_id, scope_type, scope_id, kind, name, position, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, tenantID, in.ScopeType, in.ScopeID, in.Kind, strings.TrimSpace(in.Name), in.Position, time.Now().UnixMilli(),
		); err != nil {
			return err
		}
		return insertSteps(ctx, tx, id, in.Steps)
	})
	if err != nil {
		return domain.BreakTemplate{}, err
	}
	return Get(ctx, db, tenantID, id)
}

// Replace validates in and replaces the template's fields and steps (a PUT is
// the whole template). It reports sql.ErrNoRows for an unknown id.
func Replace(ctx context.Context, db storage.DB, tenantID, id string, in Input) (domain.BreakTemplate, error) {
	if _, err := Get(ctx, db, tenantID, id); err != nil {
		return domain.BreakTemplate{}, err
	}
	if err := Validate(ctx, db, tenantID, in); err != nil {
		return domain.BreakTemplate{}, err
	}
	err := db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx,
			`UPDATE break_template SET scope_type = ?, scope_id = ?, kind = ?, name = ?, position = ? WHERE tenant_id = ? AND id = ?`,
			in.ScopeType, in.ScopeID, in.Kind, strings.TrimSpace(in.Name), in.Position, tenantID, id,
		); err != nil {
			return err
		}
		if err := tx.Exec(ctx, "DELETE FROM break_template_step WHERE break_template_id = ?", id); err != nil {
			return err
		}
		return insertSteps(ctx, tx, id, in.Steps)
	})
	if err != nil {
		return domain.BreakTemplate{}, err
	}
	return Get(ctx, db, tenantID, id)
}

// Get returns one template with its steps in order; sql.ErrNoRows when unknown.
func Get(ctx context.Context, db storage.DB, tenantID, id string) (domain.BreakTemplate, error) {
	var t domain.BreakTemplate
	if err := db.Query(ctx, &t, "SELECT "+templateColumns+" FROM break_template WHERE tenant_id = ? AND id = ?", tenantID, id); err != nil {
		return domain.BreakTemplate{}, err
	}
	steps, err := loadSteps(ctx, db, []string{id})
	if err != nil {
		return domain.BreakTemplate{}, err
	}
	t.Steps = steps[id]
	if t.Steps == nil {
		t.Steps = []domain.BreakTemplateStep{}
	}
	return t, nil
}

// List returns every template of the tenant, each with its steps in order.
func List(ctx context.Context, db storage.DB, tenantID string) ([]domain.BreakTemplate, error) {
	templates := []domain.BreakTemplate{}
	if err := db.Query(ctx, &templates, "SELECT "+templateColumns+" FROM break_template WHERE tenant_id = ? ORDER BY created_at, id", tenantID); err != nil {
		return nil, err
	}
	ids := make([]string, len(templates))
	for i, t := range templates {
		ids[i] = t.ID
	}
	steps, err := loadSteps(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	for i := range templates {
		templates[i].Steps = steps[templates[i].ID]
		if templates[i].Steps == nil {
			templates[i].Steps = []domain.BreakTemplateStep{}
		}
	}
	return templates, nil
}

func loadSteps(ctx context.Context, db storage.DB, templateIDs []string) (map[string][]domain.BreakTemplateStep, error) {
	out := map[string][]domain.BreakTemplateStep{}
	if len(templateIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(templateIDs))
	for i, id := range templateIDs {
		args[i] = id
	}
	var steps []domain.BreakTemplateStep
	q := "SELECT " + stepColumns + " FROM break_template_step WHERE break_template_id IN (" +
		strings.TrimSuffix(strings.Repeat("?,", len(templateIDs)), ",") + ") ORDER BY position, rowid"
	if err := db.Query(ctx, &steps, q, args...); err != nil {
		return nil, err
	}
	for _, s := range steps {
		out[s.BreakTemplateID] = append(out[s.BreakTemplateID], s)
	}
	return out, nil
}

// Delete removes a template (its steps go with it); sql.ErrNoRows when unknown.
func Delete(ctx context.Context, db storage.DB, tenantID, id string) error {
	if _, err := Get(ctx, db, tenantID, id); err != nil {
		return err
	}
	return db.Exec(ctx, "DELETE FROM break_template WHERE tenant_id = ? AND id = ?", tenantID, id)
}

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}
