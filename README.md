# verspaetet

Experimental project to aggregate a dataset of Deutsche Bahn delays at train stations.

verspaetet (German for "delayed") is a proof-of-concept that collects departure and arrival board data via the official DB Timetables API (IRIS), persists delay datapoints to Postgres, and serves a web UI for monitoring. Job orchestration runs on asynq (Redis-backed), with asynqmon as the job dashboard.

> **Note:** This is a POC built entirely with agentic code. The quality is rough and not production-ready.

## How it works

1. `stationimport` pulls the full station universe (~5,400 active stations incl. coordinates) from the DB StaDa API — one call
2. The **scheduler** ticks every minute and enqueues `board:fetch` tasks for every station whose hash-derived time slot matches (30-min cadence, evenly spread)
3. The **worker** fetches each station's board from the IRIS Timetables API (`/fchg` + current-hour `/plan`), merges planned and real-time data, and persists snapshots — a new row only when something observable changed (delay, platform, cancellation), so the dataset captures the **evolution** of each train's delay
4. Unknown station names seen in route paths land in `pending_stations` and are resolved by the next StaDa import (discovery loop)

## Stack

- **Go** — worker, scheduler, API server
- **asynq** (Redis) — job queue with retries
- **asynqmon** — job monitoring UI (:8081)
- **Postgres** — collected data
- **DB Timetables API (IRIS)** — planned + real-time timetable data (free plan, 60 req/min)
- **DB StaDa API** — station master data (free plan)
- **React + Vite** — monitoring UI (:8080)

## Setup

1. Register at [developers.deutschebahn.com](https://developers.deutschebahn.com) and subscribe to the **free** plans of "Timetables" and "Station Data"
2. Configure:

```bash
cp .env.example .env   # set POSTGRES_PASSWORD, IRIS_CLIENT_ID, IRIS_API_KEY
```

3. Run:

```bash
docker compose up -d
```

UIs:
- Monitor UI: `http://localhost:8080`
- asynqmon (jobs): `http://localhost:8081`

## Test one station

```bash
docker compose run --rm seeder --eva=8000105 --direction=departure
```

## Migrations

```bash
docker compose run --rm seeder migrate up
docker compose run --rm seeder migrate down 1
```

## Re-import stations (StaDa)

```bash
docker compose run --rm stationimport
```

## Build from source

```bash
go build ./...
go vet ./...
go test ./...
```

## Data model

```
stations        EVA-keyed station registry (StaDa: name, category, lat/lon)
scrape_runs     provenance: one row per (station, direction, scrape)
stop_events     the dataset: snapshots per (station, stop_id) — planned/actual
                time, platform (planned/actual), route path, cancellation,
                train category/number/operator; multiple rows per stop =
                delay evolution
pending_stations discovery: unresolved route-path names
```

Views: `delays` (NULL-preserving delay_seconds), `platform_changes`.

## Project layout

```
cmd/                Go entrypoints (api, worker, scheduler, seeder, stationimport)
activities/         IRIS client, StaDa client, persistence
asynqtasks/         task type + payload definitions
shared/             domain types, slugify, fetch-offset hash
db/migrations/      Postgres schema (0001_iris_init)
web/                React + Vite monitoring UI
docker-compose.yml
```

## License

MIT