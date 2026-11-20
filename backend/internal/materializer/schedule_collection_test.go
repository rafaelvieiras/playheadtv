package materializer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// createCollection seeds a collection holding the given query.
func (f *testFixture) createCollection(t *testing.T, id, name, query string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		"INSERT INTO collection (id, tenant_id, name, description, query, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)",
		id, f.tenantID, name, query, now, now,
	); err != nil {
		t.Fatalf("seed collection %s: %v", id, err)
	}
}

// createCollectionScheduleItem seeds a schedule item that targets a collection
// instead of a block (issue #100).
func (f *testFixture) createCollectionScheduleItem(t *testing.T, id, scheduleID, collectionID, order, recurrence string, startAt *time.Time) {
	t.Helper()
	now := time.Now().UnixMilli()
	var start any
	if startAt != nil {
		start = startAt.UnixMilli()
	}
	if err := f.db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, collection_id, playback_order, recurrence_type, start_at, active, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, ?, ?, ?, ?, 1, ?, ?)`,
		id, f.tenantID, scheduleID, collectionID, order, recurrence, start, now, now,
	); err != nil {
		t.Fatalf("seed collection schedule_item %s: %v", id, err)
	}
}

// printed returns the titles of the program entries in start order.
func printed(t *testing.T, f *testFixture) []string {
	t.Helper()
	var out []string
	if err := f.db.Query(context.Background(), &out,
		"SELECT ci.title FROM epg_entry e JOIN catalog_item ci ON ci.id = e.catalog_item_id WHERE e.channel_id = 'chan1' AND e.kind = 'program' ORDER BY e.start_at"); err != nil {
		t.Fatalf("query titles: %v", err)
	}
	return out
}

func TestScheduleCollection_AirsACollectionWithoutABlock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createCollection(t, "coll90", "Nineties", yearQuery)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	// One continuous item that names the collection: no block, no block item.
	f.createCollectionScheduleItem(t, "it1", "sched1", "coll90", domain.OrderShuffle, "continuous", nil)

	// One full cycle of the collection: five 30 min matches.
	mat := New(f.db, 2*time.Hour+30*time.Minute)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	if len(entries) == 0 {
		t.Fatal("the collection item aired nothing")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Kind != domain.EpgProgram {
			continue
		}
		if e.CatalogItemID == "old" || e.CatalogItemID == "zero" {
			t.Fatalf("aired %s, outside the collection or without a duration", e.CatalogItemID)
		}
		if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "it1" {
			t.Fatalf("entry without its schedule item id: %+v", e)
		}
		if e.SourceBlockItemID == nil || *e.SourceBlockItemID != "it1" {
			t.Fatalf("entry not tagged with the walked item (the schedule item): %+v", e)
		}
		if seen[e.CatalogItemID] {
			t.Fatalf("%s aired twice inside one cycle of the collection", e.CatalogItemID)
		}
		seen[e.CatalogItemID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("aired %d of the 5 collection matches: %v", len(seen), seen)
	}
	// The walk is a stored cursor, keyed by the schedule item.
	var origins []int64
	if err := f.db.Query(ctx, &origins, "SELECT origin_ms FROM schedule_item_cursor WHERE channel_id = 'chan1' AND schedule_item_id = 'it1'"); err != nil || len(origins) != 1 {
		t.Fatalf("schedule_item_cursor = %v (err %v), want one row", origins, err)
	}
}

func TestScheduleCollection_ResumesWhereItStopped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createCollection(t, "coll90", "Nineties", yearQuery)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createCollectionScheduleItem(t, "it1", "sched1", "coll90", domain.OrderShuffle, "continuous", nil)

	mat := New(f.db, 3*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := printed(t, f)
	if len(first) < 2 {
		t.Fatalf("only %d programs in the first window", len(first))
	}
	// A second run over a wider window regenerates the kept head and adds to
	// it: the collection must not start over from its first match.
	mat2 := New(f.db, 6*time.Hour)
	if err := mat2.Run(ctx); err != nil {
		t.Fatal(err)
	}
	second := printed(t, f)
	if len(second) <= len(first) {
		t.Fatalf("second run did not extend the guide: %v", second)
	}
	for i := range first {
		if second[i] != first[i] {
			t.Fatalf("the kept head changed at %d: %q became %q", i, first[i], second[i])
		}
	}
}

func TestScheduleCollection_WeeklyItemFillsItsSlotAndStops(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	nineties(t, f)
	f.createChannel(t, "chan1", "UTC")
	f.createCollection(t, "coll90", "Nineties", yearQuery)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	base := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute)
	f.createCollectionScheduleItem(t, "weekly", "sched1", "coll90", domain.OrderShuffle, "weekly", nil)
	if err := f.db.Exec(ctx,
		"UPDATE schedule_item SET days_of_week = ?, start_time_local = ? WHERE id = 'weekly'",
		fmt.Sprintf("%d", int(base.Weekday())), base.Format("15:04"),
	); err != nil {
		t.Fatal(err)
	}
	// A block item right after it gives the slot a hard end.
	f.createBlock(t, "blk")
	f.createCatalogItem(t, "later", epMs)
	f.addBlockItem(t, "b0", "blk", 0, "later")
	f.createOnceScheduleItem(t, "next", "sched1", "blk", base.Add(2*time.Hour))

	mat := New(f.db, 6*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	sawCollection, sawNext := false, false
	for _, e := range entries {
		if e.SourceScheduleItemID == nil {
			continue
		}
		switch *e.SourceScheduleItemID {
		case "weekly":
			sawCollection = true
			if e.StartAt >= base.Add(2*time.Hour).UnixMilli() {
				t.Fatalf("the collection crossed the next item's start: %+v", e)
			}
		case "next":
			sawNext = true
			if e.StartAt < base.Add(2*time.Hour).UnixMilli() {
				t.Fatalf("the next item started early: %+v", e)
			}
		}
	}
	if !sawCollection || !sawNext {
		t.Fatalf("weekly collection aired = %v, next item aired = %v", sawCollection, sawNext)
	}
}
