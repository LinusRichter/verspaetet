-- 0009_notes_normalization.up.sql
-- Extract notes into a separate lookup table. 99.73% of note strings are
-- duplicates (only 4,364 unique strings among 1.6M rows). This saves ~150 MB
-- on the current dataset and prevents linear growth of notes storage.
--
-- stop_events.notes TEXT → stop_events.notes_id BIGINT → note_texts(id)

-- 1. Create the notes lookup table.
CREATE TABLE IF NOT EXISTS note_texts (
  id BIGSERIAL PRIMARY KEY,
  text TEXT NOT NULL UNIQUE
);

-- 2. Insert all distinct notes into the lookup table.
INSERT INTO note_texts (text)
SELECT DISTINCT notes FROM stop_events
WHERE notes IS NOT NULL AND notes != ''
ON CONFLICT (text) DO NOTHING;

-- 3. Add notes_id column to stop_events.
ALTER TABLE stop_events ADD COLUMN notes_id BIGINT;

-- 4. Backfill notes_id from the lookup table.
UPDATE stop_events se
SET notes_id = nt.id
FROM note_texts nt
WHERE se.notes = nt.text
  AND se.notes IS NOT NULL AND se.notes != '';

-- 5. Add FK constraint.
ALTER TABLE stop_events
  ADD CONSTRAINT stop_events_notes_id_fkey
  FOREIGN KEY (notes_id) REFERENCES note_texts(id);

-- 6. Add index for the dedup query (replaces the notes column comparison).
CREATE INDEX stop_events_notes_id_idx ON stop_events (notes_id)
  WHERE notes_id IS NOT NULL;

-- 7. Drop the old notes column.
ALTER TABLE stop_events DROP COLUMN notes;

-- 8. Update the delays view (no longer references notes, but rebuild for safety).
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
  EXTRACT(EPOCH FROM (actual_time - planned_time))::int AS delay_seconds,
  scraped_at
FROM stop_events;