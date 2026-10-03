package materializer

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Series episodes are programs too: each gets its own preroll/postroll with
// the midroll between episodes, across visits, and a regeneration keeping any
// head resumes without drifting, whatever stage the head ends in.
func TestEndless_ContinuousSeriesEpisodesWithAllFillers(t *testing.T) {
	for _, freeze := range []time.Duration{20 * time.Minute, 30 * time.Minute, 47 * time.Minute, 50 * time.Minute, 75 * time.Minute, 2 * time.Hour} {
		t.Run(freeze.String(), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.seedSeries(t, "A", 6, epLen22)
			f.seedSeries(t, "B", 6, epLen22)
			f.createChannel(t, "chan1", "UTC")
			f.createBlock(t, "blk")
			f.addSeriesItemAmount(t, "sa", "blk", 0, "A", `{"mode":"count","count":2}`)
			f.addSeriesItemAmount(t, "sb", "blk", 1, "B", `{"mode":"count","count":1}`)
			for id, d := range map[string]int64{"pre": 1 * minMs, "mid": 2 * minMs, "post": 3 * minMs} {
				f.createCatalogItem(t, id, d)
				f.createFillerPool(t, id+"pool")
				f.addFillerPoolItem(t, id+"pi", id+"pool", id)
			}
			f.createFillerRule(t, "r1", "channel", "chan1", "preroll", "prepool", "sequential")
			f.createFillerRule(t, "r2", "channel", "chan1", "midroll", "midpool", "sequential")
			f.createFillerRule(t, "r3", "channel", "chan1", "postroll", "postpool", "sequential")
			f.createSchedule(t, "sched1")
			f.assignChannelSchedule(t, "chan1", "sched1")
			f.createContinuousScheduleItem(t, "cont", "sched1", "blk")

			mat := New(f.db, 5*time.Hour)
			mat.SetFreezeMargin(freeze)
			if err := mat.Run(ctx); err != nil {
				t.Fatal(err)
			}
			first := queryEntries(t, f)
			want := seq(first)
			if !strings.HasPrefix(want, "pre A001 post mid pre A002 post mid pre B001 post mid pre A003 post mid pre A004 post mid pre B002 post") {
				t.Fatalf("unexpected layout: %s", want)
			}
			for run := 0; run < 3; run++ {
				if err := mat.Run(ctx); err != nil {
					t.Fatal(err)
				}
				got := queryEntries(t, f)
				if seq(got) != want {
					t.Fatalf("run %d drifted:\n%s\nvs\n%s", run+2, seq(got), want)
				}
				assertContiguous(t, got)
			}
		})
	}
}
