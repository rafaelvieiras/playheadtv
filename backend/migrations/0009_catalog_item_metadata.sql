-- Richer catalog metadata (issue #35). Single-valued facts are plain nullable
-- columns on catalog_item. Everything that can be queried or filtered later
-- (people, studios, tags, genres, languages) lives in per-tenant dimension
-- tables plus small link tables, with tenant_id leading every key and index
-- so a hosted multi-tenant deployment never scans across tenants. Only
-- purely descriptive data (production locations, chapters) is JSON text.
--
-- Dates (premiere_date, date_added) are epoch milliseconds, UTC. Image
-- columns hold Jellyfin image tags only: the backend never stores, serves or
-- caches art, clients fetch it from the media source.
ALTER TABLE catalog_item ADD COLUMN season_number INTEGER;
ALTER TABLE catalog_item ADD COLUMN episode_number INTEGER;
ALTER TABLE catalog_item ADD COLUMN episode_number_end INTEGER;
ALTER TABLE catalog_item ADD COLUMN series_external_id TEXT;
ALTER TABLE catalog_item ADD COLUMN season_name TEXT;
ALTER TABLE catalog_item ADD COLUMN original_title TEXT;
ALTER TABLE catalog_item ADD COLUMN sort_title TEXT;
ALTER TABLE catalog_item ADD COLUMN premiere_date INTEGER;
ALTER TABLE catalog_item ADD COLUMN date_added INTEGER;
ALTER TABLE catalog_item ADD COLUMN overview TEXT;
ALTER TABLE catalog_item ADD COLUMN tagline TEXT;
ALTER TABLE catalog_item ADD COLUMN content_rating TEXT;
ALTER TABLE catalog_item ADD COLUMN community_rating REAL;
ALTER TABLE catalog_item ADD COLUMN critic_rating REAL;
ALTER TABLE catalog_item ADD COLUMN imdb_id TEXT;
ALTER TABLE catalog_item ADD COLUMN tmdb_id TEXT;
ALTER TABLE catalog_item ADD COLUMN tvdb_id TEXT;
ALTER TABLE catalog_item ADD COLUMN width INTEGER;
ALTER TABLE catalog_item ADD COLUMN height INTEGER;
ALTER TABLE catalog_item ADD COLUMN video_codec TEXT;
ALTER TABLE catalog_item ADD COLUMN container TEXT;
ALTER TABLE catalog_item ADD COLUMN bitrate INTEGER;
ALTER TABLE catalog_item ADD COLUMN production_locations TEXT; -- JSON array of strings
ALTER TABLE catalog_item ADD COLUMN chapters TEXT;             -- JSON array of {name, start_ms}
ALTER TABLE catalog_item ADD COLUMN image_primary_tag TEXT;
ALTER TABLE catalog_item ADD COLUMN image_backdrop_tag TEXT;
ALTER TABLE catalog_item ADD COLUMN image_thumb_tag TEXT;
ALTER TABLE catalog_item ADD COLUMN series_image_primary_tag TEXT;

-- Parent key for the composite (tenant_id, catalog_item_id) foreign keys of
-- the link tables below, so ON DELETE CASCADE is index-assisted.
CREATE UNIQUE INDEX idx_catalog_item_tenant_id ON catalog_item(tenant_id, id);

-- Per-tenant dimension tables. name_norm is the accent/case-folded name
-- (catalogsync.FoldText), the identity within a tenant. name keeps the first
-- spelling seen, for display.
CREATE TABLE person (
    tenant_id TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    name_norm TEXT NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name_norm)
) WITHOUT ROWID;

CREATE TABLE studio (
    tenant_id TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    name_norm TEXT NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name_norm)
) WITHOUT ROWID;

CREATE TABLE tag (
    tenant_id TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    name_norm TEXT NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name_norm)
) WITHOUT ROWID;

CREATE TABLE genre (
    tenant_id TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    name_norm TEXT NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name_norm)
) WITHOUT ROWID;

CREATE TABLE language (
    tenant_id TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL,
    name_norm TEXT NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name_norm)
) WITHOUT ROWID;

-- Link tables. kind distinguishes actor/director on people and audio/subtitle
-- on languages. position keeps billing order for people.
CREATE TABLE catalog_item_person (
    tenant_id       TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL,
    person_id       TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('actor', 'director')),
    role            TEXT,
    position        INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, catalog_item_id, person_id, kind),
    FOREIGN KEY (tenant_id, catalog_item_id) REFERENCES catalog_item(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, person_id) REFERENCES person(tenant_id, id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_catalog_item_person_by_person ON catalog_item_person(tenant_id, person_id, kind, catalog_item_id);

CREATE TABLE catalog_item_studio (
    tenant_id       TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL,
    studio_id       TEXT NOT NULL,
    PRIMARY KEY (tenant_id, catalog_item_id, studio_id),
    FOREIGN KEY (tenant_id, catalog_item_id) REFERENCES catalog_item(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, studio_id) REFERENCES studio(tenant_id, id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_catalog_item_studio_by_studio ON catalog_item_studio(tenant_id, studio_id, catalog_item_id);

CREATE TABLE catalog_item_tag (
    tenant_id       TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL,
    tag_id          TEXT NOT NULL,
    PRIMARY KEY (tenant_id, catalog_item_id, tag_id),
    FOREIGN KEY (tenant_id, catalog_item_id) REFERENCES catalog_item(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, tag_id) REFERENCES tag(tenant_id, id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_catalog_item_tag_by_tag ON catalog_item_tag(tenant_id, tag_id, catalog_item_id);

CREATE TABLE catalog_item_genre (
    tenant_id       TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL,
    genre_id        TEXT NOT NULL,
    PRIMARY KEY (tenant_id, catalog_item_id, genre_id),
    FOREIGN KEY (tenant_id, catalog_item_id) REFERENCES catalog_item(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, genre_id) REFERENCES genre(tenant_id, id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_catalog_item_genre_by_genre ON catalog_item_genre(tenant_id, genre_id, catalog_item_id);

CREATE TABLE catalog_item_language (
    tenant_id       TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL,
    language_id     TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('audio', 'subtitle')),
    PRIMARY KEY (tenant_id, catalog_item_id, language_id, kind),
    FOREIGN KEY (tenant_id, catalog_item_id) REFERENCES catalog_item(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, language_id) REFERENCES language(tenant_id, id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_catalog_item_language_by_language ON catalog_item_language(tenant_id, language_id, kind, catalog_item_id);
