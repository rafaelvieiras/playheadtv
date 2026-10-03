// Package materializer expands a channel's assigned schedule (schedule +
// schedule_item) + block/block_item + filler_rule/filler_pool_item into flat
// epg_entry rows, asynchronously and ahead of "now" — see
// docs/ARCHITECTURE.md "EPG materialization". It never runs on an HTTP
// request path: internal/api only triggers it (Trigger) after a write to a
// scheduling table, and cmd/server/main.go rolls it forward on a timer
// (Start).
//
// This package does not import internal/api: it discovers tenants generically
// from the tenant table rather than assuming api.DefaultTenantID, so it keeps
// working unchanged once real multi-tenancy (the Auth roadmap item) exists.
package materializer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/blockitem"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// scanLookback is how far behind "now" Run scans schedule_item for
// occurrences that might still be on the air (e.g. a long block that started
// yesterday). Fixed rather than configurable — see docs/ROADMAP.md.
const scanLookback = 24 * time.Hour

// DefaultFreezeMargin is how far ahead of "now" a channel driven by a
// continuous schedule item keeps its already-materialized programs untouched:
// a regeneration only recomputes what starts after now + margin, so the guide
// viewers have already seen does not shift under them.
const DefaultFreezeMargin = 3 * time.Hour

// triggerDebounce coalesces a burst of Trigger() calls (e.g. adding several
// block_item rows back to back) into a single regeneration.
const triggerDebounce = 200 * time.Millisecond

// tickInterval is the periodic "rolling timer" that keeps window ahead of
// "now" even with no writes at all.
const tickInterval = 5 * time.Minute

// runTimeout bounds each Run() invoked from Start's loop, so a stuck query
// can't wedge the background worker forever. It covers every tenant and
// channel of the run, so it is generous; channelTimeout is what bounds one
// channel.
const runTimeout = 5 * time.Minute

// channelTimeout is the budget of one channel's regeneration (loading,
// expansion and the transactional rewrite of its rows) inside a run, so one
// pathological channel can neither starve the others nor wedge the worker.
// A channel that exceeds it is logged and keeps its previous materialization.
const channelTimeout = 30 * time.Second

// scheduleItemColumns/fillerRuleColumns alias columns whose DB name doesn't
// spell out to the domain struct's field name, same pattern (and same
// underlying columns) as internal/api/schedules.go and internal/api/filler.go
// — duplicated here (rather than imported) because this package must not
// depend on internal/api.
const scheduleItemColumns = "id, tenant_id, schedule_id, block_id, recurrence_type AS recurrence, day_of_week, start_time_local, start_at, active, created_at, updated_at"
const fillerRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, filler_pool_id, selection_strategy AS selection, position, created_at"
const blockItemColumns = "id, block_id, position, item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms, spec"
const fillerPoolItemColumns = "id, filler_pool_id, catalog_item_id, weight"
const channelColumns = "id, tenant_id, number, name, guide_art_url, timezone, schedule_id, created_at, updated_at"

// Materializer owns the background EPG regeneration loop.
type Materializer struct {
	db      storage.DB
	window  time.Duration
	freeze  time.Duration
	trigger chan struct{}
	// mu serializes Run and ResetChannel: both rewrite epg_entry rows with
	// multi-statement sequences that must not interleave.
	mu sync.Mutex
}

// New creates a Materializer. window is how far ahead of "now" epg_entry is
// kept populated (docs/ROADMAP.md suggests 48h for cmd/server/main.go);
// callers pass it explicitly rather than relying on a package default.
func New(db storage.DB, window time.Duration) *Materializer {
	return &Materializer{
		db:      db,
		window:  window,
		freeze:  DefaultFreezeMargin,
		trigger: make(chan struct{}, 1),
	}
}

// SetFreezeMargin overrides DefaultFreezeMargin; call before Start.
// Non-positive values are ignored.
func (m *Materializer) SetFreezeMargin(d time.Duration) {
	if d > 0 {
		m.freeze = d
	}
}

// FreezeMargin reports the margin clients are told about (GET /epg/meta).
func (m *Materializer) FreezeMargin() time.Duration { return m.freeze }

// Trigger asks for a regeneration as soon as possible, without blocking the
// caller. Safe to call before Start (e.g. from tests that only exercise Run
// directly) — it just leaves a signal sitting in the buffered channel that
// nothing will ever drain, which is harmless.
func (m *Materializer) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Start runs Run() once immediately, then again on every debounced Trigger()
// and on a periodic ticker, until ctx is canceled. It always runs in its own
// goroutine and never panics or calls log.Fatal — this is a background
// worker, not something that's allowed to take the server down with it.
func (m *Materializer) Start(ctx context.Context) {
	go func() {
		m.runLogged(ctx)

		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.trigger:
				select {
				case <-time.After(triggerDebounce):
				case <-ctx.Done():
					return
				}
				m.drainTrigger()
				m.runLogged(ctx)
			case <-ticker.C:
				m.runLogged(ctx)
			}
		}
	}()
}

