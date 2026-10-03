-- A weekly schedule item holds a set of weekdays instead of a single one: the
-- new column is a comma-separated list of 0 (Sunday)..6 ("4,5"), empty for a
-- once or continuous item. Existing weekly items become a list of one day, so
-- nothing about their behavior changes.
ALTER TABLE schedule_item ADD COLUMN days_of_week TEXT NOT NULL DEFAULT '';

UPDATE schedule_item SET days_of_week = CAST(day_of_week AS TEXT)
WHERE recurrence_type = 'weekly' AND day_of_week IS NOT NULL;

ALTER TABLE schedule_item DROP COLUMN day_of_week;
