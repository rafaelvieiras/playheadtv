-- Continuous schedule items: a schedule_item with no fixed time that fills
-- every instant not claimed by a timed item, rotating through its block.
-- SQLite cannot alter a CHECK constraint in place, so schedule_item is
-- rebuilt. Foreign keys are switched off for the rebuild (each statement of
-- this file runs on the single pooled connection, outside any transaction, so
-- the pragma sticks) because epg_entry references schedule_item and dropping
-- the old table would otherwise trip the FK check.
PRAGMA foreign_keys = OFF;

CREATE TABLE schedule_item_new (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    schedule_id      TEXT NOT NULL REFERENCES schedule(id) ON DELETE CASCADE,
    block_id         TEXT NOT NULL REFERENCES block(id),
    recurrence_type  TEXT NOT NULL CHECK (recurrence_type IN ('weekly', 'once', 'continuous')),
    day_of_week      INTEGER,
    start_time_local TEXT,
    start_at         INTEGER,
    active           INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
INSERT INTO schedule_item_new (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
SELECT id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at
FROM schedule_item;
DROP TABLE schedule_item;
ALTER TABLE schedule_item_new RENAME TO schedule_item;
CREATE INDEX idx_schedule_item_schedule ON schedule_item(schedule_id);

PRAGMA foreign_keys = ON;

-- Rotation position persisted with the entries a continuous item emitted:
-- the index into the block's item list that the NEXT program of that item
-- takes. A regeneration resumes from the last kept entry's value, so the
-- rotation survives across materializer runs and restarts.
ALTER TABLE epg_entry ADD COLUMN rotation_next INTEGER;
