import type { Station, Line, StopEvent, Route, TopDelay, Stats, TripStop } from './types'

// Relative base: the Go API serves both the static React files and the /api
// endpoints on the same origin, so the browser resolves relative URLs against
// whatever host it loaded the page from (localhost in dev, <pi-ip> in prod).
// A hardcoded http://localhost:8080 would break when the UI is opened from
// another machine (e.g. the PC loading the Pi's UI).
const BASE = ''

export async function fetchStations(): Promise<Station[]> {
  const r = await fetch(`${BASE}/api/stations`)
  return r.json()
}
export async function fetchLines(slug: string): Promise<Line[]> {
  const r = await fetch(`${BASE}/api/stations/${slug}/lines`)
  return r.json()
}
export async function fetchRoutes(slug: string): Promise<Route[]> {
  const r = await fetch(`${BASE}/api/stations/${slug}/routes`)
  return r.json()
}
export async function fetchEvents(slug: string, lineLabel: string): Promise<StopEvent[]> {
  const r = await fetch(`${BASE}/api/stations/${slug}/lines/${encodeURIComponent(lineLabel)}/events`)
  return r.json()
}
export async function fetchTopDelays(limit: number = 20): Promise<TopDelay[]> {
  const r = await fetch(`${BASE}/api/delays/top?limit=${limit}`)
  return r.json()
}
export async function fetchStats(): Promise<Stats> {
  const r = await fetch(`${BASE}/api/stats`)
  return r.json()
}
export async function fetchTripStops(uuid: string, date?: string): Promise<TripStop[]> {
  const q = date ? `?date=${date}` : ''
  const r = await fetch(`${BASE}/api/trips/${uuid}/stops${q}`)
  return r.json()
}