package cutplan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

const min = int64(60 * 1000)

func mins(v ...int64) []int64 {
	out := make([]int64, len(v))
	for i, m := range v {
		out[i] = m * min
	}
	return out
}

func chapters(starts ...int64) []domain.CatalogChapter {
	out := make([]domain.CatalogChapter, len(starts))
	for i, s := range starts {
		out[i] = domain.CatalogChapter{Name: "Chapter", StartMs: s * min}
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

var (
	longMovieEvery1h = domain.CutRule{
		Name:     "long movies",
		Match:    domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogMovie}, MinDurationMs: 180 * min},
		Strategy: domain.CutStrategy{Type: domain.CutInterval, EveryMs: 60 * min},
	}
	movieCadence = domain.CutRule{
		Name:  "movies over 2h",
		Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogMovie}, MinDurationMs: 120 * min},
		Strategy: domain.CutStrategy{Type: domain.CutPhased, Phases: []domain.CutPhase{
			{EveryMs: 40 * min, UntilBeforeEndMs: 20 * min},
			{EveryMs: 5 * min},
		}},
	}
	episodeHalf = domain.CutRule{
		Name:     "episodes at 50%",
		Match:    domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogEpisode}},
		Strategy: domain.CutStrategy{Type: domain.CutPercentage, Percentages: []float64{50}},
	}
	chapterRule = domain.CutRule{
		Name:  "chapters",
		Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogEpisode, domain.CatalogMovie}, HasChapters: boolPtr(true)},
		Strategy: domain.CutStrategy{
			Type:  domain.CutChapter,
			Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro, domain.ChapterRoleMiddle},
		},
	}
)

