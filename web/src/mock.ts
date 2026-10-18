// DEV-Mock: liefert dieselben Daten wie die geplanten API-Endpunkte, komplett
// im Browser generiert. Wird NIE in den Produktions-Build eingebaut (siehe
// dynamischer Import in api.ts hinter import.meta.env.DEV). Keine UI-Schalter,
// kein Fake-Branding — nur eine Datenquelle während der Backend-Entwicklung.

import type { AltLine, BoardData, DayData, JourneyData, LineData, LineInfo, Meta, Run, SearchResults } from './types'
import { pct } from './lib'

function mulberry32(a: number): () => number {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
const rng = mulberry32(20261017)
const ri = (a: number, b: number) => a + Math.floor(rng() * (b - a + 1))
const ch = (p: number) => rng() < p
const clamp = (v: number, a: number, b: number) => Math.max(a, Math.min(b, v))

const SNAP_STEP = 40  // ≙ CADENCE 40 min
const PROG_WIN = 240  // ≙ fchg-Sichtbarkeitsfenster ~4 h
const T0 = 300, T1 = 1440, NOW = 14 * 60 + 37

interface LineDef extends LineInfo {
  evu: string
  first: number
  interval: number
  nruns: number
  route: [string, number][]
}

const LINES: LineDef[] = [
  { key: 'ICE-991', label: 'ICE 991', cat: 'ICE', evu: 'DB Fernverkehr', first: 360, interval: 120, nruns: 9,
    route: [['Frankfurt(Main)Hbf', 0], ['Ffm Flughafen Fernbf', 14], ['Mannheim Hbf', 54], ['Karlsruhe Hbf', 104], ['Baden-Baden', 124], ['Freiburg(Breisgau) Hbf', 164], ['Basel SBB', 218]] },
  { key: 'RE7-NRW', label: 'RE 7', cat: 'RE', evu: 'DB Regio NRW', first: 324, interval: 120, nruns: 10,
    route: [['Münster(Westf)Hbf', 0], ['Hamm(Westf)Hbf', 32], ['Dortmund Hbf', 55], ['Hagen Hbf', 80], ['Wuppertal Hbf', 108], ['Solingen Hbf', 126], ['Köln Hbf', 158]] },
  { key: 'RE7-SH', label: 'RE 7', cat: 'RE', evu: 'DB Regio Nord', first: 336, interval: 120, nruns: 9,
    route: [['Hamburg Hbf', 0], ['Neumünster', 66], ['Kiel Hbf', 96], ['Rendsburg', 126], ['Flensburg', 168]] },
  { key: 'RB-34', label: 'RB 34', cat: 'RB', evu: 'DB Regio', first: 342, interval: 60, nruns: 19,
    route: [['Minden(Westf)', 0], ['Bückeburg', 12], ['Stadthagen', 24], ['Wunstorf', 44], ['Hannover Hbf', 62]] },
  { key: 'S-6', label: 'S 6', cat: 'S', evu: 'DB Regio', first: 312, interval: 60, nruns: 19,
    route: [['Köln Nippes', 0], ['Köln Hbf', 7], ['Köln Messe/Deutz', 12], ['Porz(Rhein)', 21], ['Troisdorf', 29], ['Siegburg/Bonn', 41]] },
]

interface Snap { at: number; prog: number | null; ist: boolean; cancelled: boolean; plat: string }
interface Stop { name: string; planned: number; finalD: number; actual: number; cancelled: boolean; platFrom: string; platTo: string; platKnown: number; snaps: Snap[] }
interface MockRun extends Run { line: LineDef }

function mkSnaps(stop: Stop): Snap[] {
  const snaps: Snap[] = []
  const tEnd = stop.cancelled ? stop.planned + 2 : stop.actual + 2
  for (let at = Math.ceil((stop.planned - PROG_WIN) / SNAP_STEP) * SNAP_STEP; at <= tEnd; at += SNAP_STEP) {
    if (at > T1) break
    if (stop.cancelled && at >= stop.planned - 45) {
      snaps.push({ at, prog: null, ist: false, cancelled: true, plat: stop.platFrom })
      continue
    }
    const dt = stop.planned - at
    let prog: number | null
    if (dt > PROG_WIN) prog = null
    else {
      const conv = clamp((PROG_WIN - dt) / 80, 0, 1)
      const wob = ((ri(0, 14) - 7) / 7) * (1 - conv) * 6
      prog = clamp(Math.round(stop.finalD + wob), 0, 50)
    }
    snaps.push({ at, prog, ist: false, cancelled: false, plat: at >= stop.platKnown ? stop.platTo : stop.platFrom })
  }
  if (tEnd <= T1) {
    snaps.push({ at: tEnd, prog: stop.cancelled ? null : stop.finalD, ist: !stop.cancelled, cancelled: stop.cancelled, plat: stop.cancelled ? stop.platFrom : stop.platTo })
  }
  return snaps
}

function buildRuns(): MockRun[] {
  const runs: MockRun[] = []
  for (const L of LINES) {
    const plats = L.route.length > 6 ? 9 : 6
    for (let k = 0; k < L.nruns; k++) {
      const dep = L.first + k * L.interval
      const id = `${L.key}#${k}`
      const runCx = id === 'RB-34#7' || id === 'S-6#12'
      let d = ch(0.18) ? ri(5, 14) : ri(0, 3)
      const stops: Stop[] = L.route.map(([name, off], i) => {
        if (i > 0) {
          if (ch(0.45)) d += ri(1, 3)
          if (ch(0.28)) d -= ri(1, 2)
          d = clamp(d, 0, 45)
        }
        let incident = 0
        if (id === 'RE7-NRW#3' && i >= 3) incident = 14 + 2 * i
        if (id === 'ICE-991#2' && i >= 3 && i <= 4) incident = 12 - 2 * (i - 3)
        const finalD = runCx ? 0 : clamp(d + incident, 0, 45)
        const planned = dep + off
        const actual = runCx ? planned + 2 : planned + finalD
        const cancelled = runCx || ch(0.004)
        const platFrom = String(1 + ((k * 5 + i * 3) % plats))
        let platTo = platFrom
        let platKnown = 1e9
        if ((id === 'S-6#5' && i === 2) || ch(0.012)) {
          platTo = String(1 + ((Number(platFrom) + 2) % plats))
          platKnown = planned - ri(35, 70)
        }
        const stop: Stop = { name, planned, finalD, actual, cancelled, platFrom, platTo, platKnown, snaps: [] }
        stop.snaps = mkSnaps(stop)
        return stop
      })
      runs.push({ id, lineKey: L.key, dep, cancelled: runCx, stops, line: L })
    }
  }
  return runs
}

const RUNS = buildRuns()
const STATIONS = new Map<string, { r: MockRun; i: number }[]>()
for (const r of RUNS) {
  r.stops.forEach((st, i) => {
    if (!STATIONS.has(st.name)) STATIONS.set(st.name, [])
    STATIONS.get(st.name)!.push({ r, i })
  })
}

function delay(ms: number): Promise<void> {
  return new Promise((res) => setTimeout(res, ms))
}

/* ---------- Such-Normalisierung (identisch zum Go-Backend) ---------- */
function normName(s: string): string {
  return s.toLowerCase().replace(/ä/g, 'a').replace(/ö/g, 'o').replace(/ü/g, 'u')
    .replace(/ß/g, 'ss').replace(/[^a-z0-9]/g, '')
}
function normInput(q: string): string[] {
  const base = normName(q)
  const alt = normName(q.replace(/oe/g, 'o').replace(/ue/g, 'u').replace(/ae/g, 'a'))
  return alt === base ? [base] : [base, alt]
}
function matchScore(norm: string, variants: string[]): number {
  let best = -1
  for (const v of variants) {
    if (!v) continue
    if (norm === v) return 0
    if (norm.startsWith(v)) best = best < 0 || best > 1 ? 1 : best
    else if (norm.includes(v)) best = best < 0 ? 2 : Math.min(best, 2)
  }
  return best
}
function parseTimeHint(q: string): { min: number; rest: string } | null {
  const pats = [
    /(^|\s)([01]?\d|2[0-3])\s*(:|\.|h|uhr)\s*([0-5]\d)(?=$|\s|:|\.)/,
    /(^|\s)((?:[01]\d|2[0-3])[0-5]\d)(?=$|\s)/,
    /(^|\s)([01]?\d|2[0-3])\s*uhr(?=$|\s)/,
  ]
  for (const p of pats) {
    const m = p.exec(q)
    if (!m) continue
    const digits = m[0].replace(/[^0-9]/g, '')
    const min = digits.length <= 2 ? Number(digits) * 60 : Math.floor(Number(digits) / 100) * 60 + (Number(digits) % 100)
    return { min, rest: (q.slice(0, m.index) + ' ' + q.slice(m.index + m[0].length)).trim() }
  }
  return null
}

function linesAtStation(name: string): LineDef[] {
  const seen = new Set<string>()
  const out: LineDef[] = []
  for (const { r } of STATIONS.get(name) ?? []) {
    if (!seen.has(r.line.key)) { seen.add(r.line.key); out.push(r.line) }
  }
  return out
}

/** Sinnvollste Fahrt einer Linie für Direktsprünge (nahe „jetzt − 100 min“). */
function bestRun(lineKey: string): { runId: string; dep: number } {
  const rs = RUNS.filter((r) => r.lineKey === lineKey)
  let best = rs[0]
  for (const r of rs) {
    if (Math.abs(r.dep - (NOW - 100)) < Math.abs(best.dep - (NOW - 100))) best = r
  }
  return { runId: best.id, dep: best.dep }
}

/** Schnellauswahl: Linien des Bahnhofs, wenn die aktuelle Linie dort hält. */
function altLines(lineKey: string, station: string | null): { list: AltLine[]; scoped: boolean } {
  const scoped = !!station && linesAtStation(station).some((l) => l.key === lineKey)
  const base = scoped ? linesAtStation(station!) : LINES
  return {
    list: base.map((L) => ({ key: L.key, label: L.label, cat: L.cat, ...bestRun(L.key) })),
    scoped,
  }
}

export default {
  async meta(): Promise<Meta> {
    await delay(60)
    return { day: 'Fr 17.10.2026', t0: T0, t1: T1, now: NOW }
  },

  async search(q: string): Promise<SearchResults> {
    await delay(140)
    let time: number | null = null
    const th = parseTimeHint(q)
    if (th) { time = th.min; q = th.rest }
    const ql = q.trim()
    const out: SearchResults = { stations: [], lines: [] }
    if (ql.length === 0) { if (time != null) out.time = time; return out }

    const variants = normInput(ql)
    const stations = [...STATIONS.entries()]
      .map(([name, v]) => ({ name, n: v.length, score: matchScore(normName(name), variants) }))
      .filter((s) => s.score >= 0)
      .sort((a, b) => a.score - b.score || a.name.length - b.name.length)
      .slice(0, 7)
    out.stations = stations.map(({ name, n }) => ({ name, n }))

    const lines: SearchResults['lines'] = []
    for (const L of LINES) {
      const normCat = normName(L.cat) + normName(L.label.replace(/^[A-Z]+\s/, ''))
      let score = matchScore(normCat, variants)
      const numOnly = normName(L.label.replace(/^[A-Z]+\s/, ''))
      if (numOnly) {
        const s2 = matchScore(numOnly, variants)
        if (s2 >= 0 && (score < 0 || s2 + 1 < score)) score = s2 + 1
      }
      if (score < 0) continue
      const runs = RUNS.filter((r) => r.lineKey === L.key)
      const mid = runs[0].stops[Math.floor(runs[0].stops.length / 2)].name
      lines.push({
        key: L.key, label: L.label, cat: L.cat,
        ctx: `${mid} ⇢ ${runs[0].stops[runs[0].stops.length - 1].name}`,
        n: runs.length,
      })
    }
    lines.sort((a, b) => a.label.localeCompare(b.label))
    out.lines = lines.slice(0, 6)

    if (time != null) out.time = time
    if (stations.length > 0) {
      out.stationName = stations[0].name
      out.stationLines = linesAtStation(stations[0].name).map((L) => ({
        key: L.key, label: L.label, cat: L.cat, n: 0, ctx: 'hält hier',
      }))
    }
    return out
  },

  async board(station: string, at: number): Promise<BoardData> {
    await delay(200 + rng() * 150)
    const entries = STATIONS.get(station) ?? []
    const lo = at - 150, hi = at + 240
    const lineDefs = linesAtStation(station)
    const rows = entries
      .map(({ r, i }) => ({ r, i, st: r.stops[i] }))
      .filter(({ st }) => st.planned >= lo && st.planned <= hi)
      .sort((a, b) => a.st.planned - b.st.planned)
      .map(({ r, i, st }) => {
        let best: Snap | null = null
        for (const sn of st.snaps) { if (sn.at <= at) best = sn; else break }
        const known = best != null
        return {
          runId: r.id, stopIdx: i, lineKey: r.line.key, planned: st.planned,
          known, delay: best?.prog ?? null, ist: best?.ist ?? false,
          cancelled: best?.cancelled ?? false,
          plat: best?.plat ?? st.platFrom, platFrom: st.platFrom,
          platChanged: known && st.platTo !== st.platFrom && best?.plat === st.platTo,
          dest: r.stops[r.stops.length - 1].name,
          via: r.stops[i + 1]?.name ?? '—',
        }
      })
    return { rows, lineKeys: lineDefs.map((l) => l.key), lines: lineDefs.map(({ key, label, cat }) => ({ key, label, cat })) }
  },

  async journey(runId: string, station: string | null): Promise<JourneyData | null> {
    await delay(200 + rng() * 150)
    const r = RUNS.find((x) => x.id === runId)
    if (!r) return null
    const { line, ...run } = r
    const alt = altLines(line.key, station)
    return {
      run, line: { key: line.key, label: line.label, cat: line.cat, evu: line.evu },
      altLines: alt.list, altScoped: alt.scoped,
    }
  },

  async line(lineKey: string, station: string | null): Promise<LineData> {
    await delay(250 + rng() * 200)
    const L = LINES.find((l) => l.key === lineKey)
    if (!L) throw new Error('line not found')
    const rs = RUNS.filter((r) => r.lineKey === lineKey)
    const runs = rs.map((r) => ({
      id: r.id, dep: r.dep, cancelled: r.cancelled,
      finalD: r.stops[r.stops.length - 1].finalD,
      stops: r.stops.map((s) => ({ name: s.name, finalD: s.finalD, cancelled: s.cancelled })),
    }))
    const fin = rs.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
    const alt = altLines(lineKey, station)
    return {
      line: { key: L.key, label: L.label, cat: L.cat },
      ctx: runs.length ? `${runs[0].stops[Math.floor(runs[0].stops.length / 2)].name} ⇢ ${runs[0].stops[runs[0].stops.length - 1].name}` : '',
      runs,
      altLines: alt.list, altScoped: alt.scoped,
      stats: {
        n: rs.length,
        cx: rs.filter((r) => r.cancelled).length,
        p50: Math.round(pct(fin, 50)),
        p90: Math.round(pct(fin, 90)),
        max: fin.length ? Math.max(...fin) : 0,
        punc: Math.round((fin.filter((d) => d <= 5).length / Math.max(1, fin.length)) * 100),
        gc: rs.reduce((a, r) => a + r.stops.filter((s) => s.platTo !== s.platFrom).length, 0),
      },
    }
  },

  async day(): Promise<DayData> {
    await delay(250 + rng() * 200)
    const finAll = RUNS.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
    const byHour = new Map<number, number[]>()
    for (const r of RUNS) {
      if (r.cancelled) continue
      const h = Math.floor(r.dep / 60)
      byHour.set(h, [...(byHour.get(h) ?? []), r.stops[r.stops.length - 1].finalD])
    }
    const hours = [...byHour.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([h, v]) => ({ h, avg: Math.round((v.reduce((a, b) => a + b, 0) / v.length) * 10) / 10, n: v.length }))
    const leaderboard = LINES.map((L) => {
      const rs = RUNS.filter((r) => r.lineKey === L.key)
      const fin = rs.filter((r) => !r.cancelled).map((r) => r.stops[r.stops.length - 1].finalD)
      return {
        key: L.key, label: L.label, n: rs.length,
        p50: Math.round(pct(fin, 50)), p90: Math.round(pct(fin, 90)),
        max: fin.length ? Math.max(...fin) : 0,
        punc: Math.round((fin.filter((d) => d <= 5).length / Math.max(1, fin.length)) * 100),
        cx: rs.filter((r) => r.cancelled).length,
      }
    }).sort((a, b) => b.p90 - a.p90)
    const top = RUNS.filter((r) => !r.cancelled)
      .sort((a, b) => b.stops[b.stops.length - 1].finalD - a.stops[a.stops.length - 1].finalD)
      .slice(0, 8)
      .map((r) => ({
        runId: r.id, label: r.line.label, dep: r.dep,
        from: r.stops[0].name, to: r.stops[r.stops.length - 1].name,
        finalD: r.stops[r.stops.length - 1].finalD,
      }))
    return {
      kpis: {
        runs: RUNS.length,
        punc: Math.round((finAll.filter((d) => d <= 5).length / Math.max(1, finAll.length)) * 100),
        avg: Math.round((finAll.reduce((a, b) => a + b, 0) / Math.max(1, finAll.length)) * 10) / 10,
        cx: RUNS.filter((r) => r.cancelled).length,
        gc: RUNS.reduce((a, r) => a + r.stops.filter((s) => s.platTo !== s.platFrom).length, 0),
        snaps: RUNS.reduce((a, r) => a + r.stops.reduce((b, s) => b + s.snaps.length, 0), 0),
      },
      hours, leaderboard, top,
    }
  },
}
