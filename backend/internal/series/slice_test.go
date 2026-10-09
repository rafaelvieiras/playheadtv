package series

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func n64(v int64) *int64 { return &v }
func ip(v int) *int      { return &v }

// ep builds an episode with id sSSeEE.
func ep(season, episode int64) Episode {
	return Episode{ID: fmt.Sprintf("s%02de%02d", season, episode), DurationMs: 1000, SeasonNumber: n64(season), EpisodeNumber: n64(episode)}
}

func ids(eps []Episode) string {
	var out []string
	for _, e := range eps {
		out = append(out, e.ID)
	}
	return strings.Join(out, " ")
}

// library: a special, season 1 (1..4), season 2 (1,2,4: a gap at 3, and a
// double file 5-6), an episode with no numbers. Deliberately unsorted.
func library() []Episode {
	double := ep(2, 5)
	double.EpisodeNumberEnd = n64(6)
	return []Episode{
		ep(2, 4), ep(1, 3), double, ep(0, 1), ep(1, 1), ep(2, 1), ep(1, 4), ep(2, 2), ep(1, 2),
		{ID: "nonum", DurationMs: 1000},
	}
}

func TestApplySliceNilIsIdentity(t *testing.T) {
	in := library()
	if got := ApplySlice(in, nil, 1); len(got) != len(in) || got[0].ID != in[0].ID {
		t.Fatalf("nil slice changed the episodes")
	}
}