func TestPlanItem(t *testing.T) {
	movie := domain.CatalogMovie
	episode := domain.CatalogEpisode
	cases := []struct {
		name  string
		cfg   domain.CutConfig
		item  Item
		want  []int64
		src   Source
		index int
	}{
		{
			name: "movie over 3h every 1h",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h}},
			item: Item{Kind: movie, DurationMs: 200 * min},
			want: mins(60, 120, 180), src: SourceRule, index: 0,
		},
		{
			name: "movie under 3h does not match the 3h rule",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h}},
			item: Item{Kind: movie, DurationMs: 150 * min},
			want: []int64{}, src: SourceNone, index: -1,
		},
		{
			name: "movie over 2h every 40 min then every 5 min in the last 20",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{movieCadence}},
			item: Item{Kind: movie, DurationMs: 150 * min},
			want: mins(40, 80, 120, 135, 140, 145), src: SourceRule, index: 0,
		},
		{
			name: "first matching rule wins (3h rule before the 2h one)",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h, movieCadence}},
			item: Item{Kind: movie, DurationMs: 190 * min},
			want: mins(60, 120, 180), src: SourceRule, index: 0,
		},
		{
			name: "episode at 50%",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h, episodeHalf}},
			item: Item{Kind: episode, DurationMs: 44 * min},
			want: mins(22), src: SourceRule, index: 1,
		},
		{
			name: "episode with chapters: after the intro and at the middle chapter",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule, episodeHalf}},
			// Middle is 22.5 min: the 22 min chapter start is within tolerance.
			item: Item{Kind: episode, DurationMs: 45 * min, Chapters: chapters(0, 3, 12, 22, 30, 40)},
			want: mins(3, 22), src: SourceRule, index: 0,
		},
		{
			name: "movie with chapters: after the intro and at the middle chapter",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule}},
			item: Item{Kind: movie, DurationMs: 120 * min, Chapters: chapters(0, 6, 20, 41, 58, 75, 100)},
			want: mins(6, 58), src: SourceRule, index: 0,
		},
		{
			name: "chapters out of order and a named intro",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule}},
			item: Item{Kind: movie, DurationMs: 100 * min, Chapters: []domain.CatalogChapter{
				{Name: "Act two", StartMs: 50 * min}, {Name: "Opening credits", StartMs: 0}, {Name: "Act one", StartMs: 8 * min},
			}},
			want: mins(8, 50), src: SourceRule, index: 0,
		},
		{
			name: "middle with no boundary in tolerance keeps the middle itself",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle}},
			}}},
			item: Item{Kind: movie, DurationMs: 100 * min, Chapters: chapters(0, 10, 90)},
			want: mins(50), src: SourceRule, index: 0,
		},
		{
			name: "after_intro needs a short first chapter, else the role is skipped",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro, domain.ChapterRoleMiddle}},
			}}},
			item: Item{Kind: movie, DurationMs: 100 * min, Chapters: chapters(0, 30, 52, 80)},
			want: mins(52), src: SourceRule, index: 0,
		},
		{
			name: "movie without chapters falls back to the rule's percentages",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogMovie}},
				Strategy: domain.CutStrategy{
					Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle},
					Fallback: &domain.CutFallback{Type: domain.CutPercentage, Percentages: []float64{33, 66}},
				},
			}}},
			item: Item{Kind: movie, DurationMs: 120 * min},
			want: []int64{2376000, 4752000}, src: SourceFallback, index: 0, // 33% = 39.6 min, 66% = 79.2 min
		},
		{
			name: "chapter strategy without chapters or fallback uses the built-in middle cut",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle}},
			}}},
			item: Item{Kind: movie, DurationMs: 120 * min},
			want: mins(60), src: SourceBuiltin, index: 0,
		},
		{
			name: "chapter rule requiring chapters is skipped for an item without them",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule, episodeHalf}},
			item: Item{Kind: episode, DurationMs: 40 * min},
			want: mins(20), src: SourceRule, index: 1,
		},
		{
			name: "single chapter is not enough to have chapters",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule, episodeHalf}},
			item: Item{Kind: episode, DurationMs: 40 * min, Chapters: chapters(0)},
			want: mins(20), src: SourceRule, index: 1,
		},
		{
			name: "default decides what no rule matches",
			cfg: domain.CutConfig{
				Rules:   []domain.CutRule{longMovieEvery1h},
				Default: &domain.CutStrategy{Type: domain.CutPercentage, Percentages: []float64{50}},
			},
			item: Item{Kind: domain.CatalogOther, DurationMs: 30 * min},
			want: mins(15), src: SourceDefault, index: -1,
		},
		{
			name: "none chosen by the author means no cut",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{{Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogOther}}, Strategy: domain.CutStrategy{Type: domain.CutNone}}, episodeHalf}},
			item: Item{Kind: domain.CatalogOther, DurationMs: 90 * min},
			want: []int64{}, src: SourceRule, index: 0,
		},
		{
			name: "item too short for the minimum segment gets no cut",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{episodeHalf}},
			item: Item{Kind: episode, DurationMs: 5 * min},
			want: []int64{}, src: SourceRule, index: 0,
		},
		{
			name: "just long enough for two minimum segments",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{episodeHalf}},
			item: Item{Kind: episode, DurationMs: 6 * min},
			want: mins(3), src: SourceRule, index: 0,
		},
		{
			name: "chapter strategy on a short item is not retried as a fallback",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{chapterRule}},
			item: Item{Kind: episode, DurationMs: 5 * min, Chapters: chapters(0, 1, 2)},
			want: []int64{}, src: SourceRule, index: 0,
		},
		{
			name: "explicit offsets, those past the end are dropped",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy: domain.CutStrategy{Type: domain.CutOffsets, OffsetsMs: mins(20, 50, 200)},
			}}},
			item: Item{Kind: movie, DurationMs: 90 * min},
			want: mins(20, 50), src: SourceRule, index: 0,
		},
		{
			name: "interval cuts snap to a chapter boundary within tolerance",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy:        domain.CutStrategy{Type: domain.CutInterval, EveryMs: 30 * min},
				SnapToleranceMs: 4 * min,
			}}},
			// 30 -> 32 (within 4), 60 -> 60 (no boundary near), 90 stays (nearest 95 is 5 away).
			item: Item{Kind: movie, DurationMs: 120 * min, Chapters: chapters(0, 32, 95)},
			want: mins(32, 60, 90), src: SourceRule, index: 0,
		},
		{
			name: "guards: a minimum segment drops the later of two close cuts",
			cfg: domain.CutConfig{
				Rules:  []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutOffsets, OffsetsMs: mins(30, 32, 70)}}},
				Guards: domain.CutGuards{MinSegmentMs: 10 * min},
			},
			item: Item{Kind: movie, DurationMs: 100 * min},
			want: mins(30, 70), src: SourceRule, index: 0,
		},
		{
			name: "guards: distance from the edges",
			cfg: domain.CutConfig{
				Rules:  []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutOffsets, OffsetsMs: mins(5, 50, 95)}}},
				Guards: domain.CutGuards{MinFromStartMs: 10 * min, MinFromEndMs: 10 * min},
			},
			item: Item{Kind: movie, DurationMs: 100 * min},
			want: mins(50), src: SourceRule, index: 0,
		},
		{
			name: "guards: a rule overrides the set field by field and caps the breaks",
			cfg: domain.CutConfig{
				Rules: []domain.CutRule{{
					Strategy: domain.CutStrategy{Type: domain.CutInterval, EveryMs: 10 * min},
					Guards:   &domain.CutGuards{MaxBreaks: 2},
				}},
				Guards: domain.CutGuards{MinSegmentMs: 10 * min},
			},
			item: Item{Kind: movie, DurationMs: 100 * min},
			want: mins(10, 20), src: SourceRule, index: 0,
		},
		{
			name: "phased: a phase boundary past the end of a short item is skipped",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutPhased, Phases: []domain.CutPhase{
				{EveryMs: 40 * min, UntilMs: 120 * min}, {EveryMs: 5 * min},
			}}}}},
			item: Item{Kind: movie, DurationMs: 100 * min},
			want: mins(40, 80), src: SourceRule, index: 0,
		},
		{
			name: "non-positive duration is never cut",
			cfg:  domain.CutConfig{Rules: []domain.CutRule{episodeHalf}},
			item: Item{Kind: episode, DurationMs: 0},
			want: []int64{}, src: SourceNone, index: -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanItem(tc.cfg, tc.item)
			if !reflect.DeepEqual(got.CutsMs, tc.want) {
				t.Fatalf("cuts = %v (min: %v), want %v (min: %v); notes %v", got.CutsMs, toMins(got.CutsMs), tc.want, toMins(tc.want), got.Notes)
			}
			if got.Source != tc.src || got.RuleIndex != tc.index {
				t.Fatalf("source/rule = %s/%d, want %s/%d", got.Source, got.RuleIndex, tc.src, tc.index)
			}
		})
	}
}

