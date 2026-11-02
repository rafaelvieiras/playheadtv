-- Plugins: an out-of-process artifact that provides a content source the core
-- does not implement itself (docs/PLUGINS.md). The core knows how to install,
-- verify and run a plugin, and nothing about any particular site.
--
-- One row per installed plugin, for the platform this backend runs on: a
-- plugin is installed as a whole (download, hash check, extract), so a version
-- never half-exists. Disabling keeps the row (and the extracted files) so it
-- can be turned back on without a download.
CREATE TABLE plugin (
    id              TEXT PRIMARY KEY,
    version         TEXT NOT NULL,
    protocol        INTEGER NOT NULL,
    os              TEXT NOT NULL,
    arch            TEXT NOT NULL,
    artifact_sha256 TEXT NOT NULL,
    entrypoint      TEXT NOT NULL,
    capabilities    TEXT NOT NULL, -- comma-separated, as declared by the manifest
    enabled         INTEGER NOT NULL DEFAULT 1,
    installed_at    INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- A plugin-backed media source names the plugin that lists it, the oldest
-- plugin version that can read its configuration, and that configuration
-- itself. base_url stays unused by plugin sources and credential_cipher is not
-- where a plugin's token belongs (a plugin keeps its own): plugin_config_json
-- is plain text on purpose and must never hold a secret.
--
-- SQLite cannot add a CHECK in place, and the original table only accepted
-- jellyfin/plex/local_folder, so media_source is rebuilt (same pattern as
-- 0007 and 0010). Foreign keys are switched off for the rebuild because
-- catalog_item references this table.
PRAGMA foreign_keys = OFF;

CREATE TABLE media_source_new (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    type               TEXT NOT NULL CHECK (type IN ('jellyfin', 'plex', 'local_folder', 'plugin')),
    name               TEXT NOT NULL,
    base_url           TEXT,   -- null for local_folder and plugin
    credential_cipher  BLOB,   -- encrypted API key/token, null when unused
    sync_mode          TEXT NOT NULL CHECK (sync_mode IN ('pull', 'push')),
    plugin_id          TEXT,
    plugin_min_version TEXT,
    plugin_config_json TEXT,
    last_probe_at      INTEGER,
    last_probe_ok      INTEGER, -- 0/1
    last_synced_at     INTEGER, -- meaningful in push mode
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    -- The plugin columns exist only on a plugin row, and a plugin row always
    -- names its plugin: neither half can be half-set.
    CHECK (
        (type = 'plugin' AND plugin_id IS NOT NULL)
        OR (type != 'plugin' AND plugin_id IS NULL AND plugin_min_version IS NULL AND plugin_config_json IS NULL)
    )
);
INSERT INTO media_source_new (id, tenant_id, type, name, base_url, credential_cipher, sync_mode,
    last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
SELECT id, tenant_id, type, name, base_url, credential_cipher, sync_mode,
    last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at
FROM media_source;
DROP TABLE media_source;
ALTER TABLE media_source_new RENAME TO media_source;
CREATE INDEX idx_media_source_tenant ON media_source(tenant_id);
-- Which sources depend on a plugin: "I want to remove this plugin, what breaks".
CREATE INDEX idx_media_source_plugin ON media_source(plugin_id) WHERE plugin_id IS NOT NULL;

PRAGMA foreign_keys = ON;
