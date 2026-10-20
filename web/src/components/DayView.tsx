// Tages-Übersicht: KPIs, Stundenprofil, Linien-Rangliste, Top-Verspätungen.

import { useEffect, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { DayData } from '../types'
import { useApp } from '../state'
import { clamp, fmt, fmtNum } from '../lib'
import { delayHex, palette } from '../theme'
import { Card, ErrorBox, Loading } from './bits'
import { HourProfile } from './charts'

export function DayView(): ReactNode {
  const { meta, goLine, goRun, theme } = useApp()
  const p = palette(theme)
  const [data, setData] = useState<DayData | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    api.day()
      .then((d) => { if (alive) { setData(d); setErr(null) } })
      .catch((e) => { if (alive) setErr(String(e)) })
    return () => { alive = false }
  }, [])

  if (err) return <ErrorBox msg={err} />
  if (!data) return <Loading note="Übersicht wird geladen — Tagesaggregate werden einmalig berechnet, dann 10 min gecacht …" />

  return (
    <section>
      <div className="vhead">
        <h2>{meta?.day ?? ''}</h2>
        <span className="muted">
          {fmtNum(data.kpis.runs)} Fahrten · {data.leaderboard.length} Linien · {fmtNum(data.kpis.snaps)} Snapshots
        </span>
      </div>

      <div className="cards">
        <Card value={fmtNum(data.kpis.runs)} label="Fahrten" />
        <Card value={`${data.kpis.punc} %`} label="pünktlich (≤5)" />
        <Card value={`+${data.kpis.avg} min`} label="Ø Endverspätung" />
        <Card value={`${fmtNum(data.kpis.cx)} Fahrten`} label="komplett ausgefallen" />
        <Card value={fmtNum(data.kpis.snaps)} label="Snapshots gesammelt" />
      </div>

      <div className="panel">
        <h3>Ø Endverspätung nach Abfahrtsstunde</h3>
        <HourProfile hours={data.hours} p={p} />
      </div>

      <div className="grid2">
        <div className="panel">
          <h3>Linien-Rangliste (nach P90)</h3>
          <table className="tbl">
            <thead>
              <tr><th>Linie</th><th className="t">Fahrten</th><th className="t">P50</th><th className="t">P90</th><th className="t">max</th><th className="t">pünktl.</th><th className="t">✕</th></tr>
            </thead>
            <tbody>
              {data.leaderboard.map((l) => (
                <tr key={l.key} style={{ cursor: 'pointer' }} onClick={() => goLine(l.key)}>
                  <td>{l.label}</td>
                  <td className="t">{l.n}</td>
                  <td className="t">+{l.p50}</td>
                  <td className="t" style={{ color: delayHex(p, l.p90) }}>+{l.p90}</td>
                  <td className="t">+{l.max}</td>
                  <td className="t">{l.punc}%</td>
                  <td className="t">{l.cx}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="panel">
          <h3>Top-Verspätungen (Klick → Zuglauf)</h3>
          {data.top.map((t) => (
            <div key={t.runId} className="topline" onClick={() => goRun(t.runId, 0, t.dep)}>
              <span className="bar" style={{ width: clamp((t.finalD / 45) * 140, 4, 140), background: delayHex(p, t.finalD) }} />
              <span>{t.label} · {fmt(t.dep)} · {t.from} → {t.to}</span>
              <span style={{ marginLeft: 'auto', color: delayHex(p, t.finalD) }}>+{t.finalD} min</span>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
