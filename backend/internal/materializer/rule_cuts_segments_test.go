package materializer

import (
	"context"
	"testing"
	"time"
)

// filmWithMarkers is a 2h film on a continuous channel whose chapters put the
// heuristic intro end at 7 minutes; segments (JSON, "" for none) is stored as
// the item's media_segments.
func filmWithMarkers(t *testing.T, segments string) *testFixture {
	t.Helper()
	f := newFixture(t)
	f.createCatalogItem(t, "film", 2*hourMs)
	ctx := context.Background()
	if err := f.db.Exec(ctx, `UPDATE catalog_item SET chapters = ? WHERE id = 'film'`,
		`[{"name":"a","start_ms":0},{"name":"b","start_ms":420000},{"name":"c","start_ms":3600000}]`); err != nil {
		t.Fatal(err)
	}
	if segments != "" {
		if err := f.db.Exec(ctx, `UPDATE catalog_item SET media_segments = ? WHERE id = 'film'`, segments); err != nil {
			t.Fatal(err)
		}
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "bfilm", "blk", 0, "film")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	f.createCutRuleSet(t, "channel", "chan1", `[{"strategy":{"type":"chapter","roles":["after_intro"],"fallback":{"type":"percentage","percentages":[25]}}}]`)
	return f
}

func firstCutOffset(t *testing.T, f *testFixture) int64 {
	t.Helper()
	if err := newRuleCutsMat(f, 5*time.Hour, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	if len(entries) < 3 {
		t.Fatalf("expected a split item, got %s", segs(entries))
	}
	return entries[2].ItemOffsetMs
}

func TestRuleSetCuts_IntroSegmentSplitsTheItem(t *testing.T) {
	f := filmWithMarkers(t, `[{"type":"intro","start_ms":60000,"end_ms":300000}]`)
	if got := firstCutOffset(t, f); got != 5*minMs {
		t.Fatalf("cut at %d ms, want the end of the intro segment (%d)", got, 5*minMs)
	}
}

func TestRuleSetCuts_NoSegmentUsesTheChapterHeuristic(t *testing.T) {
	for _, segments := range []string{"", "[]", `[{"type":"outro","start_ms":6600000,"end_ms":7000000}]`} {
		f := filmWithMarkers(t, segments)
		if got := firstCutOffset(t, f); got != 7*minMs {
			t.Fatalf("segments %q: cut at %d ms, want the chapter after the intro chapter (%d)", segments, got, 7*minMs)
		}
	}
}

func TestRuleSetCuts_SegmentsAreLoadedOnlyWhenARuleCanUseThem(t *testing.T) {
	f := newFixture(t)
	f.createCatalogItem(t, "film", 2*hourMs)
	ctx := context.Background()
	f.createChannel(t, "chan1", "UTC")
	f.createCutRuleSet(t, "channel", "chan1", `[{"strategy":{"type":"chapter","roles":["middle"]}}]`)
	snap := RuleSetCuts(f.db).(ruleSetCuts).BeginPass(ctx, f.tenantID).(*ruleSetSnapshot)
	snap.PlanCuts(CutItem{CatalogItemID: "film", DurationMs: 2 * hourMs, ChannelID: "chan1"})
	for key := range snap.items {
		if key != "film#c" {
			t.Fatalf("unexpected memo key %q: a middle-only set must not read segments", key)
		}
	}
	// Planning the same item twice reads it once.
	snap.PlanCuts(CutItem{CatalogItemID: "film", DurationMs: 2 * hourMs, ChannelID: "chan1"})
	if len(snap.items) != 1 {
		t.Fatalf("memo holds %d entries, want 1", len(snap.items))
	}
}
