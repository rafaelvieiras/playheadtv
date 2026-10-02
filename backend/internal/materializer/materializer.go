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
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
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
// can't wedge the background worker forever.
const runTimeout = 30 * time.Second

// scheduleItemColumns/fillerRuleColumns alias columns whose DB name doesn't
// spell out to the domain struct's field name, same pattern (and same
// underlying columns) as internal/api/schedules.go and internal/api/filler.go
// — duplicated here (rather than imported) because this package must not
// depend on internal/api.
const scheduleItemColumns = "id, tenant_id, schedule_id, block_id, recurrence_type AS recurrence, day_of_week, start_time_local, start_at, active, created_at, updated_at"
const fillerRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, filler_pool_id, selection_strategy AS selection, position, created_at"
const blockItemColumns = "id, block_id, position, catalog_item_id, trim_duration_ms"
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
		if err := m.materializeChannel(ctx, ch, tenantID, now, scanFrom, windowEnd, cache, state); err != nil {
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
		if err := m.materializeChannel(ctx, ch, tenantID, now, now.Add(-scanLookback), windowEnd, newCatalogDurationCache(m.db), newSelectionState()); err != nil {
			return err
		}
	}
	return m.bumpRevision(ctx, tenantID, now, windowEnd)
}

func (m *Materializer) materializeChannel(ctx context.Context, ch domain.Channel, tenantID string, now, scanFrom, windowEnd time.Time, cache *catalogDurationCache, state *selectionState) error {
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
			return fmt.Errorf("list schedule items: %w", err)
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
			return fmt.Errorf("list block_item for block %s: %w", blockID, err)
		}
		blockItems[blockID] = items
	}

	dynamicBlockIDs := make(map[string]bool, len(blockIDs))
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
		var dynIDs []string
		query := fmt.Sprintf("SELECT id FROM block WHERE tenant_id = ? AND criteria IS NOT NULL AND id IN (%s)", placeholders)
		if err := m.db.Query(ctx, &dynIDs, query, args...); err != nil {
			return fmt.Errorf("list dynamic block ids: %w", err)
		}
		for _, id := range dynIDs {
			dynamicBlockIDs[id] = true
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
		return fmt.Errorf("list filler_rule: %w", err)
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
			return fmt.Errorf("list filler_pool_item for pool %s: %w", poolID, err)
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
			if err := collect(it.CatalogItemID); err != nil {
				return fmt.Errorf("catalog_item %s: %w", it.CatalogItemID, err)
			}
		}
	}
	for _, items := range poolItems {
		for _, it := range items {
			if err := collect(it.CatalogItemID); err != nil {
				return fmt.Errorf("catalog_item %s: %w", it.CatalogItemID, err)
			}
		}
	}

	// Grid stability, continuous channels only: keep the already-materialized
	// head (everything starting before now + freeze margin) and resume the
	// rotation from the position persisted on the last kept entry. A channel
	// with only timed items keeps regenerating its whole window as before.
	nowMs := now.UnixMilli()
	frozenUntilMs := now.Add(m.freeze).UnixMilli()
	var keptUntil time.Time
	continuousStart := 0
	var continuousID string
	for _, si := range scheduleItems {
		if si.Recurrence == domain.RecurrenceContinuous {
			continuousID = si.ID
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
	}

	in := channelInput{
		ChannelID:       ch.ID,
		TenantID:        tenantID,
		Timezone:        ch.Timezone,
		ScheduleItems:   scheduleItems,
		BlockItems:      blockItems,
		FillerRules:     fillerRules,
		PoolItems:       poolItems,
		CatalogDuration: catalogDuration,
		DynamicBlockIDs: dynamicBlockIDs,
		ContinuousStart: continuousStart,
		KeptUntil:       keptUntil,
	}
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
	if err := m.db.Exec(ctx, deleteSQL, ch.ID, deleteArg); err != nil {
		return fmt.Errorf("delete stale epg_entry: %w", err)
	}
	for _, e := range toInsert {
		if err := m.db.Exec(ctx,
			`INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at, rotation_next)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.TenantID, e.ChannelID, string(e.Kind), e.CatalogItemID, e.StartAt, e.EndAt, e.SourceScheduleItemID, e.GeneratedAt, e.RotationNext,
		); err != nil {
			return fmt.Errorf("insert epg_entry: %w", err)
		}
	}
	return nil
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
