package materializer

import (
	"hash/fnv"
	"sort"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
)

// maxSeriesEntries is the safety cap on the entries one series item emits in
// a single expansion. Slots skipped as "before the cursor existed" and
// entries that are already over (before "now") do not count: neither airs
// anything new. They have their own bound, maxSeriesSkips.
// Episodes with a non-positive duration never reach the materializer, so this
// only guards against a bug (or a pathological catalog of very short items)
// turning the expansion into a runaway loop. Hitting it is logged.
const maxSeriesEntries = 20000

// maxSeriesSkips bounds the slots one series item may skip before its cursor
// origin, or walk through while already over, in one expansion (the lookback is a day, so even 1-second episodes
// stay far below it).
const maxSeriesSkips = 200000

// seriesRow is an epg_entry a series item already produced: the persisted
// source of truth for what aired, which a regeneration adopts instead of
// recomputing.
type seriesRow struct {
	StartAt       int64
	CatalogItemID string
	Cycle         int
	Round         int // interleaved walks only
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

	cycle  int
	played map[string]bool
	lastEp string
	// playedIn counts the episodes of the walk (not ones the catalog lost)
	// in played, and firstFree is no later than the first unplayed one in
	// canonical order: both keep a step O(1) instead of a scan of the series.
	playedIn  int
	firstFree int
	ordinal   int
	emitted   int
	skipped   int // slots that emit nothing that will air: before the origin, or already over
	nowMs     int64
	capLogged bool // the cap warning is logged once per run

	// Interleaved walks (il != nil, order == OrderInterleaved): the series the
	// items group into, the group each episode belongs to, and the position in
	// the rounds: the round in progress, the group that aired last (-1: none
	// yet this cycle) and how many episodes it aired in its turn.
	il        *domain.Interleave
	groups    []*walkGroup
	groupOf   map[string]int
	round     int
	lastGroup int
	turn      int
	orderKey  [2]int // (cycle, round) the cached roundOrder was computed for
	orderMemo []int
}

