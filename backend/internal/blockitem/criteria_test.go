package blockitem

import (
	"encoding/json"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func intp(n int) *int { return &n }

const testQuery = `{"version":1,"all":[{"field":"kind","op":"equals","value":"movie"}]}`

func TestNewCriteriaAmount(t *testing.T) {
	ok := map[string]struct {
		in   *domain.SeriesAmountInput
		want domain.CriteriaAmount
	}{
		"count":              {&domain.SeriesAmountInput{Mode: "count", Count: intp(3)}, domain.CriteriaAmount{Mode: "count", Count: 3}},
		"count without mode": {&domain.SeriesAmountInput{Count: intp(2)}, domain.CriteriaAmount{Mode: "count", Count: 2}},
		"all":                {&domain.SeriesAmountInput{Mode: "all"}, domain.CriteriaAmount{Mode: "all"}},
	}
	for name, tc := range ok {
		got, err := NewCriteriaAmount(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, err, tc.want)
		}
	}
	bad := map[string]*domain.SeriesAmountInput{
		"nil":             nil,
		"empty":           {},
		"count no number": {Mode: "count"},
		"zero":            {Mode: "count", Count: intp(0)},
		"all with count":  {Mode: "all", Count: intp(1)},
		"series mode":     {Mode: "slot_end"},
		"series end":      {Mode: "series_end"},
	}
	for name, in := range bad {
		if _, err := NewCriteriaAmount(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewCriteriaSpecDefaultsAndValidation(t *testing.T) {
	spec, err := NewCriteriaSpec(json.RawMessage(testQuery), "", &domain.SeriesAmountInput{Mode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.PlaybackOrder != domain.OrderTitle || spec.Seed <= 0 || spec.Amount.Mode != "all" || string(spec.Query) != testQuery {
		t.Errorf("spec = %+v", spec)
	}
	other, _ := NewCriteriaSpec(json.RawMessage(testQuery), "shuffle", &domain.SeriesAmountInput{Mode: "all"})
	if other.Seed == spec.Seed {
		t.Error("two specs got the same seed")
	}
	for _, order := range []string{"original", "nope"} {
		if _, err := NewCriteriaSpec(json.RawMessage(testQuery), order, &domain.SeriesAmountInput{Mode: "all"}); err == nil {
			t.Errorf("order %q accepted", order)
		}
	}
	if _, err := NewCriteriaSpec(json.RawMessage(`{"kind":"movie"}`), "", &domain.SeriesAmountInput{Mode: "all"}); err == nil {
		t.Error("legacy query accepted")
	}
	if _, err := NewCriteriaSpec(nil, "", &domain.SeriesAmountInput{Mode: "all"}); err == nil {
		t.Error("missing query accepted")
	}
}

func TestUpdateCriteriaSpec(t *testing.T) {
	spec, _ := NewCriteriaSpec(json.RawMessage(testQuery), "title", &domain.SeriesAmountInput{Mode: "count", Count: intp(1)})
	seed := spec.Seed
	order := "title"
	got, changed, err := UpdateCriteriaSpec(spec, nil, &order, nil)
	if err != nil || changed || got.Seed != seed {
		t.Errorf("same order: %+v changed=%v %v", got, changed, err)
	}
	order = "random"
	got, changed, err = UpdateCriteriaSpec(spec, json.RawMessage(`{"version":1,"all":[{"field":"year","op":"gt","value":2000}]}`), &order, &domain.SeriesAmountInput{Mode: "all"})
	if err != nil || !changed || got.PlaybackOrder != "random" || got.Amount.Mode != "all" || got.Seed != seed || string(got.Query) == testQuery {
		t.Errorf("update: %+v changed=%v %v", got, changed, err)
	}
	bad := "original"
	if _, _, err := UpdateCriteriaSpec(spec, nil, &bad, nil); err == nil {
		t.Error("series order accepted")
	}
	if _, _, err := UpdateCriteriaSpec(spec, json.RawMessage(`{"version":1,"all":[]}`), nil, nil); err == nil {
		t.Error("invalid query accepted")
	}
}