// drainTrigger discards any Trigger() signals that arrived during the
// debounce wait, so a burst of writes collapses into the one Run() that
// follows instead of one per signal.
func (m *Materializer) drainTrigger() {
	for {
		select {
		case <-m.trigger:
		default:
			return
		}
	}
}

func (m *Materializer) runLogged(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	if err := m.Run(runCtx); err != nil {
		log.Printf("materializer: run failed: %v", err)
	}
}

// Run performs one full, synchronous regeneration of every tenant/channel's
// epg_entry rows for the window ahead of "now" (plus the fixed lookback
// behind it), and bumps epg_revision once per tenant. It is idempotent: for
// the same schedule_item/block/filler_rule/filler_pool_item state and the
// same instant "now", it always produces the same epg_entry rows.
//
// Only a failure to even list tenants is returned as an error; a failure
// processing one tenant or one channel is logged and does not stop the rest
// (see docs/ROADMAP.md's EPG materializer note on this being an accepted v1
// tradeoff, not silent data loss — the previous materialization for that
// channel is left untouched until the next successful Run()).
func (m *Materializer) Run(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	windowEnd := now.Add(m.window)
	scanFrom := now.Add(-scanLookback)

	var tenantIDs []string
	if err := m.db.Query(ctx, &tenantIDs, "SELECT id FROM tenant"); err != nil {
		return fmt.Errorf("materializer: list tenants: %w", err)
	}

	cache := newCatalogDurationCache(m.db)
	state := newSelectionState()

	for _, tenantID := range tenantIDs {
		m.runTenant(ctx, tenantID, now, scanFrom, windowEnd, cache, state)
	}
	return nil
}

func (m *Materializer) runTenant(ctx context.Context, tenantID string, now, scanFrom, windowEnd time.Time, cache *catalogDurationCache, state *selectionState) {
	var channels []domain.Channel
	if err := m.db.Query(ctx, &channels,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ?", tenantID,
	); err != nil {
		log.Printf("materializer: tenant %s: list channels: %v", tenantID, err)
		return
	}

	for _, ch := range channels {
		chCtx, cancel := context.WithTimeout(ctx, channelTimeout)
		err := m.materializeChannel(chCtx, ch, tenantID, now, scanFrom, windowEnd, cache, state)
		cancel()
		if err != nil {
			log.Printf("materializer: channel %s: %v", ch.ID, err)
		}
	}

	if err := m.bumpRevision(ctx, tenantID, now, windowEnd); err != nil {
		log.Printf("materializer: tenant %s: bump epg_revision: %v", tenantID, err)
	}
}

// ResetChannel force-resets one channel's grid: it discards the channel's
// materialized future AND the entry currently on the air, restarts every
// continuous rotation from the beginning (the persisted positions are
// cleared), and regenerates immediately, synchronously. Viewers switch
// program right away. History (entries fully in the past) is kept.
func (m *Materializer) ResetChannel(ctx context.Context, tenantID string, ch domain.Channel) error {
	return m.resetChannels(ctx, tenantID, []domain.Channel{ch})
}

// ResetSchedule resets the grid of every channel currently using the given
// schedule, with the same effect as ResetChannel on each. Schedule edits
// (items created/updated/deleted) call it so the already-materialized grid
// never lingers from the previous rules. A schedule no channel uses is a
// no-op.
func (m *Materializer) ResetSchedule(ctx context.Context, tenantID, scheduleID string) error {
	var channels []domain.Channel
	if err := m.db.Query(ctx, &channels,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND schedule_id = ?",
		tenantID, scheduleID,
	); err != nil {
		return fmt.Errorf("list channels of schedule %s: %w", scheduleID, err)
	}
	return m.resetChannels(ctx, tenantID, channels)
}

// ResetAfterScheduleEdit resets the given schedule's channels and, if that
// fails, logs the error and falls back to an asynchronous Trigger so the edit
// that already committed still ends up regenerated (just under the freeze
// margin rules). Handlers use it because the write itself has succeeded.
func (m *Materializer) ResetAfterScheduleEdit(ctx context.Context, tenantID, scheduleID string) {
	if err := m.ResetSchedule(ctx, tenantID, scheduleID); err != nil {
		log.Printf("materializer: reset after schedule edit: %v", err)
		m.Trigger()
	}
}

// ResetAfterChannelEdit is ResetAfterScheduleEdit for one channel whose
// schedule assignment just changed (assigned, swapped or cleared).
func (m *Materializer) ResetAfterChannelEdit(ctx context.Context, tenantID string, ch domain.Channel) {
	if err := m.ResetChannel(ctx, tenantID, ch); err != nil {
		log.Printf("materializer: reset after channel schedule change: %v", err)
		m.Trigger()
	}
}