func TestApplySlice(t *testing.T) {
	cases := []struct {
		name  string
		slice domain.SeriesSlice
		want  string
	}{
		{"exclude specials only", domain.SeriesSlice{ExcludeSpecials: true}, "s01e01 s01e02 s01e03 s01e04 s02e01 s02e02 s02e04 s02e05 nonum"},
		{"specials stay by default", domain.SeriesSlice{First: ip(2)}, "s00e01 s01e01"},
		{"one season", domain.SeriesSlice{Seasons: []int{1}}, "s01e01 s01e02 s01e03 s01e04"},
		{"season list", domain.SeriesSlice{Seasons: []int{2, 0}}, "s00e01 s02e01 s02e02 s02e04 s02e05"},
		{"season range", domain.SeriesSlice{SeasonFrom: ip(1), SeasonTo: ip(2)}, "s01e01 s01e02 s01e03 s01e04 s02e01 s02e02 s02e04 s02e05"},
		{"open season range", domain.SeriesSlice{SeasonFrom: ip(2)}, "s02e01 s02e02 s02e04 s02e05"},
		{"list and range are a union", domain.SeriesSlice{Seasons: []int{0}, SeasonFrom: ip(2)}, "s00e01 s02e01 s02e02 s02e04 s02e05"},
		{"episode range is by number across seasons, gaps absent", domain.SeriesSlice{EpisodeFrom: ip(3), EpisodeTo: ip(4)}, "s01e03 s01e04 s02e04"},
		{"double file is in when any number is in range", domain.SeriesSlice{EpisodeFrom: ip(6), EpisodeTo: ip(9)}, "s02e05"},
		{"double file out of range", domain.SeriesSlice{EpisodeFrom: ip(7)}, ""},
		{"first counts the double file once", domain.SeriesSlice{Seasons: []int{2}, Last: ip(1)}, "s02e05"},
		{"first N", domain.SeriesSlice{First: ip(3)}, "s00e01 s01e01 s01e02"},
		{"last N puts numberless episodes in", domain.SeriesSlice{Last: ip(2)}, "s02e05 nonum"},
		{"skip first and last", domain.SeriesSlice{SkipFirst: ip(1), SkipLast: ip(2)}, "s01e01 s01e02 s01e03 s01e04 s02e01 s02e02 s02e04"},
		{"skip beyond length is empty", domain.SeriesSlice{SkipFirst: ip(50)}, ""},
		{"season then specials then range then skip then first", domain.SeriesSlice{
			SeasonFrom: ip(0), ExcludeSpecials: true, EpisodeFrom: ip(2), SkipFirst: ip(1), First: ip(3),
		}, "s01e03 s01e04 s02e02"},
		{"first after skip is positional", domain.SeriesSlice{Seasons: []int{1}, SkipFirst: ip(1), First: ip(2)}, "s01e02 s01e03"},
		{"sample bigger than the set keeps all", domain.SeriesSlice{Seasons: []int{1}, Sample: ip(10)}, "s01e01 s01e02 s01e03 s01e04"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sl := c.slice
			got := ids(ApplySlice(library(), &sl, 7))
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestApplySliceSampleIsDeterministicAndKeepsOriginalOrder(t *testing.T) {
	var eps []Episode
	for i := int64(1); i <= 40; i++ {
		eps = append(eps, ep(1, i))
	}
	sl := &domain.SeriesSlice{Sample: ip(8)}
	a := ApplySlice(eps, sl, 99)
	b := ApplySlice(eps, sl, 99)
	if len(a) != 8 || ids(a) != ids(b) {
		t.Fatalf("sample not deterministic or wrong size: %q vs %q", ids(a), ids(b))
	}
	for i := 1; i < len(a); i++ {
		if !OriginalLess(a[i-1], a[i]) {
			t.Fatalf("sample not in original order: %s", ids(a))
		}
	}
	if c := ApplySlice(eps, sl, 100); ids(c) == ids(a) {
		t.Errorf("a different seed gave the same sample")
	}
	// Removing an episode that was not sampled leaves the sample unchanged.
	in := map[string]bool{}
	for _, e := range a {
		in[e.ID] = true
	}
	var trimmed []Episode
	dropped := false
	for _, e := range eps {
		if !in[e.ID] && !dropped {
			dropped = true
			continue
		}
		trimmed = append(trimmed, e)
	}
	if got := ApplySlice(trimmed, sl, 99); ids(got) != ids(a) {
		t.Errorf("sample moved when an unrelated episode left: %s vs %s", ids(got), ids(a))
	}
}

func TestApplySliceSampleOfFirstN(t *testing.T) {
	var eps []Episode
	for i := int64(1); i <= 20; i++ {
		eps = append(eps, ep(1, i))
	}
	got := ApplySlice(eps, &domain.SeriesSlice{First: ip(5), Sample: ip(3)}, 5)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	for _, e := range got {
		if e.EpisodeNumber == nil || *e.EpisodeNumber > 5 {
			t.Fatalf("sample escaped the first 5: %s", ids(got))
		}
	}
}

func TestNormalizeSlice(t *testing.T) {
	bad := []struct {
		name  string
		slice domain.SeriesSlice
		want  string
	}{
		{"first and last", domain.SeriesSlice{First: ip(1), Last: ip(1)}, "first and last"},
		{"zero first", domain.SeriesSlice{First: ip(0)}, "first must be a positive"},
		{"negative skip", domain.SeriesSlice{SkipLast: ip(-1)}, "skip_last must be a positive"},
		{"zero sample", domain.SeriesSlice{Sample: ip(0)}, "sample must be a positive"},
		{"negative season", domain.SeriesSlice{Seasons: []int{-1}}, "negative"},
		{"negative season_from", domain.SeriesSlice{SeasonFrom: ip(-1)}, "season_from must not be negative"},
		{"season range reversed", domain.SeriesSlice{SeasonFrom: ip(3), SeasonTo: ip(1)}, "season_from must not be greater"},
		{"episode range reversed", domain.SeriesSlice{EpisodeFrom: ip(9), EpisodeTo: ip(2)}, "episode_from must not be greater"},
		{"specials listed and excluded", domain.SeriesSlice{Seasons: []int{0, 1}, ExcludeSpecials: true}, "season 0"},
		{"specials-only range and excluded", domain.SeriesSlice{SeasonTo: ip(0), ExcludeSpecials: true}, "specials only"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			sl := c.slice
			_, err := NormalizeSlice(&sl)
			if err == nil || !strings.Contains(err.Error(), c.want) || !IsInvalid(err) {
				t.Fatalf("err = %v, want invalid containing %q", err, c.want)
			}
		})
	}
	if got, err := NormalizeSlice(&domain.SeriesSlice{}); err != nil || got != nil {
		t.Errorf("empty slice = %v, %v; want nil", got, err)
	}
	got, err := NormalizeSlice(&domain.SeriesSlice{Seasons: []int{3, 1, 3}, ExcludeSpecials: true})
	if err != nil || fmt.Sprint(got.Seasons) != "[1 3]" {
		t.Errorf("seasons not sorted and deduplicated: %v, %v", got, err)
	}
}

func TestSliceSeedFor(t *testing.T) {
	if s, _ := SliceSeedFor(nil, 5); s != 0 {
		t.Errorf("no slice kept a seed")
	}
	if s, _ := SliceSeedFor(&domain.SeriesSlice{First: ip(1)}, 5); s != 0 {
		t.Errorf("slice without sample kept a seed")
	}
	if s, _ := SliceSeedFor(&domain.SeriesSlice{Sample: ip(1)}, 5); s != 5 {
		t.Errorf("sampling slice did not keep the previous seed")
	}
	if s, _ := SliceSeedFor(&domain.SeriesSlice{Sample: ip(1)}, 0); s == 0 {
		t.Errorf("sampling slice got no seed")
	}
}

func TestNamesSeasonZero(t *testing.T) {
	for name, tc := range map[string]struct {
		slice *domain.SeriesSlice
		want  bool
	}{
		"nil":                {nil, false},
		"empty":              {&domain.SeriesSlice{}, false},
		"seasons with 0":     {&domain.SeriesSlice{Seasons: []int{0, 2}}, true},
		"seasons without 0":  {&domain.SeriesSlice{Seasons: []int{1, 2}}, false},
		"season_to 0":        {&domain.SeriesSlice{SeasonTo: ip(0)}, true},
		"season_from 0":      {&domain.SeriesSlice{SeasonFrom: ip(0), SeasonTo: ip(2)}, true},
		"range covering 0":   {&domain.SeriesSlice{SeasonTo: ip(3)}, false},
		"range from 1":       {&domain.SeriesSlice{SeasonFrom: ip(1), SeasonTo: ip(3)}, false},
		"only exclude flags": {&domain.SeriesSlice{ExcludeSpecials: true, First: ip(3)}, false},
	} {
		if got := NamesSeasonZero(tc.slice); got != tc.want {
			t.Errorf("%s: NamesSeasonZero = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDropSpecials(t *testing.T) {
	in := library() // has s00e01 and an episode with no season number
	if got := ids(DropSpecials(in, nil)); strings.Contains(got, "s00e01") || !strings.Contains(got, "nonum") || len(strings.Fields(got)) != len(in)-1 {
		t.Errorf("default: %q", got)
	}
	// An explicit selection of season 0 wins over the default.
	if got := ids(DropSpecials(in, &domain.SeriesSlice{Seasons: []int{0}})); !strings.Contains(got, "s00e01") {
		t.Errorf("seasons [0]: %q", got)
	}
	// The input is not modified.
	if len(in) != len(library()) {
		t.Errorf("input changed")
	}
}
