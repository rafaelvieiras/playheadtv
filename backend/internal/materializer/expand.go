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
	ChannelID       string
	TenantID        string
	Timezone        string // IANA name; see domain.Channel.Timezone
	ScheduleItems   []domain.ScheduleItem
	BlockItems      map[string][]domain.BlockItem      // blockID -> ordered by position
	FillerRules     []domain.FillerRule                // every rule scoped to this channel or one of its blocks
	PoolItems       map[string][]domain.FillerPoolItem // filler_pool_id -> ordered by rowid (insertion order)
	CatalogDuration map[string]int64                   // catalog_item_id -> duration_ms
	DynamicBlockIDs map[string]bool                    // blockID -> true iff block.criteria is non-NULL
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
	catalogDuration map[string]int64,
	cursor, target time.Time,
	kind domain.EpgEntryKind,
	tenantID, channelID string,
	generatedAt int64,
	sourceScheduleItemID *string,
	cursorKey string,
	state *selectionState,
) ([]domain.EpgEntry, time.Time) {
	if len(items) == 0 {
		return nil, cursor
	}
	var entries []domain.EpgEntry
	for {
		idx := state.seqCursor[cursorKey] % len(items)
		bi := items[idx]
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

	var entries []domain.EpgEntry
	cursor := scanFrom

	fillFallbackGap := func(from, to time.Time) {
		rule := resolveFillerRule(in.FillerRules, domain.FillerFallback, "", in.ChannelID)
		gapEntries, _ := fillGap(rule, in.PoolItems, in.CatalogDuration, from, to, domain.EpgFallback, in.TenantID, in.ChannelID, generatedAt, nil, state)
		entries = append(entries, gapEntries...)
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

		insertFiller := func(rule *domain.FillerRule, kind domain.EpgEntryKind) {
			if rule == nil {
				return
			}
			item, ok := selectFillerItem(*rule, in.PoolItems[rule.FillerPoolID], state)
			if !ok {
				return
			}
			dur := in.CatalogDuration[item.CatalogItemID]
			if dur <= 0 {
				return
			}
			end := cursor.Add(time.Duration(dur) * time.Millisecond)
			entries = append(entries, domain.EpgEntry{
				ID:                   uuid.NewString(),
				TenantID:             in.TenantID,
				ChannelID:            in.ChannelID,
				Kind:                 kind,
				CatalogItemID:        item.CatalogItemID,
				StartAt:              cursor.UnixMilli(),
				EndAt:                end.UnixMilli(),
				SourceScheduleItemID: &scheduleItemID,
				GeneratedAt:          generatedAt,
			})
			cursor = end
		}

		insertFiller(preroll, domain.EpgPreRoll)

		if in.DynamicBlockIDs[occ.BlockID] {
			// A dynamic block has no fixed end — it loops its (dynamically
			// resolved) item list to fill every instant up to the next
			// occurrence's nominal start (or windowEnd for the last one),
			// tagged as real programming. midroll/postroll/tail don't apply to
			// an endless loop (there's no single "end" to hang them off); only
			// preroll (already inserted above) still fires once, at the top of
			// this occurrence.
			target := windowEnd
			if i+1 < len(occurrences) {
				target = occurrences[i+1].NominalStart
			}
			// cursorKey includes ChannelID (see fillLoopingBlock's doc comment)
			// so two channels sharing this dynamic block via the same Schedule
			// don't share loop position.
			cursorKey := in.ChannelID + "|" + occ.BlockID
			loopEntries, newCursor := fillLoopingBlock(items, in.CatalogDuration, cursor, target, domain.EpgProgram, in.TenantID, in.ChannelID, generatedAt, &scheduleItemID, cursorKey, state)
			entries = append(entries, loopEntries...)
			cursor = newCursor
			continue
		}

		for idx, bi := range items {
			dur := effectiveDuration(bi, in.CatalogDuration)
			end := cursor.Add(time.Duration(dur) * time.Millisecond)
			entries = append(entries, domain.EpgEntry{
				ID:                   uuid.NewString(),
				TenantID:             in.TenantID,
				ChannelID:            in.ChannelID,
				Kind:                 domain.EpgProgram,
				CatalogItemID:        bi.CatalogItemID,
				StartAt:              cursor.UnixMilli(),
				EndAt:                end.UnixMilli(),
				SourceScheduleItemID: &scheduleItemID,
				GeneratedAt:          generatedAt,
			})
			cursor = end
			if idx != len(items)-1 {
				insertFiller(midroll, domain.EpgMidRoll)
			}
		}

		insertFiller(postroll, domain.EpgPostRoll)

		postActualEnd := cursor
		nominalNextStart := windowEnd
		if i+1 < len(occurrences) {
			nominalNextStart = occurrences[i+1].NominalStart
		}

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

	return entries
}
