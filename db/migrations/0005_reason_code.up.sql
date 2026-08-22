-- 0005_reason_code.up.sql
-- Structured delay reason on stop_events (raw data, nullable, no CHECK — the
-- taxonomy may grow). See docs/architecture/delay-forensics.md.

ALTER TABLE stop_events ADD COLUMN reason_code TEXT;