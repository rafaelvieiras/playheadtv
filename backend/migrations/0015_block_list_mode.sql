-- Manual blocks: repeat the item list or play it once.
--
-- block.list_mode is how a manual block plays its list: 'repeat' (when the
-- list ends it starts again from the first item, until the next timed item or,
-- for a continuous item, indefinitely) or 'once' (the list airs once). The
-- column default makes every existing block repeat, as decided for this
-- change; a dynamic block ignores it (it always loops by its own rules).
ALTER TABLE block ADD COLUMN list_mode TEXT NOT NULL DEFAULT 'repeat' CHECK (list_mode IN ('repeat', 'once'));

-- Blocks that now repeat but are scheduled at fixed times (weekly or once
-- items) may see their grid change: a list shorter than its slot used to end
-- and leave the rest to the tail filler, and now starts over. They are listed
-- for review (GET /blocks/list-mode-review) so the operator can mark the
-- ones that should keep playing once. A row goes away when the block's mode is
-- written explicitly, or with the block.
CREATE TABLE block_list_mode_review (
    tenant_id  TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    block_id   TEXT NOT NULL REFERENCES block(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, block_id)
) WITHOUT ROWID;

INSERT INTO block_list_mode_review (tenant_id, block_id, created_at)
SELECT b.tenant_id, b.id, CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM block b
WHERE b.collection_id IS NULL AND b.criteria IS NULL
  AND EXISTS (SELECT 1 FROM block_item bi WHERE bi.block_id = b.id)
  AND EXISTS (SELECT 1 FROM schedule_item si WHERE si.block_id = b.id AND si.recurrence_type IN ('weekly', 'once'));
