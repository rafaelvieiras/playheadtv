-- A channel may reference a second, optional "base" schedule that plays real
-- programming in every gap the channel's own (pinned) schedule leaves.
--
-- channel.base_schedule_id is nullable, like schedule_id: NULL (the default for
-- every existing channel) keeps today's behavior, where those gaps are filled
-- by the continuous item and the fallback filler rule. The base schedule can
-- not be the pinned one (a channel's two layers are two different schedules);
-- the CHECK below is the safety net behind the API validation.
ALTER TABLE channel ADD COLUMN base_schedule_id TEXT REFERENCES schedule(id)
    CHECK (base_schedule_id IS NULL OR base_schedule_id != schedule_id);
