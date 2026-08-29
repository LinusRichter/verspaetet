-- 0010_fix_reason_code.up.sql
-- Fix the 0007 SQL precedence bug that corrupted reason_code backfill.
--
-- Bug: the first UPDATE in 0007 had:
--   WHERE reason_code IS NULL OR reason_code = '' AND lower(notes) LIKE '%notarzteinsatz%'
-- AND binds tighter than OR, so every row with reason_code IS NULL was set
-- to 'MEDICAL_EMERGENCY' regardless of notes content.
--
-- Fix: reset ALL reason_codes and re-derive from note_texts using the same
-- keyword taxonomy as the Go mapReason() function (correct precedence).

-- Reset everything (both the corrupted backfill and new-row values).
UPDATE stop_events SET reason_code = NULL;

-- Re-derive using explicit parentheses. Order matters: specific before general.
UPDATE stop_events
SET reason_code = 'MEDICAL_EMERGENCY'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%notarzteinsatz%' OR lower(nt.text) LIKE '%personenunfall%');

UPDATE stop_events
SET reason_code = 'POLICE_ACTIVITY'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%polizeieinsatz%';

UPDATE stop_events
SET reason_code = 'STRIKE'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%streik%';

UPDATE stop_events
SET reason_code = 'CONSTRUCTION'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%bauarbeiten%' OR lower(nt.text) LIKE '%baustelle%');

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_SIGNAL'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%signalstörung%';

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_SWITCH'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%weichenstörung%';

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_OVERHEAD'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%oberleitungsstörung%' OR lower(nt.text) LIKE '%oberleitung%');

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_RAILWAY_SECTION'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%streckenstörung%'
       OR (lower(nt.text) LIKE '%strecke%' AND lower(nt.text) LIKE '%beeinträchtigt%'));

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_VEHICLE'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%technische störung am zug%' OR lower(nt.text) LIKE '%fahrzeugstörung%');

UPDATE stop_events
SET reason_code = 'TECHNICAL_PROBLEM_OTHER'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%technische störung%' OR lower(nt.text) LIKE '%betriebsstörung%');

UPDATE stop_events
SET reason_code = 'PREVIOUS_TRAIN_DELAY'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%verspätung eines vorausfahrenden%'
       OR lower(nt.text) LIKE '%verspätung eines vorherigen%'
       OR lower(nt.text) LIKE '%verspätung aus vorheriger%');

UPDATE stop_events
SET reason_code = 'OPERATIONAL_DELAY'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%verzögerung im betriebsablauf%' OR lower(nt.text) LIKE '%betriebsablauf%');

UPDATE stop_events
SET reason_code = 'ANIMAL_ON_TRACK'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND (lower(nt.text) LIKE '%tier auf der strecke%' OR lower(nt.text) LIKE '%tierauf%');

UPDATE stop_events
SET reason_code = 'WEATHER_HEAT'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%hitze%';

UPDATE stop_events
SET reason_code = 'WEATHER_STORM'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%unwetter%';

UPDATE stop_events
SET reason_code = 'WEATHER_WINTER'
FROM note_texts nt
WHERE nt.id = stop_events.notes_id
  AND lower(nt.text) LIKE '%winterwitterung%';

-- Normalize: no-match rows get '' (matches Go mapReason behavior), not NULL.
UPDATE stop_events SET reason_code = '' WHERE reason_code IS NULL;