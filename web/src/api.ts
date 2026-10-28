// API-Client der Data-UI. Im Produktions-Build ruft er ausschließlich die
// echten Endpunkte des Go-Servers auf. Im Dev-Modus (vite dev ohne VITE_API=1)
// bedient der Mock aus mock.ts dieselben Verträge — dynamisch importiert,
// damit Vite ihn aus dem Produktions-Build herausschneidet.
//
// Line-/Day-Antworten werden 60 s im Frontend gecacht: das Hin-und-Her
// zwischen Tafel und Linie soll nicht jede Navigation die (teuren)
// Tagesaggregate neu bezahlen lassen. Stumpfes TTL, kein Invalidate.

import type { BoardData, DayData, JourneyData, LineData, Meta, SearchResults } from './types'

type Mock = {
  meta(): Promise<Meta>
  search(q: string): Promise<SearchResults>
  board(station: string, at: number): Promise<BoardData>
  journey(runId: string, station: string | null): Promise<JourneyData | null>
  line(lineKey: string, station: string | null): Promise<LineData>
  day(): Promise<DayData>
}

const USE_REAL = !import.meta.env.DEV || !!import.meta.env.VITE_API

let mockPromise: Promise<Mock> | null = null
function getMock(): Promise<Mock> {
  mockPromise ??= import('./mock').then((m) => m.default as Mock)
  return mockPromise
}

async function get<T>(path: string): Promise<T> {
  const r = await fetch(path)
  if (!r.ok) throw new Error(`API ${r.status}: ${path}`)
  return r.json() as Promise<T>
}

async function call<T>(path: string, viaMock: (m: Mock) => Promise<T>): Promise<T> {
  if (USE_REAL) return get<T>(path)
  return viaMock(await getMock())
}

// ── 60-s-Frontend-Cache (line + day) ─────────────────────────────────────
const CACHE_TTL = 60_000
const cache = new Map<string, { ts: number; data: unknown }>()

async function cached<T>(key: string, fn: () => Promise<T>): Promise<T> {
  const hit = cache.get(key)
  if (hit && Date.now() - hit.ts < CACHE_TTL) return hit.data as T
  const data = await fn()
  cache.set(key, { ts: Date.now(), data })
  return data
}

function dayParam(day: string | null): string {
  return day ? `&day=${day}` : ''
}

export const api = {
  meta: (day: string | null = null) =>
    call<Meta>(`/api/meta?${dayParam(day).slice(1)}`, (m) => m.meta()),
  search: (q: string) =>
    call<SearchResults>(`/api/search?q=${encodeURIComponent(q)}`, (m) => m.search(q)),
  board: (station: string, at: number, day: string | null = null) =>
    call<BoardData>(`/api/board?station=${encodeURIComponent(station)}&at=${at}${dayParam(day)}`,
      (m) => m.board(station, at)),
  journey: (runId: string, station: string | null) =>
    call<JourneyData | null>(
      `/api/journey?run=${encodeURIComponent(runId)}${station ? `&station=${encodeURIComponent(station)}` : ''}`,
      (m) => m.journey(runId, station)),
  line: (lineKey: string, station: string | null, day: string | null = null) =>
    cached(`line|${lineKey}|${station ?? ''}|${day ?? ''}`,
      () => call<LineData>(
        `/api/line?line=${encodeURIComponent(lineKey)}${station ? `&station=${encodeURIComponent(station)}` : ''}${dayParam(day)}`,
        (m) => m.line(lineKey, station))),
  day: (day: string | null = null) =>
    cached(`day|${day ?? ''}`,
      () => call<DayData>(`/api/day${dayParam(day)}`, (m) => m.day())),
}