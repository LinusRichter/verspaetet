import { useState, useEffect } from 'react'
import { fetchRoutes, fetchStations } from '../api'
import type { Route, Station } from '../types'
import { Tip } from './Tip'

interface Props {
  slug: string
  stationName: string
  onSelectLine: (lineLabel: string) => void
  onSelectStation: (slug: string, name: string) => void
}

const catColors: Record<string, string> = {
  fern: '#4a9eff', regio: '#4aff4a', s_bahn: '#ff9a4a', u_bahn: '#bb4aff',
  bus: '#888', ersatz: '#ff4a4a', unknown: '#555',
}
const catNames: Record<string, string> = {
  fern: 'Fernverkehr (ICE, TGV, EC)', regio: 'Regionalverkehr (RE, RB, MEX)',
  s_bahn: 'S-Bahn', u_bahn: 'U-Bahn', bus: 'Bus',
  ersatz: 'Ersatzverkehr', unknown: 'Nicht klassifiziert',
}

export function RouteList({ slug, stationName, onSelectLine, onSelectStation }: Props) {
  const [routes, setRoutes] = useState<Route[]>([])
  const [stations, setStations] = useState<Station[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchStations().then(setStations).catch(console.error)
  }, [])
  useEffect(() => {
    setLoading(true)
    fetchRoutes(slug).then(r => { setRoutes(r); setLoading(false) }).catch(console.error)
  }, [slug])

  const nameFor = (s: string): string => {
    const st = stations.find(x => x.slug === s)
    return st ? st.name : s
  }
  const fmtDelay = (s: number) => s > 0 ? `+${Math.floor(s / 60)}m` : '—'

  return (
    <div className="list-view">
      <div className="list-header">
        <Tip tip={`Diese Züge/Linien wurden an ${stationName} beobachtet. Jede Zeile zeigt wohin der Zug fährt (Richtung) und über welche Bahnhöfe (Zwischenhalte). Bahnhöfe sind klickbar — so kannst du zwischen den Bahnhöfen navigieren.`}>
          {stationName} — Routen
        </Tip>
      </div>
      {loading && <div className="loading">Loading…</div>}
      <div className="scroll-list">
        {routes.map((r, i) => (
          <div
            key={r.line_label + r.line_category + r.direction_name + i}
            className="route-row"
            onClick={() => onSelectLine(r.line_label)}
          >
            <Tip tip={`Linie ${r.line_label} (${catNames[r.line_category] || r.line_category}). Klick = Beobachtungen dieser Linie ansehen.`}>
              <span className="line-badge" style={{ background: catColors[r.line_category] || '#555' }}>
                {r.line_label}
              </span>
            </Tip>
            <Tip tip={`Anzahl der Beobachtungen dieser Fahrtrichtung an ${stationName}.`}>
              <span className="badge">{r.stop_events}</span>
            </Tip>
            <Tip tip={`Durchschnittliche Verspätung dieser Fahrtrichtung, über alle Beobachtungen gemittelt.`}>
              <span className={r.avg_delay_s > 300 ? 'delayed' : ''}>{fmtDelay(r.avg_delay_s)}</span>
            </Tip>
            <span className="route-flow">
              {r.via_slugs.map((vs, j) => (
                <span key={vs + j} className="route-seg">
                  <a
                    className="route-link"
                    onClick={e => { e.stopPropagation(); onSelectStation(vs, nameFor(vs)) }}
                    title={`Öffne ${nameFor(vs)}`}
                  >{nameFor(vs)}</a>
                  <span className="route-arrow">›</span>
                </span>
              ))}
              {r.direction_slug && r.direction_name ? (
                <a
                  className="route-link route-dest"
                  onClick={e => { e.stopPropagation(); onSelectStation(r.direction_slug!, nameFor(r.direction_slug!)) }}
                  title={`Öffne ${r.direction_name} (Zielbahnhof)`}
                >{r.direction_name}</a>
              ) : (
                <span className="route-dest">{r.direction_name || '—'}</span>
              )}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}