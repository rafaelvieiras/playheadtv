package materializer

import (
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
)

// occurrence is one nominal airing of a schedule_item's block, resolved to
// an absolute UTC instant (see buildOccurrences).
type occurrence struct {
	ScheduleItemID string
	BlockID        string
	NominalStart   time.Time
}

// channelInput bundles everything expandChannel needs for one channel,
// pre-fetched by the orchestrator (materializer.go) so this file stays free
// of storage.DB and is trivial to unit test with hand-built data.
type channelInput struct {
	// Warnings are the configuration problems the loader met (see Warning).
	Warnings []Warning

	ChannelID       string
	TenantID        string
	Timezone        string // IANA name; see domain.Channel.Timezone
	ScheduleItems   []domain.ScheduleItem
	BlockItems      map[string][]domain.BlockItem      // blockID -> ordered by position
	FillerRules     []domain.FillerRule                // every rule scoped to this channel or one of its blocks
	BreakTemplates  []domain.BreakTemplate             // every break template scoped to this channel or one of its blocks, steps in order
	PoolItems       map[string][]domain.FillerPoolItem // filler_pool_id -> ordered by rowid (insertion order)
	CatalogDuration map[string]int64                   // catalog_item_id -> duration_ms
	// CutPlanner decides where a program is cut into segments (cuts.go); nil
	// never cuts.
	CutPlanner      CutPlanner
	DynamicBlockIDs map[string]bool // blockID -> true iff block.criteria is non-NULL
	// RepeatBlockIDs holds the manual blocks whose list repeats (list_mode
	// "repeat"). A manual block missing from it plays its list once; a dynamic
	// block always loops and is never consulted.
	RepeatBlockIDs map[string]bool
	// Series holds the walk of every series block item the channel's blocks
	// contain, keyed by block_item.id. Expansion mutates the runs.
	Series map[string]*seriesRun

	// ContinuousStart is the persisted rotation position the continuous
	// schedule item (if any) resumes from: the block-item index its next
	// program takes. Zero starts the rotation from the beginning.
	ContinuousStart int
	// ContinuousVisitDone is how many episodes of the series visit under way
	// the kept head already aired, when the head ends in the middle of a
	// "count" visit (see materializeChannel). Zero otherwise.
	ContinuousVisitDone int
	// ContinuousStage is how far the program unit (preroll, program,
	// postroll, then the midroll before the next one) the kept head ends in
	// had got, so the generation resuming right after it owes only the rest.
	ContinuousStage breakStage
	// KeptUntil is non-zero when the channel's already-materialized head is
	// being kept (grid stability): it is the end of the last kept entry.
	// Nothing is generated for the continuous item before it, and any
	// generated entry starting before it is dropped, since the kept rows
	// already cover that span.
	KeptUntil time.Time
	// Residuals, when non-nil, collects the spans a timed slot (or the
	// continuous item's stretch) ends with nothing in them after items, tail
	// and fallback, for the unfilled-slot warning. Collection is off in the
	// normal materialization.
	Residuals *[]residual
}

// residual is a stretch of a slot that no entry covers although a real
// boundary (the next timed item) follows it, and no channel fallback rule
// applies to take it over.
type residual struct {
	BlockID string
	Length  time.Duration
}

// selectionState carries the mutable, ephemeral bookkeeping a single Run()
// pass needs across every channel it processes. The sequential cursor is
// intentionally NOT persisted anywhere else: restarting the process, or
// simply running Run() again, resets it to the start of every pool. This is
// documented as an accepted v1 limitation (see docs/ROADMAP.md).
type selectionState struct {
	seqCursor map[string]int // filler_rule.ID -> next pool index
}

func newSelectionState() *selectionState {
	return &selectionState{seqCursor: make(map[string]int)}
}

