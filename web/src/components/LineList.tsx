import { useState, useEffect } from 'react'
import { fetchLines } from '../api'
import type { Line } from '../types'
import { Tip } from './Tip'

interface Props {
  slug: string
  stationName: string
  onSelect: (lineLabel: string) => void
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

export function LineList({ slug, stationName, onSelect }: Props) {
  const [lines, setLines] = useState<Line[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    fetchLines(slug).then(l => { setLines(l); setLoading(false) }).catch(console.error)
  }, [slug])

  const fmtDelay = (s: number) => s > 0 ? `+${Math.floor(s / 60)}m` : '—'

  return (
    <div className="list-view">
      <div className="list-header">
        <Tip tip={`Alle Linien, die an ${stationName} halten oder durchfahren. Klick auf eine Zeile zeigt die Abfahrten/Ankünfte dieser Linie.`}>
          {stationName} — Linien
        </Tip>
      </div>
      {loading && <div className="loading">Loading…</div>}
      <div className="scroll-list">
        {lines.map(l => (
          <div key={l.line_label + l.line_category} className="list-row" onClick={() => onSelect(l.line_label)}>
            <Tip tip={`Linien-Label: ${l.line_label}. Kategorie: ${catNames[l.line_category] || l.line_category}.`}>
              <span className="line-badge" style={{ background: catColors[l.line_category] || '#555' }}>
                {l.line_label}
              </span>
            </Tip>
            <Tip tip={`Anzahl der gescrapten Beobachtungen (StopEvents) dieser Linie an diesem Bahnhof. Klick = diese Linie öffnen.`}>
              <span className="badge clickable-badge" onClick={e => { e.stopPropagation(); onSelect(l.line_label) }}>
                {l.stop_events}
              </span>
            </Tip>
            <Tip tip={`Durchschnittliche Verspätung dieser Linie an ${stationName}, über alle Beobachtungen gemittelt.`}>
              <span className={l.avg_delay_s > 300 ? 'delayed' : ''} onClick={e => { e.stopPropagation(); onSelect(l.line_label) }}>
                {fmtDelay(l.avg_delay_s)}
              </span>
            </Tip>
          </div>
        ))}
      </div>
    </div>
  )
}