import { useState, useEffect } from 'react'
import { fetchRoutes, fetchStations } from '../api'
import type { Route, Station } from '../types'
import { Tip } from './Tip'

interface Props {
  slug: string
  stationName: string
  onSelectLine: (lineCategory: string) => void
  onSelectStation: (slug: string, name: string) => void
}

const catColors: Record<string, string> = {
  ICE: '#4a9eff', IC: '#4a9eff', EC: '#4a9eff', TGV: '#4a9eff', RJ: '#4a9eff', ECE: '#4a9eff',
  RE: '#4aff4a', RB: '#4aff4a', ME: '#4aff4a', IRE: '#4aff4a', MEX: '#4aff4a',
  S: '#ff9a4a',
}
const catNames: Record<string, string> = {
  ICE: 'ICE (Hochgeschwindigkeitszug)', IC: 'Intercity', EC: 'Eurocity',
  TGV: 'TGV (Frankreich)', RJ: 'Railjet (ÖBB)', ECE: 'Eurocity Express',
  RE: 'Regionalexpress', RB: 'Regionalbahn', ME: 'Metronom', IRE: 'Interregio-Express',
  MEX: 'Metropolexpress', S: 'S-Bahn',
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

  const slugForName = (name: string): string | null => {
    const st = stations.find(x => x.name === name)
    return st ? st.slug : null
  }
  const fmtDelay = (s: number) => s > 0 ? `+${Math.floor(s / 60)}m` : '—'

  return (
    <div className="list-view">
      <div className="list-header">
        <Tip tip={`Diese Züge wurden an ${stationName} beobachtet. Jede Zeile zeigt wohin der Zug fährt (Richtung) und über welche Bahnhöfe (Zwischenhalte).`}>
          {stationName} — Routen
        </Tip>
      </div>
      {loading && <div className="loading">Loading…</div>}
      <div className="scroll-list">
        {routes.map((r, i) => (
          <div
            key={r.line_category + r.train_number + r.direction_name + i}
            className="route-row"
            onClick={() => onSelectLine(r.line_category)}
          >
            <Tip tip={`${r.line_category}${r.train_number ? ` ${r.train_number}` : ''} (${catNames[r.line_category] || r.line_category}). Klick = Beobachtungen dieser Kategorie ansehen.`}>
              <span className="line-badge" style={{ background: catColors[r.line_category] || '#555' }}>
                {r.line_category}
              </span>
            </Tip>
            <Tip tip={`Anzahl der Beobachtungen dieser Fahrtrichtung an ${stationName}.`}>
              <span className="badge">{r.stop_events}</span>
            </Tip>
            <Tip tip={`Durchschnittliche Verspätung dieser Fahrtrichtung, über alle Beobachtungen gemittelt.`}>
              <span className={r.avg_delay_s > 300 ? 'delayed' : ''}>{fmtDelay(r.avg_delay_s)}</span>
            </Tip>
            <span className="route-flow">
              {r.via_path.map((vs, j) => {
                const target = slugForName(vs)
                return (
                  <span key={vs + j} className="route-seg">
                    {target ? (
                      <a
                        className="route-link"
                        onClick={e => { e.stopPropagation(); onSelectStation(target, vs) }}
                        title={`Öffne ${vs}`}
                      >{vs}</a>
                    ) : (
                      <span title="Bahnhof nicht in der Stationliste">{vs}</span>
                    )}
                    <span className="route-arrow">›</span>
                  </span>
                )
              })}
              {r.direction_name ? (
                <span className="route-dest">{r.direction_name}</span>
              ) : (
                <span className="route-dest">—</span>
              )}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}