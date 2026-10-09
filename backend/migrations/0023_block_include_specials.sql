-- Season 0 (specials) of a series is left out of a block's selections by default.
--
-- block.include_specials is a per-block switch: 0 (the default) keeps the
-- episodes of season 0 of a series out of everything season-based the block
-- selects (a dynamic block's matches, its criteria items and its series
-- items); 1 lets them in. Movies, items without a series and episodes with no
-- season number are never affected. The column default applies to every
-- existing block too, so their specials leave the guide at the next
-- regeneration.
ALTER TABLE block ADD COLUMN include_specials INTEGER NOT NULL DEFAULT 0 CHECK (include_specials IN (0, 1));
