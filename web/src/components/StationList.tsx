import { useState, useEffect } from 'react'
import { fetchStations } from '../api'
import type { Station } from '../types'
import { Tip } from './Tip'

interface Props {
  onSelect: (slug: string, name: string) => void
}

export function StationList({ onSelect }: Props) {
  const [stations, setStations] = useState<Station[]>([])
  const [filter, setFilter] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchStations().then(s => { setStations(s); setLoading(false) }).catch(console.error)
  }, [])

  const filtered = stations.filter(s =>
    s.name.toLowerCase().includes(filter.toLowerCase())
  )

  return (
    <div className="list-view">
      <input
        className="search-box"
        type="text"
        placeholder="Filter stations…"
        value={filter}
        onChange={e => setFilter(e.target.value)}
      />
      {loading && <div className="loading">Loading…</div>}
      <div className="scroll-list">
        {filtered.map(s => (
          <div key={s.eva} className="list-row" onClick={() => onSelect(s.slug, s.name)}>
            <Tip tip={`${s.name} — Bahnhof mit ${s.stop_events} aufgezeichneten Beobachtungen. Klick öffnet die Linien & Routen dieses Bahnhofs.`}>
              <span className="row-title">{s.name}</span>
            </Tip>
            <Tip tip={`${s.stop_events} gescrapte Abfahrts-/Ankunfts-Einträge für diesen Bahnhof.`}>
              <span className="badge clickable-badge" onClick={e => { e.stopPropagation(); onSelect(s.slug, s.name) }}>
                {s.stop_events}
              </span>
            </Tip>
          </div>
        ))}
      </div>
    </div>
  )
}