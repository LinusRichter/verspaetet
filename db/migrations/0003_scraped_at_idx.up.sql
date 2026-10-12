-- Indexes for scraped_at lookups. The /api/health endpoint counts
-- stop_events/scrape_runs rows in a 10-minute window and takes max(scraped_at)
-- — without these indexes that became full seq scans once stop_events passed
-- ~30 GB (minutes of disk-bound scanning per health poll, page-cache
-- dependent). Built live on 2026-10-12 with CREATE INDEX CONCURRENTLY; this
-- migration documents them for fresh installs (IF NOT EXISTS keeps it a no-op
-- on live DBs).
CREATE INDEX IF NOT EXISTS idx_stop_events_scraped_at ON stop_events (scraped_at);
CREATE INDEX IF NOT EXISTS idx_scrape_runs_scraped_at ON scrape_runs (scraped_at);