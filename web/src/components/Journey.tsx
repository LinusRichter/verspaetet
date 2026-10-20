// Zuglauf: Haltestreifen, Verspätungs-Entwicklung, Snapshot-Historie.
// Wird nur über Navigation erreicht (Board/Linie/Suche) — nie vorgeladen.

import { useEffect, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { JourneyData } from '../types'
import { useApp } from '../state'
import { fmt, runStateAt, stateAt } from '../lib'
import { delayHex, palette } from '../theme'
import { Chip, Empty, ErrorBox, Loading } from './bits'
import { EvolutionChart } from './charts'

export function Journey(): ReactNode {
  const { nav, scrub, meta, theme, goStation, selectLine, selectRun, setScrub } = useApp()
  const p = palette(theme)
  const [data, setData] = useState<JourneyData | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    if (!nav.run) { setData(null); return }
    setData(null)
    api.journey(nav.run, nav.station)
      .then((d) => { if (alive) { setData(d); setErr(null) } })
      .catch((e) => { if (alive) setErr(String(e)) })
    return () => { alive = false }
  }, [nav.run, nav.station])

  if (!nav.run) return <Empty hint="Zuglauf öffnen: Abfahrt in der Tafel oder Fahrt in der Linien-Ansicht anklicken." />
  if (err) return <ErrorBox msg={err} />
  if (!data || !meta) return <Loading note="Zuglauf wird geladen …" />

  const { run, line } = data
  const selIdx = Math.min(nav.stopIdx, run.stops.length - 1)
  const selStop = run.stops[selIdx]
  const maxD = Math.max(15, ...run.stops.map((s) => s.finalD)) + 3

  return (
    <section>
      {data.altLines.length > 0 && (
        <div className="chipsline">
          <span className="muted chipslabel">{data.altScoped ? `Linien ab ${nav.station}:` : 'Linien:'}</span>
          {data.altLines.map((l) => (
            <button key={l.key} className={`lchip${l.key === line.key ? ' sel' : ''}`}
              onClick={() => { selectLine(l.key); selectRun(l.runId, 0, l.dep) }}>
              {l.label}
            </button>
          ))}
        </div>
      )}
      <div className="runhead">
        <b>{line.label}</b>{line.evu ? <> · {line.evu}</> : null} · Start {fmt(run.dep)}{' '}
        <span className="stname-link" onClick={() => goStation(run.stops[0].name)}>{run.stops[0].name}</span>
        {' → '}
        <span className="stname-link" onClick={() => goStation(run.stops[run.stops.length - 1].name)}>
          {run.stops[run.stops.length - 1].name}
        </span>
        {' · '}{run.stops.length} Halte · <span className="state">{runStateAt(run, scrub)}</span>
      </div>

      <div className="stops">
        {run.stops.map((st, i) => {
          const sta = stateAt(st, scrub)
          const d = sta.known && sta.delay != null ? sta.delay : 0
          return (
            <div key={i} className={`stop${i === selIdx ? ' sel' : ''}`} onClick={() => selectRun(nav.run!, i, nav.runDep ?? undefined)}>
              <div className="st-name" title={st.name}
                onClick={(e) => { e.stopPropagation(); goStation(st.name) }}>{st.name}</div>
              <div className="st-time">{fmt(st.planned)}</div>
              <Chip st={sta} />
              <div className="st-plat">
                Gl. {(sta.known ? sta.plat : '') || st.platFrom || '–'}
                {st.platTo !== st.platFrom && sta.known && sta.plat === st.platTo && <span className="pchg">⇄</span>}
              </div>
              <div className="st-bar">
                <i style={{
                  width: `${Math.min(100, Math.abs(d) / (maxD * 0.7) * 100)}%`,
                  background: sta.cancelled ? p.bad : delayHex(p, d),
                  float: d < 0 ? 'right' : undefined,
                }} />
              </div>
            </div>
          )
        })}
      </div>

      <div className="grid2">
        <div className="panel">
          <h3>Verspätungs-Entwicklung — X = Uhrzeit, Y = min, 1 Kurve = 1 Halt · Klick setzt Wissensstand</h3>
          <EvolutionChart run={run} selIdx={selIdx} meta={meta} scrub={scrub} onScrub={setScrub} p={p} />
        </div>
        <div className="panel">
          <h3>Snapshot-Historie „{selStop.name}“ (≙ stop_events-Rows)</h3>
          <table className="tbl">
            <thead>
              <tr><th className="t">Wissensstand</th><th>Status</th><th className="t">±</th><th className="t">Zeit</th><th className="t">Gl.</th></tr>
            </thead>
            <tbody>
              {selStop.snaps.map((sn, i) => (
                <tr key={i} style={sn.at <= scrub ? undefined : { opacity: 0.35 }}>
                  <td className="t">{fmt(sn.at)}</td>
                  <td>{sn.ist ? 'Ist' : sn.cancelled ? 'Entfall' : 'Prognose'}</td>
                  <td className="t">{sn.cancelled || sn.prog == null ? '–' : `${sn.prog < 0 ? '−' : '+'}${Math.abs(sn.prog)}`}</td>
                  <td className="t">{sn.cancelled || sn.prog == null ? '–' : fmt(selStop.planned + sn.prog)}</td>
                  <td className="t">{sn.plat}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  )
}
