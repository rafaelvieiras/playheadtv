package cutplan

import (
	"reflect"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func seg(typ string, startMin, endMin int64) domain.CatalogSegment {
	return domain.CatalogSegment{Type: typ, StartMs: startMin * min, EndMs: endMin * min}
}

func TestPlanItemIntroSegment(t *testing.T) {
	afterIntro := domain.CutConfig{Rules: []domain.CutRule{{
		Strategy: domain.CutStrategy{
			Type:     domain.CutChapter,
			Roles:    []domain.ChapterRole{domain.ChapterRoleAfterIntro},
			Fallback: &domain.CutFallback{Type: domain.CutPercentage, Percentages: []float64{50}},
		},
	}}}
	both := domain.CutConfig{Rules: []domain.CutRule{{
		Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro, domain.ChapterRoleMiddle}},
	}}}
	episode := domain.CatalogEpisode
	cases := []struct {
		name string
		cfg  domain.CutConfig
		item Item
		want []int64
		src  Source
	}{
		{
			name: "intro segment decides, with no chapters at all",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 45 * min, Segments: []domain.CatalogSegment{seg("intro", 1, 4)}},
			want: mins(4), src: SourceSegment,
		},
		{
			name: "segment beats the chapter heuristic",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 45 * min, Chapters: chapters(0, 7, 22),
				Segments: []domain.CatalogSegment{seg("Intro", 1, 5)}},
			want: mins(5), src: SourceSegment,
		},
		{
			name: "no segment: the chapter heuristic",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 45 * min, Chapters: chapters(0, 7, 22), Segments: []domain.CatalogSegment{}},
			want: mins(7), src: SourceChapter,
		},
		{
			name: "only non-intro segments: the chapter heuristic",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 45 * min, Chapters: chapters(0, 7, 22),
				Segments: []domain.CatalogSegment{seg("outro", 40, 44), seg("recap", 0, 2)}},
			want: mins(7), src: SourceChapter,
		},
		{
			name: "no segment and no chapter: the rule's fallback",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 40 * min},
			want: mins(20), src: SourceFallback,
		},
		{
			name: "intro outside the item is ignored",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 40 * min, Segments: []domain.CatalogSegment{seg("intro", 50, 52)}},
			want: mins(20), src: SourceFallback,
		},
		{
			name: "intro that runs past the end is ignored",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 40 * min, Chapters: chapters(0, 6, 20), Segments: []domain.CatalogSegment{seg("intro", 30, 45)}},
			want: mins(6), src: SourceChapter,
		},
		{
			name: "empty or inverted intro is ignored",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 40 * min, Segments: []domain.CatalogSegment{seg("intro", 3, 3), seg("intro", 8, 5)}},
			want: mins(20), src: SourceFallback,
		},
		{
			name: "several intros: the first one decides",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 60 * min, Segments: []domain.CatalogSegment{seg("intro", 30, 33), seg("intro", 1, 4)}},
			want: mins(4), src: SourceSegment,
		},
		{
			name: "a valid intro is taken after an invalid earlier one",
			cfg:  afterIntro,
			item: Item{Kind: episode, DurationMs: 60 * min, Segments: []domain.CatalogSegment{seg("intro", 2, 2), seg("intro", 10, 13)}},
			want: mins(13), src: SourceSegment,
		},
		{
			name: "after_intro from the segment, middle from the chapters",
			cfg:  both,
			item: Item{Kind: episode, DurationMs: 45 * min, Chapters: chapters(0, 12, 22, 30), Segments: []domain.CatalogSegment{seg("intro", 0, 4)}},
			want: mins(4, 22), src: SourceSegment,
		},
		{
			name: "segment only, middle needs chapters and is skipped",
			cfg:  both,
			item: Item{Kind: episode, DurationMs: 45 * min, Segments: []domain.CatalogSegment{seg("intro", 0, 4)}},
			want: mins(4), src: SourceSegment,
		},
		{
			name: "middle only with a segment but no chapters still falls back",
			cfg: domain.CutConfig{Rules: []domain.CutRule{{
				Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle}},
			}}},
			item: Item{Kind: episode, DurationMs: 40 * min, Segments: []domain.CatalogSegment{seg("intro", 0, 4)}},
			want: mins(20), src: SourceBuiltin,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanItem(tc.cfg, tc.item)
			if !reflect.DeepEqual(got.CutsMs, tc.want) || got.Source != tc.src {
				t.Fatalf("cuts/source = %v (min %v)/%s, want %v (min %v)/%s; notes %v",
					got.CutsMs, toMins(got.CutsMs), got.Source, tc.want, toMins(tc.want), tc.src, got.Notes)
			}
		})
	}
}

func TestPlannerCutOffsetsUsesSegments(t *testing.T) {
	p := NewPlanner(domain.CutRuleSet{CutConfig: domain.CutConfig{Rules: []domain.CutRule{{
		Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro}},
	}}}})
	item := domain.CatalogItem{Kind: domain.CatalogEpisode}
	item.Segments = []domain.CatalogSegment{seg("intro", 0, 5)}
	if got := p.CutOffsets(item, 40*min); !reflect.DeepEqual(got, mins(5)) {
		t.Fatalf("cuts = %v, want %v", got, mins(5))
	}
}

func TestUsesSegments(t *testing.T) {
	chapterOnly := domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle}}
	afterIntro := domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro}}
	cases := []struct {
		name string
		cfg  domain.CutConfig
		want bool
	}{
		{"empty", domain.CutConfig{}, false},
		{"middle only", domain.CutConfig{Rules: []domain.CutRule{{Strategy: chapterOnly}}}, false},
		{"after_intro rule", domain.CutConfig{Rules: []domain.CutRule{{Strategy: afterIntro}}}, true},
		{"after_intro default", domain.CutConfig{Default: &afterIntro}, true},
		{"percentage", domain.CutConfig{Rules: []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutPercentage, Percentages: []float64{50}}}}}, false},
	}
	for _, tc := range cases {
		if got := UsesSegments(tc.cfg); got != tc.want {
			t.Errorf("%s: UsesSegments = %v, want %v", tc.name, got, tc.want)
		}
	}
}
