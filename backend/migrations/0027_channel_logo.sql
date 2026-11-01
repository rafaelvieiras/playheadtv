-- Channel logo: a small image an operator uploads per channel, drawn by the
-- playback client in place of the channel's name in the guide and the OSD
-- (see docs/ARCHITECTURE.md "Channel logo"). The bytes live in the channel row
-- next to the id that names them: logo_image_id changes on every upload, so a
-- client that caches the image by URL never shows a stale one, and no separate
-- image table has to be kept in step with the channel it belongs to.
--
-- All three columns are NULL until an upload; deleting the channel deletes its
-- logo with it.
ALTER TABLE channel ADD COLUMN logo_image_id TEXT;
ALTER TABLE channel ADD COLUMN logo_content_type TEXT;
ALTER TABLE channel ADD COLUMN logo_image BLOB;
