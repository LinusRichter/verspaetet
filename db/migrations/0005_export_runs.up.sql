-- Export pipeline state per operating month: export -> upload -> offsite backup -> prune.
CREATE TABLE export_runs (
  month                   TEXT PRIMARY KEY CHECK (month ~ '^[0-9]{4}-[0-9]{2}$'),
  attempts                INT NOT NULL DEFAULT 0,
  last_attempt_at         TIMESTAMPTZ,
  last_error              TEXT,
  rows                    BIGINT,       -- manifest stop_events.rows
  stop_events_sha256      TEXT,
  max_scraped_at          TIMESTAMPTZ,  -- manifest stop_events.max_scraped_at (whole seconds)
  scrape_runs_rows        BIGINT,
  scrape_runs_sha256      TEXT,
  scrape_runs_from        TIMESTAMPTZ,  -- manifest scrape_runs.from
  scrape_runs_to          TIMESTAMPTZ,  -- manifest scrape_runs.to
  exported_at             TIMESTAMPTZ,  -- files + manifest written locally
  uploaded_at             TIMESTAMPTZ,  -- uploaded and verified on HuggingFace
  offsite_at              TIMESTAMPTZ,  -- set by the operator's backup job (seeder --mark-offsite)
  prune_started_at        TIMESTAMPTZ,
  pruned_at               TIMESTAMPTZ,
  pruned_rows             BIGINT,
  prune_note              TEXT,         -- e.g. "N late rows kept"
  scrape_runs_pruned_at   TIMESTAMPTZ,
  scrape_runs_pruned_rows BIGINT
);
