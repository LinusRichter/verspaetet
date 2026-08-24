-- 0008_drop_direction_eva_fk.up.sql
-- The direction_eva FK constraint blocks inserts when the destination/origin
-- station's EVA is not yet in our stations table. The JSON API returns EVAs
-- for stations we haven't discovered yet. Drop the FK — direction_eva is
-- informational, not a hard reference.

ALTER TABLE stop_events DROP CONSTRAINT IF EXISTS stop_events_direction_eva_fkey;