// walkGroup is one series of an interleaved walk (or a lone item with none):
// its episodes in natural order.
type walkGroup struct {
	key         string
	title       string // folded, for ordering
	eps         []series.Episode
	minPremiere *int64
	keyHash     uint64
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
	if spec.PlaybackOrder == domain.OrderInterleaved {
		// Natural order (season, episode, ...) is the order within a series;
		// the groups are carved out of it below.
		sortEpisodes(eps, domain.OrderOriginal)
	} else {
		sortEpisodes(eps, spec.PlaybackOrder)
	}
	r := &seriesRun{
		lastGroup: -1,
		orderKey:  [2]int{-1, -1},
		order:     spec.PlaybackOrder,
		amount:    spec.EffectiveAmount(),
		seed:      splitmix64(uint64(spec.Seed) ^ hashString(channelID)),
		episodes:  eps,
		byID:      make(map[string]int, len(eps)),
		originMs:  originMs,
		nowMs:     adoptUntilMs,
		adopt:     make(map[int64]seriesRow),
		consumed:  make(map[int64]bool),
		played:    make(map[string]bool),
	}
	for i, e := range eps {
		r.byID[e.ID] = i
	}
	if spec.PlaybackOrder == domain.OrderInterleaved {
		il := domain.Interleave{}.Normalized()
		if spec.Interleave != nil {
			il = spec.Interleave.Normalized()
		}
		r.il = &il
		r.groupOf = make(map[string]int, len(eps))
		byKey := make(map[string]int)
		for _, e := range eps {
			key := e.GroupKey
			if key == "" {
				key = "i:" + e.ID
			}
			gi, ok := byKey[key]
			if !ok {
				gi = len(r.groups)
				byKey[key] = gi
				title := e.GroupTitle
				if title == "" {
					title = e.Title
				}
				r.groups = append(r.groups, &walkGroup{key: key, title: catalogsync.FoldText(title), keyHash: hashString(key)})
			}
			g := r.groups[gi]
			g.eps = append(g.eps, e)
			if e.PremiereDate != nil && (g.minPremiere == nil || *e.PremiereDate < *g.minPremiere) {
				v := *e.PremiereDate
				g.minPremiere = &v
			}
			r.groupOf[e.ID] = gi
		}
	}
	for _, h := range history {
		if h.StartAt >= originMs {
			r.applyRound(h.CatalogItemID, h.Cycle, h.Round)
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
	return r.playedIn == len(r.episodes)
}

func (r *seriesRun) empty() bool { return len(r.episodes) == 0 }

// apply records that episode id aired in cycle: a higher cycle than the
// current one starts a fresh played set.
func (r *seriesRun) apply(id string, cycle int) { r.applyRound(id, cycle, 0) }

// applyRound is apply for an interleaved walk, which also records the round.
// An episode the catalog no longer holds still counts as played but tells
// nothing about whose turn it was, so the position in the rounds is kept.
func (r *seriesRun) applyRound(id string, cycle, round int) {
	if cycle != r.cycle {
		r.cycle = cycle
		r.played = make(map[string]bool)
		r.playedIn, r.firstFree = 0, 0
		r.round, r.lastGroup, r.turn = 0, -1, 0
	}
	if _, in := r.byID[id]; in && !r.played[id] {
		r.playedIn++
	}
	r.played[id] = true
	r.lastEp = id
	r.ordinal++
	if r.il == nil {
		return
	}
	gi, ok := r.groupOf[id]
	if !ok {
		return
	}
	if gi == r.lastGroup && round == r.round {
		r.turn++
	} else {
		r.turn = 1
	}
	r.lastGroup, r.round = gi, round
}

// roundOrder is the sequence the series take their turns in during a round of
// a cycle: by title (or earliest premiere) the same every round, a shuffle
// fixed within the cycle, or a fresh random sequence every round. Ties break
// on the series key so the order is total.
func (r *seriesRun) roundOrder(cycle, round int) []int {
	if r.il.SeriesOrder != domain.OrderRandom {
		round = 0 // the same sequence every round
	}
	if r.orderKey == [2]int{cycle, round} && r.orderMemo != nil {
		return r.orderMemo
	}
	order := make([]int, len(r.groups))
	for i := range order {
		order[i] = i
	}
	hashOf := func(g *walkGroup) uint64 {
		h := r.seed ^ splitmix64(uint64(cycle)+1) ^ g.keyHash
		if r.il.SeriesOrder == domain.OrderRandom {
			h ^= splitmix64(uint64(round) + 0x51ed)
		}
		return splitmix64(h)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := r.groups[order[i]], r.groups[order[j]]
		switch r.il.SeriesOrder {
		case domain.OrderShuffle, domain.OrderRandom:
			if ha, hb := hashOf(a), hashOf(b); ha != hb {
				return ha < hb
			}
		case domain.OrderReleaseDate:
			if l, d := lessNilLast(a.minPremiere, b.minPremiere); d {
				return l
			}
			fallthrough
		default:
			if a.title != b.title {
				return a.title < b.title
			}
		}
		return a.key < b.key
	})
	if r.il.SeriesOrder == domain.OrderRandom && round > 0 && len(order) > 1 {
		// A fresh random order must not open with the series that closed the
		// previous round (when it is still in the rounds, which it is unless
		// the catalog changed): swap it with the second.
		prev := r.roundOrder(cycle, round-1)
		if order[0] == prev[len(prev)-1] {
			order[0], order[1] = order[1], order[0]
		}
	}
	r.orderKey, r.orderMemo = [2]int{cycle, round}, order
	return order
}

func (r *seriesRun) firstUnplayed(g *walkGroup, played map[string]bool) (series.Episode, bool) {
	for _, e := range g.eps {
		if !played[e.ID] {
			return e, true
		}
	}
	return series.Episode{}, false
}

// nextInterleaved picks the episode that airs next in an interleaved walk:
// the same series while its turn (PerRound episodes) is not over, else the
// next series of the round that still has an unplayed episode, else the first
// such series of the next round. When every episode of the cycle has aired a
// new cycle opens, which is how a "restart" walk begins again (a series that
// ran out earlier simply waited its turn out; a series the catalog gained
// mid-cycle is unplayed and takes its place the next time its turn comes up).
func (r *seriesRun) nextInterleaved() (ep series.Episode, cycle, round int, ok bool) {
	cycle, round = r.cycle, r.round
	played, last, turn := r.played, r.lastGroup, r.turn
	if r.cycleDone() {
		cycle++
		played, last, turn, round = nil, -1, 0, 0
	}
	if last >= 0 && turn < r.il.PerRound {
		if e, ok := r.firstUnplayed(r.groups[last], played); ok {
			return e, cycle, round, true
		}
	}
	order := r.roundOrder(cycle, round)
	start := 0
	if last >= 0 {
		for i, gi := range order {
			if gi == last {
				start = i + 1
				break
			}
		}
	}
	for _, gi := range order[start:] {
		if e, ok := r.firstUnplayed(r.groups[gi], played); ok {
			return e, cycle, round, true
		}
	}
	// The round is over: the next one starts with its first series that still
	// has an episode to play.
	round++
	for _, gi := range r.roundOrder(cycle, round) {
		if e, ok := r.firstUnplayed(r.groups[gi], played); ok {
			return e, cycle, round, true
		}
	}
	return series.Episode{}, cycle, round, false
}

// next returns the episode that airs next and the cycle it belongs to,
// without changing the state. ok is false for a series with no episodes.
func (r *seriesRun) next() (ep series.Episode, cycle int, ok bool) {
	ep, cycle, _, ok = r.nextRound()
	return ep, cycle, ok
}

// nextRound is next for every order; round is only meaningful (and non-zero)
// for an interleaved walk.
func (r *seriesRun) nextRound() (ep series.Episode, cycle, round int, ok bool) {
	n := len(r.episodes)
	if n == 0 {
		return series.Episode{}, 0, 0, false
	}
	if r.il != nil {
		return r.nextInterleaved()
	}
	ep, cycle, ok = r.nextLinear()
	return ep, cycle, 0, ok
}

func (r *seriesRun) nextLinear() (ep series.Episode, cycle int, ok bool) {
	n := len(r.episodes)
	if r.order == domain.OrderRandom {
		idx := int(splitmix64(r.seed^splitmix64(uint64(r.ordinal))) % uint64(n))
		return r.episodes[idx], 0, true
	}

	cycle = r.cycle
	played := r.played
	rolled := false
	unplayed := n - r.playedIn
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
	start := 0
	if !rolled {
		for r.firstFree < n && played[r.episodes[r.firstFree].ID] {
			r.firstFree++
		}
		start = r.firstFree
	}
	for _, e := range r.episodes[start:] {
		if !played[e.ID] {
			return e, cycle, true
		}
	}
	return r.episodes[0], cycle, true // unreachable: unplayed > 0
}

// adoptAt returns the episode a persisted row already assigned to a slot
// starting at startMs, if that episode still exists.
func (r *seriesRun) adoptAt(startMs int64) (series.Episode, int, int, bool) {
	row, ok := r.adopt[startMs]
	if !ok {
		return series.Episode{}, 0, 0, false
	}
	idx, exists := r.byID[row.CatalogItemID]
	if !exists {
		return series.Episode{}, 0, 0, false
	}
	return r.episodes[idx], row.Cycle, row.Round, true
}

// slot is the series' decision for a slot starting at startMs.
type seriesSlot struct {
	Episode series.Episode
	Cycle   int
	Round   int // interleaved walks only
	// Skip is set when the slot ended before the cursor existed: time passes
	// by the episode's length, but nothing is emitted and nothing consumed.
	Skip bool
	// adopted rows are applied through apply like any other.
}

// plan decides the slot starting at startMs without consuming it.
func (r *seriesRun) plan(startMs int64) (seriesSlot, bool) {
	if ep, cycle, round, ok := r.adoptAt(startMs); ok {
		return seriesSlot{Episode: ep, Cycle: cycle, Round: round}, true
	}
	ep, cycle, round, ok := r.nextRound()
	if !ok {
		return seriesSlot{}, false
	}
	if startMs+ep.DurationMs <= r.originMs {
		return seriesSlot{Episode: ep, Cycle: cycle, Round: round, Skip: true}, true
	}
	return seriesSlot{Episode: ep, Cycle: cycle, Round: round}, true
}

// commit consumes a planned, emitted slot.
func (r *seriesRun) commit(startMs int64, s seriesSlot) {
	if _, ok := r.adopt[startMs]; ok {
		r.consumed[startMs] = true
	}
	r.applyRound(s.Episode.ID, s.Cycle, s.Round)
	if startMs+s.Episode.DurationMs <= r.nowMs {
		// Already over: the entry only exists to keep the walk's state (the
		// writer drops it), so it must not eat the cap meant for what will
		// actually air. It counts against the separate, larger skip bound.
		r.skipped++
		return
	}
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
			r.applyRound(row.CatalogItemID, row.Cycle, row.Round)
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
	return domain.SeriesSpec{PlaybackOrder: cs.PlaybackOrder, Seed: cs.Seed, Amount: &amount, Interleave: cs.Interleave}
}
