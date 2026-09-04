-- 0001_iris_init.up.sql
-- Clean start: IRIS-native schema. No notes, no reason codes, no board-scrape
-- artefacts. Stations come from StaDa (EVA-keyed); stop events come from the
-- IRIS Timetables API (/plan + /fchg), keyed by the persistent s.id.

-- ── stations ─────────────────────────────────────────────────────────────
-- Universe = StaDa import (~5400 active stations, category 1-7).
CREATE TABLE stations (
  eva            TEXT PRIMARY KEY,          -- main EVA (StaDa evaNumbers[isMain])
  name           TEXT NOT NULL,             -- StaDa name
  slug           TEXT NOT NULL UNIQUE,      -- name-derived slug (UI/API compat)
  category       INT,                       -- StaDa station category (1-7)
  lat            DOUBLE PRECISION,          -- GeoJSON [lon,lat] — order swapped on purpose
  lon            DOUBLE PRECISION,
  federal_state  TEXT,
  fetch_offset   INT NOT NULL,              -- hash(slug) % 30: stagger minute slot
  discovered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  discovered_from TEXT                     -- station eva that first saw this name in a path (nullable)
);

CREATE INDEX stations_fetch_offset_idx ON stations (fetch_offset);
CREATE INDEX stations_name_idx ON stations (name);

-- ── scrape_runs ──────────────────────────────────────────────────────────
-- One row per (eva, direction, scrape). Provenance for stop_events batches.
CREATE TABLE scrape_runs (
  id            BIGSERIAL PRIMARY KEY,
  station_eva   TEXT NOT NULL REFERENCES stations(eva),
  direction     TEXT NOT NULL CHECK (direction IN ('departure','arrival')),
  scraped_at    TIMESTAMPTZ NOT NULL,
  UNIQUE (station_eva, direction, scraped_at)
);

CREATE INDEX scrape_runs_station_idx ON scrape_runs (station_eva, scraped_at DESC);

-- ── stop_events ──────────────────────────────────────────────────────────
-- One row per (station, stop_id, direction, scrape) — a snapshot of one
-- train's arrival or departure as observed at one point in time. Multiple
-- snapshots per stop_id = delay evolution.
CREATE TABLE stop_events (
  id               BIGSERIAL PRIMARY KEY,
  scrape_run_id    BIGINT NOT NULL REFERENCES scrape_runs(id),
  station_eva      TEXT NOT NULL REFERENCES stations(eva),
  direction        TEXT NOT NULL CHECK (direction IN ('departure','arrival')),
  stop_id          TEXT NOT NULL,           -- IRIS s.id: {dailyTripId}-{YYMMddHHmm}-{stopIndex}
  trip_date        DATE,                   -- start date parsed from stop_id part 2
  line_category    TEXT NOT NULL,           -- tl.c (ICE, RE, S, ...)
  train_number     TEXT,                    -- tl.n
  owner            TEXT,                    -- tl.o (operator)
  trip_kind        TEXT,                    -- tl.t enum: p/e/z/s/h/n
  direction_name   TEXT,                    -- pde/cde (distant endpoint)
  via_path         TEXT[],                 -- ppth/cpth split on | (route station names)
  planned_time     TIMESTAMPTZ NOT NULL,    -- pt, parsed Europe/Berlin → UTC
  actual_time      TIMESTAMPTZ,            -- ct (forecast/actual; NULL = not yet known)
  planned_platform TEXT,                    -- pp
  platform         TEXT,                    -- cp when present, else pp
  cancelled        BOOLEAN NOT NULL DEFAULT false,  -- cs='c' (or ps='c' in plan-only stops)
  scraped_at       TIMESTAMPTZ NOT NULL,
  UNIQUE (station_eva, direction, stop_id, scraped_at)  -- DB-level dedup safety net
);

CREATE INDEX stop_events_stop_idx ON stop_events (station_eva, direction, stop_id, scraped_at DESC);
CREATE INDEX stop_events_trip_idx ON stop_events (stop_id, scraped_at DESC);
CREATE INDEX stop_events_line_idx ON stop_events (line_category, scraped_at DESC);
CREATE INDEX stop_events_station_time_idx ON stop_events (station_eva, direction, planned_time);

-- ── pending_stations ────────────────────────────────────────────────────
-- Route-path names seen in IRIS that have no stations row yet. Resolved by
-- the periodic StaDa refresh (stationimport) — name match against StaDa.
CREATE TABLE pending_stations (
  name        TEXT PRIMARY KEY,             -- the unresolved path name
  first_seen  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  seen_from   TEXT                          -- station eva whose board showed this name
);

-- ── views ────────────────────────────────────────────────────────────────
-- delay_seconds is NULL when actual_time is NULL (not yet arrived ≠ on time).
CREATE VIEW delays AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_category,
  train_number,
  stop_id,
  planned_time,
  actual_time,
  EXTRACT(EPOCH FROM (actual_time - planned_time))::int AS delay_seconds,
  cancelled,
  scraped_at
FROM stop_events;

CREATE VIEW platform_changes AS
SELECT
  id AS stop_event_id,
  station_eva,
  direction,
  line_category,
  train_number,
  stop_id,
  planned_platform,
  platform,
  scraped_at
FROM stop_events
WHERE planned_platform IS NOT NULL
  AND platform IS NOT NULL
  AND planned_platform <> platform;