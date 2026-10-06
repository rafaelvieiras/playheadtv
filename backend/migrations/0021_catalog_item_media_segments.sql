-- Media segments of an item (Jellyfin 10.10+: intro, outro, recap, ...): a JSON
-- array of {type, start_ms, end_ms}, offsets from the start of the item. NULL
-- means "not looked up" (source without the feature, lookup skipped or failed);
-- '[]' means the source was asked and has none. The cut planner uses the Intro
-- segment for the after_intro role.
ALTER TABLE catalog_item ADD COLUMN media_segments TEXT;
