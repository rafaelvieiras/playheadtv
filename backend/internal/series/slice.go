package series

import (
	"crypto/rand"
	"encoding/binary"
	"hash/fnv"
	"sort"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// NormalizeSlice validates a client-supplied slice and returns the canonical
// form to store: seasons sorted and deduplicated, nil when no operator is set
// (an empty slice is no slice). Contradictory or nonsensical combinations are
// rejected.
func NormalizeSlice(in *domain.SeriesSlice) (*domain.SeriesSlice, error) {
	if in == nil {
		return nil, nil
	}
	s := *in
	named := func(vals map[string]*int, check func(int) bool, msg string) error {
		for name, v := range vals {
			if v != nil && !check(*v) {
				return InvalidError("series.slice." + name + " " + msg)
			}
		}
		return nil
	}
	if err := named(map[string]*int{
		"season_from": s.SeasonFrom, "season_to": s.SeasonTo,
		"episode_from": s.EpisodeFrom, "episode_to": s.EpisodeTo,
	}, func(n int) bool { return n >= 0 }, "must not be negative"); err != nil {
		return nil, err
	}
	if err := named(map[string]*int{
		"skip_first": s.SkipFirst, "skip_last": s.SkipLast,
		"first": s.First, "last": s.Last, "sample": s.Sample,
	}, func(n int) bool { return n >= 1 }, "must be a positive integer"); err != nil {
		return nil, err
	}
	if len(s.Seasons) > 0 {
		seen := map[int]bool{}
		var seasons []int
		for _, n := range s.Seasons {
			if n < 0 {
				return nil, InvalidError("series.slice.seasons must not contain negative numbers")
			}
			if !seen[n] {
				seen[n] = true
				seasons = append(seasons, n)
			}
		}
		sort.Ints(seasons)
		s.Seasons = seasons
	} else {
		s.Seasons = nil
	}
	if s.SeasonFrom != nil && s.SeasonTo != nil && *s.SeasonFrom > *s.SeasonTo {
		return nil, InvalidError("series.slice.season_from must not be greater than season_to")
	}
	if s.EpisodeFrom != nil && s.EpisodeTo != nil && *s.EpisodeFrom > *s.EpisodeTo {
		return nil, InvalidError("series.slice.episode_from must not be greater than episode_to")
	}
	if s.First != nil && s.Last != nil {
		return nil, InvalidError("series.slice.first and last cannot both be set")
	}
	if s.ExcludeSpecials {
		for _, n := range s.Seasons {
			if n == 0 {
				return nil, InvalidError("series.slice.seasons includes season 0 but exclude_specials is set")
			}
		}
		if len(s.Seasons) == 0 && s.SeasonTo != nil && *s.SeasonTo == 0 {
			return nil, InvalidError("series.slice.season_to is 0 (specials only) but exclude_specials is set")
		}
	}
	if sliceEmpty(s) {
		return nil, nil
	}
	return &s, nil
}

func sliceEmpty(s domain.SeriesSlice) bool {
	return len(s.Seasons) == 0 && s.SeasonFrom == nil && s.SeasonTo == nil && !s.ExcludeSpecials &&
		s.EpisodeFrom == nil && s.EpisodeTo == nil && s.SkipFirst == nil && s.SkipLast == nil &&
		s.First == nil && s.Last == nil && s.Sample == nil
}

// SliceSeedFor returns the seed to store next to slice: none without a
// sample, the previous one while the slice keeps sampling (so editing other
// operators does not reshuffle the sample), else a fresh random one.
func SliceSeedFor(slice *domain.SeriesSlice, prevSeed int64) (int64, error) {
	if slice == nil || slice.Sample == nil {
		return 0, nil
	}
	if prevSeed != 0 {
		return prevSeed, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:])>>1) | 1, nil
}

// SameSlice reports whether two normalized slices are equal.
func SameSlice(a, b *domain.SeriesSlice) bool {
	if a == nil || b == nil {
		return a == b
	}
	eq := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	if len(a.Seasons) != len(b.Seasons) || a.ExcludeSpecials != b.ExcludeSpecials {
		return false
	}
	for i := range a.Seasons {
		if a.Seasons[i] != b.Seasons[i] {
			return false
		}
	}
	return eq(a.SeasonFrom, b.SeasonFrom) && eq(a.SeasonTo, b.SeasonTo) && eq(a.EpisodeFrom, b.EpisodeFrom) &&
		eq(a.EpisodeTo, b.EpisodeTo) && eq(a.SkipFirst, b.SkipFirst) && eq(a.SkipLast, b.SkipLast) &&
		eq(a.First, b.First) && eq(a.Last, b.Last) && eq(a.Sample, b.Sample)
}