func toMins(v []int64) []float64 {
	out := make([]float64, len(v))
	for i, ms := range v {
		out[i] = float64(ms) / float64(min)
	}
	return out
}

func TestPlanItemDeterministic(t *testing.T) {
	cfg := domain.CutConfig{Rules: []domain.CutRule{chapterRule, movieCadence, episodeHalf}}
	item := Item{Kind: domain.CatalogMovie, DurationMs: 7_523_411, Chapters: chapters(0, 7, 33, 61, 98)}
	first := PlanItem(cfg, item)
	for i := 0; i < 50; i++ {
		if got := PlanItem(cfg, item); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs: %+v vs %+v", i, got, first)
		}
	}
	// Strictly ascending, inside the item.
	prev := int64(0)
	for _, c := range first.CutsMs {
		if c <= prev || c >= item.DurationMs {
			t.Fatalf("cuts not strictly ascending inside the item: %v", first.CutsMs)
		}
		prev = c
	}
}

func TestPlanItemManyCandidatesStayBounded(t *testing.T) {
	cfg := domain.CutConfig{Rules: []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutInterval, EveryMs: domain.MinCutIntervalMs}}}}
	got := PlanItem(cfg, Item{Kind: domain.CatalogMovie, DurationMs: 1000 * 24 * 60 * min})
	if len(got.CutsMs) != DefaultMaxBreaks {
		t.Fatalf("cuts = %d, want the default cap of %d", len(got.CutsMs), DefaultMaxBreaks)
	}
}

