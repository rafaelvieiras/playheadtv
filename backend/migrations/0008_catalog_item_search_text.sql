-- catalog_item.search_text holds the case- and accent-folded title and
-- series title, so GET /catalog-items?q= can match them without an unaccent
-- function (SQLite LIKE only folds ASCII case). It is maintained by
-- catalogsync.Upsert; rows that predate this column are filled in at startup
-- by catalogsync.BackfillSearchText, since the folding is done in Go.
ALTER TABLE catalog_item ADD COLUMN search_text TEXT NOT NULL DEFAULT '';
