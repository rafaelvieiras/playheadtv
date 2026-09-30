-- Issue #7: decouple the reusable "when do things air" rule (Schedule) from
-- the channel it airs on (the ErsatzTV Schedule/Playout split — see
-- docs/ARCHITECTURE.md "Scheduling model" and the issue's own "Context"
-- section). Channel keeps the "Playout" role implicitly: it just gains a
-- schedule_id pointing at the Schedule driving it, and still tracks its own
-- materialized epg_entry rows independently of any other channel sharing the
-- same Schedule.
--
-- schedule_entry (one channel, one recurrence rule per row) is replaced by:
--   - schedule: a reusable, named group of rules, not tied to any channel.
--   - schedule_item: one recurrence rule (schedule_entry's old shape minus
--     channel_id) belonging to a schedule.
--   - channel.schedule_id: which Schedule (if any) drives a channel. The
--     same Schedule can be assigned to more than one channel; a channel
--     needing no schedule at all (e.g. a pure dynamic-filler-pool channel,
--     already supported today) simply leaves this NULL.

CREATE TABLE schedule (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- One recurrence rule inside a schedule — schedule_entry's old shape, minus
-- channel_id (a schedule_item belongs to a Schedule, not to a channel
-- directly; a channel picks it up transitively via channel.schedule_id).
CREATE TABLE schedule_item (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    schedule_id      TEXT NOT NULL REFERENCES schedule(id) ON DELETE CASCADE,
    block_id         TEXT NOT NULL REFERENCES block(id),
    recurrence_type  TEXT NOT NULL CHECK (recurrence_type IN ('weekly', 'once')),
    day_of_week      INTEGER, -- 0 (Sunday)..6, required if recurrence_type = 'weekly'
    start_time_local TEXT,    -- "HH:MM", required if recurrence_type = 'weekly'
    start_at         INTEGER, -- epoch millis, required if recurrence_type = 'once'
    active           INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX idx_schedule_item_schedule ON schedule_item(schedule_id);

-- Nullable: a channel with no Schedule assigned relies only on its
-- channel-scoped fallback filler rule, exactly as today.
ALTER TABLE channel ADD COLUMN schedule_id TEXT REFERENCES schedule(id);

-- Data migration: one auto-created Schedule per channel that already has
-- schedule_entry rows (named after the channel, since there was no prior
-- concept of a Schedule's own name to preserve), its schedule_entry rows
-- carried over as schedule_item rows under it, keeping their original id so
-- epg_entry.source_schedule_entry_id values keep resolving correctly once
-- that column is repointed below — no id remapping needed.
INSERT INTO schedule (id, tenant_id, name, created_at, updated_at)
SELECT 'migrated-' || c.id, c.tenant_id, c.name || ' schedule',
       (SELECT MIN(created_at) FROM schedule_entry WHERE channel_id = c.id),
       (SELECT MAX(updated_at) FROM schedule_entry WHERE channel_id = c.id)
FROM channel c
WHERE EXISTS (SELECT 1 FROM schedule_entry se WHERE se.channel_id = c.id);

INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
SELECT id, tenant_id, 'migrated-' || channel_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at
FROM schedule_entry;

UPDATE channel SET schedule_id = 'migrated-' || id
WHERE EXISTS (SELECT 1 FROM schedule_entry se WHERE se.channel_id = channel.id);

-- epg_entry.source_schedule_entry_id -> source_schedule_item_id, now
-- referencing schedule_item(id) instead of the dropped schedule_entry(id).
-- SQLite can't repoint a FK's target table via ALTER, so the table is
-- rebuilt (values carry over unchanged; schedule_item reused the same ids
-- as schedule_entry above, so every existing non-NULL reference still
-- resolves).
CREATE TABLE epg_entry_new (
    id                      TEXT PRIMARY KEY,
    tenant_id               TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id              TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    kind                    TEXT NOT NULL CHECK (kind IN ('program', 'preroll', 'midroll', 'postroll', 'tail', 'fallback')),
    catalog_item_id         TEXT NOT NULL REFERENCES catalog_item(id),
    start_at                INTEGER NOT NULL,
    end_at                  INTEGER NOT NULL,
    source_schedule_item_id TEXT REFERENCES schedule_item(id), -- null for fallback entries
    generated_at            INTEGER NOT NULL
);
INSERT INTO epg_entry_new (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at)
SELECT id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_entry_id, generated_at
FROM epg_entry;
DROP TABLE epg_entry;
ALTER TABLE epg_entry_new RENAME TO epg_entry;
CREATE INDEX idx_epg_entry_channel_window ON epg_entry(channel_id, start_at);

DROP TABLE schedule_entry;
