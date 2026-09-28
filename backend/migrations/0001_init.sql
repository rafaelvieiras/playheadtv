-- Playhead TV backend schema.
-- SQLite dialect, compatible with both a local file (self-hosted default) and
-- Cloudflare D1 (cloud-sync tier, accessed over its HTTP API). See
-- docs/ARCHITECTURE.md for the storage backend abstraction and the reasoning
-- behind every table below.
--
-- All timestamps are Unix epoch milliseconds unless the column name says
-- "_local" (wall-clock string, resolved against channel.timezone).

CREATE TABLE tenant (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    tier       TEXT NOT NULL CHECK (tier IN ('self_hosted', 'cloud_sync')),
    created_at INTEGER NOT NULL
);

-- Web UI / MCP auth principal. Named app_user (not "user") to avoid clashing
-- with the SQL keyword in some dialects/tools.
CREATE TABLE app_user (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    UNIQUE (tenant_id, email)
);

-- A playback client (desktop/mobile/TV app). Also the identity behind push
-- catalog syncs when a media_source is unreachable from the backend.
CREATE TABLE device (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    platform       TEXT NOT NULL, -- linux | windows | android | android_tv | ios | tvos
    api_token_hash TEXT NOT NULL,
    last_seen_at   INTEGER,
    created_at     INTEGER NOT NULL
);
CREATE INDEX idx_device_tenant ON device(tenant_id);

