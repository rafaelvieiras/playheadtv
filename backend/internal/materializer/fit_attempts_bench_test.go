package materializer

import (
	"fmt"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// benchTailInput is a channel of 96 half-hour once-slots, each holding a 21
// minute item and a tail rule over a pool of poolSize items, mostly long, so
// most candidates miss the 9 residual minutes. It prices the attempts in a
// full-window regeneration.
func benchTailInput(poolSize int, sel domain.SelectionStrategy, attempts int) channelInput {
	in := channelInput{
		ChannelID: "chan1", TenantID: "t", Timezone: "UTC",
		BlockItems:      map[string][]domain.BlockItem{"blk": {{ID: "b0", BlockID: "blk", CatalogItemID: "p21"}}},
		CatalogDuration: map[string]int64{"p21": 21 * minMs},
		PoolItems:       map[string][]domain.FillerPoolItem{"pool": nil},
		RepeatBlockIDs:  map[string]bool{},
		FillerRules: []domain.FillerRule{{
			ID: "tail", Scope: domain.ScopeBlock, ScopeID: "blk", Kind: domain.FillerTail,
			FillerPoolID: "pool", Selection: sel, MaxFitAttempts: attempts,
		}},
	}
	for i := 0; i < poolSize; i++ {
		id := fmt.Sprintf("f%d", i)
		// One item in ten is short (30s to 3 min); the rest are 12 to 60 min.
		d := (12 + int64(i%49)) * minMs
		if i%10 == 0 {
			d = (30 + int64(i%6)*30) * sec
		}
		in.CatalogDuration[id] = d
		in.PoolItems["pool"] = append(in.PoolItems["pool"], domain.FillerPoolItem{ID: "pi" + id, FillerPoolID: "pool", CatalogItemID: id, Weight: 1 + i%5})
	}
	for s := 0; s < 96; s++ {
		in.ScheduleItems = append(in.ScheduleItems, domain.ScheduleItem{
			ID: fmt.Sprintf("s%d", s), BlockID: "blk", Recurrence: domain.RecurrenceOnce,
			StartAt: int64Ptr(t0.Add(time.Duration(s) * 30 * time.Minute).UnixMilli()), Active: true,
		})
	}
	return in
}

func BenchmarkTailFitAttempts(b *testing.B) {
	for _, sel := range []domain.SelectionStrategy{domain.SelectionSequential, domain.SelectionRandom, domain.SelectionWeighted} {
		for _, n := range []int{1, 5, 20} {
			in := benchTailInput(20000, sel, n)
			b.Run(fmt.Sprintf("%s/attempts=%d", sel, n), func(b *testing.B) {
				var entries int
				for i := 0; i < b.N; i++ {
					entries = len(expandChannel(in, t0, t0, t0.Add(49*time.Hour), newSelectionState()))
				}
				b.ReportMetric(float64(entries), "entries")
			})
		}
	}
}
