package domain

import (
	"encoding/json"
	"testing"
)

func TestNormalizeWeekdays(t *testing.T) {
	got, err := NormalizeWeekdays([]int{5, 4})
	if err != nil || len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("got %v, %v; want [4 5]", got, err)
	}
	for name, in := range map[string][]int{
		"empty": {}, "nil": nil, "duplicate": {1, 1}, "negative": {-1}, "too big": {7},
	} {
		if _, err := NormalizeWeekdays(in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWeekdaysStorageAndJSON(t *testing.T) {
	v, _ := Weekdays{4, 5}.Value()
	if v != "4,5" {
		t.Errorf("Value = %v, want 4,5", v)
	}
	var w Weekdays
	if err := w.Scan("0,6"); err != nil || len(w) != 2 || w[0] != 0 || w[1] != 6 {
		t.Errorf("Scan = %v, %v", w, err)
	}
	if err := w.Scan(""); err != nil || len(w) != 0 {
		t.Errorf("Scan empty = %v, %v", w, err)
	}
	if err := w.Scan("x"); err == nil {
		t.Error("Scan of garbage should fail")
	}
	b, _ := json.Marshal(struct{ D Weekdays }{})
	if string(b) != `{"D":[]}` {
		t.Errorf("empty marshals as %s, want an empty array", b)
	}
	b, _ = json.Marshal(Weekdays{1, 3})
	if string(b) != "[1,3]" {
		t.Errorf("marshals as %s", b)
	}
}
