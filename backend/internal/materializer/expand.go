package materializer

import (
	"log"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
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
	PoolItems       map[string][]domain.FillerPoolItem // filler_pool_id -> ordered by rowid (insertion order)
	CatalogDuration map[string]int64                   // catalog_item_id -> duration_ms
	DynamicBlockIDs map[string]bool                    // blockID -> true iff block.criteria is non-NULL
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
	// KeptUntil is non-zero when the channel's already-materialized head is
	// being kept (grid stability): it is the end of the last kept entry.
	// Nothing is generated for the continuous item before it, and any
	// generated entry starting before it is dropped, since the kept rows
	// already cover that span.
	KeptUntil time.Time
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
// windowEnd), at which a RecurrenceWeekly schedule_item (dayOfWeek 0=Sunday,
// startTimeLocal "HH:MM") nominally starts, per loc (channel.timezone).
//
// It walks every calendar day (in loc) touching the window, padded by one day
// on each side to stay correct even for large UTC offsets where a local
// calendar day can straddle the UTC window boundary; the final UTC-window
// check filters out anything the padding over-generates.
func weeklyOccurrenceTimes(dayOfWeek int, startTimeLocal string, loc *time.Location, scanFrom, windowEnd time.Time) ([]time.Time, error) {
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
		if int(dayCursor.Weekday()) == dayOfWeek {
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
			if e.DayOfWeek == nil || e.StartTimeLocal == nil {
				continue
			}
			times, err := weeklyOccurrenceTimes(*e.DayOfWeek, *e.StartTimeLocal, loc, scanFrom, windowEnd)
			if err != nil {
				// The API layer already validates start_time_local as HH:MM;
				// this only defends against bad data written some other way.
				log.Printf("materializer: schedule_item %s: invalid start_time_local %q: %v", e.ID, *e.StartTimeLocal, err)
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

// fillGap inserts, starting at cursor, as many whole items selected from
// rule's pool as fit before target, advancing the cursor after each one. It
// never cuts an item short: the residual space smaller than the next
// candidate item is left without an epg_entry (a known v1 limitation). It is
// a no-op (returns cursor unchanged) when rule is nil or its pool is empty.
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

	var entries []domain.EpgEntry
	for {
		item, ok := selectFillerItem(*rule, pool, state)
		if !ok {
			break
		}
		durMs := catalogDuration[item.CatalogItemID]
		if durMs <= 0 {
			// Guards against bad/missing catalog data looping forever.
			break
		}
		end := cursor.Add(time.Duration(durMs) * time.Millisecond)
		if end.After(target) {
			break
		}
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
		epEnd := cursor.Add(time.Duration(slot.Episode.DurationMs) * time.Millisecond)
		if strict && epEnd.After(limit) {
			break
		}
		if slot.Skip {
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
		run.commit(startMs, slot)
		aired++
		cursor = epEnd
		completed = reached()
		if visit.RotIdx != nil {
			r := *visit.RotIdx
			if completed && visit.RotNext != nil {
				r = *visit.RotNext
			}
			e.RotationNext = &r
		}
		entries = append(entries, e)
		if completed {
			break
		}
	}
	if !completed && cursor.Before(limit) && (run.emitted >= maxSeriesEntries || run.skipped >= maxSeriesSkips) && !run.capLogged {
		run.capLogged = true
		log.Printf("materializer: series cap hit: block item %s on channel %s stopped after %d entries (%d skipped slots), the rest of the window is not filled by it (cap %d entries / %d skips; items this short are almost certainly bad catalog data)",
			blockItemID, channelID, run.emitted, run.skipped, maxSeriesEntries, maxSeriesSkips)
	}
	return entries, cursor, completed
}

// fillLoopingBlock inserts, starting at cursor, entries drawn from a
// continuous, wrapping walk over items (a dynamic block's block_item list)
// until target — same "whole items only, never cut short" contract as
// fillGap, but keyed by cursorKey in state instead of a filler_rule's ID, and
// always sequential: a dynamic block has no selection_strategy of its own, it
// just rotates through everything currently matching its criteria, in order,
// forever. This is what makes a "genre channel" real programming
// (kind=program) instead of filler: a dynamic block occurrence never really
// "ends" until the next schedule_item occurrence (or the end of the
// materialized window).
//
// cursorKey must be scoped by channelID, not just blockID (issue #7's
// point 2): a Schedule (and any dynamic Block it schedules) can be assigned
// to more than one channel, and each channel's loop position must be
// independent — two channels airing the same dynamic block must not silently
// share (and race on) one rotation cursor within a single Run().
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
	state *selectionState,
) ([]domain.EpgEntry, time.Time) {
	if len(items) == 0 {
		return nil, cursor
	}
	var entries []domain.EpgEntry
	emptySeries := 0
	for {
		idx := state.seqCursor[cursorKey] % len(items)
		next := (idx + 1) % len(items)
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
			visit := seriesVisit{Amount: run.amount, Done: resumeDone, RotIdx: &rot, RotNext: &nxt}
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
		end := cursor.Add(time.Duration(durMs) * time.Millisecond)
		if end.After(target) {
			break
		}
		entries = append(entries, domain.EpgEntry{
			ID:                   uuid.NewString(),
			TenantID:             tenantID,
			ChannelID:            channelID,
			Kind:                 kind,
			CatalogItemID:        bi.CatalogItemID,
			StartAt:              cursor.UnixMilli(),
			EndAt:                end.UnixMilli(),
			SourceScheduleItemID: sourceScheduleItemID,
			GeneratedAt:          generatedAt,
			RotationNext:         &next,
		})
		cursor = end
		state.seqCursor[cursorKey] = idx + 1
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
		log.Printf("materializer: channel %s: invalid timezone %q, falling back to UTC: %v", in.ChannelID, in.Timezone, err)
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

	fillFallbackOnly := func(from, to time.Time) {
		rule := resolveFillerRule(in.FillerRules, domain.FillerFallback, "", in.ChannelID)
		gapEntries, _ := fillGap(rule, in.PoolItems, in.CatalogDuration, from, to, domain.EpgFallback, in.TenantID, in.ChannelID, generatedAt, nil, state)
		entries = append(entries, gapEntries...)
	}

	// fillFallbackGap fills a span no timed item claims: with the continuous
	// item's rotation when the schedule has one (whole programs only, so a
	// residual shorter than the next program is handed to the fallback rule),
	// otherwise with the channel-scoped fallback rule alone.
	fillFallbackGap := func(from, to time.Time) {
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
		loopEntries, end := fillLoopingBlock(in.BlockItems[continuous.BlockID], in.Series, in.CatalogDuration, start, to, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &continuous.ID, continuousKey, resume, state)
		entries = append(entries, loopEntries...)
		if end.Before(to) {
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
			fillFallbackGap(cursor, actualStart)
		}
		cursor = actualStart

		scheduleItemID := occ.ScheduleItemID
		items := in.BlockItems[occ.BlockID]
		preroll := resolveFillerRule(in.FillerRules, domain.FillerPreRoll, occ.BlockID, in.ChannelID)
		midroll := resolveFillerRule(in.FillerRules, domain.FillerMidRoll, occ.BlockID, in.ChannelID)
		postroll := resolveFillerRule(in.FillerRules, domain.FillerPostRoll, occ.BlockID, in.ChannelID)
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

		insertFiller := func(rule *domain.FillerRule, kind domain.EpgEntryKind) {
			item, dur, ok := pickFiller(rule, in.PoolItems, in.CatalogDuration, state)
			if !ok {
				return
			}
			emit(item.CatalogItemID, dur, kind)
		}

		insertFiller(preroll, domain.EpgPreRoll)

		if in.DynamicBlockIDs[occ.BlockID] {
			// A dynamic block has no fixed end — it loops its (dynamically
			// resolved) item list to fill every instant up to
			// nominalNextStart, tagged as real programming. It already never
			// crosses that boundary by construction, so it needs no overrun
			// handling of its own. midroll/postroll/tail don't apply to an
			// endless loop (there's no single "end" to hang them off); only
			// preroll (already inserted above) still fires once, at the top
			// of this occurrence.
			// cursorKey includes ChannelID (see fillLoopingBlock's doc comment)
			// so two channels sharing this dynamic block via the same Schedule
			// don't share loop position.
			cursorKey := in.ChannelID + "|" + occ.BlockID
			loopEntries, newCursor := fillLoopingBlock(items, in.Series, in.CatalogDuration, cursor, nominalNextStart, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID, cursorKey, 0, state)
			entries = append(entries, loopEntries...)
			cursor = newCursor
			continue
		}

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
		var postrollItem domain.FillerPoolItem
		var postrollDur int64
		var hasPostroll bool
		itemsCutoff := nominalNextStart
		if hasNextOccurrence {
			postrollItem, postrollDur, hasPostroll = pickFiller(postroll, in.PoolItems, in.CatalogDuration, state)
			if hasPostroll {
				itemsCutoff = nominalNextStart.Add(-time.Duration(postrollDur) * time.Millisecond)
			}
		}
		fits := func(dur int64) bool {
			if !hasNextOccurrence {
				return true
			}
			return !cursor.Add(time.Duration(dur) * time.Millisecond).After(itemsCutoff)
		}

		for idx, bi := range items {
			if bi.IsWalked() {
				// A series item airs its amount per visit. "slot_end" airs
				// until the boundary (the next occurrence, reserving
				// postroll, or the window's end), so nothing after it in the
				// block gets on the air; "count" and "series_end" visits end
				// by themselves and the block moves on to its next item. A
				// visit cut by the boundary ends the block's airing.
				run := in.Series[bi.ID]
				if run == nil || run.empty() {
					continue
				}
				seriesEntries, end, completed := emitSeries(run, bi.ID, seriesVisit{Amount: run.amount}, cursor, itemsCutoff, hasNextOccurrence, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID)
				entries = append(entries, seriesEntries...)
				cursor = end
				if !completed {
					break
				}
				continue
			}
			dur := effectiveDuration(bi, in.CatalogDuration)
			if !fits(dur) {
				break
			}
			emit(bi.CatalogItemID, dur, domain.EpgProgram)
			if idx == len(items)-1 {
				continue
			}
			midItem, midDur, ok := pickFiller(midroll, in.PoolItems, in.CatalogDuration, state)
			if !ok {
				continue
			}
			if !fits(midDur) {
				break
			}
			emit(midItem.CatalogItemID, midDur, domain.EpgMidRoll)
		}

		if hasNextOccurrence {
			if hasPostroll {
				emit(postrollItem.CatalogItemID, postrollDur, domain.EpgPostRoll)
			}
		} else {
			// No cutoff was reserved for this one, so pick it fresh — same
			// as preroll/midroll above.
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
	}

	if cursor.Before(windowEnd) {
		fillFallbackGap(cursor, windowEnd)
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