func TestSegments(t *testing.T) {
	got := Segments(mins(20, 50), 90*min)
	if !reflect.DeepEqual(got, mins(20, 30, 40)) {
		t.Fatalf("segments = %v", got)
	}
	if got := Segments(nil, 90*min); !reflect.DeepEqual(got, mins(90)) {
		t.Fatalf("segments without cuts = %v", got)
	}
}

func TestPlannerCutOffsets(t *testing.T) {
	p := NewPlanner(domain.CutRuleSet{CutConfig: domain.CutConfig{Rules: []domain.CutRule{chapterRule, episodeHalf}}})
	item := domain.CatalogItem{Kind: domain.CatalogEpisode}
	item.Chapters = chapters(0, 3, 22)
	if got := p.CutOffsets(item, 45*min); !reflect.DeepEqual(got, mins(3, 22)) {
		t.Fatalf("with chapters = %v", toMins(got))
	}
	item.Chapters = nil
	// The occurrence is trimmed to 30 minutes: the cut follows it.
	if got := p.CutOffsets(item, 30*min); !reflect.DeepEqual(got, mins(15)) {
		t.Fatalf("without chapters, trimmed = %v", toMins(got))
	}
}

func TestPickSet(t *testing.T) {
	set := func(id string, scope domain.FillerScope, scopeID string, pos int) domain.CutRuleSet {
		return domain.CutRuleSet{ID: id, Scope: scope, ScopeID: scopeID, Position: pos}
	}
	sets := []domain.CutRuleSet{
		set("c2", domain.ScopeChannel, "ch", 2), set("c1", domain.ScopeChannel, "ch", 1),
		set("b1", domain.ScopeBlock, "blk", 5), set("other", domain.ScopeChannel, "x", 0),
	}
	if got := PickSet(sets, "ch", "blk"); got == nil || got.ID != "b1" {
		t.Fatalf("block set must beat the channel's, got %+v", got)
	}
	if got := PickSet(sets, "ch", "no-block"); got == nil || got.ID != "c1" {
		t.Fatalf("lowest position of the channel wins, got %+v", got)
	}
	if got := PickSet(sets, "none", ""); got != nil {
		t.Fatalf("no set expected, got %+v", got)
	}
}

