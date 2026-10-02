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
	"fmt"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Item is one row to upsert into catalog_item, source-agnostic: both the
// push JSON payload (internal/api's catalogSyncItem, after its own
// external_id/title/kind validation) and the pull mapper
// (internal/pullsync's mapJellyfinItem) convert into this shape before
// calling Upsert.
//
// An upsert replaces the whole stored snapshot of the item: a field left nil
// or empty clears whatever an earlier sync stored.
type Item struct {
	ExternalID       string
	Kind             domain.CatalogItemKind
	Title            string
	SeriesTitle      *string // only meaningful when Kind == domain.CatalogEpisode
	DurationMs       int64
	SourceModifiedAt *int64
	Genres           string
	ProductionYear   *int64
	domain.CatalogMetadata
}

// Upsert inserts or updates one catalog_item row keyed by
// (media_source_id, external_id), then brings the item's dimension links
// (people, studios, tags, genres, languages) in line with item: new links are
// added and links no longer present are removed, so re-syncing the same item
// is idempotent.
func Upsert(ctx context.Context, db storage.DB, tenantID, mediaSourceID string, item Item, now int64) error {
	locations, err := marshalOptionalJSON(item.Locations)
	if err != nil {
		return err
	}
	chapters, err := marshalOptionalJSON(item.Chapters)
	if err != nil {
		return err
	}
	err = db.Exec(ctx,
		`INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, genres, production_year, synced_at, search_text,
		   season_number, episode_number, episode_number_end, series_external_id, season_name, original_title, sort_title, premiere_date, date_added,
		   overview, tagline, content_rating, community_rating, critic_rating, imdb_id, tmdb_id, tvdb_id,
		   width, height, video_codec, container, bitrate, production_locations, chapters,
		   image_primary_tag, image_backdrop_tag, image_thumb_tag, series_image_primary_tag)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		   ?, ?, ?, ?, ?, ?, ?, ?, ?,
		   ?, ?, ?, ?, ?, ?, ?, ?,
		   ?, ?, ?, ?, ?, ?, ?,
		   ?, ?, ?, ?)
		 ON CONFLICT (media_source_id, external_id) DO UPDATE SET
		   kind = excluded.kind, title = excluded.title, series_title = excluded.series_title,
		   duration_ms = excluded.duration_ms, source_modified_at = excluded.source_modified_at,
		   genres = excluded.genres, production_year = excluded.production_year,
		   synced_at = excluded.synced_at, search_text = excluded.search_text,
		   season_number = excluded.season_number, episode_number = excluded.episode_number,
		   episode_number_end = excluded.episode_number_end, series_external_id = excluded.series_external_id,
		   season_name = excluded.season_name, original_title = excluded.original_title,
		   sort_title = excluded.sort_title, premiere_date = excluded.premiere_date, date_added = excluded.date_added,
		   overview = excluded.overview, tagline = excluded.tagline, content_rating = excluded.content_rating,
		   community_rating = excluded.community_rating, critic_rating = excluded.critic_rating,
		   imdb_id = excluded.imdb_id, tmdb_id = excluded.tmdb_id, tvdb_id = excluded.tvdb_id,
		   width = excluded.width, height = excluded.height, video_codec = excluded.video_codec,
		   container = excluded.container, bitrate = excluded.bitrate,
		   production_locations = excluded.production_locations, chapters = excluded.chapters,
		   image_primary_tag = excluded.image_primary_tag, image_backdrop_tag = excluded.image_backdrop_tag,
		   image_thumb_tag = excluded.image_thumb_tag, series_image_primary_tag = excluded.series_image_primary_tag`,
		uuid.NewString(), tenantID, mediaSourceID, item.ExternalID, string(item.Kind), item.Title,
		item.SeriesTitle, item.DurationMs, item.SourceModifiedAt, item.Genres, item.ProductionYear, now,
		SearchText(item.Title, item.SeriesTitle),
		item.SeasonNumber, item.EpisodeNumber, item.EpisodeNumberEnd, item.SeriesExternalID, item.SeasonName,
		item.OriginalTitle, item.SortTitle, item.PremiereDate, item.DateAdded,
		item.Overview, item.Tagline, item.ContentRating, item.CommunityRating, item.CriticRating,
		item.ImdbID, item.TmdbID, item.TvdbID,
		item.Width, item.Height, item.VideoCodec, item.Container, item.Bitrate, locations, chapters,
		item.ImagePrimaryTag, item.ImageBackdropTag, item.ImageThumbTag, item.SeriesImagePrimaryTag,
	)
	if err != nil {
		return err
	}

	// The row keeps its original id on conflict, so look it up rather than
	// trusting the uuid generated above.
	var ids []string
	if err := db.Query(ctx, &ids,
		"SELECT id FROM catalog_item WHERE media_source_id = ? AND external_id = ?",
		mediaSourceID, item.ExternalID,
	); err != nil {
		return err
	}
	if len(ids) != 1 {
		return fmt.Errorf("catalogsync: catalog_item %s/%s not found after upsert", mediaSourceID, item.ExternalID)
	}
	return syncLinks(ctx, db, tenantID, ids[0], item)
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
