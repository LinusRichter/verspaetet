-- 0009_notes_normalization.down.sql
-- Re-add notes column and drop the lookup table.

ALTER TABLE stop_events ADD COLUMN notes TEXT;
UPDATE stop_events se
SET notes = nt.text
FROM note_texts nt
WHERE se.notes_id = nt.id;

ALTER TABLE stop_events
  DROP CONSTRAINT IF EXISTS stop_events_notes_id_fkey;
DROP INDEX IF EXISTS stop_events_notes_id_idx;
ALTER TABLE stop_events DROP COLUMN notes_id;
DROP TABLE IF EXISTS note_texts;