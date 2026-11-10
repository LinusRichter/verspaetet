-- Index for the stop_events.scrape_run_id foreign key. Deleting fetch-log
-- rows (seeder --prune-month) checks every deleted scrape_runs row against
-- stop_events; without this index each check is a full table scan.
-- On a large live database build it first by hand, without blocking writes:
--   CREATE INDEX CONCURRENTLY stop_events_scrape_run_idx ON stop_events (scrape_run_id);
-- then this migration is a no-op (same pattern as 0003).
CREATE INDEX IF NOT EXISTS stop_events_scrape_run_idx ON stop_events (scrape_run_id);
