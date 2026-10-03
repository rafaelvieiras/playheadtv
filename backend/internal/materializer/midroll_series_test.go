package materializer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const (
	midDur  = int64(3 * 60 * 1000)
	fbDur   = int64(60 * 1000)
	epLen22 = int64(22 * 60 * 1000)
)

// addSeriesItemAmount seeds a series block item for seedSeries' series `title`.
func (f *testFixture) addSeriesItemAmount(t *testing.T, id, blockID string, position int, title, amountJSON string) {
	t.Helper()
	spec := fmt.Sprintf(`{"media_source_id":%q,"series_external_id":"ser-%s","series_title":%q,"playback_order":"original","seed":42,"amount":%s}`, f.sourceID, title, title, amountJSON)
	if err := f.db.Exec(context.Background(),
		"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, spec) VALUES (?, ?, ?, 'series', NULL, ?)",
		id, blockID, position, spec,
	); err != nil {
		t.Fatalf("seed series block_item %s: %v", id, err)
	}
}

// midrollFillers seeds a midroll pool (one 3-minute item "mid") and a
// fallback pool (one 1-minute item "fb"), both as channel rules.
func (f *testFixture) midrollFillers(t *testing.T, withFallback bool) {
	t.Helper()
	f.createCatalogItem(t, "mid", midDur)
	f.createFillerPool(t, "midpool")
	f.addFillerPoolItem(t, "midpi", "midpool", "mid")
	f.createFillerRule(t, "midrule", "channel", "chan1", "midroll", "midpool", "sequential")
	if withFallback {
		f.createCatalogItem(t, "fb", fbDur)
		f.createFillerPool(t, "fbpool")
		f.addFillerPoolItem(t, "fbpi", "fbpool", "fb")
		f.createFillerRule(t, "fbrule", "channel", "chan1", "fallback", "fbpool", "sequential")
	}
}

// layout renders entries as "A001 [mid] A002 ...", marking midroll with
// the item id in brackets.
func layout(entries []domain.EpgEntry) string {
	var parts []string
	for _, e := range entries {
		if e.Kind == domain.EpgMidRoll {
			parts = append(parts, "["+e.CatalogItemID+"]")
		} else {
			parts = append(parts, e.CatalogItemID)
		}
	}
	return strings.Join(parts, " ")
}

func nextFreeSlot() time.Time { return time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute) }

// The reported case: a timed 2h play-once block with A (2 per visit), B (1)
// and C (1), a midroll rule and a fallback rule.
func TestMidrollSeries_EndToEnd_TimedBlockWithSeveralSeries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedSeries(t, "A", 6, epLen22)
	f.seedSeries(t, "B", 6, epLen22)
	f.seedSeries(t, "C", 6, epLen22)
	f.createCatalogItem(t, "after", 30*60*1000)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":2}`)
	f.addSeriesItemAmount(t, "sb", "blk", 1, "B", `{"mode":"count","count":1}`)
	f.addSeriesItemAmount(t, "sc", "blk", 2, "C", `{"mode":"count","count":1}`)
	f.createBlock(t, "next")
	f.addBlockItem(t, "bnext", "next", 0, "after")
	f.midrollFillers(t, true)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	start := nextFreeSlot()
	end := start.Add(2 * time.Hour)
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", start)
	f.createOnceScheduleItem(t, "occ2", "sched1", "next", end)

	if err := New(f.db, 6*time.Hour).Run(ctx); err != nil {
		t.Fatal(err)
	}
	var entries []domain.EpgEntry
	for _, e := range queryEntries(t, f) {
		// The fallback also pads from "now" up to the slot start.
		if e.StartAt >= start.UnixMilli() {
			entries = append(entries, e)
		}
	}
	assertContiguous(t, entries)
	var slot []domain.EpgEntry
	for _, e := range entries {
		if e.StartAt < end.UnixMilli() {
			slot = append(slot, e)
		}
		if e.StartAt < end.UnixMilli() && e.EndAt > end.UnixMilli() {
			t.Fatalf("entry %s crosses the slot end", e.CatalogItemID)
		}
	}
	got := layout(slot)
	// 4 episodes and 3 breaks = 97 minutes; the fallback pads the other 23.
	wantHead := "A001 [mid] A002 [mid] B001 [mid] C001"
	if !strings.HasPrefix(got, wantHead) {
		t.Fatalf("slot = %s, want it to start %q", got, wantHead)
	}
	rest := strings.Fields(strings.TrimPrefix(got, wantHead))
	if len(rest) != 23 {
		t.Fatalf("expected 23 one-minute fallback entries after the block, got %d: %v", len(rest), rest)
	}
	for _, id := range rest {
		if id != "fb" {
			t.Fatalf("after the block only fallback may air, got %s", id)
		}
	}
	if slot[0].StartAt != start.UnixMilli() {
		t.Errorf("slot starts at %v, want %v", time.UnixMilli(slot[0].StartAt), start)
	}
	if last := slot[len(slot)-1]; last.EndAt != end.UnixMilli() {
		t.Errorf("slot coverage ends at %v, want %v", time.UnixMilli(last.EndAt), end)
	}
	// The next occurrence starts exactly on time and nothing of the first
	// block airs after the slot end.
	next := entries[len(slot)]
	if next.CatalogItemID != "after" || next.StartAt != end.UnixMilli() {
		t.Errorf("next occurrence = %s at %v, want it at %v", next.CatalogItemID, time.UnixMilli(next.StartAt), end)
	}
	// Breaks never carry a series identity.
	for _, e := range entries {
		if e.Kind == domain.EpgMidRoll && (e.SourceBlockItemID != nil || e.SeriesCycle != nil) {
			t.Fatalf("a midroll break carries series bookkeeping: %+v", e)
		}
	}
}

