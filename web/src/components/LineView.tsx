// Linien-Ansicht: Spaghetti, Heatmap, Kennzahlen, schlechteste Fahrten.
// Fahrt-Zeilen der Heatmap und Tabelle springen in den Zuglauf.

import { useEffect, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { DayData, LineData } from '../types'
import { useApp } from '../state'
import { fmt, fmtNum } from '../lib'
import { delayHex, palette } from '../theme'
import { Card, Empty, ErrorBox, Loading } from './bits'
import { SpaghettiChart } from './charts'

function heatHex(p: ReturnType<typeof palette>, d: number): string {
  return d === 0 ? p.grid : delayHex(p, Math.max(d, 1) >= 3 ? d : 1)
}

export function LineView(): ReactNode {
  const { nav, goRun, selectLine, theme } = useApp()
  const p = palette(theme)
  const [data, setData] = useState<LineData | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [day, setDay] = useState<DayData | null>(null)

  useEffect(() => {
    let alive = true
    if (!nav.line) { setData(null); return }
    setData(null)
    api.line(nav.line, nav.station)
      .then((d) => { if (alive) { setData(d); setErr(null) } })
      .catch((e) => { if (alive) setErr(String(e)) })
    return () => { alive = false }
  }, [nav.line, nav.station])

  // Für die Linien-Chips: Rangliste (eine billige, gecachte Tages-Abfrage).
  useEffect(() => {
    if (nav.line) return
    api.day().then(setDay).catch(() => setDay(null))
  }, [nav.line])

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

  const worst = [...data.runs.filter((r) => !r.cancelled)]
    .sort((a, b) => b.finalD - a.finalD).slice(0, 6)
  const route = data.runs[0]?.stops.map((s) => s.name) ?? []

  return (
    <section>
      {data.altLines.length > 0 && (
        <div className="chipsline">
          <span className="muted chipslabel">{data.altScoped ? `Linien ab ${nav.station}:` : 'Linien:'}</span>
          {data.altLines.map((l) => (
            <button key={l.key} className={`lchip${l.key === data.line.key ? ' sel' : ''}`}
              onClick={() => selectLine(l.key)}>
              {l.label}
            </button>
          ))}
        </div>
      )}
      <div className="cards">
        <Card value={fmtNum(data.stats.n)} label="Fahrten" />
        <Card value={`${data.stats.cx} ✕`} label="Ausfälle" />
        <Card value={`+${data.stats.p50} min`} label="P50 Endversp." />
        <Card value={`+${data.stats.p90} min`} label="P90 Endversp." />
        <Card value={`${data.stats.punc} %`} label="pünktlich (≤5)" />
        <Card value={data.stats.gc} label="Gleiswechsel" />
      </div>

      <div className="panel">
        <h3>Verspätung entlang der Route — X = Halt, Y = min · rot = schlechteste, grün = beste Fahrt</h3>
        <SpaghettiChart runs={data.runs} p={p} />
      </div>

      <div className="panel">
        <h3>Heatmap Fahrten × Halte — Farbe = Verspätung · Zeile klicken → Zuglauf</h3>
        <div className="hmwrap">
          <div className="hm" style={{ gridTemplateColumns: `64px repeat(${route.length}, 30px)` }}>
            <div />
            {route.map((n, i) => (
              <div key={i} className="hmcollab" title={n}>{n.slice(0, 5)}</div>
            ))}
            {data.runs.map((r) => (
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

function Row({ run, p, onOpen }: { run: LineData['runs'][number]; p: ReturnType<typeof palette>; onOpen: () => void }): ReactNode {
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