func (m *Materializer) resetChannels(ctx context.Context, tenantID string, channels []domain.Channel) error {
	if len(channels) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	windowEnd := now.Add(m.window)

	for _, ch := range channels {
		if err := m.db.Exec(ctx, "UPDATE epg_entry SET rotation_next = NULL WHERE channel_id = ?", ch.ID); err != nil {
			return fmt.Errorf("clear rotation: %w", err)
		}
		if err := m.db.Exec(ctx, "DELETE FROM epg_entry WHERE channel_id = ? AND end_at > ?", ch.ID, nowMs); err != nil {
			return fmt.Errorf("discard epg_entry: %w", err)
		}
		// A reset restarts every series item of the channel from its first
		// episode: the cursor's origin moves to now, so entries that aired
		// before it no longer count.
		if err := m.db.Exec(ctx, "UPDATE series_cursor SET origin_ms = ? WHERE tenant_id = ? AND channel_id = ?", nowMs, tenantID, ch.ID); err != nil {
			return fmt.Errorf("reset series cursor: %w", err)
		}
		if err := m.db.Exec(ctx, "UPDATE block_cursor SET origin_ms = ? WHERE tenant_id = ? AND channel_id = ?", nowMs, tenantID, ch.ID); err != nil {
			return fmt.Errorf("reset block cursor: %w", err)
		}
		if err := m.materializeChannel(ctx, ch, tenantID, now, now.Add(-scanLookback), windowEnd, newCatalogDurationCache(m.db), newSelectionState()); err != nil {
			return err
		}
	}
	return m.bumpRevision(ctx, tenantID, now, windowEnd)
}

