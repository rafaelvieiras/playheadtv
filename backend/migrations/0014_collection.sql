-- The collection entity (issue #5, stage 2).
--
-- 1. A collection is a named, reusable catalog query: only the condition tree
--    (catalogsync.Query, the same JSON block.criteria held in stage 1), a name
--    and a description. Playback order, amount, interleave settings and seeds
--    stay on whatever uses the collection. Names are labels, not identities:
--    they are not unique (the migration names a collection after its first
--    owner, and an owner of another kind may carry the same name).
CREATE TABLE collection (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    query       TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX idx_collection_tenant_name ON collection(tenant_id, name);

-- 2. Dynamic blocks and filler pools reference a collection. The default NO ACTION
--    (checked at the end of the statement, so deleting a tenant still cascades
--    through both tables) is the safety net behind the API's 409 for a
--    collection that is in use. SQLite
--    cannot check that the collection belongs to the same tenant, so the API
--    does. The old criteria columns are NOT dropped: they are deprecated, kept
--    as the pre-migration snapshot (the way back from a failed migration), and
--    only read while collection_id is NULL, i.e. for a legacy row still held
--    for review. Setting collection_id is what makes a row stop reading them.
ALTER TABLE block ADD COLUMN collection_id TEXT REFERENCES collection(id);
ALTER TABLE filler_pool ADD COLUMN collection_id TEXT REFERENCES collection(id);
CREATE INDEX idx_block_collection ON block(tenant_id, collection_id) WHERE collection_id IS NOT NULL;
CREATE INDEX idx_filler_pool_collection ON filler_pool(tenant_id, collection_id) WHERE collection_id IS NOT NULL;

-- 3. A criteria block item references its collection as "collection_id"
--    inside block_item.spec (the embedded "query" of stage 1 is deprecated and
--    ignored once collection_id is there). block_item has no tenant_id (the
--    block carries it), so this expression index leads with the collection id:
--    it serves "which items use collection X".
CREATE INDEX idx_block_item_collection ON block_item(json_extract(spec, '$.collection_id')) WHERE item_type = 'criteria';

-- 4. The review table also lists criteria items whose query could not be
--    moved into a collection without changing what they match. SQLite cannot
--    widen a CHECK in place, so the table is rebuilt.
CREATE TABLE criteria_migration_review_new (
    tenant_id       TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    owner_type      TEXT NOT NULL CHECK (owner_type IN ('block', 'filler_pool', 'block_item')),
    owner_id        TEXT NOT NULL,
    legacy_criteria TEXT NOT NULL,
    proposed_query  TEXT,
    problem         TEXT,
    before_count    INTEGER NOT NULL,
    after_count     INTEGER NOT NULL,
    only_before     TEXT NOT NULL,
    only_after      TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, owner_type, owner_id)
) WITHOUT ROWID;
INSERT INTO criteria_migration_review_new SELECT tenant_id, owner_type, owner_id, legacy_criteria, proposed_query, problem, before_count, after_count, only_before, only_after, created_at FROM criteria_migration_review;
DROP TABLE criteria_migration_review;
ALTER TABLE criteria_migration_review_new RENAME TO criteria_migration_review;
