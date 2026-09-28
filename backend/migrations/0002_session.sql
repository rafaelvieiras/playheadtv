-- Web UI / MCP session tokens for app_user. Device tokens don't need a
-- table of their own — they live directly on device.api_token_hash (see
-- 0001_init.sql) since a device has exactly one active token at a time.
CREATE TABLE session (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_session_user ON session(user_id);
