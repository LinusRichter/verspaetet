-- 0011_stop_events_unique.up.sql
-- Add a unique constraint to enforce the dedup at the DB level.
-- The application dedup (WHERE NOT EXISTS) has a TOCTOU race: two concurrent
-- transactions can both pass the check and insert duplicates. This unique
-- constraint is the safety net — the second insert fails with a constraint
-- violation instead of creating a duplicate row.
--
-- Note: the natural key is (station_eva, direction, trip_id, trip_date,
-- scraped_at). Ersatzbus rows with empty trip_id and NULL trip_date are
-- handled: NULLs are distinct in unique indexes by default, so each Ersatzbus
-- insert within the same scrape (same scraped_at) collapses to one row per
-- (station_eva, direction, scraped_at) — matching the intended behavior.

-- Deduplicate existing rows first (keep the lowest id per key).
DELETE FROM stop_events a
USING stop_events b
WHERE a.id > b.id
  AND a.station_eva = b.station_eva
  AND a.direction = b.direction
  AND a.trip_id IS NOT DISTINCT FROM b.trip_id
  AND a.trip_date IS NOT DISTINCT FROM b.trip_date
  AND a.scraped_at = b.scraped_at;

CREATE UNIQUE INDEX stop_events_natural_key_idx
  ON stop_events (station_eva, direction, trip_id, trip_date, scraped_at);