// parseHHMM parses a "HH:MM" string (the format schedule_item.start_time_local
// is validated against by the API layer) into hour/minute components.
func parseHHMM(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// onceOccurrenceTime resolves a RecurrenceOnce schedule_item's single
// occurrence, reporting whether it lands inside [scanFrom, windowEnd).
func onceOccurrenceTime(startAtMs int64, scanFrom, windowEnd time.Time) (time.Time, bool) {
	t := time.UnixMilli(startAtMs).UTC()
	if !t.Before(scanFrom) && t.Before(windowEnd) {
		return t, true
	}
	return time.Time{}, false
}

// weeklyOccurrenceTimes resolves every UTC instant, within [scanFrom,
// windowEnd), at which a RecurrenceWeekly schedule_item (daysOfWeek 0=Sunday,
// one occurrence per listed day; startTimeLocal "HH:MM") nominally starts, per
// loc (channel.timezone).
//
// It walks every calendar day (in loc) touching the window, padded by one day
// on each side to stay correct even for large UTC offsets where a local
// calendar day can straddle the UTC window boundary; the final UTC-window
// check filters out anything the padding over-generates.
func weeklyOccurrenceTimes(daysOfWeek domain.Weekdays, startTimeLocal string, loc *time.Location, scanFrom, windowEnd time.Time) ([]time.Time, error) {
	hour, minute, err := parseHHMM(startTimeLocal)
	if err != nil {
		return nil, err
	}

	localStart := scanFrom.In(loc)
	localEnd := windowEnd.In(loc)
	dayCursor := time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	lastDay := time.Date(localEnd.Year(), localEnd.Month(), localEnd.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)

	var out []time.Time
	for !dayCursor.After(lastDay) {
		if daysOfWeek.Contains(int(dayCursor.Weekday())) {
			candidate := time.Date(dayCursor.Year(), dayCursor.Month(), dayCursor.Day(), hour, minute, 0, 0, loc).UTC()
			if !candidate.Before(scanFrom) && candidate.Before(windowEnd) {
				out = append(out, candidate)
			}
		}
		dayCursor = dayCursor.AddDate(0, 0, 1)
	}
	return out, nil
}

// buildOccurrences expands every schedule item into its nominal occurrences
// within [scanFrom, windowEnd), sorted ascending by NominalStart. Callers are
// expected to have already filtered items to active=1 for this channel's
// assigned schedule.
func buildOccurrences(items []domain.ScheduleItem, loc *time.Location, scanFrom, windowEnd time.Time) []occurrence {
	var out []occurrence
	for _, e := range items {
		switch e.Recurrence {
		case domain.RecurrenceOnce:
			if e.StartAt == nil {
				continue
			}
			if t, ok := onceOccurrenceTime(*e.StartAt, scanFrom, windowEnd); ok {
				out = append(out, occurrence{ScheduleItemID: e.ID, BlockID: e.BlockID, NominalStart: t})
			}
		case domain.RecurrenceWeekly:
			if len(e.DaysOfWeek) == 0 || e.StartTimeLocal == nil {
				continue
			}
			times, err := weeklyOccurrenceTimes(e.DaysOfWeek, *e.StartTimeLocal, loc, scanFrom, windowEnd)
			if err != nil {
				// The API layer already validates start_time_local as HH:MM;
				// this only defends against bad data written some other way.
				logging.Warnf("materializer: schedule_item %s: invalid start_time_local %q: %v", e.ID, *e.StartTimeLocal, err)
				continue
			}
			for _, t := range times {
				out = append(out, occurrence{ScheduleItemID: e.ID, BlockID: e.BlockID, NominalStart: t})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NominalStart.Before(out[j].NominalStart) })
	return out
}

// effectiveDuration is CatalogItem.DurationMs, unless BlockItem.TrimDurationMs
// overrides it.
func effectiveDuration(bi domain.BlockItem, catalogDuration map[string]int64) int64 {
	if bi.TrimDurationMs != nil {
		return *bi.TrimDurationMs
	}
	return catalogDuration[bi.CatalogItemID]
}

// resolveFillerRule picks the applicable rule for kind, preferring a
// block-scoped rule (scope_id == blockID) over a channel-scoped one
// (scope_id == channelID); ties within a scope are broken by the lowest
// Position. Returns nil if no rule of that kind applies.
func resolveFillerRule(rules []domain.FillerRule, kind domain.FillerKind, blockID, channelID string) *domain.FillerRule {
	var blockCandidates, channelCandidates []domain.FillerRule
	for _, r := range rules {
		if r.Kind != kind {
			continue
		}
		switch {
		case r.Scope == domain.ScopeBlock && blockID != "" && r.ScopeID == blockID:
			blockCandidates = append(blockCandidates, r)
		case r.Scope == domain.ScopeChannel && r.ScopeID == channelID:
			channelCandidates = append(channelCandidates, r)
		}
	}
	if best := lowestPosition(blockCandidates); best != nil {
		return best
	}
	return lowestPosition(channelCandidates)
}

func lowestPosition(candidates []domain.FillerRule) *domain.FillerRule {
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Position < best.Position {
			best = c
		}
	}
	return &best
}

// selectFillerItem picks one item from pool per rule.Selection, mutating
// state for the "sequential" strategy. Reports ok=false iff pool is empty.
//
// For "sequential", the pick-then-check-fit loop in fillGap always commits
// this call's advance even when the picked item ends up not fitting and the
// gap-fill loop stops without inserting it — the cursor is per-Run()-call
// state anyway (see selectionState), so a burned rotation slot on a
// gap-fill's last, rejected candidate is not worth the extra bookkeeping a
// peek/commit split would add.
func selectFillerItem(rule domain.FillerRule, pool []domain.FillerPoolItem, state *selectionState) (domain.FillerPoolItem, bool) {
	if len(pool) == 0 {
		return domain.FillerPoolItem{}, false
	}
	switch rule.Selection {
	case domain.SelectionSequential:
		idx := state.seqCursor[rule.ID] % len(pool)
		state.seqCursor[rule.ID]++
		return pool[idx], true
	case domain.SelectionWeighted:
		total := 0
		for _, it := range pool {
			total += it.Weight
		}
		if total <= 0 {
			return pool[0], true
		}
		roll := rand.Intn(total)
		acc := 0
		for _, it := range pool {
			acc += it.Weight
			if roll < acc {
				return it, true
			}
		}
		return pool[len(pool)-1], true
	default: // SelectionRandom, or any unrecognized value
		return pool[rand.Intn(len(pool))], true
	}
}

// pickFiller selects the item a filler rule would insert next, without
// emitting an entry — split out from insertFiller (expandChannel) so
// postroll can be picked once, up front, to learn its duration before
// deciding where program items must stop (issue #10's overrun policy).
// ok is false for exactly the same reasons insertFiller used to silently
// no-op: a nil rule, an empty pool, or a non-positive duration.
func pickFiller(rule *domain.FillerRule, poolItems map[string][]domain.FillerPoolItem, catalogDuration map[string]int64, state *selectionState) (domain.FillerPoolItem, int64, bool) {
	if rule == nil {
		return domain.FillerPoolItem{}, 0, false
	}
	item, ok := selectFillerItem(*rule, poolItems[rule.FillerPoolID], state)
	if !ok {
		return domain.FillerPoolItem{}, 0, false
	}
	dur := catalogDuration[item.CatalogItemID]
	if dur <= 0 {
		return domain.FillerPoolItem{}, 0, false
	}
	return item, dur, true
}

// maxFitAttemptsLimit is the largest max_fit_attempts a filler rule may carry
// (validated by the API and MCP on write). Each failed attempt costs one more
// look at the pool, so the bound keeps a full-window regeneration cheap.
const maxFitAttemptsLimit = domain.MaxFitAttemptsLimit

// fitAttempts is how many candidates a fit-seeking fill (tail, fallback) may
// reject in a row before it gives up: the rule's max_fit_attempts, 1 (the
// pre-attempts behavior) when unset.
func fitAttempts(rule *domain.FillerRule) int {
	if rule.MaxFitAttempts < 1 {
		return 1
	}
	if rule.MaxFitAttempts > maxFitAttemptsLimit {
		return maxFitAttemptsLimit
	}
	return rule.MaxFitAttempts
}

// fitPicker draws the candidates of one fillGap call. Unlike
// selectFillerItem it can look at several candidates for one stretch of time
// (up to attempts of them, each at most once per round) and it only commits a
// "sequential" rotation position for an item that actually airs: a candidate
// skipped because it did not fit never uses up a slot, and a round in which
// nothing fits leaves the rotation where it was.
type fitPicker struct {
	rule     *domain.FillerRule
	pool     []domain.FillerPoolItem
	durMs    map[string]int64
	attempts int
	state    *selectionState
	// minDur is the shortest positive item of the pool: a residual below it can
	// never be filled, whatever the attempts.
	minDur int64
	// totalWeight is the pool's weight sum, for the weighted strategy.
	totalWeight int
	tried       []int // scratch: pool indexes rejected in the current round
}

func newFitPicker(rule *domain.FillerRule, pool []domain.FillerPoolItem, durMs map[string]int64, state *selectionState) *fitPicker {
	p := &fitPicker{rule: rule, pool: pool, durMs: durMs, attempts: fitAttempts(rule), state: state}
	if p.attempts > len(pool) {
		p.attempts = len(pool)
	}
	for _, it := range pool {
		if d := durMs[it.CatalogItemID]; d > 0 && (p.minDur == 0 || d < p.minDur) {
			p.minDur = d
		}
		p.totalWeight += it.Weight
	}
	return p
}

func (p *fitPicker) wasTried(idx int) bool {
	for _, t := range p.tried {
		if t == idx {
			return true
		}
	}
	return false
}

// candidate returns the pool index of the k-th candidate of a round (k counts
// from 0), or false when the strategy has nothing left to offer.
func (p *fitPicker) candidate(k, seqStart int) (int, bool) {
	n := len(p.pool)
	switch p.rule.Selection {
	case domain.SelectionSequential:
		return (seqStart + k) % n, true
	case domain.SelectionWeighted:
		remaining := p.totalWeight
		for _, t := range p.tried {
			remaining -= p.pool[t].Weight
		}
		if remaining <= 0 {
			// Same fallback as selectFillerItem: unusable weights, first item
			// still on offer.
			for i := range p.pool {
				if !p.wasTried(i) {
					return i, true
				}
			}
			return 0, false
		}
		roll := rand.Intn(remaining)
		acc := 0
		for i, it := range p.pool {
			if p.wasTried(i) {
				continue
			}
			acc += it.Weight
			if roll < acc {
				return i, true
			}
		}
		return 0, false
	default: // SelectionRandom, or any unrecognized value
		if k >= n {
			return 0, false
		}
		for {
			idx := rand.Intn(n)
			if !p.wasTried(idx) {
				return idx, true
			}
		}
	}
}

// pick returns an item that ends within remaining milliseconds, trying up to
// attempts candidates; ok is false when none fit.
func (p *fitPicker) pick(remaining int64) (domain.FillerPoolItem, int64, bool) {
	if p.minDur == 0 || remaining < p.minDur {
		return domain.FillerPoolItem{}, 0, false
	}
	seqStart := 0
	if p.rule.Selection == domain.SelectionSequential {
		seqStart = p.state.seqCursor[p.rule.ID] % len(p.pool)
	}
	p.tried = p.tried[:0]
	for k := 0; k < p.attempts; k++ {
		idx, ok := p.candidate(k, seqStart)
		if !ok {
			break
		}
		item := p.pool[idx]
		// A non-positive duration (bad catalog data) never fits, so it cannot
		// loop forever either.
		if d := p.durMs[item.CatalogItemID]; d > 0 && d <= remaining {
			if p.rule.Selection == domain.SelectionSequential {
				p.state.seqCursor[p.rule.ID] += k + 1
			}
			return item, d, true
		}
		p.tried = append(p.tried, idx)
	}
	return domain.FillerPoolItem{}, 0, false
}

// fillGap inserts, starting at cursor, as many whole items selected from
// rule's pool as fit before target, advancing the cursor after each one. It
// never cuts an item short. When a picked item does not fit it tries other
// candidates, up to the rule's max_fit_attempts in a row (1 gives up at the
// first miss), and keeps going with whatever does fit while the residual is
// not filled: the attempts bound consecutive misses, not the number of items
// aired. Skipped candidates do not use up a sequential rotation slot (see
// fitPicker). What no candidate can fill is left without an epg_entry, for the
// caller's next layer (the fallback rule, then a gap). It is a no-op
// (returns cursor unchanged) when rule is nil or its pool is empty.
func fillGap(
	rule *domain.FillerRule,
	poolItemsByPool map[string][]domain.FillerPoolItem,
	catalogDuration map[string]int64,
	cursor, target time.Time,
	kind domain.EpgEntryKind,
	tenantID, channelID string,
	generatedAt int64,
	sourceScheduleItemID *string,
	state *selectionState,
) ([]domain.EpgEntry, time.Time) {
	if rule == nil {
		return nil, cursor
	}
	pool := poolItemsByPool[rule.FillerPoolID]
	if len(pool) == 0 {
		return nil, cursor
	}

	picker := newFitPicker(rule, pool, catalogDuration, state)
	var entries []domain.EpgEntry
	for len(entries) < maxLoopEntries {
		remaining := target.Sub(cursor).Milliseconds()
		item, durMs, ok := picker.pick(remaining)
		if !ok {
			break
		}
		end := cursor.Add(time.Duration(durMs) * time.Millisecond)
		entries = append(entries, domain.EpgEntry{
			ID:                   uuid.NewString(),
			TenantID:             tenantID,
			ChannelID:            channelID,
			Kind:                 kind,
			CatalogItemID:        item.CatalogItemID,
			StartAt:              cursor.UnixMilli(),
			EndAt:                end.UnixMilli(),
			SourceScheduleItemID: sourceScheduleItemID,
			GeneratedAt:          generatedAt,
		})
		cursor = end
	}
	if len(entries) >= maxLoopEntries {
		warnLoopCap(channelID, sourceScheduleItemID)
	}
	return entries, cursor
}

// seriesVisit describes one visit of a series item: how much it airs, how
// much of that already aired (a visit resumed after a regeneration), and the
// rotation indexes its entries store.
type seriesVisit struct {
	Amount domain.SeriesAmount
	// Done is the number of episodes of this visit already on the air, for a
	// "count" visit that a regeneration resumes midway.
	Done int
	// RotIdx is stored on every entry of a continuous block's visit (the
	// rotation stays on the item while the visit is under way); RotNext
	// replaces it on the entry that completes the visit, so the rotation moves
	// on. Both nil for a manual block, which has no rotation.
	RotIdx, RotNext *int
	// Mid, when set, puts a midroll break between two consecutive episodes
	// (see midrollSource). PrevEpisode says the entry right before the visit's
	// cursor is itself an episode of a series walk, so a break also goes
	// before the visit's first episode.
	Mid         *midrollSource
	PrevEpisode bool
	// Cuts, with Mid, splits an episode into segments with a midroll between
	// them (see segmenter); nil plays episodes whole. Unit carries its own.
	Cuts CutPlanner
	// BlockID is the block the visit's item belongs to, for the planner.
	BlockID string
	// Unit, when set, replaces Mid/PrevEpisode: every episode is a unit with
	// its own preroll and postroll and the midroll between episodes (see
	// breakLoop). Used by the endless sequences (continuous items, dynamic
	// blocks), where a unit must fit whole before the limit.
	Unit *breakLoop
}

// midrollSource is what a midroll break needs: the resolved midroll rule and
// the data to pick from its pool. The very same pickFiller serves the breaks
// between catalog items of a manual block and the ones between series
// episodes, so rule resolution, pool selection and weights have no special
// case for either. A nil source or a nil rule never breaks.
//
// When a break template governs the boundary (tmpl, see resolveBreak) the
// source airs the template's steps instead of the rule's single item
// (pickBreak); the rule is then nil.
type midrollSource struct {
	rule      *domain.FillerRule
	tmpl      *domain.BreakTemplate
	pools     map[string][]domain.FillerPoolItem
	durations map[string]int64
	state     *selectionState

	steps []*stepSource // lazily built from tmpl, see pickBreak
}

// emitSeries expands one visit of a series block item into consecutive
// episodes starting at cursor, following the run's playback order, for as
// long as a slot starts before limit and the visit's amount is not reached:
// "count" stops after that many episodes, "series_end" when every episode of
// the cycle in progress has aired (a random series, which has no cycles, airs
// as many episodes as it holds), "slot_end" only at limit. completed reports
// that the amount was reached (never for slot_end), as opposed to the visit
// being cut by limit. With strict, an episode that would end after limit is
// not aired (it and everything after it is dropped, the overrun policy);
// without it the last episode may run past limit, like the last occurrence of
// a window does. Every entry remembers the block item and cycle it came from,
// which is how a later run rebuilds the cursor.
func emitSeries(
	run *seriesRun,
	blockItemID string,
	visit seriesVisit,
	cursor, limit time.Time,
	strict bool,
	kind domain.EpgEntryKind,
	tenantID, channelID string,
	generatedAt int64,
	sourceScheduleItemID *string,
) (entries []domain.EpgEntry, end time.Time, completed bool) {
	aired := visit.Done
	prevEpisode := visit.PrevEpisode
	reached := func() bool {
		switch visit.Amount.Mode {
		case domain.AmountCount:
			if aired >= visit.Amount.Count {
				return true
			}
			// An interleaved walk set to drop treats the end of the playback
			// cycle as the end of the list: a visit stops there instead of
			// rolling into the next cycle.
			return run.il != nil && run.il.OnExhaustion == domain.OnExhaustionDrop && aired > 0 && run.cycleDone()
		case domain.AmountSeriesEnd:
			if run.order == domain.OrderRandom {
				return aired >= len(run.episodes)
			}
			return aired > 0 && run.cycleDone()
		}
		return false
	}
	if reached() {
		return nil, cursor, true
	}
	for run.emitted < maxSeriesEntries && run.skipped < maxSeriesSkips && cursor.Before(limit) {
		startMs := cursor.UnixMilli()
		slot, ok := run.plan(startMs)
		if !ok {
			break
		}
		if visit.Unit != nil {
			if slot.Skip {
				run.skipped++
				cursor = cursor.Add(time.Duration(slot.Episode.DurationMs) * time.Millisecond)
				continue
			}
			rotBefore := 0
			if visit.RotIdx != nil {
				rotBefore = *visit.RotIdx
			}
			var planned seriesSlot
			var plannedStartMs int64
			unitEntries, unitEnd, progIdx, unitOK := visit.Unit.unit(cursor, limit, rotBefore, func(start time.Time) (domain.EpgEntry, bool) {
				sl, planOK := run.plan(start.UnixMilli())
				if !planOK || sl.Skip {
					return domain.EpgEntry{}, false
				}
				planned, plannedStartMs = sl, start.UnixMilli()
				cycle := sl.Cycle
				var round *int
				if run.il != nil {
					rd := sl.Round
					round = &rd
				}
				itemID := blockItemID
				return domain.EpgEntry{
					ID:                   uuid.NewString(),
					TenantID:             tenantID,
					ChannelID:            channelID,
					Kind:                 kind,
					CatalogItemID:        sl.Episode.ID,
					StartAt:              plannedStartMs,
					EndAt:                plannedStartMs + sl.Episode.DurationMs,
					SourceScheduleItemID: sourceScheduleItemID,
					GeneratedAt:          generatedAt,
					SourceBlockItemID:    &itemID,
					SeriesCycle:          &cycle,
					SeriesRound:          round,
				}, true
			})
			if !unitOK {
				break
			}
			run.commit(plannedStartMs, planned)
			aired++
			completed = reached()
			if visit.RotIdx != nil {
				r := *visit.RotIdx
				if completed && visit.RotNext != nil {
					r = *visit.RotNext
				}
				setRotation(unitEntries, progIdx, r)
			}
			entries = append(entries, unitEntries...)
			cursor = unitEnd
			if completed {
				break
			}
			continue
		}
		// A midroll break between two episodes, exactly like the one a manual
		// block puts between two catalog items: it must fit before limit when
		// strict (a break that fits followed by an episode that does not
		// leaves the break last, as it does for catalog items). Breaks carry
		// no block item id, so they are never mistaken for episodes when a
		// later run rebuilds the cursor.
		if prevEpisode && !slot.Skip {
			if parts := visit.Mid.pickBreak(); len(parts) > 0 {
				midEnd := cursor.Add(time.Duration(parts.total()) * time.Millisecond)
				if strict && midEnd.After(limit) {
					break
				}
				at := cursor
				for _, part := range parts {
					partEnd := at.Add(time.Duration(part.dur) * time.Millisecond)
					mid := domain.EpgEntry{
						ID:                   uuid.NewString(),
						TenantID:             tenantID,
						ChannelID:            channelID,
						Kind:                 domain.EpgMidRoll,
						CatalogItemID:        part.item.CatalogItemID,
						StartAt:              at.UnixMilli(),
						EndAt:                partEnd.UnixMilli(),
						SourceScheduleItemID: sourceScheduleItemID,
						GeneratedAt:          generatedAt,
					}
					if visit.RotIdx != nil {
						r := *visit.RotIdx
						mid.RotationNext = &r
					}
					entries = append(entries, mid)
					at = partEnd
				}
				cursor = midEnd
				startMs = cursor.UnixMilli()
				if slot, ok = run.plan(startMs); !ok {
					break
				}
			}
		}
		epEnd := cursor.Add(time.Duration(slot.Episode.DurationMs) * time.Millisecond)
		if slot.Skip {
			if strict && epEnd.After(limit) {
				break
			}
			run.skipped++
			cursor = epEnd
			continue
		}
		cycle := slot.Cycle
		var round *int
		if run.il != nil {
			rd := slot.Round
			round = &rd
		}
		itemID := blockItemID
		e := domain.EpgEntry{
			ID:                   uuid.NewString(),
			TenantID:             tenantID,
			ChannelID:            channelID,
			Kind:                 kind,
			CatalogItemID:        slot.Episode.ID,
			StartAt:              startMs,
			EndAt:                epEnd.UnixMilli(),
			SourceScheduleItemID: sourceScheduleItemID,
			GeneratedAt:          generatedAt,
			SourceBlockItemID:    &itemID,
			SeriesCycle:          &cycle,
			SeriesRound:          round,
		}
		if visit.RotIdx != nil {
			r := *visit.RotIdx
			e.RotationNext = &r
		}
		// An episode cut into segments (cuts.go) takes the breaks' time too.
		epEntries := segmenter{planner: visit.Cuts, mid: visit.Mid, channelID: channelID, blockID: visit.BlockID}.split(e)
		epEnd = spanEnd(epEntries)
		if strict && epEnd.After(limit) {
			break
		}
		run.commit(startMs, slot)
		aired++
		cursor = epEnd
		prevEpisode = true
		completed = reached()
		if completed && visit.RotIdx != nil && visit.RotNext != nil {
			r := *visit.RotNext
			for i := range epEntries {
				epEntries[i].RotationNext = &r
			}
		}
		entries = append(entries, epEntries...)
		if completed {
			break
		}
	}
	if !completed && cursor.Before(limit) && (run.emitted >= maxSeriesEntries || run.skipped >= maxSeriesSkips) && !run.capLogged {
		run.capLogged = true
		logging.Warnf("materializer: series cap hit: block item %s on channel %s stopped after %d entries (%d skipped slots), the rest of the window is not filled by it (cap %d entries / %d skips; items this short are almost certainly bad catalog data)",
			blockItemID, channelID, run.emitted, run.skipped, maxSeriesEntries, maxSeriesSkips)
	}
	return entries, cursor, completed
}

// maxRepeatEntries bounds the entries one repeating block occurrence emits, as
// maxSeriesEntries does for a series item: a list of near-zero-length items
// would otherwise fill a whole window with millions of rows.
const maxRepeatEntries = maxSeriesEntries

// fillLoopingBlock inserts, starting at cursor, entries drawn from a
// continuous, wrapping walk over items (a block's block_item list) until
// target — same "whole items only, never cut short" contract as fillGap, but
// keyed by cursorKey in state instead of a filler_rule's ID, and always
// sequential: an endless block has no selection_strategy of its own, it just
// rotates through everything it holds, in order, forever. This is what makes a
// "genre channel" real programming (kind=program) instead of filler: it never
// really "ends" until the next schedule_item occurrence (or the end of the
// materialized window).
//
// Every program (a catalog item, or an episode of a series/criteria item) is a
// unit with the filler rules' preroll before and postroll after it, and the
// midroll goes between two consecutive programs (see breakLoop); a unit is aired
// only whole. What is left when the next unit no longer fits is for the caller's
// tail and fallback rules. br carries the rules and, for a span resuming after
// a kept head, the stage the previous unit stopped at.
//
// cursorKey must be scoped by channelID, not just blockID (issue #7's
// point 2): a Schedule (and any dynamic Block it schedules) can be assigned
// to more than one channel, and each channel's loop position must be
// independent — two channels airing the same dynamic block must not silently
// share (and race on) one rotation cursor within a single Run().
//
// With once (a continuous item over a manual block set to play once) the
// rotation does not wrap: the position is the unwrapped index into items, the
// walk stops when it reaches len(items), and that value is what the entries
// persist, so a later run still knows the list has been played. A walk that
// goes around the list without airing anything (empty list, empty series)
// stops instead of spinning.
func fillLoopingBlock(
	items []domain.BlockItem,
	runs map[string]*seriesRun,
	catalogDuration map[string]int64,
	cursor, target time.Time,
	kind domain.EpgEntryKind,
	tenantID, channelID string,
	generatedAt int64,
	sourceScheduleItemID *string,
	cursorKey string,
	resumeDone int,
	br *breakLoop,
	once bool,
	state *selectionState,
) ([]domain.EpgEntry, time.Time) {
	if len(items) == 0 {
		return nil, cursor
	}
	var entries []domain.EpgEntry
	// A kept head can end between a program and its postroll.
	if post, end, ok := br.flushPost(cursor, target, state.seqCursor[cursorKey]); !ok {
		return nil, cursor
	} else {
		entries = append(entries, post...)
		cursor = end
	}
	emptySeries := 0
	// idle counts consecutive iterations that aired nothing and did not move
	// the cursor; a whole lap of them is a zero-progress cycle.
	idle := 0
	lastLen, lastCursor := 0, cursor
	for len(entries) < maxLoopEntries {
		if len(entries) == lastLen && cursor.Equal(lastCursor) {
			idle++
			if idle > 2*len(items) {
				break
			}
		} else {
			idle = 0
		}
		lastLen, lastCursor = len(entries), cursor
		idx := state.seqCursor[cursorKey]
		next := idx + 1
		if once {
			if idx >= len(items) {
				break
			}
		} else {
			idx %= len(items)
			next = (idx + 1) % len(items)
		}
		bi := items[idx]
		if bi.IsWalked() {
			// A "slot_end" series stays on the air until target: the rotation
			// sits on it (every entry stores idx), so the next gap resumes
			// it. A "count"/"series_end" visit airs its amount and then the
			// rotation moves to the next item (the entry completing the
			// visit stores idx+1, unwrapped so a one-item block can tell a
			// finished visit from a running one); a visit cut by target
			// stays on the item. A series with no episodes is skipped; if
			// every item is one, stop.
			run := runs[bi.ID]
			if run == nil || run.empty() {
				emptySeries++
				if emptySeries >= len(items) {
					break
				}
				state.seqCursor[cursorKey] = idx + 1
				continue
			}
			emptySeries = 0
			run.catchUp(cursor.UnixMilli())
			rot, nxt := idx, idx+1
			visit := seriesVisit{Amount: run.amount, Done: resumeDone, RotIdx: &rot, RotNext: &nxt, Unit: br}
			resumeDone = 0
			seriesEntries, end, completed := emitSeries(run, bi.ID, visit, cursor, target, true, kind, tenantID, channelID, generatedAt, sourceScheduleItemID)
			entries = append(entries, seriesEntries...)
			cursor = end
			if !completed {
				break
			}
			state.seqCursor[cursorKey] = idx + 1
			continue
		}
		emptySeries = 0
		durMs := effectiveDuration(bi, catalogDuration)
		if durMs <= 0 {
			break
		}
		unitEntries, end, progIdx, ok := br.unit(cursor, target, idx, func(start time.Time) (domain.EpgEntry, bool) {
			return domain.EpgEntry{
				ID:                   uuid.NewString(),
				TenantID:             tenantID,
				ChannelID:            channelID,
				Kind:                 kind,
				CatalogItemID:        bi.CatalogItemID,
				StartAt:              start.UnixMilli(),
				EndAt:                start.Add(time.Duration(durMs) * time.Millisecond).UnixMilli(),
				SourceScheduleItemID: sourceScheduleItemID,
				GeneratedAt:          generatedAt,
			}, true
		})
		if !ok {
			break
		}
		setRotation(unitEntries, progIdx, next)
		entries = append(entries, unitEntries...)
		cursor = end
		state.seqCursor[cursorKey] = idx + 1
	}
	if len(entries) >= maxLoopEntries {
		warnLoopCap(channelID, sourceScheduleItemID)
	}
	return entries, cursor
}

// expandChannel is the core of the materializer: schedule_item occurrences
// plus block/filler data in, a flat, gap-free (where filler rules allow),
// non-overlapping list of epg_entry rows out. It never touches storage.DB —
// all inputs are pre-fetched by the orchestrator — which is what makes it
// straightforward to unit test.
//
// now/scanFrom/windowEnd are all UTC. Entries whose EndAt <= now can appear
// in the result (an occurrence starting within the lookback window but
// ending before now); the caller (materializer.go) is responsible for
// discarding those before writing to epg_entry.
func expandChannel(in channelInput, now, scanFrom, windowEnd time.Time, state *selectionState) []domain.EpgEntry {
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		logging.Warnf("materializer: channel %s: invalid timezone %q, falling back to UTC: %v", in.ChannelID, in.Timezone, err)
		loc = time.UTC
	}

	occurrences := buildOccurrences(in.ScheduleItems, loc, scanFrom, windowEnd)
	generatedAt := now.UnixMilli()

	// The continuous item (at most one per schedule, validated by the API)
	// is the base layer: it airs in every gap the timed items leave, in
	// place of the channel's fallback filler.
	var continuous *domain.ScheduleItem
	for i := range in.ScheduleItems {
		if in.ScheduleItems[i].Recurrence == domain.RecurrenceContinuous {
			continuous = &in.ScheduleItems[i]
			break
		}
	}
	continuousKey := ""
	// Never generate the continuous item in the past or inside the kept head.
	continuousFloor := now
	if in.KeptUntil.After(continuousFloor) {
		continuousFloor = in.KeptUntil
	}
	if continuous != nil {
		continuousKey = in.ChannelID + "|continuous|" + continuous.ID
		state.seqCursor[continuousKey] = in.ContinuousStart
	}

	var entries []domain.EpgEntry
	cursor := scanFrom

	// playOnce reports a manual block that airs its list once (list_mode
	// "once"); dynamic blocks and repeating manual blocks loop.
	playOnce := func(blockID string) bool {
		return !in.DynamicBlockIDs[blockID] && !in.RepeatBlockIDs[blockID]
	}

	// noteResidual records what a slot is left with at a real boundary when no
	// fallback rule will cover it (see residual); seams under a minute are not
	// worth a warning.
	noteResidual := func(blockID string, from, to time.Time) {
		if in.Residuals == nil || to.Sub(from) < unfilledSlotMin || !to.After(now) {
			return
		}
		if resolveFillerRule(in.FillerRules, domain.FillerFallback, "", in.ChannelID) != nil {
			return
		}
		*in.Residuals = append(*in.Residuals, residual{BlockID: blockID, Length: to.Sub(from)})
	}

	fillFallbackOnly := func(from, to time.Time) {
		rule := resolveFillerRule(in.FillerRules, domain.FillerFallback, "", in.ChannelID)
		gapEntries, _ := fillGap(rule, in.PoolItems, in.CatalogDuration, from, to, domain.EpgFallback, in.TenantID, in.ChannelID, generatedAt, nil, state)
		entries = append(entries, gapEntries...)
	}

	// fillFallbackGap fills a span no timed item claims: with the continuous
	// item's rotation when the schedule has one (whole programs only, so a
	// residual shorter than the next program is handed to the fallback rule),
	// otherwise with the channel-scoped fallback rule alone.
	//
	// boundary says the span ends at a real next timed item, as opposed to the
	// end of the window.
	fillFallbackGap := func(from, to time.Time, boundary bool) {
		if continuous == nil {
			fillFallbackOnly(from, to)
			return
		}
		start := from
		if start.Before(continuousFloor) {
			start = continuousFloor
		}
		if !start.Before(to) {
			return
		}
		// A visit the kept head left midway resumes only if the new span
		// starts exactly where the head ends.
		resume := 0
		if !in.KeptUntil.IsZero() && start.Equal(in.KeptUntil) {
			resume = in.ContinuousVisitDone
		}
		stage := stageFresh
		if start.Equal(in.KeptUntil) {
			stage = in.ContinuousStage
		}
		pre, mid, post := in.breakSources(continuous.BlockID, state)
		br := newBreakLoop(pre, mid, post, stage, in.TenantID, in.ChannelID, generatedAt, &continuous.ID)
		br.cuts, br.blockID = in.CutPlanner, continuous.BlockID
		loopEntries, end := fillLoopingBlock(in.BlockItems[continuous.BlockID], in.Series, in.CatalogDuration, start, to, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &continuous.ID, continuousKey, resume, br, playOnce(continuous.BlockID), state)
		entries = append(entries, loopEntries...)
		// Whole programs no longer fit: the tail covers the leftover
		// before the fallback does.
		if end.Before(to) {
			tailRule := resolveFillerRule(in.FillerRules, domain.FillerTail, continuous.BlockID, in.ChannelID)
			tailEntries, tailEnd := fillGap(tailRule, in.PoolItems, in.CatalogDuration, end, to, domain.EpgTail, in.TenantID, in.ChannelID, generatedAt, &continuous.ID, state)
			entries = append(entries, tailEntries...)
			end = tailEnd
		}
		if end.Before(to) {
			if boundary {
				noteResidual(continuous.BlockID, end, to)
			}
			fillFallbackOnly(end, to)
		}
	}

	for i, occ := range occurrences {
		actualStart := occ.NominalStart
		if cursor.After(actualStart) {
			actualStart = cursor
		}
		if actualStart.After(cursor) {
			// A gap with nothing scheduled: [cursor, actualStart). Only a
			// channel-scoped fallback rule fills this (Passo B.2) — no
			// schedule_item owns it, so SourceScheduleItemID stays nil.
			fillFallbackGap(cursor, actualStart, true)
		}
		cursor = actualStart

		scheduleItemID := occ.ScheduleItemID
		items := in.BlockItems[occ.BlockID]
		preroll := in.breakSource(domain.FillerPreRoll, occ.BlockID, state)
		midSrc := in.breakSource(domain.FillerMidRoll, occ.BlockID, state)
		postroll := in.breakSource(domain.FillerPostRoll, occ.BlockID, state)
		tailRule := resolveFillerRule(in.FillerRules, domain.FillerTail, occ.BlockID, in.ChannelID)

		// nominalNextStart is the hard boundary this occurrence must never
		// cross (issue #10: a schedule_item's nominal start is always
		// honored, never delayed by a previous occurrence running long) —
		// but only when there IS a next occurrence. windowEnd is just how
		// far this particular materializer Run() happened to compute ahead
		// to, not a real commitment the schedule made; the last occurrence
		// in a window is free to run past it exactly as before (the next
		// Run() picks up the continuation), see hasNextOccurrence below.
		hasNextOccurrence := i+1 < len(occurrences)
		nominalNextStart := windowEnd
		if hasNextOccurrence {
			nominalNextStart = occurrences[i+1].NominalStart
		}

		// emit appends one entry starting at cursor and advances cursor past
		// it. Every insertion in this occurrence (preroll/program/midroll/
		// postroll) goes through this so cursor is always the single source
		// of truth for "what time is it now".
		emit := func(catalogItemID string, dur int64, kind domain.EpgEntryKind) {
			end := cursor.Add(time.Duration(dur) * time.Millisecond)
			entries = append(entries, domain.EpgEntry{
				ID:                   uuid.NewString(),
				TenantID:             in.TenantID,
				ChannelID:            in.ChannelID,
				Kind:                 kind,
				CatalogItemID:        catalogItemID,
				StartAt:              cursor.UnixMilli(),
				EndAt:                end.UnixMilli(),
				SourceScheduleItemID: &scheduleItemID,
				GeneratedAt:          generatedAt,
			})
			cursor = end
		}

		// emitBreak airs every part of a break back to back: one entry for a plain
		// rule, the steps' items for a break template.
		emitBreak := func(parts breakParts, kind domain.EpgEntryKind) {
			for _, p := range parts {
				emit(p.item.CatalogItemID, p.dur, kind)
			}
		}
		insertFiller := func(src *midrollSource, kind domain.EpgEntryKind) {
			emitBreak(src.pickBreak(), kind)
		}

		if in.DynamicBlockIDs[occ.BlockID] {
			// A dynamic block has no fixed end — it loops its (dynamically
			// resolved) item list to fill every instant up to nominalNextStart,
			// tagged as real programming. It already never crosses that
			// boundary by construction, so it needs no overrun handling of
			// its own. Like a continuous item, every program gets the block's
			// (or the channel's) preroll before and postroll after it, with the
			// midroll between two programs (breakLoop); the tail covers what is
			// left when whole programs no longer fit.
			// cursorKey includes ChannelID (see fillLoopingBlock's doc comment)
			// so two channels sharing this dynamic block via the same Schedule
			// don't share loop position.
			cursorKey := in.ChannelID + "|" + occ.BlockID
			pre, mid, post := in.breakSources(occ.BlockID, state)
			br := newBreakLoop(pre, mid, post, stageFresh, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID)
			br.cuts, br.blockID = in.CutPlanner, occ.BlockID
			loopEntries, newCursor := fillLoopingBlock(items, in.Series, in.CatalogDuration, cursor, nominalNextStart, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID, cursorKey, 0, br, false, state)
			entries = append(entries, loopEntries...)
			cursor = newCursor
			if cursor.Before(nominalNextStart) && tailRule != nil {
				tailEntries, tailEnd := fillGap(tailRule, in.PoolItems, in.CatalogDuration, cursor, nominalNextStart, domain.EpgTail, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID, state)
				entries = append(entries, tailEntries...)
				cursor = tailEnd
			}
			if hasNextOccurrence {
				noteResidual(occ.BlockID, cursor, nominalNextStart)
			}
			continue
		}

		insertFiller(preroll, domain.EpgPreRoll)

		// Overrun policy (issue #10): "stop before end", but only when
		// hasNextOccurrence — a manual block's own nominal start is never
		// delayed by the previous one running long, so its content must
		// stop in time to let nominalNextStart happen on schedule. postroll
		// counts toward that budget (it's the block's sign-off, always
		// played in full) and is reserved for up front, which is why it's
		// picked here rather than after the items loop like preroll/midroll
		// are: program items (and any midroll between them) that don't fit
		// before nominalNextStart once postroll's own time is set aside are
		// simply dropped, in order, starting from the first one that
		// doesn't fit — down to zero items if even the first one alone
		// doesn't fit. This can end a block earlier than its full item list
		// would otherwise call for; there's no client-side support for
		// trimming a single item mid-playback, so "drop whole trailing
		// items" is the only available lever (see the issue's "Open
		// questions"). Without a next occurrence, there's nothing to
		// protect: items and postroll play in full, exactly as before this
		// issue.
		var postrollParts breakParts
		itemsCutoff := nominalNextStart
		if hasNextOccurrence {
			postrollParts = postroll.pickBreak()
			if len(postrollParts) > 0 {
				itemsCutoff = nominalNextStart.Add(-time.Duration(postrollParts.total()) * time.Millisecond)
			}
		}
		repeat := !playOnce(occ.BlockID)
		fits := func(dur int64) bool {
			if !hasNextOccurrence {
				return true
			}
			return !cursor.Add(time.Duration(dur) * time.Millisecond).After(itemsCutoff)
		}

		// midBreak is the midroll between item idx and the next one of the
		// block: the same step for a catalog item and for a series visit's
		// last episode. It reports false when the block must stop (the
		// break does not fit before the boundary).
		//
		// In a repeating block the list wraps, so the last item is also
		// followed by a break (before the first item of the next lap) as
		// long as the boundary is not reached yet.
		midBreak := func(idx int) bool {
			if repeat && !cursor.Before(itemsCutoff) {
				return false
			}
			if idx == len(items)-1 && !repeat {
				return true
			}
			parts := midSrc.pickBreak()
			if len(parts) == 0 {
				return true
			}
			if !fits(parts.total()) {
				return false
			}
			emitBreak(parts, domain.EpgMidRoll)
			return true
		}

		// A manual block set to repeat starts its list over whenever it ends,
		// until the boundary (itemsCutoff: the next occurrence, reserving
		// postroll, or the window's end). Every lap starts from the first
		// item, so a regenerated occurrence is deterministic and needs no
		// persisted position (series cursors persist on their own). A lap
		// that airs nothing (empty list, empty series, no positive duration)
		// ends the block instead of spinning, and maxRepeatEntries bounds a
		// list of very short items. A block that plays once makes one lap.
		listStart := len(entries)
	listLoop:
		for {
			lapStart := cursor
			for idx, bi := range items {
				if repeat && (!cursor.Before(itemsCutoff) || len(entries)-listStart >= maxRepeatEntries) {
					break listLoop
				}
				if bi.IsWalked() {
					// A series item airs its amount per visit. "slot_end" airs
					// until the boundary (the next occurrence, reserving
					// postroll, or the window's end), so nothing after it in the
					// block gets on the air; "count" and "series_end" visits end
					// by themselves and the block moves on to its next item. A
					// visit cut by the boundary ends the block's airing. Midroll
					// breaks go between the visit's episodes (emitSeries) and
					// between the visit and the next item (midBreak), as they do
					// between catalog items.
					run := in.Series[bi.ID]
					if run == nil || run.empty() {
						continue
					}
					seriesEntries, end, completed := emitSeries(run, bi.ID, seriesVisit{Amount: run.amount, Mid: midSrc, Cuts: in.CutPlanner, BlockID: occ.BlockID}, cursor, itemsCutoff, hasNextOccurrence, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID)
					entries = append(entries, seriesEntries...)
					cursor = end
					if !completed {
						break listLoop
					}
					if len(seriesEntries) > 0 && !midBreak(idx) {
						break listLoop
					}
					continue
				}
				dur := effectiveDuration(bi, in.CatalogDuration)
				if repeat && dur <= 0 {
					continue
				}
				if !fits(dur) {
					break listLoop
				}
				// An item cut into segments (cuts.go) takes the breaks' time
				// too, so the whole span has to fit.
				progEntries := segmenter{planner: in.CutPlanner, mid: midSrc, channelID: in.ChannelID, blockID: occ.BlockID}.split(domain.EpgEntry{
					ID:                   uuid.NewString(),
					TenantID:             in.TenantID,
					ChannelID:            in.ChannelID,
					Kind:                 domain.EpgProgram,
					CatalogItemID:        bi.CatalogItemID,
					StartAt:              cursor.UnixMilli(),
					EndAt:                cursor.Add(time.Duration(dur) * time.Millisecond).UnixMilli(),
					SourceScheduleItemID: &scheduleItemID,
					GeneratedAt:          generatedAt,
				})
				if !fits(spanEnd(progEntries).Sub(cursor).Milliseconds()) {
					break listLoop
				}
				entries = append(entries, progEntries...)
				cursor = spanEnd(progEntries)
				if !midBreak(idx) {
					break listLoop
				}
			}
			if !repeat || !cursor.After(lapStart) {
				break
			}
		}
		if repeat && len(entries)-listStart >= maxRepeatEntries {
			logging.Warnf("materializer: repeat cap hit: block %s on channel %s stopped after %d entries in one occurrence (cap %d; items this short are almost certainly bad catalog data)", occ.BlockID, in.ChannelID, len(entries)-listStart, maxRepeatEntries)
		}

		if hasNextOccurrence {
			emitBreak(postrollParts, domain.EpgPostRoll)
		} else if !repeat || cursor.Before(windowEnd) {
			// No cutoff was reserved for this one, so pick it fresh — same
			// as preroll/midroll above. A repeating block that ran up to the
			// window's end has not ended, so it has no sign-off yet.
			insertFiller(postroll, domain.EpgPostRoll)
		}

		postActualEnd := cursor

		if postActualEnd.Before(nominalNextStart) && tailRule != nil {
			tailEntries, tailEnd := fillGap(tailRule, in.PoolItems, in.CatalogDuration, postActualEnd, nominalNextStart, domain.EpgTail, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID, state)
			entries = append(entries, tailEntries...)
			cursor = tailEnd
		} else {
			cursor = postActualEnd
		}
		if hasNextOccurrence {
			noteResidual(occ.BlockID, cursor, nominalNextStart)
		}
	}

	if cursor.Before(windowEnd) {
		fillFallbackGap(cursor, windowEnd, false)
	}

	if !in.KeptUntil.IsZero() {
		keptMs := in.KeptUntil.UnixMilli()
		kept := entries[:0]
		for _, e := range entries {
			if e.StartAt >= keptMs {
				kept = append(kept, e)
			}
		}
		entries = kept
	}

	return entries
}
