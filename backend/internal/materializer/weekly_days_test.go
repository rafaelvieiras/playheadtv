package materializer

import (
	"context"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func TestWeeklyOccurrenceTimes_SeveralDays(t *testing.T) {
	// Monday 2026-09-28 .. Sunday 2026-10-04.
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	times, err := weeklyOccurrenceTimes(domain.Weekdays{4, 5}, "22:00", time.UTC, scanFrom, windowEnd)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC),
	}
	if len(times) != len(want) {
		t.Fatalf("got %v, want %v", times, want)
	}
	for i := range want {
		if !times[i].Equal(want[i]) {
			t.Errorf("occurrence %d = %v, want %v", i, times[i], want[i])
		}
	}

	all, _ := weeklyOccurrenceTimes(domain.Weekdays{0, 1, 2, 3, 4, 5, 6}, "18:00", time.UTC, scanFrom, windowEnd)
	if len(all) != 7 {
		t.Errorf("every day: %d occurrences, want 7", len(all))
	}
}

// A local time that lands on the next UTC day still belongs to the listed
// local weekday.
func TestWeeklyOccurrenceTimes_SeveralDaysNonUTC(t *testing.T) {
	loc := mustLoc(t, "America/Sao_Paulo") // UTC-3
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	// Thursday and Friday 22:00 local = Friday and Saturday 01:00 UTC.
	times, _ := weeklyOccurrenceTimes(domain.Weekdays{4, 5}, "22:00", loc, scanFrom, windowEnd)
	if len(times) != 2 ||
		!times[0].Equal(time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)) ||
		!times[1].Equal(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v", times)
	}
}

func TestBuildOccurrences_OneItemManyDaysSortedWithSameID(t *testing.T) {
	items := []domain.ScheduleItem{
		{ID: "multi", BlockID: "b", Recurrence: domain.RecurrenceWeekly, DaysOfWeek: domain.Weekdays{5, 1, 3}, StartTimeLocal: strPtr("20:00"), Active: true},
		{ID: "legacy", BlockID: "b", Recurrence: domain.RecurrenceWeekly, DaysOfWeek: domain.Weekdays{2}, StartTimeLocal: strPtr("09:00"), Active: true},
		{ID: "nodays", BlockID: "b", Recurrence: domain.RecurrenceWeekly, StartTimeLocal: strPtr("09:00"), Active: true},
	}
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	occ := buildOccurrences(items, time.UTC, scanFrom, windowEnd)
	var ids []string
	for i, o := range occ {
		ids = append(ids, o.ScheduleItemID)
		if i > 0 && o.NominalStart.Before(occ[i-1].NominalStart) {
			t.Errorf("occurrences not sorted: %v", occ)
		}
	}
	want := []string{"multi", "legacy", "multi", "multi"} // Mon, Tue, Wed, Fri
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

// The overrun rule applies per occurrence of a multi-day item: on the day a
// later item cuts it short its program is dropped, on the other day it airs.
func TestExpandChannel_MultiDayItemOverrunAndBoundary(t *testing.T) {
	hour := int64(time.Hour / time.Millisecond)
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		ScheduleItems: []domain.ScheduleItem{
			{ID: "multi", BlockID: "long", Recurrence: domain.RecurrenceWeekly, DaysOfWeek: domain.Weekdays{3, 4}, StartTimeLocal: strPtr("20:00"), Active: true},
			{ID: "cut", BlockID: "short", Recurrence: domain.RecurrenceWeekly, DaysOfWeek: domain.Weekdays{3}, StartTimeLocal: strPtr("20:30"), Active: true},
		},
		BlockItems: map[string][]domain.BlockItem{
			"long":  {{ID: "bi1", BlockID: "long", CatalogItemID: "movie"}},
			"short": {{ID: "bi2", BlockID: "short", CatalogItemID: "clip"}},
		},
		CatalogDuration: map[string]int64{"movie": hour, "clip": hour / 4},
	}
	scanFrom := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	entries := expandChannel(in, scanFrom, scanFrom, windowEnd, newSelectionState())

	var wed, thu []domain.EpgEntry
	for _, e := range entries {
		switch time.UnixMilli(e.StartAt).UTC().Weekday() {
		case time.Wednesday:
			wed = append(wed, e)
		case time.Thursday:
			thu = append(thu, e)
		}
	}
	// Wednesday: the 60-minute movie does not fit before the 20:30 item, so
	// only the clip airs, exactly at its nominal start.
	if len(wed) != 1 || wed[0].CatalogItemID != "clip" || wed[0].StartAt != time.Date(2026, 9, 30, 20, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("wednesday = %+v", wed)
	}
	// Thursday: nothing cuts it, the movie airs in full.
	if len(thu) != 1 || thu[0].CatalogItemID != "movie" || thu[0].StartAt != time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("thursday = %+v", thu)
	}
}

// End to end through the loader: one stored row, several weekdays.
func TestMaterializer_MultiDayItemMaterializesOnlyListedDays(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.createCatalogItem(t, "ep", int64(30*time.Minute/time.Millisecond))
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "bi1", "blk", 0, "ep")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createMultiDayScheduleItem(t, "multi", "sched1", "blk", domain.Weekdays{1, 4}, "10:00")

	mat := New(f.db, 7*24*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	if len(entries) < 2 {
		t.Fatalf("expected at least two occurrences in a week, got %d", len(entries))
	}
	for _, e := range entries {
		start := time.UnixMilli(e.StartAt).UTC()
		if wd := start.Weekday(); wd != time.Monday && wd != time.Thursday {
			t.Errorf("entry on %v, want only Monday/Thursday", wd)
		}
		if start.Hour() != 10 || start.Minute() != 0 {
			t.Errorf("entry at %v, want 10:00", start)
		}
		if e.SourceScheduleItemID == nil || *e.SourceScheduleItemID != "multi" {
			t.Errorf("entry source = %v, want multi", e.SourceScheduleItemID)
		}
	}
}
