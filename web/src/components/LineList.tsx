import { useState, useEffect } from 'react'
import { fetchLines } from '../api'
import type { Line } from '../types'
import { Tip } from './Tip'

interface Props {
  slug: string
  stationName: string
  onSelect: (lineCategory: string) => void
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
        <Tip tip={`Alle Zugkategorien, die an ${stationName} halten oder durchfahren. Klick auf eine Zeile zeigt die Abfahrten/Ankünfte dieser Kategorie.`}>
          {stationName} — Zugkategorien
        </Tip>
      </div>
      {loading && <div className="loading">Loading…</div>}
      <div className="scroll-list">
        {lines.map(l => (
          <div key={l.line_category + l.train_number} className="list-row" onClick={() => onSelect(l.line_category)}>
            <Tip tip={`Kategorie: ${catNames[l.line_category] || l.line_category}.`}>
              <span className="line-badge" style={{ background: catColors[l.line_category] || '#555' }}>
                {l.line_category}
              </span>
            </Tip>
            <Tip tip={`Anzahl der aufgezeichneten Beobachtungen dieser Kategorie an diesem Bahnhof. Klick = diese Kategorie öffnen.`}>
              <span className="badge clickable-badge" onClick={e => { e.stopPropagation(); onSelect(l.line_category) }}>
                {l.stop_events}
              </span>
            </Tip>
            <Tip tip={`Durchschnittliche Verspätung dieser Kategorie an ${stationName}, über alle Beobachtungen gemittelt.`}>
              <span className={l.avg_delay_s > 300 ? 'delayed' : ''} onClick={e => { e.stopPropagation(); onSelect(l.line_category) }}>
                {fmtDelay(l.avg_delay_s)}
              </span>
            </Tip>
          </div>
        ))}
      </div>
    </div>
  )
}