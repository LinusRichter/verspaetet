-- 0007_data_quality.up.sql
-- Fix semantic bugs and backfill dead columns.
--
-- 1. delays view: NULL actual_time must stay NULL (was COALESCE'd to 0,
--    conflating "not yet arrived" with "on time" — poisons ML training).
-- 2. trip_date: 0001-01-01 sentinel (Go zero value) → NULL for Ersatzbus rows.
-- 3. direction_eva: backfill from direction_slug via stations table (was
--    hardcoded nil in Go, 100% NULL).
-- 4. reason_code: backfill from notes text using expanded keyword taxonomy.

-- (1) Fix delays view: NULL actual_time → NULL delay (not 0).
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

-- (2) Backfill trip_date: 0001-01-01 → NULL.
UPDATE stop_events SET trip_date = NULL WHERE trip_date = '0001-01-01';

-- (3) Backfill direction_eva from direction_slug via stations table.
UPDATE stop_events se
SET direction_eva = st.eva
FROM stations st
WHERE se.direction_slug = st.slug
  AND se.direction_eva IS NULL;

-- (4) Backfill reason_code from notes using expanded taxonomy.
UPDATE stop_events SET reason_code = 'MEDICAL_EMERGENCY'
WHERE reason_code IS NULL OR reason_code = ''
  AND lower(notes) LIKE '%notarzteinsatz%';

UPDATE stop_events SET reason_code = 'MEDICAL_EMERGENCY'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%personenunfall%';

UPDATE stop_events SET reason_code = 'POLICE_ACTIVITY'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%polizeieinsatz%';

UPDATE stop_events SET reason_code = 'CONSTRUCTION'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%bauarbeiten%' OR lower(notes) LIKE '%baustelle%');

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_SIGNAL'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%signalstörung%';

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_SWITCH'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%weichenstörung%';

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_OVERHEAD'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%oberleitungsstörung%' OR lower(notes) LIKE '%oberleitung%');

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_RAILWAY_SECTION'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%streckenstörung%'
       OR (lower(notes) LIKE '%strecke%' AND lower(notes) LIKE '%beeinträchtigt%'));

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_VEHICLE'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%technische störung am zug%'
       OR lower(notes) LIKE '%fahrzeugstörung%');

UPDATE stop_events SET reason_code = 'TECHNICAL_PROBLEM_OTHER'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%technische störung%'
       OR lower(notes) LIKE '%betriebsstörung%');

UPDATE stop_events SET reason_code = 'PREVIOUS_TRAIN_DELAY'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%verspätung eines vorausfahrenden%'
       OR lower(notes) LIKE '%verspätung eines vorherigen%'
       OR lower(notes) LIKE '%verspätung aus vorheriger%');

UPDATE stop_events SET reason_code = 'OPERATIONAL_DELAY'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%verzögerung im betriebsablauf%'
       OR lower(notes) LIKE '%betriebsablauf%');

UPDATE stop_events SET reason_code = 'ANIMAL_ON_TRACK'
WHERE (reason_code IS NULL OR reason_code = '')
  AND (lower(notes) LIKE '%tier auf der strecke%'
       OR lower(notes) LIKE '%tierauf%');

UPDATE stop_events SET reason_code = 'STRIKE'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%streik%';

UPDATE stop_events SET reason_code = 'WEATHER_HEAT'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%hitze%';

UPDATE stop_events SET reason_code = 'WEATHER_STORM'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%unwetter%';

UPDATE stop_events SET reason_code = 'WEATHER_WINTER'
WHERE (reason_code IS NULL OR reason_code = '')
  AND lower(notes) LIKE '%winterwitterung%';