// loadChannelInput reads everything expandChannel needs for one channel from
// the stored configuration (schedule items, blocks, filler rules/pools,
// catalog durations). It is the single loader shared by the real
// materialization (materializeChannel) and the read-only dry-run
// (PreviewReset); the grid-stability fields (ContinuousStart, KeptUntil) are
// left zero for the caller to fill in.
func (m *Materializer) loadChannelInput(ctx context.Context, ch domain.Channel, tenantID string, cache *catalogDurationCache, now, scanFrom time.Time, preview bool) (channelInput, error) {
	// A channel with no Schedule assigned (ch.ScheduleID == nil) has no
	// schedule_item rows at all — it relies solely on its channel-scoped
	// fallback filler rule, exactly like today's "24/7 genre loop with no
	// Schedule at all" case (docs/ARCHITECTURE.md "Scheduling model").
	var scheduleItems []domain.ScheduleItem
	if ch.ScheduleID != nil {
		if err := m.db.Query(ctx, &scheduleItems,
			"SELECT "+scheduleItemColumns+" FROM schedule_item WHERE tenant_id = ? AND schedule_id = ? AND active = 1",
			tenantID, *ch.ScheduleID,
		); err != nil {
			return channelInput{}, fmt.Errorf("list schedule items: %w", err)
		}
	}

	blockIDs := make(map[string]struct{}, len(scheduleItems))
	for _, si := range scheduleItems {
		blockIDs[si.BlockID] = struct{}{}
	}

	blockItems := make(map[string][]domain.BlockItem, len(blockIDs))
	for blockID := range blockIDs {
		var items []domain.BlockItem
		if err := m.db.Query(ctx, &items,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? ORDER BY position",
			blockID,
		); err != nil {
			return channelInput{}, fmt.Errorf("list block_item for block %s: %w", blockID, err)
		}
		blockItems[blockID] = items
	}

	dynamicBlockIDs := make(map[string]bool, len(blockIDs))
	// dynamicWalks holds the dynamic blocks set to an interleaved order: their
	// item list is replaced by one walked item that stands for the block, and
	// the walk is keyed by the block (block_cursor), not by a block_item, since
	// a dynamic block's items are recreated on every catalog change.
	dynamicWalks := make(map[string]dynamicWalk)
	// repeatBlockIDs holds the manual blocks set to repeat their list.
	repeatBlockIDs := make(map[string]bool, len(blockIDs))
	if len(blockIDs) > 0 {
		ids := make([]string, 0, len(blockIDs))
		for id := range blockIDs {
			ids = append(ids, id)
		}
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = strings.TrimSuffix(placeholders, ",")
		args := make([]any, 0, len(ids)+1)
		args = append(args, tenantID)
		for _, id := range ids {
			args = append(args, id)
		}
		var dyn []struct {
			ID             string
			Criteria       string
			PlaybackOrder  string
			InterleaveSpec *string
			PlaybackSeed   int64
		}
		// The effective query is the collection's; the deprecated criteria
		// column is only read for a legacy row still held for review.
		query := fmt.Sprintf("SELECT b.id AS id, COALESCE(c.query, b.criteria) AS criteria, b.playback_order AS playback_order, b.interleave_spec AS interleave_spec, b.playback_seed AS playback_seed "+
			"FROM block b LEFT JOIN collection c ON c.tenant_id = b.tenant_id AND c.id = b.collection_id "+
			"WHERE b.tenant_id = ? AND (b.collection_id IS NOT NULL OR b.criteria IS NOT NULL) AND b.id IN (%s)", placeholders)
		if err := m.db.Query(ctx, &dyn, query, args...); err != nil {
			return channelInput{}, fmt.Errorf("list dynamic block ids: %w", err)
		}
		var repeating []string
		repeatQuery := fmt.Sprintf("SELECT id FROM block WHERE tenant_id = ? AND collection_id IS NULL AND criteria IS NULL AND list_mode = 'repeat' AND id IN (%s)", placeholders)
		if err := m.db.Query(ctx, &repeating, repeatQuery, args...); err != nil {
			return channelInput{}, fmt.Errorf("list repeating block ids: %w", err)
		}
		for _, id := range repeating {
			repeatBlockIDs[id] = true
		}
		for _, d := range dyn {
			dynamicBlockIDs[d.ID] = true
			if d.PlaybackOrder != domain.OrderInterleaved {
				continue
			}
			var il domain.Interleave
			if d.InterleaveSpec != nil {
				if err := json.Unmarshal([]byte(*d.InterleaveSpec), &il); err != nil {
					log.Printf("materializer: block %s: bad interleave spec, using defaults: %v", d.ID, err)
				}
			}
			il = il.Normalized()
			if _, err := catalogsync.ParseQuery([]byte(d.Criteria)); err != nil {
				// A legacy criteria still held for review cannot be walked.
				log.Printf("materializer: block %s: interleaved order needs a condition tree, playing by title: %v", d.ID, err)
				continue
			}
			amount := domain.SeriesAmount{Mode: domain.AmountSlotEnd}
			dynamicWalks[d.ID] = dynamicWalk{
				Criteria: domain.CriteriaSpec{Query: json.RawMessage(d.Criteria)},
				Spec:     domain.SeriesSpec{PlaybackOrder: domain.OrderInterleaved, Seed: d.PlaybackSeed, Amount: &amount, Interleave: &il},
			}
			blockItems[d.ID] = []domain.BlockItem{{ID: d.ID, BlockID: d.ID, ItemType: domain.BlockItemCriteria}}
		}
	}

	// filler_rule scoped to this channel, or to any block this channel
	// actually schedules.
	query := "SELECT " + fillerRuleColumns + " FROM filler_rule WHERE tenant_id = ? AND ((scope_type = 'channel' AND scope_id = ?)"
	args := []any{tenantID, ch.ID}
	for blockID := range blockIDs {
		query += " OR (scope_type = 'block' AND scope_id = ?)"
		args = append(args, blockID)
	}
	query += ")"
	var fillerRules []domain.FillerRule
	if err := m.db.Query(ctx, &fillerRules, query, args...); err != nil {
		return channelInput{}, fmt.Errorf("list filler_rule: %w", err)
	}

	poolIDs := make(map[string]struct{}, len(fillerRules))
	for _, fr := range fillerRules {
		poolIDs[fr.FillerPoolID] = struct{}{}
	}
	poolItems := make(map[string][]domain.FillerPoolItem, len(poolIDs))
	for poolID := range poolIDs {
		var items []domain.FillerPoolItem
		// filler_pool_item has no explicit position column (unlike
		// block_item.position) — ORDER BY rowid is what "sequential"
		// selection means: the order rows were inserted in.
		if err := m.db.Query(ctx, &items,
			"SELECT "+fillerPoolItemColumns+" FROM filler_pool_item WHERE filler_pool_id = ? ORDER BY rowid",
			poolID,
		); err != nil {
			return channelInput{}, fmt.Errorf("list filler_pool_item for pool %s: %w", poolID, err)
		}
		poolItems[poolID] = items
	}

	catalogDuration := make(map[string]int64)
	collect := func(catalogItemID string) error {
		if _, ok := catalogDuration[catalogItemID]; ok {
			return nil
		}
		d, err := cache.Get(ctx, catalogItemID)
		if err != nil {
			return err
		}
		catalogDuration[catalogItemID] = d
		return nil
	}
	for _, items := range blockItems {
		for _, it := range items {
			if it.IsWalked() {
				continue
			}
			if err := collect(it.CatalogItemID); err != nil {
				return channelInput{}, fmt.Errorf("catalog_item %s: %w", it.CatalogItemID, err)
			}
		}
	}
	for _, items := range poolItems {
		for _, it := range items {
			if err := collect(it.CatalogItemID); err != nil {
				return channelInput{}, fmt.Errorf("catalog_item %s: %w", it.CatalogItemID, err)
			}
		}
	}

	runs, warnings, err := m.loadSeriesRuns(ctx, ch, tenantID, blockItems, dynamicWalks, now, scanFrom, preview)
	if err != nil {
		return channelInput{}, err
	}

	warnings = append(warnings, repeatWarnings(scheduleItems, blockItems, repeatBlockIDs, catalogDuration, runs, warnings)...)

	return channelInput{
		RepeatBlockIDs:  repeatBlockIDs,
		Series:          runs,
		ChannelID:       ch.ID,
		TenantID:        tenantID,
		Timezone:        ch.Timezone,
		ScheduleItems:   scheduleItems,
		BlockItems:      blockItems,
		FillerRules:     fillerRules,
		PoolItems:       poolItems,
		CatalogDuration: catalogDuration,
		DynamicBlockIDs: dynamicBlockIDs,
		Warnings:        warnings,
	}, nil
}

