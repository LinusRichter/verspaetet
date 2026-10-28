// Linien-Ansicht: Spaghetti, Heatmap, Kennzahlen, schlechteste Fahrten.
// Mit Stations-Scope: Richtungs-Toggle (→ Station / Station →) — aus der
// Position des Bahnhofs in der Halteliste je Fahrt abgeleitet. Stats
// werden bei Filterung clientseitig neu gerechnet. Alle Responses kommen
// aus dem 60-s-Frontend-Cache (api.ts).

import { useEffect, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { DayData, LineData, LineRun } from '../types'
import { useApp, type LineDir } from '../state'
import { fmt, pct } from '../lib'
import { delayHex, palette } from '../theme'
import { Card, Empty, ErrorBox, Loading } from './bits'
import { SpaghettiChart } from './charts'

function heatHex(p: ReturnType<typeof palette>, d: number): string {
  return d === 0 ? p.grid : delayHex(p, Math.max(d, 1) >= 3 ? d : 1)
}

/** Richtung einer Fahrt aus Sicht des Bahnhofs: frühe Haltposition = „Station →", späte = „→ Station". */
function runDir(r: LineRun, station: string): LineDir | null {
  const idx = r.stops.findIndex((s) => s.name === station)
  if (idx < 0) return null
  return idx < r.stops.length / 2 ? 'out' : 'in'
}

function statsOf(runs: LineRun[]) {
  const fin = runs.filter((r) => !r.cancelled).map((r) => r.finalD)
  const cx = runs.filter((r) => r.cancelled).length
  const punc = fin.length ? Math.round((fin.filter((d) => d <= 5).length / fin.length) * 100) : 0
  return {
    n: runs.length,
    cx,
    p50: Math.round(pct(fin, 50)),
    p90: Math.round(pct(fin, 90)),
    max: fin.length ? Math.max(...fin) : 0,
    punc,
  }
}

export function LineView(): ReactNode {
  const { nav, goRun, selectLineDir, theme } = useApp()
  const p = palette(theme)
  const [data, setData] = useState<LineData | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [day, setDay] = useState<DayData | null>(null)

  useEffect(() => {
    let alive = true
    if (!nav.line) { setData(null); return }
    setData(null)
    api.line(nav.line, nav.station, nav.day)
      .then((d) => { if (alive) { setData(d); setErr(null) } })
      .catch((e) => { if (alive) setErr(String(e)) })
    return () => { alive = false }
  }, [nav.line, nav.station, nav.day])

  // Für die Linien-Chips ohne eigene Linie: Rangliste (gecacht).
  useEffect(() => {
    if (nav.line) return
    api.day(nav.day).then(setDay).catch(() => setDay(null))
  }, [nav.line, nav.day])

  if (!nav.line) {
    return (
      <section>
        <Empty hint="Linie wählen — über die Suche, einen Bahnhof oder die Übersicht." />
        {day && (
          <div className="chipsline" style={{ marginTop: 12 }}>
            <span className="muted chipslabel">Linien des Tages (nach P90):</span>
            {day.leaderboard.map((l) => (
              <LineChip key={l.key} k={l.key} label={l.label} />
            ))}
          </div>
        )}
      </section>
    )
  }
  if (err) return <ErrorBox msg={err} />
  if (!data) return <Loading note="Linie wird geladen …" />

  const scoped = !!nav.station
  const allRuns = data.runs
  const runs = scoped && nav.lineDir !== 'all'
    ? allRuns.filter((r) => runDir(r, nav.station!) === nav.lineDir)
    : allRuns
  const nIn = scoped ? allRuns.filter((r) => runDir(r, nav.station!) === 'in').length : 0
  const nOut = scoped ? allRuns.filter((r) => runDir(r, nav.station!) === 'out').length : 0
  const stats = scoped && nav.lineDir !== 'all' ? statsOf(runs) : { ...data.stats, gc: data.stats.gc }

  const worst = [...runs.filter((r) => !r.cancelled)]
    .sort((a, b) => b.finalD - a.finalD).slice(0, 6)
  const route = runs[0]?.stops.map((s) => s.name) ?? data.runs[0]?.stops.map((s) => s.name) ?? []

  return (
    <section>
      {scoped && (
        <div className="chipsline">
          <span className="muted chipslabel">Richtung:</span>
          <button className={`lchip${nav.lineDir === 'all' ? ' sel' : ''}`} onClick={() => selectLineDir('all')}>
            Alle ({allRuns.length})
          </button>
          <button className={`lchip${nav.lineDir === 'in' ? ' sel' : ''}`} onClick={() => selectLineDir('in')}>
            → {nav.station} ({nIn})
          </button>
          <button className={`lchip${nav.lineDir === 'out' ? ' sel' : ''}`} onClick={() => selectLineDir('out')}>
            {nav.station} → ({nOut})
          </button>
        </div>
      )}
      <div className="cards">
        <Card value={stats.n} label="Fahrten" />
        <Card value={`${stats.cx} ✕`} label="Ausfälle" />
        <Card value={`+${stats.p50} min`} label="P50 Endversp." />
        <Card value={`+${stats.p90} min`} label="P90 Endversp." />
        <Card value={`${stats.punc} %`} label="pünktlich (≤5)" />
        <Card value={data.stats.gc} label="Gleiswechsel" />
      </div>

      <div className="panel">
        <h3>Verspätung entlang der Route — X = Halt, Y = min · rot = schlechteste, grün = beste Fahrt</h3>
        <SpaghettiChart runs={runs} p={p} />
      </div>

      <div className="panel">
        <h3>Heatmap Fahrten × Halte — Farbe = Verspätung · Zeile klicken → Zuglauf</h3>
        <div className="hmwrap">
          <div className="hm" style={{ gridTemplateColumns: `64px repeat(${route.length}, 30px)` }}>
            <div />
            {route.map((n, i) => (
              <div key={i} className="hmcollab" title={n}>{n.slice(0, 5)}</div>
            ))}
            {runs.map((r) => (
              <Row key={r.id} run={r} p={p} onOpen={() => goRun(r.id, 0, r.dep)} />
            ))}
          </div>
        </div>
      </div>

      <div className="panel">
        <h3>Schlechteste Fahrten</h3>
        <table className="tbl">
          <thead>
            <tr><th>Linie</th><th className="t">Start</th><th>von → nach</th><th className="t">Endverspätung</th><th className="t">max unterwegs</th></tr>
          </thead>
          <tbody>
            {worst.map((r) => (
              <tr key={r.id} style={{ cursor: 'pointer' }} onClick={() => goRun(r.id, r.stops.length - 1, r.dep)}>
                <td>{data.line.label}</td>
                <td className="t">{fmt(r.dep)}</td>
                <td>{route[0]} → {route[route.length - 1]}</td>
                <td className="t" style={{ color: delayHex(p, r.finalD) }}>+{r.finalD}</td>
                <td className="t">{Math.max(...r.stops.map((s) => s.finalD))}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function Row({ run, p, onOpen }: { run: LineRun; p: ReturnType<typeof palette>; onOpen: () => void }): ReactNode {
  return (
    <>
      <div className="hm-rowlab" title="Zuglauf öffnen" onClick={onOpen}>
        {fmt(run.dep)}{run.cancelled ? ' ✕' : ''}
      </div>
      {run.stops.map((s, i) => (
        <div key={i} className="cell" style={{ background: s.cancelled ? p.bad : heatHex(p, s.finalD) }}
          title={`${fmt(run.dep)} · ${s.name} · ${s.cancelled ? 'entfällt' : `+${s.finalD} min`}`}>
          {s.cancelled ? '✕' : ''}
        </div>
      ))}
    </>
  )
}

function LineChip({ k, label }: { k: string; label: string }): ReactNode {
  const { goLine } = useApp()
  return <button className="lchip" onClick={() => goLine(k)}>{label}</button>
}