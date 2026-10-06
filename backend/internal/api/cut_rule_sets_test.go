package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const cutRulesBody = `{
  "scope_type":"channel","scope_id":"chan-1","name":"Cuts","position":3,
  "rules":[
    {"name":"long movies","match":{"kinds":["movie"],"min_duration_ms":7200000},
     "strategy":{"type":"phased","phases":[{"every_ms":2400000,"until_before_end_ms":1200000},{"every_ms":300000}]}},
    {"match":{"kinds":["episode","movie"],"has_chapters":true},
     "strategy":{"type":"chapter","roles":["after_intro","middle"],"fallback":{"type":"percentage","percentages":[50]}},
     "snap_tolerance_ms":300000},
    {"match":{"kinds":["episode"]},"strategy":{"type":"percentage","percentages":[50]}}
  ],
  "default":{"type":"none"},
  "guards":{"min_segment_ms":120000,"max_breaks":8}
}`

func TestCutRuleSetsCRUD(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/cut-rule-sets", cutRulesBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var set domain.CutRuleSet
	json.Unmarshal(rec.Body.Bytes(), &set)
	if set.ID == "" || set.Scope != domain.ScopeChannel || set.Position != 3 || set.Name != "Cuts" || len(set.Rules) != 3 {
		t.Fatalf("unexpected set: %+v", set)
	}
	if r := set.Rules[0]; r.Strategy.Type != domain.CutPhased || len(r.Strategy.Phases) != 2 || r.Strategy.Phases[0].UntilBeforeEndMs != 1200000 {
		t.Fatalf("phased rule did not round-trip: %+v", r)
	}
	if r := set.Rules[1]; r.Strategy.Fallback == nil || r.Strategy.Fallback.Type != domain.CutPercentage || r.SnapToleranceMs != 300000 || r.Match.HasChapters == nil || !*r.Match.HasChapters {
		t.Fatalf("chapter rule did not round-trip: %+v", r)
	}
	if set.Default == nil || set.Default.Type != domain.CutNone || set.Guards.MaxBreaks != 8 || set.Guards.MinSegmentMs != 120000 {
		t.Fatalf("default/guards did not round-trip: %+v", set)
	}

	var list []domain.CutRuleSet
	rec = doRequest(t, mux, "GET", "/cut-rule-sets", "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != set.ID || len(list[0].Rules) != 3 {
		t.Fatalf("list: %s", rec.Body.String())
	}
	if rec = doRequest(t, mux, "GET", "/cut-rule-sets/"+set.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d", rec.Code)
	}

	// PUT replaces the whole set.
	put := `{"scope_type":"block","scope_id":"block-1","rules":[{"strategy":{"type":"interval","every_ms":3600000}}]}`
	rec = doRequest(t, mux, "PUT", "/cut-rule-sets/"+set.ID, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated domain.CutRuleSet
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Scope != domain.ScopeBlock || updated.ScopeID != "block-1" || len(updated.Rules) != 1 || updated.Default != nil || updated.Name != "" {
		t.Fatalf("PUT did not replace the set: %+v", updated)
	}
	if rec = doRequest(t, mux, "PUT", "/cut-rule-sets/missing", put); rec.Code != http.StatusNotFound {
		t.Fatalf("put unknown: status = %d", rec.Code)
	}

	if rec = doRequest(t, mux, "DELETE", "/cut-rule-sets/"+set.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	if rec = doRequest(t, mux, "GET", "/cut-rule-sets/"+set.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d", rec.Code)
	}
	if rec = doRequest(t, mux, "DELETE", "/cut-rule-sets/"+set.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice: status = %d", rec.Code)
	}
}

func TestCutRuleSetsValidation(t *testing.T) {
	_, mux := newTestDB(t)
	rules := func(r string) string {
		return `{"scope_type":"channel","scope_id":"c","rules":[` + r + `]}`
	}
	cases := []struct {
		name, body, want string
	}{
		{"bad scope", `{"scope_type":"tenant","scope_id":"c","rules":[]}`, "scope_type"},
		{"no scope id", `{"scope_type":"channel","rules":[]}`, "scope_id"},
		{"no rules", `{"scope_type":"channel","scope_id":"c","rules":[]}`, "at least one rule"},
		{"bad interval", rules(`{"strategy":{"type":"interval","every_ms":5}}`), "every_ms"},
		{"bad percentage", rules(`{"strategy":{"type":"percentage","percentages":[0]}}`), "percentages"},
		{"unknown type", rules(`{"strategy":{"type":"random"}}`), "strategy type"},
		{"overlapping phases", rules(`{"strategy":{"type":"phased","phases":[{"every_ms":600000,"until_ms":3600000},{"every_ms":600000,"until_ms":1800000},{"every_ms":300000}]}}`), "overlaps"},
		{"shadowed", rules(`{"strategy":{"type":"percentage","percentages":[50]}},{"match":{"kinds":["movie"]},"strategy":{"type":"percentage","percentages":[50]}}`), "can never match"},
		{"malformed body", `{"scope_type":`, "invalid request body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, mux, "POST", "/cut-rule-sets", tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status = %d, body = %s, want 400 containing %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	// Nothing was stored by the rejected writes.
	rec := doRequest(t, mux, "GET", "/cut-rule-sets", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("list after rejected writes = %s", rec.Body.String())
	}
}

func TestCutRuleSetsPreview(t *testing.T) {
	_, mux := newTestDB(t)
	body := `{"rules":[
	  {"match":{"kinds":["movie"],"min_duration_ms":7200000},
	   "strategy":{"type":"phased","phases":[{"every_ms":2400000,"until_before_end_ms":1200000},{"every_ms":300000}]}}
	 ],"kind":"movie","duration_ms":9000000}`
	rec := doRequest(t, mux, "POST", "/cut-rule-sets/preview", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		CutsMs     []int64 `json:"cuts_ms"`
		SegmentsMs []int64 `json:"segments_ms"`
		RuleIndex  int     `json:"rule_index"`
		Source     string  `json:"source"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	want := []int64{2400000, 4800000, 7200000, 8100000, 8400000, 8700000}
	if len(res.CutsMs) != len(want) || res.RuleIndex != 0 || res.Source != "rule" || len(res.SegmentsMs) != len(want)+1 {
		t.Fatalf("preview = %+v", res)
	}
	for i := range want {
		if res.CutsMs[i] != want[i] {
			t.Fatalf("cuts = %v, want %v", res.CutsMs, want)
		}
	}

	// Chapters feed the chapter strategy.
	chap := `{"rules":[{"strategy":{"type":"chapter","roles":["middle"]}}],"kind":"episode","duration_ms":2700000,
	  "chapters":[{"name":"A","start_ms":0},{"name":"B","start_ms":600000},{"name":"C","start_ms":1320000}]}`
	rec = doRequest(t, mux, "POST", "/cut-rule-sets/preview", chap)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cuts_ms":[1320000]`) {
		t.Fatalf("chapter preview: %d %s", rec.Code, rec.Body.String())
	}

	for name, bad := range map[string]string{
		"bad kind":     `{"rules":[{"strategy":{"type":"none"}}],"kind":"clip","duration_ms":1000}`,
		"no duration":  `{"rules":[{"strategy":{"type":"none"}}],"kind":"movie"}`,
		"bad strategy": `{"rules":[{"strategy":{"type":"interval"}}],"kind":"movie","duration_ms":9000000}`,
	} {
		if rec := doRequest(t, mux, "POST", "/cut-rule-sets/preview", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}
}
