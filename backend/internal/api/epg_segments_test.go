package api

import (
	"context"
	"testing"
)

func TestEPGSegmentFieldsAreAdditive(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	ctx := context.Background()
	s.entry(t, "plain", "c1", "program", "ep", 1000, 1801000)
	s.entry(t, "seg1", "c1", "program", "mov", 1801000, 2401000)
	s.entry(t, "brk", "c1", "midroll", "clip0", 2401000, 2411000)
	s.entry(t, "seg2", "c1", "program", "mov", 2411000, 3011000)
	if err := db.Exec(ctx, "UPDATE epg_entry SET split_group = 'seg1' WHERE id IN ('seg1', 'seg2')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(ctx, "UPDATE epg_entry SET item_offset_ms = 600000 WHERE id = 'seg2'"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/epg?channel_id=c1", "/epg?channel_id=c1&group_tail=1"} {
		got := getEPGJSON(t, mux, path)
		if len(got) != 4 {
			t.Fatalf("%s: entries = %d, want 4", path, len(got))
		}
		// An ordinary entry keeps the shape it always had.
		if _, ok := got[0]["item_offset_ms"]; ok {
			t.Errorf("%s: a plain entry carries item_offset_ms", path)
		}
		if _, ok := got[0]["split_group"]; ok {
			t.Errorf("%s: a plain entry carries split_group", path)
		}
		if _, ok := got[1]["item_offset_ms"]; ok {
			t.Errorf("%s: the first segment (offset 0) carries item_offset_ms", path)
		}
		if got[1]["split_group"] != "seg1" || got[3]["split_group"] != "seg1" {
			t.Errorf("%s: segments do not share the group: %v %v", path, got[1]["split_group"], got[3]["split_group"])
		}
		if got[3]["item_offset_ms"] != float64(600000) {
			t.Errorf("%s: item_offset_ms = %v, want 600000", path, got[3]["item_offset_ms"])
		}
		if got[3]["title"] != "A Movie" || got[3]["duration_ms"] != float64(5400000) {
			t.Errorf("%s: a segment is enriched from its item: %v", path, got[3])
		}
		if _, ok := got[2]["split_group"]; ok {
			t.Errorf("%s: the break carries a split group", path)
		}
	}
	// Offsets are never negative.
	if err := db.Exec(ctx, "UPDATE epg_entry SET item_offset_ms = -1 WHERE id = 'seg2'"); err == nil {
		t.Errorf("a negative item_offset_ms was accepted")
	}
}
