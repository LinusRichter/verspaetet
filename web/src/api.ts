// API-Client der Data-UI. Im Produktions-Build ruft er ausschließlich die
// echten Endpunkte des Go-Servers auf. Im Dev-Modus (vite dev ohne VITE_API=1)
// bedient der Mock aus mock.ts dieselben Verträge — dynamisch importiert,
// damit Vite ihn aus dem Produktions-Build herausschneidet.

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

export const api = {
  meta: () => call<Meta>('/api/meta', (m) => m.meta()),
  search: (q: string) => call<SearchResults>(`/api/search?q=${encodeURIComponent(q)}`, (m) => m.search(q)),
  board: (station: string, at: number) =>
    call<BoardData>(`/api/board?station=${encodeURIComponent(station)}&at=${at}`, (m) => m.board(station, at)),
  journey: (runId: string, station: string | null) =>
    call<JourneyData | null>(
      `/api/journey?run=${encodeURIComponent(runId)}${station ? `&station=${encodeURIComponent(station)}` : ''}`,
      (m) => m.journey(runId, station)),
  line: (lineKey: string, station: string | null) =>
    call<LineData>(
      `/api/line?line=${encodeURIComponent(lineKey)}${station ? `&station=${encodeURIComponent(station)}` : ''}`,
      (m) => m.line(lineKey, station)),
  day: () => call<DayData>('/api/day', (m) => m.day()),
}
