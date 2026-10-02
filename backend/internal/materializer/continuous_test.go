package materializer

import (
	"context"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func (f *testFixture) createContinuousScheduleItem(t *testing.T, id, scheduleID, blockID string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := f.db.Exec(context.Background(),
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'continuous', 1, ?, ?)`,
		id, f.tenantID, scheduleID, blockID, now, now,
	); err != nil {
		t.Fatalf("seed continuous schedule_item %s: %v", id, err)
	}
}

// continuousSetup seeds a channel driven by one continuous item that rotates
// through three one-hour movies (m0, m1, m2).
func continuousSetup(t *testing.T) *testFixture {
	t.Helper()
	f := newFixture(t)
	hour := int64(60 * 60 * 1000)
	for _, id := range []string{"m0", "m1", "m2"} {
		f.createCatalogItem(t, id, hour)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createDynamicBlock(t, "rot")
	for i, id := range []string{"m0", "m1", "m2"} {
		f.addBlockItem(t, "bi"+id, "rot", i, id)
	}
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "rot")
	return f
}

func queryEntries(t *testing.T, f *testFixture) []domain.EpgEntry {
	t.Helper()
	var entries []domain.EpgEntry
	if err := f.db.Query(context.Background(), &entries,
		"SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE channel_id = 'chan1' ORDER BY start_at"); err != nil {
		t.Fatalf("query epg_entry: %v", err)
	}
	return entries
}

// assertRotation checks entries are contiguous and follow m0,m1,m2,m0,...
// starting at startIdx.
func assertRotation(t *testing.T, entries []domain.EpgEntry, startIdx int) {
	t.Helper()
	names := []string{"m0", "m1", "m2"}
	for i, e := range entries {
		if want := names[(startIdx+i)%3]; e.CatalogItemID != want {
			t.Errorf("entry %d = %s, want %s", i, e.CatalogItemID, want)
		}
		if i > 0 && e.StartAt != entries[i-1].EndAt {
			t.Errorf("entry %d starts at %d, previous ended at %d (not contiguous)", i, e.StartAt, entries[i-1].EndAt)
		}
	}
}

func TestContinuous_AloneRotatesFromNow(t *testing.T) {
	f := continuousSetup(t)
	mat := New(f.db, 5*time.Hour)
	before := time.Now()
	if err := mat.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries := queryEntries(t, f)
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5 one-hour programs in a 5h window: %+v", len(entries), entries)
	}
	assertRotation(t, entries, 0)
	if entries[0].StartAt < before.UnixMilli()-1000 {
		t.Errorf("first continuous entry starts in the past: %d < %d", entries[0].StartAt, before.UnixMilli())
	}
	for _, e := range entries {
		if e.Kind != domain.EpgProgram || e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "cont" {
			t.Errorf("unexpected entry %+v", e)
		}
	}
}

func TestContinuous_WithTimedItemResumesRotation(t *testing.T) {
	f := continuousSetup(t)
	// Short movies so the timed slot, a few minutes out, interrupts the rotation.
	min := int64(60 * 1000)
	f.createCatalogItem(t, "timed", 5*min)
	f.createBlock(t, "timedblock")
	f.addBlockItem(t, "bt", "timedblock", 0, "timed")
	if err := f.db.Exec(context.Background(), "UPDATE catalog_item SET duration_ms = ? WHERE id IN ('m0','m1','m2')", 2*min); err != nil {
		t.Fatal(err)
	}
	dow, hhmm, occ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "wk", "sched1", "timedblock", dow, hhmm)

	mat := New(f.db, 20*time.Minute)
	if err := mat.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries := queryEntries(t, f)

	var timedIdx = -1
	for i, e := range entries {
		if e.CatalogItemID == "timed" {
			timedIdx = i
			if e.StartAt != occ.UnixMilli() {
				t.Errorf("timed item starts at %d, want its nominal %d", e.StartAt, occ.UnixMilli())
			}
		}
	}
	if timedIdx <= 0 || timedIdx == len(entries)-1 {
		t.Fatalf("expected continuous programs before and after the timed one, got %+v", entries)
	}
	before, after := entries[:timedIdx], entries[timedIdx+1:]
	for _, e := range before {
		if e.EndAt > occ.UnixMilli() {
			t.Errorf("continuous program %+v crosses the timed item's start", e)
		}
	}
	assertRotation(t, before, 0)
	// Rotation resumes where it left off, not from m0.
	assertRotation(t, after, len(before))
	if after[0].StartAt != entries[timedIdx].EndAt {
		t.Errorf("continuous does not resume right after the timed item")
	}
}

func TestContinuous_FreezeKeepsRowsAndRotationContinues(t *testing.T) {
	f := continuousSetup(t)
	mat := New(f.db, 6*time.Hour)
	mat.SetFreezeMargin(150 * time.Minute) // keeps the programs starting in the first 2.5h
	ctx := context.Background()
	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	first := queryEntries(t, f)
	if len(first) != 6 {
		t.Fatalf("run 1: %d entries, want 6", len(first))
	}

	if err := mat.Run(ctx); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	second := queryEntries(t, f)
	if len(second) < 6 {
		t.Fatalf("run 2: %d entries, want at least 6", len(second))
	}
	// Programs starting before now+margin (the first three) keep their ids.
	for i := 0; i < 3; i++ {
		if second[i].ID != first[i].ID {
			t.Errorf("entry %d was regenerated inside the freeze margin", i)
		}
	}
	// What starts after the margin is recomputed (fresh ids)...
	if second[3].ID == first[3].ID {
		t.Errorf("entry 3 was not recomputed after the freeze margin")
	}
	// ...and the rotation carries on seamlessly across the kept/new boundary.
	assertRotation(t, second, 0)
}

func TestContinuous_RotationSurvivesAcrossTimeAndInstances(t *testing.T) {
	f := continuousSetup(t)
	ctx := context.Background()
	mat := New(f.db, 4*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Move the whole grid 90 minutes into the past: m0 is history, m1 airs now.
	shift := int64(90 * 60 * 1000)
	if err := f.db.Exec(ctx, "UPDATE epg_entry SET start_at = start_at - ?, end_at = end_at - ?", shift, shift); err != nil {
		t.Fatal(err)
	}
	// A brand new Materializer (no in-memory state) must resume from the
	// stored position, not restart at m0.
	mat2 := New(f.db, 4*time.Hour)
	mat2.SetFreezeMargin(time.Hour)
	if err := mat2.Run(ctx); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	assertRotation(t, entries, 0)
	last := entries[len(entries)-1]
	if last.EndAt < time.Now().Add(3*time.Hour).UnixMilli() {
		t.Errorf("grid was not extended ahead: last ends %v", time.UnixMilli(last.EndAt))
	}
}

func TestContinuous_ResetDiscardsAiringEntryAndRestartsRotation(t *testing.T) {
	f := continuousSetup(t)
	ctx := context.Background()
	mat := New(f.db, 4*time.Hour)
	mat.SetFreezeMargin(time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	shift := int64(90 * 60 * 1000)
	if err := f.db.Exec(ctx, "UPDATE epg_entry SET start_at = start_at - ?, end_at = end_at - ?", shift, shift); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	var airing []domain.EpgEntry
	if err := f.db.Query(ctx, &airing, "SELECT "+epgEntryColumnsForTest+" FROM epg_entry WHERE start_at <= ? AND end_at > ?", now, now); err != nil || len(airing) != 1 {
		t.Fatalf("airing entry: %v %v", airing, err)
	}
	if airing[0].CatalogItemID != "m1" {
		t.Fatalf("setup: airing = %s, want m1", airing[0].CatalogItemID)
	}

	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	var revBefore []int64
	f.db.Query(ctx, &revBefore, "SELECT revision FROM epg_revision WHERE tenant_id = ?", f.tenantID)
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatalf("ResetChannel: %v", err)
	}

	entries := queryEntries(t, f)
	var future []domain.EpgEntry
	for _, e := range entries {
		if e.EndAt > now {
			future = append(future, e)
		}
		if e.ID == airing[0].ID {
			t.Errorf("the airing entry survived the reset")
		}
	}
	if len(future) == 0 {
		t.Fatal("reset did not regenerate")
	}
	if future[0].CatalogItemID != "m0" {
		t.Errorf("rotation did not restart: first program = %s, want m0", future[0].CatalogItemID)
	}
	if future[0].StartAt < now-1000 || future[0].StartAt > time.Now().UnixMilli()+1000 {
		t.Errorf("first regenerated program should start now, got %v", time.UnixMilli(future[0].StartAt))
	}
	assertRotation(t, future, 0)

	var revAfter []int64
	f.db.Query(ctx, &revAfter, "SELECT revision FROM epg_revision WHERE tenant_id = ?", f.tenantID)
	if len(revAfter) != 1 || (len(revBefore) == 1 && revAfter[0] <= revBefore[0]) {
		t.Errorf("revision not bumped: before=%v after=%v", revBefore, revAfter)
	}
}

func TestContinuous_TimedOnlyChannelStillRegeneratesFully(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.createCatalogItem(t, "item1", 5*60*1000)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "block1")
	f.addBlockItem(t, "bi1", "block1", 0, "item1")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	dow, hhmm, _ := nearFutureWeeklySlot()
	f.createWeeklyScheduleItem(t, "se1", "sched1", "block1", dow, hhmm)
	mat := New(f.db, 2*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first := queryEntries(t, f)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	second := queryEntries(t, f)
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Errorf("timed-only channel should be fully regenerated: %v / %v", first, second)
	}
}
