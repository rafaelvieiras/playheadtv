// Package catalogsync holds the catalog_item upsert/delete/last_synced_at
// SQL shared by both directions a media_source's catalog can be kept in
// sync: push (internal/api's catalogSync handler — a Device already computed
// a batch of items+deletes and posts it) and pull (internal/pullsync's
// Syncer — the backend itself queries Jellyfin and diffs the result against
// what's already in catalog_item). This package only knows about storage.DB
// and domain types — no HTTP, no Jellyfin-specific shapes, no import of
// internal/api or internal/pullsync (both import this package, not the
// other way around, so there's no cycle).
package catalogsync

import (
	"context"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Item is one row to upsert into catalog_item, source-agnostic: both the
// push JSON payload (internal/api's catalogSyncItem, after its own
// external_id/title/kind validation) and the pull mapper
// (internal/pullsync's mapJellyfinItem) convert into this shape before
// calling Upsert.
type Item struct {
	ExternalID       string
	Kind             domain.CatalogItemKind
	Title            string
	SeriesTitle      *string // only meaningful when Kind == domain.CatalogEpisode
	DurationMs       int64
	SourceModifiedAt *int64
	Genres           string
	ProductionYear   *int64
}

// Upsert inserts or updates one catalog_item row keyed by
// (media_source_id, external_id) — byte-identical SQL to what
// internal/api's catalogSync handler used to run inline before this package
// existed.
func Upsert(ctx context.Context, db storage.DB, tenantID, mediaSourceID string, item Item, now int64) error {
	return db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, genres, production_year, synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (media_source_id, external_id) DO UPDATE SET
		   kind = excluded.kind, title = excluded.title, series_title = excluded.series_title,
		   duration_ms = excluded.duration_ms, source_modified_at = excluded.source_modified_at,
		   genres = excluded.genres, production_year = excluded.production_year,
		   synced_at = excluded.synced_at`,
		uuid.NewString(), tenantID, mediaSourceID, item.ExternalID, string(item.Kind), item.Title,
		item.SeriesTitle, item.DurationMs, item.SourceModifiedAt, item.Genres, item.ProductionYear, now,
	)
}

// Delete removes one catalog_item row by (media_source_id, external_id),
// along with any filler_pool_item/block_item rows that reference it. Not an
// error if no such row exists — mirrors the original inline DELETE, which
// never checked rows-affected either.
//
// The filler_pool_item/block_item cleanup isn't optional: catalog_item.id has
// no ON DELETE CASCADE from either table (migrations/0001_init.sql), so
// deleting a catalog_item still referenced by one would otherwise fail on
// the FK constraint. Before dynamic pools/blocks this was a rare
// manual-curation edge case (a self-hoster would notice and remove the stale
// item by hand); ResolveDynamicPools/ResolveDynamicBlocks make it the common
// case, since they're the very thing populating those references from the
// previous sync — a sync that legitimately drops a stale item now routinely
// runs into exactly these rows.
func Delete(ctx context.Context, db storage.DB, mediaSourceID, externalID string) error {
	var catalogItemIDs []string
	if err := db.Query(ctx, &catalogItemIDs,
		"SELECT id FROM catalog_item WHERE media_source_id = ? AND external_id = ?",
		mediaSourceID, externalID,
	); err != nil {
		return err
	}
	if len(catalogItemIDs) == 0 {
		return nil
	}
	if err := db.Exec(ctx, "DELETE FROM filler_pool_item WHERE catalog_item_id = ?", catalogItemIDs[0]); err != nil {
		return err
	}
	if err := db.Exec(ctx, "DELETE FROM block_item WHERE catalog_item_id = ?", catalogItemIDs[0]); err != nil {
		return err
	}
	return db.Exec(ctx,
		"DELETE FROM catalog_item WHERE media_source_id = ? AND external_id = ?",
		mediaSourceID, externalID,
	)
}

// TouchLastSynced stamps media_source.last_synced_at and updated_at with
// now. Called once per sync pass, after every Upsert/Delete for that source
// — same statement catalogSync used to run inline at the end.
func TouchLastSynced(ctx context.Context, db storage.DB, tenantID, mediaSourceID string, now int64) error {
	return db.Exec(ctx,
		"UPDATE media_source SET last_synced_at = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
		now, now, tenantID, mediaSourceID,
	)
}
