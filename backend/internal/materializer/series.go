package materializer

import (
	"hash/fnv"
	"sort"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

// maxSeriesEntries is the safety cap on the entries one series item emits in
// a single expansion (episodes skipped as "before the cursor existed" count
// too). Episodes with a non-positive duration never reach the materializer,
// so this only guards against a bug turning the expansion into a runaway loop.
const maxSeriesEntries = 20000

// seriesRow is an epg_entry a series item already produced: the persisted
// source of truth for what aired, which a regeneration adopts instead of
// recomputing.
type seriesRow struct {
	StartAt       int64
	CatalogItemID string
	Cycle         int
}

// seriesRun is the walk of one series block item on one channel during one
// expansion. It is rebuilt from persisted epg_entry rows on every run (no
// state of its own is stored): cycle + the set of episodes already played in
// that cycle + how many episodes were played since the cursor's origin. That
// is what lets a grid regeneration continue where the kept entries stopped
// instead of reshuffling.
type seriesRun struct {
	order    string
	amount   domain.SeriesAmount // how much one visit airs
	seed     uint64
	episodes []series.Episode // canonical order (see newSeriesRun)
	byID     map[string]int   // episode id -> index in episodes
	originMs int64

	// adopt holds persisted rows at or after the expansion's scan start that
	// have started, keyed by start time: a slot that starts exactly there
	// reuses that row's episode, so what the guide already showed does not
	// change when the catalog does. rows is every persisted row from there
	// on, for catchUp. consumed marks the ones already applied.
	adopt    map[int64]seriesRow
	rows     []seriesRow // adopt, sorted by start, for catchUp
	consumed map[int64]bool

	cycle   int
	played  map[string]bool
	lastEp  string
	ordinal int
	emitted int
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// splitmix64 is the usual 64-bit finalizer: cheap, well mixed, deterministic.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func lessNilLast(a, b *int64) (less, decided bool) {
	switch {
	case a == nil && b == nil:
		return false, false
	case a == nil:
		return false, true
	case b == nil:
		return true, true
	case *a != *b:
		return *a < *b, true
	}
	return false, false
}

// sortEpisodes puts episodes in the canonical order of the playback order:
// original is season then episode, release_date is premiere date first; both
// fall back to the other keys, then title and id so the order is total.
// shuffle and random only need a stable base, so they sort by id. A criteria
// item's title order sorts by folded title, then id.
func sortEpisodes(eps []series.Episode, order string) {
	if order == domain.OrderTitle {
		keys := make(map[string]string, len(eps))
		for _, e := range eps {
			keys[e.ID] = catalogsync.FoldText(e.Title)
		}
		sort.SliceStable(eps, func(i, j int) bool {
			if ki, kj := keys[eps[i].ID], keys[eps[j].ID]; ki != kj {
				return ki < kj
			}
			return eps[i].ID < eps[j].ID
		})
		return
	}
	sort.SliceStable(eps, func(i, j int) bool {
		a, b := eps[i], eps[j]
		switch order {
		case domain.OrderOriginal:
			if l, d := lessNilLast(a.SeasonNumber, b.SeasonNumber); d {
				return l
			}
			if l, d := lessNilLast(a.EpisodeNumber, b.EpisodeNumber); d {
				return l
			}
			if l, d := lessNilLast(a.PremiereDate, b.PremiereDate); d {
				return l
			}
			if a.Title != b.Title {
				return a.Title < b.Title
			}
		case domain.OrderReleaseDate:
			if l, d := lessNilLast(a.PremiereDate, b.PremiereDate); d {
				return l
			}
			if l, d := lessNilLast(a.SeasonNumber, b.SeasonNumber); d {
				return l
			}
			if l, d := lessNilLast(a.EpisodeNumber, b.EpisodeNumber); d {
				return l
			}
			if a.Title != b.Title {
				return a.Title < b.Title
			}
		}
		return a.ID < b.ID
	})
}

// newSeriesRun builds the walk for one series item on one channel. seed is
// the item's stored seed; it is mixed with channelID so two channels airing
// the same block never shuffle identically. history are the persisted rows
// that start before the expansion's scan start (they define the cursor),
// ahead are the ones from there on. Of those, only the ones that started by
// adoptUntilMs (what is on the air or already aired) are adopted slot by
// slot; later ones are recomputed, so catalog changes still reach the future.
// Rows before originMs are ignored.
func newSeriesRun(spec domain.SeriesSpec, channelID string, episodes []series.Episode, originMs int64, history, ahead []seriesRow, adoptUntilMs int64) *seriesRun {
	eps := append([]series.Episode(nil), episodes...)
	sortEpisodes(eps, spec.PlaybackOrder)
	r := &seriesRun{
		order:    spec.PlaybackOrder,
		amount:   spec.EffectiveAmount(),
		seed:     splitmix64(uint64(spec.Seed) ^ hashString(channelID)),
		episodes: eps,
		byID:     make(map[string]int, len(eps)),
		originMs: originMs,
		adopt:    make(map[int64]seriesRow),
		consumed: make(map[int64]bool),
		played:   make(map[string]bool),
	}
	for i, e := range eps {
		r.byID[e.ID] = i
	}
	for _, h := range history {
		if h.StartAt >= originMs {
			r.apply(h.CatalogItemID, h.Cycle)
		}
	}
	for _, a := range ahead {
		if a.StartAt >= originMs {
			r.rows = append(r.rows, a)
			if a.StartAt <= adoptUntilMs {
				r.adopt[a.StartAt] = a
			}
		}
	}
	sort.Slice(r.rows, func(i, j int) bool { return r.rows[i].StartAt < r.rows[j].StartAt })
	return r
}

// cycleDone reports whether every episode of the cycle in progress has aired,
// i.e. the next episode would open a new cycle. Meaningless for random, which
// has no cycles.
func (r *seriesRun) cycleDone() bool {
	if len(r.episodes) == 0 {
		return false
	}
	for _, e := range r.episodes {
		if !r.played[e.ID] {
			return false
		}
	}
	return true
}

func (r *seriesRun) empty() bool { return len(r.episodes) == 0 }

// apply records that episode id aired in cycle: a higher cycle than the
// current one starts a fresh played set.
func (r *seriesRun) apply(id string, cycle int) {
	if cycle != r.cycle {
		r.cycle = cycle
		r.played = make(map[string]bool)
	}
	r.played[id] = true
	r.lastEp = id
	r.ordinal++
}

// next returns the episode that airs next and the cycle it belongs to,
// without changing the state. ok is false for a series with no episodes.
func (r *seriesRun) next() (ep series.Episode, cycle int, ok bool) {
	n := len(r.episodes)
	if n == 0 {
		return series.Episode{}, 0, false
	}
	if r.order == domain.OrderRandom {
		idx := int(splitmix64(r.seed^splitmix64(uint64(r.ordinal))) % uint64(n))
		return r.episodes[idx], 0, true
	}

	cycle = r.cycle
	played := r.played
	rolled := false
	unplayed := 0
	for _, e := range r.episodes {
		if !played[e.ID] {
			unplayed++
		}
	}
	if unplayed == 0 {
		// Every episode has aired: a new cycle starts. Removed episodes left
		// the set, new ones are simply unplayed, so nothing else to settle.
		cycle++
		played = nil
		rolled = true
		unplayed = n
	}

	if r.order == domain.OrderShuffle {
		// A cycle's order is the episodes sorted by a hash of (seed, cycle,
		// id): the pick is the unplayed episode with the smallest key. An
		// episode the catalog gained mid-cycle is unplayed, so it takes its
		// hashed place among the ones still to play; one it lost leaves no
		// hole. On a fresh cycle the previous cycle's last episode is not
		// allowed to open it (unless it is the only episode).
		best, bestKey := -1, uint64(0)
		for i, e := range r.episodes {
			if played[e.ID] {
				continue
			}
			if rolled && n > 1 && e.ID == r.lastEp {
				continue
			}
			key := splitmix64(r.seed ^ splitmix64(uint64(cycle)+1) ^ hashString(e.ID))
			if best < 0 || key < bestKey {
				best, bestKey = i, key
			}
		}
		return r.episodes[best], cycle, true
	}

	// original and release_date: the first unplayed episode in canonical order.
	for _, e := range r.episodes {
		if !played[e.ID] {
			return e, cycle, true
		}
	}
	return r.episodes[0], cycle, true // unreachable: unplayed > 0
}

// adoptAt returns the episode a persisted row already assigned to a slot
// starting at startMs, if that episode still exists.
func (r *seriesRun) adoptAt(startMs int64) (series.Episode, int, bool) {
	row, ok := r.adopt[startMs]
	if !ok {
		return series.Episode{}, 0, false
	}
	idx, exists := r.byID[row.CatalogItemID]
	if !exists {
		return series.Episode{}, 0, false
	}
	return r.episodes[idx], row.Cycle, true
}

// slot is the series' decision for a slot starting at startMs.
type seriesSlot struct {
	Episode series.Episode
	Cycle   int
	// Skip is set when the slot ended before the cursor existed: time passes
	// by the episode's length, but nothing is emitted and nothing consumed.
	Skip bool
	// adopted rows are applied through apply like any other.
}

// plan decides the slot starting at startMs without consuming it.
func (r *seriesRun) plan(startMs int64) (seriesSlot, bool) {
	if ep, cycle, ok := r.adoptAt(startMs); ok {
		return seriesSlot{Episode: ep, Cycle: cycle}, true
	}
	ep, cycle, ok := r.next()
	if !ok {
		return seriesSlot{}, false
	}
	if startMs+ep.DurationMs <= r.originMs {
		return seriesSlot{Episode: ep, Cycle: cycle, Skip: true}, true
	}
	return seriesSlot{Episode: ep, Cycle: cycle}, true
}

// commit consumes a planned, emitted slot.
func (r *seriesRun) commit(startMs int64, s seriesSlot) {
	if _, ok := r.adopt[startMs]; ok {
		r.consumed[startMs] = true
	}
	r.apply(s.Episode.ID, s.Cycle)
	r.emitted++
}

// catchUp consumes every adopted row starting before t that was not applied
// yet. A generation that begins at t (the continuous item starts at the end
// of the kept head) must count the entries the kept head already holds.
func (r *seriesRun) catchUp(t int64) {
	for _, row := range r.rows {
		if row.StartAt >= t {
			break
		}
		if r.consumed[row.StartAt] {
			continue
		}
		r.consumed[row.StartAt] = true
		if _, ok := r.byID[row.CatalogItemID]; ok {
			r.apply(row.CatalogItemID, row.Cycle)
		}
	}
}

// criteriaWalkSpec maps a criteria item's spec onto the series spec the walk
// is built from: the same order, seed and amount, with "all matches" being
// the rest of the playback cycle in progress.
func criteriaWalkSpec(cs domain.CriteriaSpec) domain.SeriesSpec {
	amount := domain.SeriesAmount{Mode: domain.AmountSeriesEnd}
	if cs.Amount.Mode == domain.CriteriaAmountCount {
		amount = domain.SeriesAmount{Mode: domain.AmountCount, Count: cs.Amount.Count}
	}
	return domain.SeriesSpec{PlaybackOrder: cs.PlaybackOrder, Seed: cs.Seed, Amount: &amount}
}
