package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// dynamicOwner is one dynamic filler pool or block to resolve. Criteria is
// the effective query text: the collection's when the owner references one,
// else the deprecated criteria column (a legacy row held for review).
type dynamicOwner struct {
	ID           string
	CollectionID string
	Criteria     string
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

// resolveGroup is every dynamic owner that shares one query: all the pools
// and blocks of a collection, or a single legacy owner.
type resolveGroup struct {
	criteria string
	pools    []string
	blocks   []string
}

func listDynamicOwners(ctx context.Context, db storage.DB, tenantID, table string) ([]dynamicOwner, error) {
	var owners []dynamicOwner
	err := db.Query(ctx, &owners,
		"SELECT o.id AS id, COALESCE(o.collection_id, '') AS collection_id, COALESCE(c.query, o.criteria) AS criteria FROM "+table+" o "+
			"LEFT JOIN collection c ON c.tenant_id = o.tenant_id AND c.id = o.collection_id "+
			"WHERE o.tenant_id = ? AND (o.collection_id IS NOT NULL OR o.criteria IS NOT NULL) ORDER BY o.id",
		tenantID)
	return owners, err
}

// resolveDynamic recomputes the membership of the tenant's dynamic pools
// and/or blocks. Owners are grouped by collection and each distinct query is
// evaluated against the catalog exactly once, however many blocks and pools
// use it (a legacy owner held for review is its own group); only the
// per-owner row rewrite is per owner.
func resolveDynamic(ctx context.Context, db storage.DB, tenantID string, pools, blocks bool, onlyCollection string) error {
	groups := map[string]*resolveGroup{}
	add := func(kind string, owners []dynamicOwner) {
		for _, o := range owners {
			if onlyCollection != "" && o.CollectionID != onlyCollection {
				continue
			}
			key := "c:" + o.CollectionID
			if o.CollectionID == "" {
				key = "o:" + kind + ":" + o.ID
			}
			g := groups[key]
			if g == nil {
				g = &resolveGroup{criteria: o.Criteria}
				groups[key] = g
			}
			if kind == OwnerFillerPool {
				g.pools = append(g.pools, o.ID)
			} else {
				g.blocks = append(g.blocks, o.ID)
			}
		}
	}
	if pools {
		owners, err := listDynamicOwners(ctx, db, tenantID, "filler_pool")
		if err != nil {
			return fmt.Errorf("catalogsync: list dynamic filler_pool: %w", err)
		}
		add(OwnerFillerPool, owners)
	}
	if blocks {
		owners, err := listDynamicOwners(ctx, db, tenantID, "block")
		if err != nil {
			return fmt.Errorf("catalogsync: list dynamic block: %w", err)
		}
		add(OwnerBlock, owners)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		ids, err := matchStored(ctx, db, tenantID, g.criteria)
		if err != nil {
			return fmt.Errorf("catalogsync: match query of %s: %w", k, err)
		}
		for _, id := range g.pools {
			if err := resolveOnePool(ctx, db, id, ids); err != nil {
				return fmt.Errorf("catalogsync: resolve filler_pool %s: %w", id, err)
			}
		}
		for _, id := range g.blocks {
			if err := resolveOneBlock(ctx, db, id, ids); err != nil {
				return fmt.Errorf("catalogsync: resolve block %s: %w", id, err)
			}
		}
	}
	return nil
}

// ResolveDynamicPools recomputes filler_pool_item membership for every
// dynamic filler_pool in tenantID (one that references a collection, or a
// legacy row still held for review), matching against the current
// catalog_item table. Full replace per pool (delete all its filler_pool_item
// rows, reinsert the current match set, ordered by title) — same "always
// correct, no incremental diff" tradeoff pullsync.SyncOne already accepts for
// catalog_item itself; cheap enough for a self-hosted library. Called after
// every catalog sync (push's catalogSync handler and pull's SyncOne, via
// ResolveDynamic) and once synchronously right after a pool's collection is
// written via the API, so a channel built from a collection never needs a
// manual re-run to pick up newly synced matching content. One evaluation per
// distinct collection, not per pool.
func ResolveDynamicPools(ctx context.Context, db storage.DB, tenantID string) error {
	return resolveDynamic(ctx, db, tenantID, true, false, "")
}

func resolveOnePool(ctx context.Context, db storage.DB, poolID string, catalogItemIDs []string) error {
	return db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx, "DELETE FROM filler_pool_item WHERE filler_pool_id = ?", poolID); err != nil {
			return fmt.Errorf("clear filler_pool_item: %w", err)
		}
		for _, itemID := range catalogItemIDs {
			if err := tx.Exec(ctx,
				"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, ?)",
				uuid.NewString(), poolID, itemID, 1,
			); err != nil {
				return fmt.Errorf("insert filler_pool_item: %w", err)
			}
		}
		return nil
	})
}

// ResolveDynamicBlocks is ResolveDynamicPools' twin for block/block_item:
// recomputes block_item membership (position 0..N-1, trim_duration_ms NULL,
// ordered same as MatchQuery) for every dynamic block in tenantID. Called from
// the same trigger points as ResolveDynamicPools (see ResolveDynamic) plus
// once synchronously right after a block's collection is written via the API
// or a collection's query is edited.
func ResolveDynamicBlocks(ctx context.Context, db storage.DB, tenantID string) error {
	return resolveDynamic(ctx, db, tenantID, false, true, "")
}

func resolveOneBlock(ctx context.Context, db storage.DB, blockID string, catalogItemIDs []string) error {
	return db.Transact(ctx, func(tx storage.DB) error {
		if err := tx.Exec(ctx, "DELETE FROM block_item WHERE block_id = ?", blockID); err != nil {
			return fmt.Errorf("clear block_item: %w", err)
		}
		for position, itemID := range catalogItemIDs {
			if err := tx.Exec(ctx,
				"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES (?, ?, ?, ?, NULL)",
				uuid.NewString(), blockID, position, itemID,
			); err != nil {
				return fmt.Errorf("insert block_item: %w", err)
			}
		}
		return nil
	})
}

// ResolveDynamic resolves both dynamic filler pools and dynamic blocks for
// tenantID — the one call site every catalog-sync trigger point needs
// (pullsync.Syncer.SyncOne, the push catalogSync handler), since a sync can
// affect either kind of dynamic membership. Every distinct collection is
// evaluated once for the pools and blocks together. api/filler.go and
// api/blocks.go still call ResolveDynamicPools/ResolveDynamicBlocks
// individually right after writing that one object's own collection, for
// immediate feedback without recomputing the other kind unnecessarily.
func ResolveDynamic(ctx context.Context, db storage.DB, tenantID string) error {
	return resolveDynamic(ctx, db, tenantID, true, true, "")
}

// ResolveCollection re-resolves only the dynamic pools and blocks that use
// one collection (the query was just edited): one evaluation, however many use
// it. Criteria items evaluate their collection live and need nothing here.
func ResolveCollection(ctx context.Context, db storage.DB, tenantID, collectionID string) error {
	return resolveDynamic(ctx, db, tenantID, true, true, collectionID)
}
