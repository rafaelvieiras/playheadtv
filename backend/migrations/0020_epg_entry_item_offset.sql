-- A program can be played in segments: one catalog item cut at some offsets,
-- with a break between consecutive segments.
--
-- item_offset_ms is where in the catalog item the entry starts playing (0 for
-- every ordinary entry, and for the first segment of a split item); the entry
-- plays for end_at - start_at from there, which for a segment is shorter than
-- the item. split_group ties the segments of one airing of an item together:
-- every segment of the airing carries the same opaque value (the first
-- segment's entry id), NULL for an entry that is not a segment. The breaks
-- between segments are ordinary midroll entries and carry no group.
ALTER TABLE epg_entry ADD COLUMN item_offset_ms INTEGER NOT NULL DEFAULT 0 CHECK (item_offset_ms >= 0);
ALTER TABLE epg_entry ADD COLUMN split_group TEXT;
CREATE INDEX idx_epg_entry_split_group ON epg_entry(channel_id, split_group) WHERE split_group IS NOT NULL;