func (m *Materializer) materializeChannel(ctx context.Context, ch domain.Channel, tenantID string, now, scanFrom, windowEnd time.Time, cache *catalogDurationCache, state *selectionState) error {
	in, err := m.loadChannelInput(ctx, ch, tenantID, cache, now, scanFrom, false)
	if err != nil {
		return err
	}
	scheduleItems := in.ScheduleItems

	// Grid stability, continuous channels only: keep the already-materialized
	// head (everything starting before now + freeze margin) and resume the
	// rotation from the position persisted on the last kept entry. A channel
	// with only timed items keeps regenerating its whole window as before.
	nowMs := now.UnixMilli()
	frozenUntilMs := now.Add(m.freeze).UnixMilli()
	var keptUntil time.Time
	continuousStart := 0
	continuousVisitDone := 0
	continuousPrevEpisode := false
	var continuousID, continuousBlockID string
	for _, si := range scheduleItems {
		if si.Recurrence == domain.RecurrenceContinuous {
			continuousID = si.ID
			continuousBlockID = si.BlockID
			break
		}
	}
	// keptCutMs bounds what is kept: rows starting at or after it are
	// regenerated. Normally that is the freeze margin; a hole inside the kept
	// head moves it back to the hole's start so the hole gets regenerated.
	keptCutMs := frozenUntilMs
	if continuousID != "" {
		type keptRow struct {
			StartAt int64
			EndAt   int64
		}
		var kept []keptRow
		if err := m.db.Query(ctx, &kept,
			"SELECT start_at, end_at FROM epg_entry WHERE channel_id = ? AND end_at > ? AND start_at < ? ORDER BY start_at",
			ch.ID, nowMs, frozenUntilMs,
		); err != nil {
			return fmt.Errorf("read kept epg_entry: %w", err)
		}
		// Walk the kept rows: a gap before the first one (nothing on air) or
		// between two of them is not preserved. A head hole discards the
		// whole kept head; an interior hole discards from the hole onwards.
		contiguousEnd := nowMs
		headHole := false
		for i, k := range kept {
			if k.StartAt > contiguousEnd {
				if i == 0 {
					headHole = true
				} else {
					keptCutMs = contiguousEnd
				}
				break
			}
			if k.EndAt > contiguousEnd {
				contiguousEnd = k.EndAt
			}
		}
		if headHole {
			keptCutMs = nowMs
		} else if len(kept) > 0 {
			keptUntil = time.UnixMilli(contiguousEnd).UTC()
		}
		var rotations []int64
		if err := m.db.Query(ctx, &rotations,
			"SELECT rotation_next FROM epg_entry WHERE channel_id = ? AND source_schedule_item_id = ? AND rotation_next IS NOT NULL AND start_at < ? ORDER BY start_at DESC LIMIT 1",
			ch.ID, continuousID, keptCutMs,
		); err != nil {
			return fmt.Errorf("read rotation position: %w", err)
		}
		if len(rotations) > 0 {
			continuousStart = int(rotations[0])
		}
		// A kept head that ends in the middle of a "count" visit: the visit's
		// entries are the trailing ones that are adjacent, belong to the item
		// the rotation sits on and still carry that item's own index (the one
		// completing a visit carries the next index). Their number is how
		// much of the visit already aired.
		if items := in.BlockItems[continuousBlockID]; len(items) > 0 && !keptUntil.IsZero() {
			idx := continuousStart % len(items)
			if run := in.Series[items[idx].ID]; items[idx].IsWalked() && run != nil && run.amount.Mode == domain.AmountCount {
				var tail []struct {
					Kind              string
					StartAt           int64
					EndAt             int64
					SourceBlockItemID *string
					RotationNext      *int64
					SourceScheduleID  *string
				}
				if err := m.db.Query(ctx, &tail,
					"SELECT kind, start_at, end_at, source_block_item_id, rotation_next, source_schedule_item_id AS source_schedule_id FROM epg_entry WHERE channel_id = ? AND start_at < ? ORDER BY start_at DESC LIMIT ?",
					ch.ID, keptCutMs, 2*run.amount.Count+1,
				); err != nil {
					return fmt.Errorf("read series visit: %w", err)
				}
				expectedEnd := keptUntil.UnixMilli()
				for _, row := range tail {
					// Midroll breaks between the visit's episodes are
					// transparent: they only keep the chain adjacent.
					if row.Kind == string(domain.EpgMidRoll) {
						if row.EndAt != expectedEnd {
							break
						}
						expectedEnd = row.StartAt
						continue
					}
					if continuousVisitDone >= run.amount.Count {
						break
					}
					if row.EndAt != expectedEnd || row.SourceBlockItemID == nil || *row.SourceBlockItemID != items[idx].ID ||
						row.SourceScheduleID == nil || *row.SourceScheduleID != continuousID ||
						row.RotationNext == nil || int(*row.RotationNext) != idx {
						break
					}
					continuousVisitDone++
					expectedEnd = row.StartAt
				}
			}
		}
	}

	if continuousID != "" && !keptUntil.IsZero() {
		// Whether the kept head ends on an episode of the continuous item (as
		// opposed to a midroll break, a program or another item's entry): the
		// generation resuming there then owes a break before its first episode.
		var ends []int
		if err := m.db.Query(ctx, &ends,
			"SELECT 1 FROM epg_entry WHERE channel_id = ? AND end_at = ? AND start_at < ? AND source_schedule_item_id = ? AND source_block_item_id IS NOT NULL LIMIT 1",
			ch.ID, keptUntil.UnixMilli(), keptCutMs, continuousID,
		); err != nil {
			return fmt.Errorf("read kept head end: %w", err)
		}
		continuousPrevEpisode = len(ends) > 0
	}

	in.ContinuousStart = continuousStart
	in.ContinuousVisitDone = continuousVisitDone
	in.ContinuousPrevEpisode = continuousPrevEpisode
	in.KeptUntil = keptUntil
	generated := expandChannel(in, now, scanFrom, windowEnd, state)

	toInsert := make([]domain.EpgEntry, 0, len(generated))
	for _, e := range generated {
		// Entries entirely in the past only exist because of the lookback
		// window (Passo A); silently drop them rather than writing rows
		// GET /epg would never return anyway.
		if e.EndAt > nowMs {
			toInsert = append(toInsert, e)
		}
	}

	// Deleting only end_at > now preserves history (anything fully in the
	// past) while safely regenerating everything still relevant, including
	// whatever is airing right now.
	deleteSQL, deleteArg := "DELETE FROM epg_entry WHERE channel_id = ? AND end_at > ?", nowMs
	if !keptUntil.IsZero() {
		// Keep the frozen head; only what starts at or after the margin goes.
		deleteSQL, deleteArg = "DELETE FROM epg_entry WHERE channel_id = ? AND start_at >= ?", keptCutMs
	}
	// Delete and re-insert in one transaction: one commit instead of one per
	// row (a capped expansion writes ~20k rows), and a failure or timeout
	// midway leaves the channel's previous materialization untouched instead
	// of half-rewritten.
	err = m.db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx, deleteSQL, ch.ID, deleteArg); err != nil {
			return fmt.Errorf("delete stale epg_entry: %w", err)
		}
		for _, e := range toInsert {
			if err := tx.Exec(ctx,
				`INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at, rotation_next, source_block_item_id, series_cycle, series_round)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.ID, e.TenantID, e.ChannelID, string(e.Kind), e.CatalogItemID, e.StartAt, e.EndAt, e.SourceScheduleItemID, e.GeneratedAt, e.RotationNext, e.SourceBlockItemID, e.SeriesCycle, e.SeriesRound,
			); err != nil {
				return fmt.Errorf("insert epg_entry: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// PreviewReset returns the entries a ResetChannel call made right now would
// leave on the air, in [startMs, endMs] (same overlap rule as GET /epg; a
// non-positive bound is open), without touching epg_entry or epg_revision.
// It runs the exact same loader and expansion as the real reset: stored
// configuration, rotations from the beginning, no freeze margin. Entry ids
// are fresh and not persisted; random/weighted filler selection may differ
// from what an actual reset later picks.
func (m *Materializer) PreviewReset(ctx context.Context, tenantID string, ch domain.Channel, startMs, endMs int64) ([]domain.EpgEntry, error) {
	out, _, err := m.PreviewResetWithWarnings(ctx, tenantID, ch, startMs, endMs)
	return out, err
}

// PreviewResetWithWarnings is PreviewReset plus the configuration warnings
// found while loading (see Warning).
func (m *Materializer) PreviewResetWithWarnings(ctx context.Context, tenantID string, ch domain.Channel, startMs, endMs int64) ([]domain.EpgEntry, []Warning, error) {
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	in, err := m.loadChannelInput(ctx, ch, tenantID, newCatalogDurationCache(m.db), now, now.Add(-scanLookback), true)
	if err != nil {
		return nil, nil, err
	}
	generated := expandChannel(in, now, now.Add(-scanLookback), now.Add(m.window), newSelectionState())
	out := make([]domain.EpgEntry, 0, len(generated))
	for _, e := range generated {
		if e.EndAt <= nowMs {
			continue
		}
		if startMs > 0 && e.EndAt < startMs {
			continue
		}
		if endMs > 0 && e.StartAt > endMs {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartAt < out[j].StartAt })
	warnings := in.Warnings
	if warnings == nil {
		warnings = []Warning{}
	}
	return out, warnings, nil
}

// bumpRevision upserts the tenant's epg_revision row, same ON CONFLICT
// pattern as the catalog-sync upsert in internal/api/media_sources.go.
func (m *Materializer) bumpRevision(ctx context.Context, tenantID string, now, windowEnd time.Time) error {
	return m.db.Exec(ctx,
		`INSERT INTO epg_revision (tenant_id, revision, valid_until, updated_at) VALUES (?, 1, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1, valid_until = excluded.valid_until, updated_at = excluded.updated_at`,
		tenantID, windowEnd.UnixMilli(), now.UnixMilli(),
	)
}

// catalogDurationCache memoizes catalog_item.duration_ms lookups for the
// lifetime of one Run() call — shared across every tenant/channel it
// processes, per docs comment in the task brief ("simple map[string]int64,
// no need for more optimization than that").
type catalogDurationCache struct {
	db    storage.DB
	cache map[string]int64
}

func newCatalogDurationCache(db storage.DB) *catalogDurationCache {
	return &catalogDurationCache{db: db, cache: make(map[string]int64)}
}

func (c *catalogDurationCache) Get(ctx context.Context, catalogItemID string) (int64, error) {
	if d, ok := c.cache[catalogItemID]; ok {
		return d, nil
	}
	var durations []int64
	if err := c.db.Query(ctx, &durations, "SELECT duration_ms FROM catalog_item WHERE id = ?", catalogItemID); err != nil {
		return 0, err
	}
	if len(durations) == 0 {
		return 0, fmt.Errorf("catalog_item %s not found", catalogItemID)
	}
	c.cache[catalogItemID] = durations[0]
	return durations[0], nil
}

// Warning kinds a channel's configuration can raise.
const (
	// WarningCriteriaError: a criteria item or dynamic block whose query could
	// not be evaluated (invalid stored query or a failing lookup).
	WarningCriteriaError = "criteria-error"
	// WarningSeriesError: a series item whose stored spec cannot be read.
	WarningSeriesError = "series-error"
	// WarningRepeatEmpty: a block set to repeat whose list airs nothing (no
	// item, only empty series or items without a positive duration), so the
	// repetition has nothing to loop and the block's time stays empty.
	WarningRepeatEmpty = "repeat-empty"
)

// Warning is a problem found in a channel's stored configuration that makes
// part of its programming silently absent. BlockItemID is empty for a dynamic
// block (the block itself is the walked item).
type Warning struct {
	Kind        string `json:"kind"`
	BlockID     string `json:"block_id"`
	BlockItemID string `json:"block_item_id,omitempty"`
	Message     string `json:"message"`
}

// repeatWarnings reports the scheduled manual blocks set to repeat whose list
// cannot air anything. Expansion guards against such a list (a lap that airs
// nothing ends the block), this makes the empty time visible. A block that
// already carries a warning (an item that failed to load) is not reported
// again: that warning is the cause.
func repeatWarnings(items []domain.ScheduleItem, blockItems map[string][]domain.BlockItem, repeat map[string]bool, durations map[string]int64, runs map[string]*seriesRun, existing []Warning) []Warning {
	var out []Warning
	seen := make(map[string]bool)
	for _, w := range existing {
		seen[w.BlockID] = true
	}
	for _, si := range items {
		if !repeat[si.BlockID] || seen[si.BlockID] {
			continue
		}
		seen[si.BlockID] = true
		airs := false
		for _, bi := range blockItems[si.BlockID] {
			if bi.IsWalked() {
				if run := runs[bi.ID]; run != nil && !run.empty() {
					airs = true
				}
			} else if effectiveDuration(bi, durations) > 0 {
				airs = true
			}
		}
		if !airs {
			log.Printf("materializer: WARNING block %s is set to repeat but none of its items can air (empty list, empty series or no positive duration)", si.BlockID)
			out = append(out, Warning{
				Kind: WarningRepeatEmpty, BlockID: si.BlockID,
				Message: "the block repeats its list, but none of its items can air (empty list, empty series or no positive duration). Its time stays empty until it has content.",
			})
		}
	}
	return out
}

// Warnings evaluates the channel's stored configuration read-only and returns
// what would leave holes in its grid (see Warning). It runs the same loader
// as the real materialization, so it reports exactly what a run would skip.
func (m *Materializer) Warnings(ctx context.Context, tenantID string, ch domain.Channel) ([]Warning, error) {
	now := time.Now().UTC()
	in, err := m.loadChannelInput(ctx, ch, tenantID, newCatalogDurationCache(m.db), now, now.Add(-scanLookback), true)
	if err != nil {
		return nil, err
	}
	if in.Warnings == nil {
		return []Warning{}, nil
	}
	return in.Warnings, nil
}

// dynamicWalk is the interleaved walk of a dynamic block: the query it
// matches and the spec the walk is built from.
type dynamicWalk struct {
	Criteria domain.CriteriaSpec
	Spec     domain.SeriesSpec
}

// loadSeriesRuns builds the walk of every series item in the channel's
// blocks. The cursor of each is rebuilt from the epg_entry rows the item
// already produced on this channel (see seriesRun); its origin comes from
// series_cursor, created the first time the item is met (a preview never
// writes, it just treats "now" as the origin and ignores stored rows, like
// the reset it simulates). dynamic holds the walked stand-in item of each
// interleaved dynamic block (keyed by block id, which is also the id its
// entries record), whose origin comes from block_cursor instead.
func (m *Materializer) loadSeriesRuns(ctx context.Context, ch domain.Channel, tenantID string, blockItems map[string][]domain.BlockItem, dynamic map[string]dynamicWalk, now, scanFrom time.Time, preview bool) (map[string]*seriesRun, []Warning, error) {
	runs := make(map[string]*seriesRun)
	var warnings []Warning
	// skip records an item that cannot be walked: it is logged as an error and
	// reported as a warning (see Warning), and the item airs nothing, which
	// leaves a hole the fallback filler (if any) covers.
	skip := func(kind, blockID, itemID, what string, err error) {
		log.Printf("materializer: ERROR channel %s: block %s item %s: %s: %v (the item is skipped, its time is left to the fallback filler or a gap)", ch.ID, blockID, itemID, what, err)
		warnings = append(warnings, Warning{
			Kind: kind, BlockID: blockID, BlockItemID: itemID,
			Message: fmt.Sprintf("%s: %v. The item airs nothing until this is fixed.", what, err),
		})
	}
	for blockID, items := range blockItems {
		for _, it := range items {
			if !it.IsWalked() {
				continue
			}
			if _, done := runs[it.ID]; done {
				continue
			}
			var spec domain.SeriesSpec
			var episodes []series.Episode
			dw, isDynamic := dynamic[it.ID]
			if isDynamic {
				var err error
				spec = dw.Spec
				if episodes, err = blockitem.Episodes(ctx, m.db, tenantID, dw.Criteria); err != nil {
					skip(WarningCriteriaError, it.ID, "", "the dynamic block's query failed to evaluate", err)
					continue
				}
			} else if it.IsCriteria() {
				// A criteria item is walked exactly like a series: its matches
				// play the part of the episodes, in the item's playback order.
				cs, err := blockitem.ParseCriteriaSpec(it.Spec)
				if err != nil {
					skip(WarningCriteriaError, blockID, it.ID, "the criteria item's spec is invalid", err)
					continue
				}
				spec = criteriaWalkSpec(cs)
				if episodes, err = blockitem.Episodes(ctx, m.db, tenantID, cs); err != nil {
					skip(WarningCriteriaError, blockID, it.ID, "the criteria item's query failed to evaluate", err)
					continue
				}
			} else {
				var err error
				if spec, err = series.ParseSpec(it.Spec); err != nil {
					skip(WarningSeriesError, blockID, it.ID, "the series item's spec is invalid", err)
					continue
				}
				if episodes, err = series.Episodes(ctx, m.db, tenantID, spec); err != nil {
					return nil, nil, fmt.Errorf("list episodes of block_item %s: %w", it.ID, err)
				}
			}

			cursorTable, cursorCol := "series_cursor", "block_item_id"
			if isDynamic {
				cursorTable, cursorCol = "block_cursor", "block_id"
			}
			originMs := now.UnixMilli()
			var history, ahead []seriesRow
			if !preview {
				var origins []int64
				if err := m.db.Query(ctx, &origins,
					"SELECT origin_ms FROM "+cursorTable+" WHERE tenant_id = ? AND channel_id = ? AND "+cursorCol+" = ?",
					tenantID, ch.ID, it.ID,
				); err != nil {
					return nil, nil, fmt.Errorf("read series cursor: %w", err)
				}
				if len(origins) > 0 {
					originMs = origins[0]
				} else if err := m.db.Exec(ctx,
					"INSERT OR IGNORE INTO "+cursorTable+" (tenant_id, channel_id, "+cursorCol+", origin_ms) VALUES (?, ?, ?, ?)",
					tenantID, ch.ID, it.ID, originMs,
				); err != nil {
					return nil, nil, fmt.Errorf("create series cursor: %w", err)
				}
				var rows []struct {
					StartAt       int64
					CatalogItemID string
					SeriesCycle   *int
					SeriesRound   *int
				}
				if err := m.db.Query(ctx, &rows,
					"SELECT start_at, catalog_item_id, series_cycle, series_round FROM epg_entry WHERE tenant_id = ? AND channel_id = ? AND source_block_item_id = ? AND start_at >= ? ORDER BY start_at",
					tenantID, ch.ID, it.ID, originMs,
				); err != nil {
					return nil, nil, fmt.Errorf("read series entries: %w", err)
				}
				for _, r := range rows {
					cycle, round := 0, 0
					if r.SeriesCycle != nil {
						cycle = *r.SeriesCycle
					}
					if r.SeriesRound != nil {
						round = *r.SeriesRound
					}
					row := seriesRow{StartAt: r.StartAt, CatalogItemID: r.CatalogItemID, Cycle: cycle, Round: round}
					if r.StartAt < scanFrom.UnixMilli() {
						history = append(history, row)
					} else {
						ahead = append(ahead, row)
					}
				}
			}
			runs[it.ID] = newSeriesRun(spec, ch.ID, episodes, originMs, history, ahead, now.UnixMilli())
		}
	}
	return runs, warnings, nil
}
