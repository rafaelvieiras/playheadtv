-- Break templates: an ordered list of steps that replaces the single item a
-- preroll, midroll or postroll rule inserts at a program boundary.
--
-- A template is scoped like a filler_rule (a whole channel or one block) and
-- attaches to one boundary kind. Where it applies it wins over a plain rule of
-- the same kind at the same scope level (docs/ARCHITECTURE.md "Break
-- templates"). Existing filler_rule rows are untouched: a plain rule keeps
-- behaving exactly as before, as the one-step case.
--
-- A step is either 'single' (one item from a pool, picked by its strategy) or
-- 'fill' (whole items from a pool until target_ms is filled, with up to
-- max_fit_attempts rejected candidates in a row). The API bounds the step count
-- (10), target_ms (1 hour) and max_fit_attempts (20).
CREATE TABLE break_template (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('channel', 'block')),
    scope_id   TEXT NOT NULL, -- channel.id or block.id, per scope_type
    kind       TEXT NOT NULL CHECK (kind IN ('preroll', 'midroll', 'postroll')),
    name       TEXT NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0, -- order among templates of the same kind+scope
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_break_template_scope ON break_template(scope_type, scope_id);

CREATE TABLE break_template_step (
    id                 TEXT PRIMARY KEY,
    break_template_id  TEXT NOT NULL REFERENCES break_template(id) ON DELETE CASCADE,
    position           INTEGER NOT NULL,
    step_type          TEXT NOT NULL CHECK (step_type IN ('single', 'fill')),
    filler_pool_id     TEXT NOT NULL REFERENCES filler_pool(id),
    selection_strategy TEXT NOT NULL CHECK (selection_strategy IN ('sequential', 'random', 'weighted')),
    target_ms          INTEGER NOT NULL DEFAULT 0, -- 'fill' only
    max_fit_attempts   INTEGER NOT NULL DEFAULT 1 CHECK (max_fit_attempts BETWEEN 1 AND 20)
);
CREATE INDEX idx_break_template_step_template ON break_template_step(break_template_id, position);
