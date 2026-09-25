# verspaetet

Experimental project to aggregate a dataset of Deutsche Bahn delays at train stations.

verspaetet (German for "delayed") is a proof-of-concept that collects departure and arrival board data via the official DB Timetables API (IRIS), persists delay datapoints to Postgres, and serves a web UI for monitoring. Job orchestration runs on asynq (Redis-backed).

> **Note:** This is a POC built entirely with agentic code. The quality is rough.
>
> **Rate limits:** Building a gapless dataset (all ~5,400 stations at a consistent, fine-grained cadence) requires a rate limit beyond the free tier (60 req/min for IRIS). Such an increase must be requested from the API provider (developers.deutschebahn.com / IRIS-TTS.API@deutschebahn.com).

## The dataset

Monthly Parquet chunks of delay-evolution snapshots for all German IRIS stations.

- **What:** per train stop: planned/actual time, delay, platform (planned/actual), cancellation, line category, train number, operator, route path. Multiple snapshots per train stop = how the communicated delay evolved over time (the unique value of this dataset: endpoint-only "final delay" data exists elsewhere; this records the whole curve).
- **Coverage:** all ~5,400 IRIS-active stations, one snapshot every 40 minutes (same cadence for every station), both directions from one request per station.
- **Gaps:** known downtime is documented per chunk in `manifest.json` (`coverage.downtimes`) and the chunk README; `coverage.missing_days` lists operating days without any data.
- **Format:** Parquet (zstd), partitioned by operating month (`trip_date`), plus `stations.parquet` and a `manifest.json` (SHA256 checksums, coverage, schema version).
- **License:** CC BY 4.0 (attribution required, see below).
- **Where:** [huggingface.co/datasets/LinusRichter404/verspaetet](https://huggingface.co/datasets/LinusRichter404/verspaetet) - first chunk (2026-09) publishes 2026-10-04, one chunk on the 4th of each month (previous operating month, +3 days finality lag). Local copies on the collector server under `/exports/<YYYY-MM>/`.

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
- Ops dashboard (Grafana): `http://localhost:8082`

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

## License

The code of this repository is licensed under **MIT**.

The **collected dataset** (delay snapshots of Deutsche Bahn trains) is derived from data provided by the **Deutsche Bahn Timetables API (IRIS)** and the **DB StaDa API**, which Deutsche Bahn publishes under the [Creative Commons Attribution 4.0 International (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/) license. Any published dataset will therefore be released under CC BY 4.0 with attribution:

> Daten: Deutsche Bahn (Timetables API & StaDa API), Lizenz: CC BY 4.0 — verspaetet (github.com/LinusRichter/verspaetet)