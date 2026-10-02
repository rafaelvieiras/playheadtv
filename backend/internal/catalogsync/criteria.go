package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// dynamicPool is the row shape queried out of filler_pool for pools that
// have a non-NULL criteria — a named type rather than an anonymous struct so
// storage's reflection-based scan (which requires addressable, named struct
// fields) has something stable to match columns against.
type dynamicPool struct {
	ID       string
	Criteria string
}

// dynamicBlock mirrors dynamicPool but for block.
type dynamicBlock struct {
	ID       string
	Criteria string
}

// matchStored returns the catalog_item ids a stored criteria value matches
// for tenantID, ordered by title. A value in the legacy fixed shape (one that
// startup migration held back for review, see MigrateLegacyCriteria) still
// resolves exactly as before; everything else is a condition tree.
func matchStored(ctx context.Context, db storage.DB, tenantID, criteria string) ([]string, error) {
	if IsLegacyCriteria(criteria) {
		var c legacyCriteria
		if err := json.Unmarshal([]byte(criteria), &c); err != nil {
			return nil, fmt.Errorf("parse legacy criteria: %w", err)
		}
		return matchLegacy(ctx, db, tenantID, c)
	}
	q, err := ParseQuery([]byte(criteria))
	if err != nil {
		return nil, err
	}
	return MatchQuery(ctx, db, tenantID, q)
}

// MatchQuery returns the catalog_item ids matching q for tenantID, ordered by
// title (then id) — the one definition of "matches" shared by dynamic
// pool/block resolution, PreviewQuery and criteria block items.
func MatchQuery(ctx context.Context, db storage.DB, tenantID string, q *Query) ([]string, error) {
	where, args := q.Where(tenantID)
	var ids []string
	if err := db.Query(ctx, &ids, "SELECT id FROM catalog_item WHERE "+where+" ORDER BY title, id", args...); err != nil {
		return nil, fmt.Errorf("query matching catalog_item: %w", err)
	}
	return ids, nil
}

// CriteriaPreview is the result of PreviewQuery.
type CriteriaPreview struct {
	MatchCount      int                  `json:"match_count"`
	TotalDurationMs int64                `json:"total_duration_ms"`
	Sample          []domain.CatalogItem `json:"sample"`
}

const previewItemColumns = ListColumns

type previewTotals struct {
	MatchCount      int
	TotalDurationMs int64
}

// PreviewQuery reports what a query would currently match (count, total
// duration, and the first sampleLimit items ordered by title) without
// persisting anything. It uses the same matching clause as dynamic
// pool/block resolution.
func PreviewQuery(ctx context.Context, db storage.DB, tenantID string, q *Query, sampleLimit int) (CriteriaPreview, error) {
	where, args := q.Where(tenantID)

	var totals previewTotals
	if err := db.Query(ctx, &totals,
		"SELECT COUNT(*) AS match_count, COALESCE(SUM(duration_ms), 0) AS total_duration_ms FROM catalog_item WHERE "+where, args...,
	); err != nil {
		return CriteriaPreview{}, fmt.Errorf("preview totals: %w", err)
	}
	sample := []domain.CatalogItem{}
	if err := db.Query(ctx, &sample,
		"SELECT "+previewItemColumns+" FROM catalog_item WHERE "+where+" ORDER BY title, id LIMIT ?", append(args, sampleLimit)...,
	); err != nil {
		return CriteriaPreview{}, fmt.Errorf("preview sample: %w", err)
	}
	return CriteriaPreview{MatchCount: totals.MatchCount, TotalDurationMs: totals.TotalDurationMs, Sample: sample}, nil
}

