package materializer

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func (f *testFixture) createCutRuleSet(t *testing.T, scope, scopeID string, rulesJSON string) domain.CutRuleSet {
	t.Helper()
	in := cutruleset.Input{ScopeType: scope, ScopeID: scopeID}
	if err := json.Unmarshal([]byte(rulesJSON), &in.Rules); err != nil {
		t.Fatalf("rules: %v", err)
	}
	set, err := cutruleset.Create(context.Background(), f.db, f.tenantID, in)
	if err != nil {
		t.Fatalf("create cut rule set: %v", err)
	}
	return set
}

func newRuleCutsMat(f *testFixture, window time.Duration, freeze time.Duration) *Materializer {
	mat := New(f.db, window)
	if freeze > 0 {
		mat.SetFreezeMargin(freeze)
	}
	mat.SetCutPlanner(RuleSetCuts(f.db))
	return mat
}

// shape is an entry minus its random id, to compare two generations.
func shape(entries []domain.EpgEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		grp := "-"
		if e.SplitGroup != nil {
			grp = "g"
		}
		out[i] = fmt.Sprintf("%s|%s|%d|%d|%d|%s", e.Kind, e.CatalogItemID, e.StartAt, e.EndAt, e.ItemOffsetMs, grp)
	}
	return out
}

// bigMovieSetup: a continuous channel airing one 4 hour movie (and the 3 minute
// midroll filler).
func bigMovieSetup(t *testing.T) *testFixture {
	t.Helper()
	f := newFixture(t)
	f.createCatalogItem(t, "big", 4*hourMs)
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "bbig", "blk", 0, "big")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	return f
}

const movieEveryHour = `[{"match":{"kinds":["movie"],"min_duration_ms":10800000},"strategy":{"type":"interval","every_ms":3600000}}]`

