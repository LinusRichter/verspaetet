-- 0006_cancelled.up.sql
-- Structured cancellation flag on stop_events (raw data). See
-- docs/architecture/delay-forensics.md.

ALTER TABLE stop_events ADD COLUMN cancelled boolean NOT NULL DEFAULT false;