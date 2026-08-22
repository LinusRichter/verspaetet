import { useState, useEffect } from 'react'
import { fetchTopDelays } from '../api'
import type { TopDelay } from '../types'
import { Tip } from './Tip'

function fmtDelay(s: number): string {
  const m = Math.floor(s / 60)
  return `+${m}m`
}
function fmtTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })
}

export function TopDelays() {
  const [delays, setDelays] = useState<TopDelay[]>([])
  useEffect(() => {
    const load = () => fetchTopDelays(20).then(setDelays).catch(console.error)
    load()
    const id = setInterval(load, 30000)
    return () => clearInterval(id)
  }, [])

  return (
    <div className="top-delays">
      <div className="sidebar-header"><Tip tip="Die 20 Züge mit der höchsten gemessenen Verspätung, über alle Bahnhöfe. Aktualisiert alle 30s.">Top Delays</Tip></div>
      <div className="sidebar-scroll">
        {delays.map((d, i) => (
          <div key={i} className="delay-row">
            <div className="delay-line">
              <Tip tip={`Linie ${d.line_label} am Bahnhof ${d.station_name}`}>
                <span className="delay-badge">{d.line_label}</span>
              </Tip>
              <Tip tip={`${Math.floor(d.delay_s / 60)}m ${d.delay_s % 60}s Verspätung`}>
                <span className="delay-value">{fmtDelay(d.delay_s)}</span>
              </Tip>
            </div>
            <div className="delay-meta">
              {d.station_name} → {d.direction_name || '—'}
            </div>
            <div className="delay-time muted">
              <Tip tip={`Geplant ${new Date(d.planned_time).toLocaleString('de-DE')} — Tatsächlich ${d.actual_time ? new Date(d.actual_time).toLocaleString('de-DE') : 'unbekannt'}`}>
                {fmtTime(d.planned_time)} → {fmtTime(d.actual_time || '')}
              </Tip>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}