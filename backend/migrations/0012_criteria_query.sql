-- Composable catalog queries (issue #5, stage 1).
--
-- 1. block.criteria and filler_pool.criteria now hold a versioned condition
--    tree instead of the fixed {kind, genres, year_from, year_to} shape. SQL
--    cannot compare matched sets or normalize names, so the conversion is not
--    done here: at startup catalogsync.MigrateLegacyCriteria rewrites every
--    legacy value whose matched set is unchanged and records the others in
--    criteria_migration_review, leaving their legacy value in place and
--    working until a human applies or edits the proposed query.
CREATE TABLE criteria_migration_review (
    tenant_id       TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    owner_type      TEXT NOT NULL CHECK (owner_type IN ('block', 'filler_pool')),
    owner_id        TEXT NOT NULL,
    legacy_criteria TEXT NOT NULL,
    proposed_query  TEXT,            -- NULL when the legacy value cannot be translated
    problem         TEXT,
    before_count    INTEGER NOT NULL,
    after_count     INTEGER NOT NULL,
    only_before     TEXT NOT NULL,   -- JSON array of {id, title, series_title}, capped
    only_after      TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, owner_type, owner_id)
) WITHOUT ROWID;

-- 2. The overview field of a query is an accent- and case-insensitive
--    "contains". SQLite cannot fold accents, so the folded text
--    (catalogsync.FoldText) is stored next to the overview, written on every
--    sync and backfilled at startup for existing rows (NULL until then).
ALTER TABLE catalog_item ADD COLUMN overview_norm TEXT;

-- 3. A third block item type next to 'catalog' and 'series': 'criteria', a
--    condition tree expanded by the materializer into N of its matches. Its
--    configuration lives in spec ({query, playback_order, seed, amount}) and
--    catalog_item_id is NULL, like a series item.
--
--    SQLite cannot widen a CHECK in place, so block_item is rebuilt (same
--    pattern as 0010; foreign keys are off for the rebuild and series_cursor's
--    reference to block_item(id) resolves to the new table by name).
PRAGMA foreign_keys = OFF;

CREATE TABLE block_item_new (
    id               TEXT PRIMARY KEY,
    block_id         TEXT NOT NULL REFERENCES block(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL,
    item_type        TEXT NOT NULL DEFAULT 'catalog' CHECK (item_type IN ('catalog', 'series', 'criteria')),
    catalog_item_id  TEXT REFERENCES catalog_item(id),
    trim_duration_ms INTEGER,
    spec             TEXT,
    UNIQUE (block_id, position),
    CHECK (
        (item_type = 'catalog' AND catalog_item_id IS NOT NULL)
        OR (item_type IN ('series', 'criteria') AND catalog_item_id IS NULL AND spec IS NOT NULL)
    )
);
INSERT INTO block_item_new (id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec)
SELECT id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec
FROM block_item;
DROP TABLE block_item;
ALTER TABLE block_item_new RENAME TO block_item;
CREATE INDEX idx_block_item_catalog_item ON block_item(catalog_item_id);

PRAGMA foreign_keys = ON;