// A break that would push the next episode past the slot end follows the
// catalog-item rule: the break may air if it fits, the episode that would not
// end in time is dropped, nothing airs past the slot end.
func TestMidrollSeries_SlotEndStillWins(t *testing.T) {
	f := newFixture(t)
	f.seedSeries(t, "A", 6, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"slot_end"}`)
	f.createBlock(t, "next")
	f.createCatalogItem(t, "after", 30*60*1000)
	f.addBlockItem(t, "bnext", "next", 0, "after")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	start := nextFreeSlot()
	end := start.Add(60 * time.Minute) // 22 + 3 + 22 = 47, then 3 + 22 would end at 72
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", start)
	f.createOnceScheduleItem(t, "occ2", "sched1", "next", end)
	if err := New(f.db, 6*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var slot []domain.EpgEntry
	for _, e := range queryEntries(t, f) {
		if e.StartAt < end.UnixMilli() {
			slot = append(slot, e)
			if e.EndAt > end.UnixMilli() {
				t.Fatalf("%s airs past the slot end", e.CatalogItemID)
			}
		}
	}
	if got, want := layout(slot), "A001 [mid] A002 [mid]"; got != want {
		t.Fatalf("slot = %s, want %s", got, want)
	}
}

// Regenerating with midroll active never mistakes a break for an episode: the
// episode sequence is identical on every run and the cursor continues across
// two occurrences of one block (visits of two episodes: A001 A002 | A003 A004).
func TestMidrollSeries_RegenerationDoesNotDriftTheCursor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedSeries(t, "A", 8, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":2}`)
	f.createCatalogItem(t, "outro", 10*60*1000)
	f.addBlockItem(t, "bo", "blk", 1, "outro")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	start := nextFreeSlot()
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", start)
	f.createOnceScheduleItem(t, "occ2", "sched1", "blk", start.Add(2*time.Hour))

	mat := New(f.db, 6*time.Hour)
	var first string
	for run := 0; run < 5; run++ {
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		got := layout(queryEntries(t, f))
		if run == 0 {
			first = got
			want := "A001 [mid] A002 [mid] outro A003 [mid] A004 [mid] outro"
			if got != want {
				t.Fatalf("first run = %s, want %s", got, want)
			}
			continue
		}
		if got != first {
			t.Fatalf("run %d drifted:\n%s\nvs\n%s", run+1, got, first)
		}
	}
	// A reset restarts the walk at the first episode.
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	if err := mat.ResetChannel(ctx, f.tenantID, ch); err != nil {
		t.Fatal(err)
	}
	if got := layout(queryEntries(t, f)); got != first {
		t.Fatalf("after reset = %s, want %s", got, first)
	}
}