func TestRuleSetCuts_MovieOverThreeHoursIsCutEveryHour(t *testing.T) {
	f := bigMovieSetup(t)
	f.createCutRuleSet(t, "channel", "chan1", movieEveryHour)
	if err := newRuleCutsMat(f, 12*time.Hour, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	want := "program:big@0 midroll:mid@0 program:big@60 midroll:mid@0 program:big@120 midroll:mid@0 program:big@180"
	if got := segs(entries[:7]); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	assertTight(t, entries)
	for _, e := range entries[:7] {
		if e.Kind == domain.EpgProgram && (e.SplitGroup == nil || *e.SplitGroup != *entries[0].SplitGroup) {
			t.Fatalf("segments do not share a split group")
		}
	}
}

func TestRuleSetCuts_NoSetIsByteIdenticalToNoPlanner(t *testing.T) {
	// The same grid is generated twice at the same instant: without a planner and
	// with the rule set planner while no set governs it (none at all, then one on
	// another channel and one that matches nothing here).
	run := func(f *testFixture, withPlanner bool) []string {
		mat := New(f.db, 13*time.Hour)
		if withPlanner {
			mat.SetCutPlanner(RuleSetCuts(f.db))
		}
		if err := mat.ResetChannel(context.Background(), f.tenantID, domain.Channel{ID: "chan1", Timezone: "UTC", ScheduleID: strPtr("sched1")}); err != nil {
			t.Fatal(err)
		}
		return shape(queryEntries(t, f))
	}
	// ResetChannel regenerates from "now", which moved between runs: compare
	// only shapes relative to the first entry's start.
	rel := func(s []string) []string {
		var base int64
		out := make([]string, len(s))
		for i, line := range s {
			parts := strings.Split(line, "|")
			var st, en int64
			fmt.Sscan(parts[2], &st)
			fmt.Sscan(parts[3], &en)
			if i == 0 {
				base = st
			}
			out[i] = fmt.Sprintf("%s|%s|%d|%d|%s", parts[0], parts[1], st-base, en-base, parts[4])
		}
		return out
	}
	f := bigMovieSetup(t)
	plain := run(f, false)
	if len(plain) == 0 {
		t.Fatal("no entries")
	}
	if got := run(f, true); !reflect.DeepEqual(rel(got), rel(plain)) {
		t.Fatalf("a planner with no set changed the grid:\n%v\n%v", rel(got), rel(plain))
	}
	f.createChannel2(t, "other")
	f.createCutRuleSet(t, "channel", "other", movieEveryHour)
	f.createCutRuleSet(t, "channel", "chan1", `[{"match":{"kinds":["episode"]},"strategy":{"type":"percentage","percentages":[50]}}]`)
	if got := run(f, true); !reflect.DeepEqual(rel(got), rel(plain)) {
		t.Fatalf("an unrelated set changed the grid:\n%v\n%v", rel(got), rel(plain))
	}
}

func (f *testFixture) createChannel2(t *testing.T, id string) {
	t.Helper()
	f.createChannelWithNumber(t, id, 2, "UTC")
}

func TestRuleSetCuts_BlockSetBeatsChannelSet(t *testing.T) {
	f := bigMovieSetup(t)
	f.createCutRuleSet(t, "channel", "chan1", `[{"strategy":{"type":"offsets","offsets_ms":[3600000]}}]`)
	f.createCutRuleSet(t, "block", "blk", `[{"strategy":{"type":"offsets","offsets_ms":[7200000]}}]`)
	if err := newRuleCutsMat(f, 6*time.Hour, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	if got, want := segs(entries[:3]), "program:big@0 midroll:mid@0 program:big@120"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestRuleSetCuts_ChapterStrategyReadsStoredChapters(t *testing.T) {
	f := newFixture(t)
	f.createCatalogItem(t, "film", 2*hourMs)
	// Chapters at 0, 40, 62 and 95 minutes: the middle (60) snaps to 62.
	if err := f.db.Exec(context.Background(), `UPDATE catalog_item SET chapters = ? WHERE id = 'film'`,
		`[{"name":"a","start_ms":0},{"name":"b","start_ms":2400000},{"name":"c","start_ms":3720000},{"name":"d","start_ms":5700000}]`); err != nil {
		t.Fatal(err)
	}
	f.createChannel(t, "chan1", "UTC")
	f.createBlock(t, "blk")
	f.addBlockItem(t, "bfilm", "blk", 0, "film")
	f.midrollFillers(t, false)
	f.createSchedule(t, "sched1")
	f.assignChannelSchedule(t, "chan1", "sched1")
	f.createContinuousScheduleItem(t, "cont", "sched1", "blk")
	f.createCutRuleSet(t, "channel", "chan1", `[{"match":{"has_chapters":true},"strategy":{"type":"chapter","roles":["middle"]}}]`)
	if err := newRuleCutsMat(f, 5*time.Hour, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := queryEntries(t, f)
	if len(entries) < 3 || entries[2].Kind != domain.EpgProgram || entries[2].ItemOffsetMs != 62*minMs {
		t.Fatalf("expected the cut at the 62 minute chapter, got %s", segs(entries))
	}
}

// The episode rule (50 %) cuts every episode of a series in two, the series
// cursor still advances one episode per airing (not per segment), and a
// regeneration, including one landing inside a split episode, resumes in the
// same place.
func TestRuleSetCuts_SeriesEpisodesAreCutAndTheCursorFollowsAirings(t *testing.T) {
	f := seriesContinuousSetup(t, domain.OrderOriginal)
	f.midrollFillers(t, false)
	f.createCutRuleSet(t, "channel", "chan1", `[{"match":{"kinds":["episode"]},"strategy":{"type":"percentage","percentages":[50]}}]`)
	ctx := context.Background()

	// airings lists the series episodes by airing, each split group once.
	airings := func(entries []domain.EpgEntry) []string {
		var out []string
		seen := map[string]bool{}
		for _, e := range entries {
			if e.Kind != domain.EpgProgram {
				continue
			}
			key := e.ID
			if e.SplitGroup != nil {
				key = *e.SplitGroup
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, e.CatalogItemID)
			}
		}
		return out
	}

	for _, freeze := range []time.Duration{20 * time.Minute, 40 * time.Minute, 90 * time.Minute} {
		mat := newRuleCutsMat(f, 8*time.Hour, freeze)
		if err := mat.Run(ctx); err != nil {
			t.Fatal(err)
		}
		first := queryEntries(t, f)
		// Each episode is two 15 minute segments around a midroll.
		if got := segs(first[:3]); got != "program:s1@0 midroll:mid@0 program:s1@15" {
			t.Fatalf("freeze %v: first airing: %s", freeze, segs(first[:6]))
		}
		for _, e := range first {
			if e.Kind == domain.EpgProgram && e.EndAt-e.StartAt != epMs/2 {
				t.Fatalf("segment of %s lasts %d ms", e.CatalogItemID, e.EndAt-e.StartAt)
			}
		}
		assertTight(t, first)
		want := airings(first)
		if len(want) < 6 || strings.Join(want[:6], ",") != "s1,s2,s3,s4,s5,s1" {
			t.Fatalf("freeze %v: series order broken: %v", freeze, want)
		}
		for run := 0; run < 3; run++ {
			if err := mat.Run(ctx); err != nil {
				t.Fatal(err)
			}
			got := queryEntries(t, f)
			assertTight(t, got)
			if !reflect.DeepEqual(airings(got), want) {
				t.Fatalf("freeze %v run %d: the cursor drifted:\n%v\nvs\n%v", freeze, run+2, airings(got), want)
			}
		}
		// Every segment of an airing is still there after a rerun.
		groups := map[string]int{}
		for _, e := range queryEntries(t, f) {
			if e.SplitGroup != nil {
				groups[*e.SplitGroup]++
			}
		}
		for g, n := range groups {
			if n != 2 {
				t.Fatalf("freeze %v: group %s kept %d segments", freeze, g, n)
			}
		}
		if err := mat.ResetChannel(ctx, f.tenantID, domain.Channel{ID: "chan1", Timezone: "UTC", ScheduleID: strPtr("sched1")}); err != nil {
			t.Fatal(err)
		}
	}
}
