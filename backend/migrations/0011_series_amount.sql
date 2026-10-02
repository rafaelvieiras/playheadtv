-- How much of a series one visit of the block rotation airs. The amount lives
-- in the item's JSON spec as {"mode": "count"|"slot_end"|"series_end",
-- "count": N}. Series items stored before amounts existed aired until the end
-- of the slot, so they are marked "slot_end" explicitly and keep that
-- behavior; only items created from now on without an amount default to one
-- episode.
UPDATE block_item
SET spec = json_set(spec, '$.amount', json('{"mode":"slot_end"}'))
WHERE item_type = 'series' AND json_extract(spec, '$.amount') IS NULL;
