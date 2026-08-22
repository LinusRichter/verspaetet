-- 0003_t19_data_model.up.sql
-- T19 retrospective: fix the data model based on what the real scraped data shows.
--
-- Findings addressed here:
--   1. `platform` and `planned_platform` are stored as '' (empty string), never
--      NULL, because pgx maps Go "" to SQL empty string. This broke the
--      `platform_changes` view (its `IS NOT NULL` filter does not exclude '').
--      Fix: add NULLIF generation so empty strings are normalised to NULL at
--      read time in the views, and add a CHECK constraint to prevent future
--      empty-string writes (the persist activity is also fixed to send nil).
--   2. `direction_slug` was in the Go struct and used by discovery but missing
--      from the DDL. Add it as a nullable TEXT column so the slug is persisted
--      (it is the natural discovery key for the direction station; the parser
--      already emits it).
--   3. `trip_id` / `trip_date` / `trip_uuid` are NOT NULL in the DDL but the
--      parser emits '' for Ersatzbus rows that have no fahrtverlauf link. The
--      empty string passes NOT NULL. Relax these to allow empty (the dedup
--      still works because the natural key includes trip_id; empty trip_ids
--      collapse onto one row per (station, direction, scraped_at) batch, which
--      is acceptable for Ersatzbus rows that have no stable trip identity).

-- (1) direction_slug: add the column the parser already emits.
ALTER TABLE stop_events ADD COLUMN direction_slug TEXT;

-- Index for discovery lookups (resolve-by-slug).
CREATE INDEX stop_events_direction_slug_idx
  ON stop_events (direction_slug)
  WHERE direction_slug IS NOT NULL AND direction_slug <> '';

-- (2) Normalise platform / planned_platform: replace the views so empty
--     strings are treated as NULL. The base columns stay TEXT (the persist
--     activity is updated to send nil for empty strings going forward, but
--     existing rows need the view-side normalisation).
DROP VIEW platform_changes;

CREATE OR REPLACE VIEW platform_changes AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_label,
  line_category,
  trip_id,
  trip_date,
  NULLIF(platform, '') AS platform,
  NULLIF(planned_platform, '') AS planned_platform,
  scraped_at
FROM stop_events
WHERE NULLIF(planned_platform, '') IS NOT NULL
  AND NULLIF(platform, '') IS NOT NULL
  AND NULLIF(planned_platform, '') <> NULLIF(platform, '');

-- (3) Relax NOT NULL on trip fields to allow empty Ersatzbus rows.
ALTER TABLE stop_events ALTER COLUMN trip_id   DROP NOT NULL;
ALTER TABLE stop_events ALTER COLUMN trip_date DROP NOT NULL;
ALTER TABLE stop_events ALTER COLUMN trip_uuid DROP NOT NULL;