// ResolveDynamicPools recomputes filler_pool_item membership for every
// filler_pool in tenantID that has a non-NULL criteria, matching against the
// current catalog_item table. Full replace per pool (delete all its
// filler_pool_item rows, reinsert the current match set, ordered by title)
// — same "always correct, no incremental diff" tradeoff pullsync.SyncOne
// already accepts for catalog_item itself; cheap enough for a self-hosted
// library. Called after every catalog sync (push's catalogSync handler and
// pull's SyncOne, via ResolveDynamic) and once synchronously right after a
// pool's criteria is written via the API, so a channel built from a criteria
// never needs a manual re-run to pick up newly synced matching content.
func ResolveDynamicPools(ctx context.Context, db storage.DB, tenantID string) error {
	var pools []dynamicPool
	if err := db.Query(ctx, &pools,
		"SELECT id, criteria FROM filler_pool WHERE tenant_id = ? AND criteria IS NOT NULL",
		tenantID,
	); err != nil {
		return fmt.Errorf("catalogsync: list dynamic filler_pool: %w", err)
	}

	for _, p := range pools {
		ids, err := matchStored(ctx, db, tenantID, p.Criteria)
		if err != nil {
			return fmt.Errorf("catalogsync: match criteria of filler_pool %s: %w", p.ID, err)
		}
		if err := resolveOnePool(ctx, db, p.ID, ids); err != nil {
			return fmt.Errorf("catalogsync: resolve filler_pool %s: %w", p.ID, err)
		}
	}
	return nil
}

func resolveOnePool(ctx context.Context, db storage.DB, poolID string, catalogItemIDs []string) error {
	if err := db.Exec(ctx, "DELETE FROM filler_pool_item WHERE filler_pool_id = ?", poolID); err != nil {
		return fmt.Errorf("clear filler_pool_item: %w", err)
	}
	for _, itemID := range catalogItemIDs {
		if err := db.Exec(ctx,
			"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, ?)",
			uuid.NewString(), poolID, itemID, 1,
		); err != nil {
			return fmt.Errorf("insert filler_pool_item: %w", err)
		}
	}
	return nil
}

// ResolveDynamicBlocks is ResolveDynamicPools' twin for block/block_item:
// recomputes block_item membership (position 0..N-1, trim_duration_ms NULL,
// ordered same as MatchQuery) for every block in tenantID with a
// non-NULL criteria. Called from the same trigger points as
// ResolveDynamicPools (see ResolveDynamic) plus once synchronously right
// after a block's criteria is written via the API.
func ResolveDynamicBlocks(ctx context.Context, db storage.DB, tenantID string) error {
	var blocks []dynamicBlock
	if err := db.Query(ctx, &blocks,
		"SELECT id, criteria FROM block WHERE tenant_id = ? AND criteria IS NOT NULL",
		tenantID,
	); err != nil {
		return fmt.Errorf("catalogsync: list dynamic block: %w", err)
	}

	for _, b := range blocks {
		ids, err := matchStored(ctx, db, tenantID, b.Criteria)
		if err != nil {
			return fmt.Errorf("catalogsync: match criteria of block %s: %w", b.ID, err)
		}
		if err := resolveOneBlock(ctx, db, b.ID, ids); err != nil {
			return fmt.Errorf("catalogsync: resolve block %s: %w", b.ID, err)
		}
	}
	return nil
}

func resolveOneBlock(ctx context.Context, db storage.DB, blockID string, catalogItemIDs []string) error {
	if err := db.Exec(ctx, "DELETE FROM block_item WHERE block_id = ?", blockID); err != nil {
		return fmt.Errorf("clear block_item: %w", err)
	}
	for position, itemID := range catalogItemIDs {
		if err := db.Exec(ctx,
			"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES (?, ?, ?, ?, NULL)",
			uuid.NewString(), blockID, position, itemID,
		); err != nil {
			return fmt.Errorf("insert block_item: %w", err)
		}
	}
	return nil
}

// ResolveDynamic resolves both dynamic filler pools and dynamic blocks for
// tenantID — the one call site every catalog-sync trigger point needs
// (pullsync.Syncer.SyncOne, the push catalogSync handler), since a sync can
// affect either kind of dynamic membership. api/filler.go and api/blocks.go
// still call ResolveDynamicPools/ResolveDynamicBlocks individually right
// after writing that one object's own criteria, for immediate feedback
// without recomputing the other kind unnecessarily.
func ResolveDynamic(ctx context.Context, db storage.DB, tenantID string) error {
	if err := ResolveDynamicPools(ctx, db, tenantID); err != nil {
		return err
	}
	return ResolveDynamicBlocks(ctx, db, tenantID)
}
