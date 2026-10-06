-- Cut rule sets: where inside an item a break goes (the cut-point time model,
-- docs/ARCHITECTURE.md "Cut points").
--
-- A set is scoped like a filler_rule (a whole channel or one block). Its
-- ordered rules, the optional default strategy and the guard rails are one JSON
-- document in `config` (domain.CutConfig): they have no foreign keys, are
-- always read and written whole, and are evaluated by internal/cutplan, which
-- is the one place that knows their shape. Among the sets of one scope the
-- lowest position wins; a block's set beats its channel's. Nothing reads the
-- table at generation time yet (the materializer is not wired to it).
CREATE TABLE cut_rule_set (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('channel', 'block')),
    scope_id   TEXT NOT NULL, -- channel.id or block.id, per scope_type
    name       TEXT NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0, -- order among sets of the same scope
    config     TEXT NOT NULL, -- JSON domain.CutConfig
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_cut_rule_set_scope ON cut_rule_set(scope_type, scope_id);
