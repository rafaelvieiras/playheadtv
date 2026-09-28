package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// poolCriteria is the JSON shape stored in filler_pool.criteria and
// block.criteria. Genres matches by OR (any genre in the list appearing in
// catalog_item.genres is enough) — callers are expected to list every
// spelling/language variant that should match (e.g. both "Comedy" and
// its Portuguese spelling "Comédia"), no normalization happens here. YearFrom/YearTo are an
// inclusive, independent range.
type poolCriteria struct {
	Kind     string   `json:"kind"`
	Genres   []string `json:"genres,omitempty"`
	YearFrom *int     `json:"year_from,omitempty"`
	YearTo   *int     `json:"year_to,omitempty"`
}

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

// matchCriteria returns the catalog_item ids matching c for tenantID,
// ordered by title — the same query resolveOnePool/resolveOneBlock use to
// recompute a dynamic pool's/block's membership.
func matchCriteria(ctx context.Context, db storage.DB, tenantID string, c poolCriteria) ([]string, error) {
	query := "SELECT id FROM catalog_item WHERE tenant_id = ? AND kind = ?"
	args := []any{tenantID, c.Kind}
	if c.YearFrom != nil {
		query += " AND production_year >= ?"
		args = append(args, *c.YearFrom)
	}
	if c.YearTo != nil {
		query += " AND production_year <= ?"
		args = append(args, *c.YearTo)
	}
	if len(c.Genres) > 0 {
		var clauses []string
		for _, g := range c.Genres {
			clauses = append(clauses, "genres LIKE ?")
			args = append(args, "%"+g+"%")
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	query += " ORDER BY title"

	var catalogItemIDs []string
	if err := db.Query(ctx, &catalogItemIDs, query, args...); err != nil {
		return nil, fmt.Errorf("query matching catalog_item: %w", err)
	}
	return catalogItemIDs, nil
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
		var c poolCriteria
		if err := json.Unmarshal([]byte(p.Criteria), &c); err != nil {
			return fmt.Errorf("catalogsync: parse criteria for filler_pool %s: %w", p.ID, err)
		}
		if err := resolveOnePool(ctx, db, tenantID, p.ID, c); err != nil {
			return fmt.Errorf("catalogsync: resolve filler_pool %s: %w", p.ID, err)
		}
	}
	return nil
}

func resolveOnePool(ctx context.Context, db storage.DB, tenantID, poolID string, c poolCriteria) error {
	catalogItemIDs, err := matchCriteria(ctx, db, tenantID, c)
	if err != nil {
		return err
	}

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
// ordered same as matchCriteria) for every block in tenantID with a
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
		var c poolCriteria
		if err := json.Unmarshal([]byte(b.Criteria), &c); err != nil {
			return fmt.Errorf("catalogsync: parse criteria for block %s: %w", b.ID, err)
		}
		if err := resolveOneBlock(ctx, db, tenantID, b.ID, c); err != nil {
			return fmt.Errorf("catalogsync: resolve block %s: %w", b.ID, err)
		}
	}
	return nil
}

func resolveOneBlock(ctx context.Context, db storage.DB, tenantID, blockID string, c poolCriteria) error {
	catalogItemIDs, err := matchCriteria(ctx, db, tenantID, c)
	if err != nil {
		return err
	}

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