func TestValidateConfig(t *testing.T) {
	ok := domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h, movieCadence, chapterRule, episodeHalf}}
	if err := ValidateConfig(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := ValidateConfig(domain.CutConfig{Default: &domain.CutStrategy{Type: domain.CutNone}}); err != nil {
		t.Fatalf("default only should be valid: %v", err)
	}

	pct := func(p ...float64) domain.CutStrategy {
		return domain.CutStrategy{Type: domain.CutPercentage, Percentages: p}
	}
	rule := func(s domain.CutStrategy) domain.CutConfig {
		return domain.CutConfig{Rules: []domain.CutRule{{Strategy: s}}}
	}
	phases := func(p ...domain.CutPhase) domain.CutConfig {
		return rule(domain.CutStrategy{Type: domain.CutPhased, Phases: p})
	}
	bad := []struct {
		name string
		cfg  domain.CutConfig
		want string
	}{
		{"empty", domain.CutConfig{}, "at least one rule"},
		{"unknown type", rule(domain.CutStrategy{Type: "weird"}), "strategy type"},
		{"interval too short", rule(domain.CutStrategy{Type: domain.CutInterval, EveryMs: 1000}), "every_ms"},
		{"percentage 0", rule(pct(0)), "above 0"},
		{"percentage 100", rule(pct(100)), "below 100"},
		{"percentages unordered", rule(pct(60, 40)), "ascending"},
		{"percentages empty", rule(pct()), "1 to"},
		{"offsets unordered", rule(domain.CutStrategy{Type: domain.CutOffsets, OffsetsMs: mins(20, 10)}), "ascending"},
		{"offsets negative", rule(domain.CutStrategy{Type: domain.CutOffsets, OffsetsMs: []int64{-1}}), "positive"},
		{"stray field", rule(domain.CutStrategy{Type: domain.CutPercentage, Percentages: []float64{50}, EveryMs: 5 * min}), "does not apply"},
		{"chapter without roles", rule(domain.CutStrategy{Type: domain.CutChapter}), "roles"},
		{"unknown role", rule(domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{"end"}}), "roles must be"},
		{"duplicate role", rule(domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{"middle", "middle"}}), "twice"},
		{"chapter fallback chapter", rule(domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{"middle"}, Fallback: &domain.CutFallback{Type: domain.CutChapter}}), "fallback"},
		{"phases overlap from start", phases(domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min}, domain.CutPhase{EveryMs: 10 * min, UntilMs: 50 * min}, domain.CutPhase{EveryMs: 5 * min}), "overlaps phase 1"},
		{"phases equal from start", phases(domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min}, domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min}, domain.CutPhase{EveryMs: 5 * min}), "overlaps"},
		{"phases overlap before end", phases(domain.CutPhase{EveryMs: 10 * min, UntilBeforeEndMs: 20 * min}, domain.CutPhase{EveryMs: 10 * min, UntilBeforeEndMs: 30 * min}, domain.CutPhase{EveryMs: 5 * min}), "overlaps phase 1"},
		{"phases mixed bounds", phases(domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min}, domain.CutPhase{EveryMs: 10 * min, UntilBeforeEndMs: 20 * min}, domain.CutPhase{EveryMs: 5 * min}), "bounds must all be"},
		{"unbounded middle phase", phases(domain.CutPhase{EveryMs: 10 * min}, domain.CutPhase{EveryMs: 5 * min}), "only the last"},
		{"bounded last phase", phases(domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min}), "last phase"},
		{"both bounds", phases(domain.CutPhase{EveryMs: 10 * min, UntilMs: 60 * min, UntilBeforeEndMs: 5 * min}, domain.CutPhase{EveryMs: 5 * min}), "not both"},
		{"min above max", domain.CutConfig{Rules: []domain.CutRule{{Match: domain.CutSelector{MinDurationMs: 10 * min, MaxDurationMs: 5 * min}, Strategy: pct(50)}}}, "min_duration_ms"},
		{"unknown kind", domain.CutConfig{Rules: []domain.CutRule{{Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{"clip"}}, Strategy: pct(50)}}}, "kinds"},
		{"shadowed rule", domain.CutConfig{Rules: []domain.CutRule{{Strategy: pct(50)}, episodeHalf}}, "rule 2 can never match: rule 1"},
		{"shadowed by wider duration", domain.CutConfig{Rules: []domain.CutRule{
			{Match: domain.CutSelector{MinDurationMs: 60 * min}, Strategy: pct(50)},
			{Match: domain.CutSelector{Kinds: []domain.CatalogItemKind{domain.CatalogMovie}, MinDurationMs: 90 * min, MaxDurationMs: 150 * min}, Strategy: pct(50)},
		}}, "rule 2 can never match"},
		{"guards out of range", domain.CutConfig{Rules: []domain.CutRule{episodeHalf}, Guards: domain.CutGuards{MaxBreaks: 51}}, "max_breaks"},
		{"too many rules", domain.CutConfig{Rules: func() []domain.CutRule {
			out := make([]domain.CutRule, domain.MaxCutRules+1)
			for i := range out {
				out[i] = domain.CutRule{Match: domain.CutSelector{MinDurationMs: int64(i+1) * 1000 * 60 * 60 * 24}, Strategy: pct(50)}
			}
			return out
		}()}, "at most"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !IsValidation(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a validation error containing %q", err, tc.want)
			}
		})
	}

	// Not shadowed: a narrower earlier rule leaves the later one reachable.
	notShadowed := domain.CutConfig{Rules: []domain.CutRule{longMovieEvery1h, {Strategy: pct(50)}}}
	if err := ValidateConfig(notShadowed); err != nil {
		t.Fatalf("narrow rule then catch-all should be valid: %v", err)
	}
}
