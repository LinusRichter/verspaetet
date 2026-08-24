-- 0007_data_quality.down.sql
-- Revert the delays view to the original COALESCE version.
-- The UPDATE backfills are not reversible (data was NULL before, now populated).

CREATE OR REPLACE VIEW delays AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_label,
  line_category,
  trip_id,
  trip_date,
  planned_time,
  actual_time,
  COALESCE(EXTRACT(EPOCH FROM (actual_time - planned_time)), 0)::int AS delay_seconds,
  scraped_at
FROM stop_events;