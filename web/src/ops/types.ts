// Ops dashboard types — kept isolated so this page can be split into its
// own app later without touching the data UI.

export interface OpsQueue {
  queue: string
  pending: number
  active: number
  scheduled: number
  retry: number
  archived: number
  processed: number
  failed: number
  last_error: string
  last_failed_at: string
}

export interface OpsFailedTask {
  id: string
  queue: string
  type: string
  payload: string
  last_error: string
  retries: number
  last_failed_at: string
}

export interface OpsCollection {
  events_last_hour: number
  stations_scraped_last_hour: number
  total_stations: number
  no_iris_stations: number
  pending_stations: number
  events_per_minute: number
  punctual_pct: number
  cancelled_pct: number
  avg_snaps_per_stop: number
  oldest_event: string
  db_size: string
}

export interface OpsExportChunk {
  name: string
  files: number
  total_bytes: number
}

export interface OpsHealth {
  recent_runs: number
  recent_events: number
  last_scrape_ago_s: number
  expected_runs: number
  fetch_rate: number
}