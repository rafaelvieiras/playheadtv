-- The audio side of the primary media file: the codec's short name (aac,
-- ac3, eac3, dts, mp3, ...) and the channel count (2 for stereo, 6 for 5.1).
-- A client shows them ("AAC Stereo") next to the video codec it already has.
--
-- Nullable and additive: a row synced before this migration keeps nulls until
-- its source is synced again, and an empty value means "the source did not
-- say", never "no audio".
ALTER TABLE catalog_item ADD COLUMN audio_codec TEXT;
ALTER TABLE catalog_item ADD COLUMN audio_channels INTEGER;
