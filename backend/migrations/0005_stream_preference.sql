-- Tier 2 (user profile) and tiers 3-4 (admin: channel/global) of the
-- stream (audio/subtitle) selection cascade. Tier 1 (item/channel local
-- override) is client-only (shared_preferences) and never reaches the
-- backend schema at all.
--
-- `rules` on both tables stores a JSON-encoded ordered cascade
-- (domain.StreamRuleCascade) as free-form text — same shape as
-- filler_pool.criteria (migration 0003) — decoded/encoded by the API layer,
-- never interpreted in SQL.

-- One row per app_user: that user's personal default cascade, synced across
-- every device they log into.
CREATE TABLE stream_preference (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    rules      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (user_id)
);

-- Admin-authored tiers: scope_type mirrors filler_rule's scope_type/scope_id
-- shape, but with its own enum (global|channel) — filler_rule's
-- (channel|block) values don't apply here. scope_id is '' for
-- scope_type = 'global' (at most one row per tenant); a channel.id for
-- scope_type = 'channel'.
CREATE TABLE stream_preference_rule (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('global', 'channel')),
    scope_id   TEXT NOT NULL DEFAULT '',
    rules      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (tenant_id, scope_type, scope_id)
);
CREATE INDEX idx_stream_preference_rule_scope ON stream_preference_rule(scope_type, scope_id);