// OriginalLess is the series' original order: season then episode (specials
// first, missing numbers last), ties by id.
func OriginalLess(a, b Episode) bool {
	if c := cmpNum(a.SeasonNumber, b.SeasonNumber); c != 0 {
		return c < 0
	}
	if c := cmpNum(a.EpisodeNumber, b.EpisodeNumber); c != 0 {
		return c < 0
	}
	return a.ID < b.ID
}

func cmpNum(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	}
	return 0
}

// NamesSeasonZero reports whether the slice explicitly selects season 0: it
// lists 0 in seasons, or its season range starts at or ends at 0. A range that
// only happens to cover 0 (season_to 3 with no season_from) does not name it.
func NamesSeasonZero(sl *domain.SeriesSlice) bool {
	if sl == nil {
		return false
	}
	for _, n := range sl.Seasons {
		if n == 0 {
			return true
		}
	}
	if sl.SeasonFrom != nil && *sl.SeasonFrom == 0 && (sl.SeasonTo == nil || *sl.SeasonTo >= 0) {
		return true
	}
	return sl.SeasonTo != nil && *sl.SeasonTo == 0 && (sl.SeasonFrom == nil || *sl.SeasonFrom <= 0)
}

// DropSpecials removes the season 0 episodes of a series from eps, the default
// of a block that does not include specials. Episodes with no season number
// stay. An explicit season selection that names season 0 wins over the default
// (the episodes are returned unchanged), see NamesSeasonZero. eps must be the
// episodes of one series (or a selection of series episodes).
func DropSpecials(eps []Episode, sl *domain.SeriesSlice) []Episode {
	if NamesSeasonZero(sl) {
		return eps
	}
	out := make([]Episode, 0, len(eps))
	for _, e := range eps {
		if e.SeasonNumber != nil && *e.SeasonNumber == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ApplySlice returns the episodes that remain after the slice, in the series'
// original order. A nil slice returns eps unchanged. See domain.SeriesSlice
// for the order of application. seed drives the random sample.
func ApplySlice(eps []Episode, sl *domain.SeriesSlice, seed int64) []Episode {
	if sl == nil {
		return eps
	}
	out := make([]Episode, 0, len(eps))
	hasSeasonFilter := len(sl.Seasons) > 0 || sl.SeasonFrom != nil || sl.SeasonTo != nil
	for _, e := range eps {
		if hasSeasonFilter {
			if e.SeasonNumber == nil {
				continue
			}
			n := int(*e.SeasonNumber)
			ok := false
			for _, s := range sl.Seasons {
				if s == n {
					ok = true
				}
			}
			if sl.SeasonFrom != nil || sl.SeasonTo != nil {
				if (sl.SeasonFrom == nil || n >= *sl.SeasonFrom) && (sl.SeasonTo == nil || n <= *sl.SeasonTo) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		if sl.ExcludeSpecials && e.SeasonNumber != nil && *e.SeasonNumber == 0 {
			continue
		}
		if sl.EpisodeFrom != nil || sl.EpisodeTo != nil {
			if e.EpisodeNumber == nil {
				continue
			}
			lo := int(*e.EpisodeNumber)
			hi := lo
			if e.EpisodeNumberEnd != nil && int(*e.EpisodeNumberEnd) > hi {
				hi = int(*e.EpisodeNumberEnd)
			}
			if (sl.EpisodeFrom != nil && hi < *sl.EpisodeFrom) || (sl.EpisodeTo != nil && lo > *sl.EpisodeTo) {
				continue
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return OriginalLess(out[i], out[j]) })
	if sl.SkipFirst != nil {
		out = out[min(*sl.SkipFirst, len(out)):]
	}
	if sl.SkipLast != nil {
		out = out[:len(out)-min(*sl.SkipLast, len(out))]
	}
	if sl.First != nil && *sl.First < len(out) {
		out = out[:*sl.First]
	}
	if sl.Last != nil && *sl.Last < len(out) {
		out = out[len(out)-*sl.Last:]
	}
	if sl.Sample != nil && *sl.Sample < len(out) {
		out = sample(out, *sl.Sample, seed)
	}
	return out
}

// sample keeps the n episodes with the smallest hash of (seed, id), in their
// original order: deterministic for a seed, and stable when unrelated
// episodes come and go.
func sample(eps []Episode, n int, seed int64) []Episode {
	type scored struct {
		idx  int
		hash uint64
	}
	sc := make([]scored, len(eps))
	for i, e := range eps {
		h := fnv.New64a()
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(seed))
		h.Write(b[:])
		h.Write([]byte(e.ID))
		sc[i] = scored{i, h.Sum64()}
	}
	sort.Slice(sc, func(i, j int) bool {
		if sc[i].hash != sc[j].hash {
			return sc[i].hash < sc[j].hash
		}
		return sc[i].idx < sc[j].idx
	})
	keep := make([]int, n)
	for i := range keep {
		keep[i] = sc[i].idx
	}
	sort.Ints(keep)
	out := make([]Episode, n)
	for i, k := range keep {
		out[i] = eps[k]
	}
	return out
}
