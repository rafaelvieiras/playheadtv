-- catalog_item gains genre/year so channels can be curated by content
-- (comedy, action, romance, decade, cartoons) instead of by hand. genres is
-- a comma-joined list in whatever language the source provided (Jellyfin
-- mixes pt-BR movie genres with pt/en series genres — no normalization
-- here, callers match against both spellings). production_year is nullable
-- since not every source item necessarily carries one.
ALTER TABLE catalog_item ADD COLUMN genres TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_item ADD COLUMN production_year INTEGER;

-- filler_pool gains an optional dynamic-membership rule. NULL keeps today's
-- behavior (a manually managed list via filler_pool_item). Non-NULL makes
-- catalogsync.ResolveDynamicPools own filler_pool_item for this pool,
-- recomputed after every catalog sync (pull or push) so a channel built
-- from a criteria (e.g. genre=Comedy) picks up newly synced matching items
-- automatically instead of needing a one-off script re-run.
ALTER TABLE filler_pool ADD COLUMN criteria TEXT;
