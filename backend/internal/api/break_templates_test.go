package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func btBody(poolID string, steps ...string) string {
	return fmt.Sprintf(`{"scope_type":"channel","scope_id":"chan-1","kind":"midroll","name":"Break","position":2,"steps":[%s]}`, strings.Join(steps, ","))
}

func btStep(kind, poolID, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"step_type":%q,"filler_pool_id":%q,"selection_strategy":"sequential"%s}`, kind, poolID, extra)
}

func TestBreakTemplatesCRUD(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)

	body := btBody(pool.ID, btStep("single", pool.ID, ""), btStep("fill", pool.ID, `"target_ms":240000,"max_fit_attempts":3`), btStep("single", pool.ID, ""))
	rec = doRequest(t, mux, "POST", "/break-templates", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var bt domain.BreakTemplate
	json.Unmarshal(rec.Body.Bytes(), &bt)
	if bt.Scope != domain.ScopeChannel || bt.Kind != domain.FillerMidRoll || bt.Position != 2 || bt.Name != "Break" || len(bt.Steps) != 3 {
		t.Fatalf("unexpected template: %+v", bt)
	}
	if s := bt.Steps[1]; s.StepType != domain.BreakStepFill || s.TargetMs != 240000 || s.MaxFitAttempts != 3 || s.Position != 1 {
		t.Fatalf("unexpected fill step: %+v", s)
	}
	if s := bt.Steps[0]; s.StepType != domain.BreakStepSingle || s.TargetMs != 0 || s.MaxFitAttempts != 1 {
		t.Fatalf("unexpected single step: %+v", s)
	}

	rec = doRequest(t, mux, "GET", "/break-templates", "")
	var list []domain.BreakTemplate
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || len(list[0].Steps) != 3 {
		t.Fatalf("list: %s", rec.Body.String())
	}
	if rec = doRequest(t, mux, "GET", "/break-templates/"+bt.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	// PUT replaces the whole template, steps included.
	put := `{"scope_type":"block","scope_id":"block-1","kind":"preroll","steps":[` + btStep("fill", pool.ID, `"target_ms":60000`) + `]}`
	rec = doRequest(t, mux, "PUT", "/break-templates/"+bt.ID, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.BreakTemplate
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Scope != domain.ScopeBlock || updated.Kind != domain.FillerPreRoll || len(updated.Steps) != 1 || updated.Steps[0].TargetMs != 60000 {
		t.Fatalf("unexpected updated template: %+v", updated)
	}

	if rec = doRequest(t, mux, "PUT", "/break-templates/nope", put); rec.Code != http.StatusNotFound {
		t.Fatalf("update unknown: status = %d, want 404", rec.Code)
	}

	// The pool is referenced by a step: it cannot be deleted from under it.
	if rec = doRequest(t, mux, "DELETE", "/filler-pools/"+pool.ID, ""); rec.Code == http.StatusNoContent {
		t.Fatalf("deleting a pool used by a template succeeded")
	}

	if rec = doRequest(t, mux, "DELETE", "/break-templates/"+bt.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	if rec = doRequest(t, mux, "GET", "/break-templates/"+bt.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want 404", rec.Code)
	}
	if rec = doRequest(t, mux, "DELETE", "/break-templates/"+bt.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice: status = %d, want 404", rec.Code)
	}
}

func TestBreakTemplatesCascadeSteps(t *testing.T) {
	db, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)
	rec = doRequest(t, mux, "POST", "/break-templates", btBody(pool.ID, btStep("single", pool.ID, "")))
	var bt domain.BreakTemplate
	json.Unmarshal(rec.Body.Bytes(), &bt)
	doRequest(t, mux, "DELETE", "/break-templates/"+bt.ID, "")
	var n []int
	if err := db.Query(t.Context(), &n, "SELECT COUNT(*) FROM break_template_step"); err != nil || len(n) != 1 || n[0] != 0 {
		t.Fatalf("steps left after deleting the template: %v (err %v)", n, err)
	}
}

func TestBreakTemplatesValidation(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/filler-pools", `{"name":"Bumpers"}`)
	var pool domain.FillerPool
	json.Unmarshal(rec.Body.Bytes(), &pool)
	single := btStep("single", pool.ID, "")

	many := make([]string, domain.MaxBreakSteps+1)
	for i := range many {
		many[i] = single
	}
	at := func(scope, kind, steps string) string {
		return fmt.Sprintf(`{"scope_type":%q,"scope_id":"c","kind":%q,"steps":[%s]}`, scope, kind, steps)
	}
	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{`},
		{"bad scope", at("tenant", "midroll", single)},
		{"missing scope id", `{"scope_type":"channel","kind":"midroll","steps":[` + single + `]}`},
		{"tail is not a boundary kind", at("channel", "tail", single)},
		{"fallback is not a boundary kind", at("channel", "fallback", single)},
		{"no steps", at("channel", "midroll", "")},
		{"too many steps", at("channel", "midroll", strings.Join(many, ","))},
		{"bad step type", at("channel", "midroll", btStep("loop", pool.ID, ""))},
		{"missing pool", at("channel", "midroll", `{"step_type":"single","selection_strategy":"random"}`)},
		{"unknown pool", at("channel", "midroll", btStep("single", "no-such-pool", ""))},
		{"bad selection", at("channel", "midroll", `{"step_type":"single","filler_pool_id":"`+pool.ID+`","selection_strategy":"best"}`)},
		{"fill without target", at("channel", "midroll", btStep("fill", pool.ID, ""))},
		{"fill target too long", at("channel", "midroll", btStep("fill", pool.ID, `"target_ms":3600001`))},
		{"fill target negative", at("channel", "midroll", btStep("fill", pool.ID, `"target_ms":-5`))},
		{"fill attempts too many", at("channel", "midroll", btStep("fill", pool.ID, `"target_ms":1000,"max_fit_attempts":21`))},
		{"fill attempts negative", at("channel", "midroll", btStep("fill", pool.ID, `"target_ms":1000,"max_fit_attempts":-1`))},
		{"single with a target", at("channel", "midroll", btStep("single", pool.ID, `"target_ms":1000`))},
		{"name too long", `{"scope_type":"channel","scope_id":"c","kind":"midroll","name":"` + strings.Repeat("x", 101) + `","steps":[` + single + `]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := doRequest(t, mux, "POST", "/break-templates", tc.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
	// The bounds themselves are accepted.
	ok := make([]string, domain.MaxBreakSteps)
	for i := range ok {
		ok[i] = btStep("fill", pool.ID, `"target_ms":3600000,"max_fit_attempts":20`)
	}
	if rec := doRequest(t, mux, "POST", "/break-templates", at("channel", "postroll", strings.Join(ok, ","))); rec.Code != http.StatusCreated {
		t.Fatalf("limits: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