// A criteria item gets the same breaks, resolved through the same rule.
func TestMidrollSeries_CriteriaItemGetsMidrollBetweenItsEpisodes(t *testing.T) {
	f := newFixture(t)
	f.seedSeries(t, "A", 3, epLen22)
	f.seedSeries(t, "B", 3, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addCriteriaBlockItem(t, "crit", "blk", 0, epsQuery, "original", `{"mode":"count","count":3}`)
	f.createCatalogItem(t, "outro", 10*60*1000)
	f.addBlockItem(t, "bo", "blk", 1, "outro")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", nextFreeSlot())
	if err := New(f.db, 6*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Break between episodes, and between the visit and the next item.
	if got, want := layout(queryEntries(t, f)), "A001 [mid] B001 [mid] A002 [mid] outro"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
}

// A block-scoped midroll rule wins over the channel's, for series episodes as
// for catalog items.
func TestMidrollSeries_BlockScopedRuleOverridesChannelRule(t *testing.T) {
	f := newFixture(t)
	f.seedSeries(t, "A", 3, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":3}`)
	f.midrollFillers(t, false)
	f.createCatalogItem(t, "bmid", midDur)
	f.createFillerPool(t, "bpool")
	f.addFillerPoolItem(t, "bpi", "bpool", "bmid")
	f.createFillerRule(t, "brule", "block", "blk", "midroll", "bpool", "sequential")
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", nextFreeSlot())
	if err := New(f.db, 6*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := layout(queryEntries(t, f)), "A001 [bmid] A002 [bmid] A003"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
}

// Without a midroll rule nothing changes for series.
func TestMidrollSeries_NoRuleNoBreaks(t *testing.T) {
	f := newFixture(t)
	f.seedSeries(t, "A", 3, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":3}`)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", nextFreeSlot())
	if err := New(f.db, 6*time.Hour).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := layout(queryEntries(t, f)), "A001 A002 A003"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
}

// Continuous channel: breaks inside visits and between visits, and a
// regeneration whose kept head ends mid-visit (or on a break) reproduces the
// uninterrupted sequence exactly.
func TestMidrollSeries_ContinuousChannelRegenerationIsStable(t *testing.T) {
	for _, freeze := range []time.Duration{20 * time.Minute, 30 * time.Minute, 47 * time.Minute, 50 * time.Minute, 75 * time.Minute, 2 * time.Hour} {
		t.Run(freeze.String(), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.seedSeries(t, "A", 6, epLen22)
			f.seedSeries(t, "B", 6, epLen22)
			f.createChannel(t, "chan1", "UTC")
			f.createBlock(t, "blk")
			f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":3}`)
			f.addSeriesItemAmount(t, "sb", "blk", 1, "B", `{"mode":"count","count":1}`)
			f.midrollFillers(t, false)
			f.createSchedule(t, "sched1")
			f.assignChannelSchedule(t, "chan1", "sched1")
			f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

			mat := New(f.db, 5*time.Hour)
			mat.SetFreezeMargin(freeze)
			if err := mat.Run(ctx); err != nil {
				t.Fatal(err)
			}
			first := queryEntries(t, f)
			want := layout(first)
			if !strings.HasPrefix(want, "A001 [mid] A002 [mid] A003 [mid] B001 [mid] A004 [mid] A005 [mid] A006 [mid] B002 [mid] A001") {
				t.Fatalf("unexpected layout: %s", want)
			}
			for run := 0; run < 3; run++ {
				if err := mat.Run(ctx); err != nil {
					t.Fatal(err)
				}
				got := queryEntries(t, f)
				if layout(got) != want {
					t.Fatalf("run %d drifted:\n%s\nvs\n%s", run+2, layout(got), want)
				}
				assertContiguous(t, got)
			}
		})
	}
}

// An interleaved dynamic block gets breaks between its episodes.
func TestMidrollSeries_InterleavedDynamicBlock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedSeries(t, "A", 4, epLen22)
	f.seedSeries(t, "B", 4, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createInterleavedBlock(t, "dyn", epsQuery, domain.OrderInterleaved, `{"per_round":1,"on_exhaustion":"restart","series_order":"title"}`, 42)
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	start := nextFreeSlot()
	f.createOnceScheduleItem(t, "occ1", "sched1", "dyn", start)
	mat := New(f.db, 4*time.Hour)
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got := layout(queryEntries(t, f))
	if !strings.HasPrefix(got, "A001 [mid] B001 [mid] A002 [mid] B002 [mid] A003") {
		t.Fatalf("entries = %s", got)
	}
	if err := mat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if again := layout(queryEntries(t, f)); again != got {
		t.Fatalf("regeneration drifted:\n%s\nvs\n%s", again, got)
	}

	// The dry-run (epg-preview) shows the same breaks and writes nothing.
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	preview, err := mat.PreviewReset(ctx, f.tenantID, ch, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p := layout(preview); !strings.HasPrefix(p, "A001 [mid] B001 [mid] A002 [mid] B002 [mid] A003") {
		t.Fatalf("preview = %s", p)
	}
}

// The dry-run of a manual block with series shows the breaks too.
func TestMidrollSeries_PreviewShowsBreaksForManualBlock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedSeries(t, "A", 3, epLen22)
	f.seedSeries(t, "B", 3, epLen22)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":2}`)
	f.addSeriesItemAmount(t, "sb", "blk", 1, "B", `{"mode":"count","count":1}`)
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createOnceScheduleItem(t, "occ1", "sched1", "blk", nextFreeSlot())
	var ch domain.Channel
	if err := f.db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE id = 'chan1'"); err != nil {
		t.Fatal(err)
	}
	preview, err := New(f.db, 6*time.Hour).PreviewReset(ctx, f.tenantID, ch, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layout(preview), "A001 [mid] A002 [mid] B001"; got != want {
		t.Fatalf("preview = %s, want %s", got, want)
	}
	if n := len(queryEntries(t, f)); n != 0 {
		t.Fatalf("preview wrote %d entries", n)
	}
}
