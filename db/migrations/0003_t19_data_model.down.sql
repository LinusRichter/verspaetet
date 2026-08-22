-- 0003_t19_data_model.down.sql
-- Reverts 0003_t19_data_model.up.sql.

-- Restore original platform_changes view.
DROP VIEW platform_changes;

CREATE VIEW platform_changes AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_label,
  line_category,
  trip_id,
  trip_date,
  platform,
  planned_platform,
  scraped_at
FROM stop_events
WHERE planned_platform IS NOT NULL
  AND platform IS NOT NULL
  AND planned_platform <> platform;

-- Restore NOT NULL on trip fields.
ALTER TABLE stop_events ALTER COLUMN trip_id   SET NOT NULL;
ALTER TABLE stop_events ALTER COLUMN trip_date SET NOT NULL;
ALTER TABLE stop_events ALTER COLUMN trip_uuid SET NOT NULL;

-- Drop direction_slug.
DROP INDEX IF EXISTS stop_events_direction_slug_idx;
ALTER TABLE stop_events DROP COLUMN direction_slug;