-- A Jellyfin/Plex/local-folder library. sync_mode is decided automatically by
-- a reachability probe at creation time (pull if the backend can reach it
-- directly, push otherwise — see docs/ARCHITECTURE.md "Media sources").
CREATE TABLE media_source (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    type              TEXT NOT NULL CHECK (type IN ('jellyfin', 'plex', 'local_folder')), -- only 'jellyfin' implemented in v1
    name              TEXT NOT NULL,
    base_url          TEXT,   -- null for local_folder
    credential_cipher BLOB,   -- encrypted API key/token, null for local_folder
    sync_mode         TEXT NOT NULL CHECK (sync_mode IN ('pull', 'push')),
    last_probe_at     INTEGER,
    last_probe_ok     INTEGER, -- 0/1
    last_synced_at    INTEGER, -- meaningful in push mode
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
CREATE INDEX idx_media_source_tenant ON media_source(tenant_id);

-- Source-agnostic cache of just enough metadata to schedule an item.
-- block_item/filler_pool_item always reference catalog_item.id, never a
-- source's native ID directly — this is what lets one channel mix items from
-- different media sources without special-casing anything downstream.
CREATE TABLE catalog_item (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    media_source_id    TEXT NOT NULL REFERENCES media_source(id) ON DELETE CASCADE,
    external_id        TEXT NOT NULL, -- native ID in the source (e.g. Jellyfin item GUID)
    kind               TEXT NOT NULL CHECK (kind IN ('movie', 'episode', 'other')),
    title              TEXT NOT NULL,
    series_title       TEXT,          -- set only when kind = 'episode'
    duration_ms        INTEGER NOT NULL,
    source_modified_at INTEGER,       -- source's own last-modified marker, used to diff push syncs
    synced_at          INTEGER NOT NULL,
    UNIQUE (media_source_id, external_id)
);
CREATE INDEX idx_catalog_item_tenant ON catalog_item(tenant_id);
CREATE INDEX idx_catalog_item_title ON catalog_item(tenant_id, title);

CREATE TABLE channel (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    number        INTEGER NOT NULL,
    name          TEXT NOT NULL,
    guide_art_url TEXT,
    timezone      TEXT NOT NULL DEFAULT 'UTC', -- IANA name; resolves schedule_entry.start_time_local
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE (tenant_id, number)
);

-- A reusable programming template (e.g. "Sitcom Night"): an ordered list of
-- catalog items, scheduled onto one or more channels via schedule_entry.
CREATE TABLE block (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE block_item (
    id               TEXT PRIMARY KEY,
    block_id         TEXT NOT NULL REFERENCES block(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL,
    catalog_item_id  TEXT NOT NULL REFERENCES catalog_item(id),
    trim_duration_ms INTEGER, -- optional override (e.g. artificial trim, as used in the client spike)
    UNIQUE (block_id, position)
);

-- Named collection of filler content (commercials, bumpers, station IDs)
-- that a filler_rule draws from.
CREATE TABLE filler_pool (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE filler_pool_item (
    id              TEXT PRIMARY KEY,
    filler_pool_id  TEXT NOT NULL REFERENCES filler_pool(id) ON DELETE CASCADE,
    catalog_item_id TEXT NOT NULL REFERENCES catalog_item(id),
    weight          INTEGER NOT NULL DEFAULT 1 -- used by the 'weighted' selection_strategy
);

-- ErsatzTV-inspired filler slots (PreRoll/MidRoll/PostRoll/Tail/Fallback),
-- scoped to either a whole channel or a single block.
CREATE TABLE filler_rule (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    scope_type         TEXT NOT NULL CHECK (scope_type IN ('channel', 'block')),
    scope_id           TEXT NOT NULL, -- channel.id or block.id, per scope_type
    kind               TEXT NOT NULL CHECK (kind IN ('preroll', 'midroll', 'postroll', 'tail', 'fallback')),
    filler_pool_id     TEXT NOT NULL REFERENCES filler_pool(id),
    selection_strategy TEXT NOT NULL CHECK (selection_strategy IN ('sequential', 'random', 'weighted')),
    position           INTEGER NOT NULL DEFAULT 0, -- order among rules of the same kind+scope
    created_at         INTEGER NOT NULL
);
CREATE INDEX idx_filler_rule_scope ON filler_rule(scope_type, scope_id);

-- When a block airs on a channel: either a weekly recurrence (day_of_week +
-- start_time_local) or a one-off absolute start_at. This is the input to EPG
-- materialization, never read directly by clients.
CREATE TABLE schedule_entry (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id       TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    block_id         TEXT NOT NULL REFERENCES block(id),
    recurrence_type  TEXT NOT NULL CHECK (recurrence_type IN ('weekly', 'once')),
    day_of_week      INTEGER, -- 0 (Sunday)..6, required if recurrence_type = 'weekly'
    start_time_local TEXT,    -- "HH:MM", required if recurrence_type = 'weekly'
    start_at         INTEGER, -- epoch millis, required if recurrence_type = 'once'
    active           INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX idx_schedule_entry_channel ON schedule_entry(channel_id);

-- Materialized, flattened timeline — the ONLY table GET /epg reads. Produced
-- asynchronously by the EPG materializer whenever schedule_entry/block/
-- block_item/filler_rule/filler_pool_item change, and rolled forward on a
-- timer so a window (e.g. 48h) stays materialized ahead of "now". No request
-- ever resolves a schedule on the fly.
CREATE TABLE epg_entry (
    id                       TEXT PRIMARY KEY,
    tenant_id                TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    channel_id               TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    kind                     TEXT NOT NULL CHECK (kind IN ('program', 'preroll', 'midroll', 'postroll', 'tail', 'fallback')),
    catalog_item_id          TEXT NOT NULL REFERENCES catalog_item(id),
    start_at                 INTEGER NOT NULL,
    end_at                   INTEGER NOT NULL,
    source_schedule_entry_id TEXT REFERENCES schedule_entry(id), -- null for fallback entries
    generated_at             INTEGER NOT NULL
);
CREATE INDEX idx_epg_entry_channel_window ON epg_entry(channel_id, start_at);

-- One row per tenant: the cheap version marker GET /epg/meta serves so
-- clients can poll for staleness without paying for the full EPG payload.
CREATE TABLE epg_revision (
    tenant_id   TEXT PRIMARY KEY REFERENCES tenant(id) ON DELETE CASCADE,
    revision    INTEGER NOT NULL DEFAULT 0,
    valid_until INTEGER NOT NULL, -- end of the materialized window
    updated_at  INTEGER NOT NULL
);
