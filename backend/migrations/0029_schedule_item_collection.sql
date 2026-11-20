-- Issue #100: a schedule item can target a collection directly, with its own
-- playback order, instead of requiring a block wrapper.
--
-- Before: schedule_item.block_id NOT NULL, so "air this collection 24/7" needed
-- a collection, a block, a criteria block item, a schedule and a schedule item
-- — five objects for one rule, and the only order a dynamic block accepted was
-- title/interleaved.
--
-- After: block_id and collection_id are mutually exclusive; an item that names
-- a collection airs its matches in playback_order (title, release_date,
-- random, shuffle or interleaved, the same orders a criteria block item
-- accepts) and needs no block at all.
--
-- SQLite cannot relax a NOT NULL or add a CHECK in place, so schedule_item is
-- rebuilt. Foreign keys are switched off for the rebuild (each statement of
-- this file runs on the single pooled connection, outside any transaction, so
-- the pragma sticks) because epg_entry references schedule_item and dropping
-- the old table would otherwise trip the FK check.
PRAGMA foreign_keys = OFF;

CREATE TABLE schedule_item_new (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    schedule_id      TEXT NOT NULL REFERENCES schedule(id) ON DELETE CASCADE,
    block_id         TEXT REFERENCES block(id),
    collection_id    TEXT REFERENCES collection(id),
    playback_order   TEXT,
    recurrence_type  TEXT NOT NULL CHECK (recurrence_type IN ('weekly', 'once', 'continuous')),
    days_of_week     TEXT NOT NULL DEFAULT '',
    start_time_local TEXT,
    start_at         INTEGER,
    active           INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    -- Exactly one target: a block or a collection, never both and never none.
    CHECK ((block_id IS NOT NULL AND collection_id IS NULL)
        OR (block_id IS NULL AND collection_id IS NOT NULL)),
    -- playback_order belongs to a collection item and is required there.
    CHECK ((collection_id IS NULL AND playback_order IS NULL)
        OR (collection_id IS NOT NULL AND playback_order IS NOT NULL))
);
INSERT INTO schedule_item_new (id, tenant_id, schedule_id, block_id, collection_id, playback_order,
                               recurrence_type, days_of_week, start_time_local, start_at, active,
                               created_at, updated_at)
SELECT id, tenant_id, schedule_id, block_id, NULL, NULL,
       recurrence_type, days_of_week, start_time_local, start_at, active,
       created_at, updated_at
FROM schedule_item;
DROP TABLE schedule_item;
ALTER TABLE schedule_item_new RENAME TO schedule_item;
CREATE INDEX idx_schedule_item_schedule ON schedule_item(schedule_id);
CREATE INDEX idx_schedule_item_collection ON schedule_item(collection_id);

PRAGMA foreign_keys = ON;

-- The playback cursor of a collection schedule item, per channel: the instant
-- its walk started. Same role series_cursor plays for a block item and
-- block_cursor for a dynamic block, and it cannot live in either (both key on
-- the id of a block or a block item, with a foreign key to it). A reset or a
-- playback order change moves it to "now"; entries that start earlier are
-- ignored when the cursor is rebuilt.
--
-- epg_entry.source_block_item_id holds the schedule item id for these walks
-- (that column has no foreign key), so the existing rebuild of a walk from its
-- own entries keeps working unchanged.
CREATE TABLE schedule_item_cursor (
    tenant_id        TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id       TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    schedule_item_id TEXT NOT NULL REFERENCES schedule_item(id) ON DELETE CASCADE,
    origin_ms        INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, channel_id, schedule_item_id)
) WITHOUT ROWID;
CREATE INDEX idx_schedule_item_cursor_item ON schedule_item_cursor(tenant_id, schedule_item_id);
