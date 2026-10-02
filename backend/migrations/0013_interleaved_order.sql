-- Interleaved-by-series playback order.
--
-- 1. A dynamic block (block.criteria set) used to have no playback settings:
--    every match by title, looped. block.playback_order makes that explicit
--    and adds 'interleaved' (round-robin across series). The default 'title'
--    is exactly the old behavior, so existing rows keep working untouched.
--    interleave_spec holds the JSON parameters ({per_round, on_exhaustion,
--    series_order}), NULL unless the order is interleaved; playback_seed makes
--    its shuffle/random series order reproducible (mixed with the channel id).
ALTER TABLE block ADD COLUMN playback_order TEXT NOT NULL DEFAULT 'title';
ALTER TABLE block ADD COLUMN interleave_spec TEXT;
ALTER TABLE block ADD COLUMN playback_seed INTEGER NOT NULL DEFAULT 0;

-- 2. The round of an interleaved walk an epg_entry belongs to, next to
--    series_cycle: the materializer rebuilds the walk's cursor from these.
ALTER TABLE epg_entry ADD COLUMN series_round INTEGER;

-- 3. Per channel and interleaved dynamic block: the instant its cursor
--    started (same role as series_cursor for a block item; a dynamic block's
--    items are recreated on every catalog change, so the cursor is keyed by
--    the block). epg_entry.source_block_item_id holds the block id for these.
CREATE TABLE block_cursor (
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    block_id   TEXT NOT NULL REFERENCES block(id) ON DELETE CASCADE,
    origin_ms  INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, channel_id, block_id)
) WITHOUT ROWID;
CREATE INDEX idx_block_cursor_block ON block_cursor(tenant_id, block_id);
