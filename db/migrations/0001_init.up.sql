-- 0001_init.up.sql
-- Authoritative DDL from docs/data/schema.md. Per ADR-07: raw datapoints, no aggregation.

-- stations
CREATE TABLE stations (
  eva              TEXT PRIMARY KEY,
  slug             TEXT NOT NULL UNIQUE,
  name             TEXT NOT NULL,
  category         INT  CHECK (category BETWEEN 1 AND 7),
  discovered_at    TIMESTAMPTZ,
  discovered_from  TEXT REFERENCES stations(eva),
  cadence_override INTERVAL
);

-- lines
CREATE TABLE lines (
  line_label    TEXT NOT NULL,
  line_category TEXT NOT NULL,
  name          TEXT,
  PRIMARY KEY (line_label, line_category),
  CHECK (line_category IN
    ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz'))
);

-- scrape_runs
CREATE TABLE scrape_runs (
  id           BIGSERIAL PRIMARY KEY,
  station_eva  TEXT NOT NULL REFERENCES stations(eva),
  direction    TEXT NOT NULL CHECK (direction IN ('departure','arrival')),
  scraped_at   TIMESTAMPTZ NOT NULL,
  UNIQUE (station_eva, direction, scraped_at)
);
CREATE INDEX scrape_runs_station_idx ON scrape_runs (station_eva, scraped_at DESC);

-- stop_events (the central table)
CREATE TABLE stop_events (
  id               BIGSERIAL PRIMARY KEY,
  scrape_run_id    BIGINT NOT NULL REFERENCES scrape_runs(id),
  station_eva      TEXT NOT NULL REFERENCES stations(eva),
  direction        TEXT NOT NULL CHECK (direction IN ('departure','arrival')),
  line_label       TEXT NOT NULL,
  line_category    TEXT NOT NULL,
  direction_name   TEXT,
  direction_eva    TEXT REFERENCES stations(eva),
  planned_time     TIMESTAMPTZ NOT NULL,
  actual_time      TIMESTAMPTZ,
  platform         TEXT,
  planned_platform TEXT,
  via_evas         TEXT[] DEFAULT '{}',
  via_slugs        TEXT[] DEFAULT '{}',
  trip_id          TEXT NOT NULL,
  trip_date        DATE NOT NULL,
  trip_uuid        TEXT NOT NULL,
  notes            TEXT,
  scraped_at       TIMESTAMPTZ NOT NULL,
  FOREIGN KEY (line_label, line_category) REFERENCES lines(line_label, line_category),
  CHECK (line_category IN
    ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz'))
);

CREATE INDEX stop_events_trip_idx
  ON stop_events (station_eva, direction, trip_id, trip_date, scraped_at DESC);
CREATE INDEX stop_events_line_idx
  ON stop_events (line_label, line_category, scraped_at DESC);
CREATE INDEX stop_events_station_time_idx
  ON stop_events (station_eva, direction, planned_time);
CREATE INDEX stop_events_scrape_run_idx
  ON stop_events (scrape_run_id);

-- views (derived, not base tables — per ADR-07)

CREATE VIEW delays AS
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
  COALESCE(EXTRACT(EPOCH FROM (actual_time - planned_time)), 0)::int AS delay_seconds,
  scraped_at
FROM stop_events;

CREATE VIEW platform_changes AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_label,
  line_category,
  trip_id,
  trip_date,
  platform,
  planned_platform,
  scraped_at
FROM stop_events
WHERE planned_platform IS NOT NULL
  AND platform IS NOT NULL
  AND planned_platform <> platform;