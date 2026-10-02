-- Typed block items. A block_item is either a single catalog item (the only
-- kind until now) or a series (item_type = 'series'): "this show, in this
-- order", expanded into its episodes by the materializer. Type-specific
-- configuration lives in the JSON column spec so further kinds (a criteria
-- item, a series slice) add a type value and a spec shape, not columns. For
-- a series item catalog_item_id is NULL and spec holds
-- {media_source_id, series_external_id?, series_title?, playback_order,
-- seed, slice}. Existing rows become item_type = 'catalog' unchanged.
--
-- SQLite cannot relax a NOT NULL or add a CHECK in place, so block_item is
-- rebuilt. Foreign keys are switched off for the rebuild (same pattern as
-- 0007); nothing references block_item.
PRAGMA foreign_keys = OFF;

CREATE TABLE block_item_new (
    id               TEXT PRIMARY KEY,
    block_id         TEXT NOT NULL REFERENCES block(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL,
    item_type        TEXT NOT NULL DEFAULT 'catalog' CHECK (item_type IN ('catalog', 'series')),
    catalog_item_id  TEXT REFERENCES catalog_item(id),
    trim_duration_ms INTEGER,
    spec             TEXT,
    UNIQUE (block_id, position),
    CHECK (
        (item_type = 'catalog' AND catalog_item_id IS NOT NULL)
        OR (item_type = 'series' AND catalog_item_id IS NULL AND spec IS NOT NULL)
    )
);
INSERT INTO block_item_new (id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec)
SELECT id, block_id, position, 'catalog', catalog_item_id, trim_duration_ms, NULL
FROM block_item;
DROP TABLE block_item;
ALTER TABLE block_item_new RENAME TO block_item;
CREATE INDEX idx_block_item_catalog_item ON block_item(catalog_item_id);

PRAGMA foreign_keys = ON;

-- Series lookups: the episodes of a series, by its external id or by title.
CREATE INDEX idx_catalog_item_series_ext ON catalog_item(tenant_id, media_source_id, series_external_id) WHERE kind = 'episode';
CREATE INDEX idx_catalog_item_series_title ON catalog_item(tenant_id, media_source_id, series_title) WHERE kind = 'episode';

-- Which series item (and which cycle of its playback order) produced an
-- epg_entry. The materializer rebuilds a series cursor from these rows, so
-- grid regeneration continues where the kept entries left off. No foreign key
-- on source_block_item_id: editing a block recreates its items and old rows
-- are only history.
ALTER TABLE epg_entry ADD COLUMN source_block_item_id TEXT;
ALTER TABLE epg_entry ADD COLUMN series_cycle INTEGER;
CREATE INDEX idx_epg_entry_series ON epg_entry(tenant_id, channel_id, source_block_item_id, start_at);

-- Per channel and series item: the instant its cursor started. A reset, or a
-- change of playback order, moves it to "now"; entries that start earlier
-- are ignored when rebuilding the cursor. Created the first time the
-- materializer meets the item on a channel.
CREATE TABLE series_cursor (
    tenant_id     TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id    TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    block_item_id TEXT NOT NULL REFERENCES block_item(id) ON DELETE CASCADE,
    origin_ms     INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, channel_id, block_item_id)
) WITHOUT ROWID;
CREATE INDEX idx_series_cursor_item ON series_cursor(tenant_id, block_item_id);
