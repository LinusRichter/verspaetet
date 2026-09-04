import { useState, useEffect } from 'react'
import { fetchEvents, fetchStations } from '../api'
import type { StopEvent, Station } from '../types'
import { Tip } from './Tip'

interface Props {
  slug: string
  lineLabel: string
  stationName: string
  onSelectStation: (slug: string, name: string) => void
  onSelectTrip: (stopId: string) => void
}

const catColors: Record<string, string> = {
  ICE: '#4a9eff', IC: '#4a9eff', TGV: '#4a9eff', RJ: '#4a9eff',
  RE: '#4aff4a', RB: '#4aff4a', ME: '#4aff4a',
  S: '#ff9a4a',
}

function fmtTime(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })
}
function fmtFull(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleString('de-DE', {
    weekday: 'short', day: '2-digit', month: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}
function fmtDelay(s: number | null): string {
  if (s === null || s === 0) return ''
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `+${m}m${sec > 0 ? ` ${sec}s` : ''}`
}

export function EventTable({ slug, lineLabel, stationName, onSelectStation, onSelectTrip }: Props) {
  const [events, setEvents] = useState<StopEvent[]>([])
  const [stations, setStations] = useState<Station[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchStations().then(setStations).catch(console.error)
  }, [])
  useEffect(() => {
    setLoading(true)
    fetchEvents(slug, lineLabel).then(e => { setEvents(e); setLoading(false) }).catch(console.error)
  }, [slug, lineLabel])

  const nameFor = (name: string): string => {
    // Via path entries are station NAMES (not slugs); look up by name.
    const st = stations.find(x => x.name === name)
    return st ? st.name : name
  }
  const slugForName = (name: string): string | null => {
    const st = stations.find(x => x.name === name)
    return st ? st.slug : null
  }

  const first = events[0]
  const routeEl = first && (first.direction_name || (first.via_path && first.via_path.length)) ? (
    <div className="route-bar">
      <span className="route-station">{stationName}</span>
      <span className="route-arrow">→</span>
      <span className="line-badge" style={{ background: catColors[first.line_category] || '#555' }}>
        {first.line_category}{first.train_number ? ` ${first.train_number}` : ''}
      </span>
      <span className="route-arrow">→</span>
      {first.via_path && first.via_path.map((vs, i) => (
        <span key={vs + i} className="route-seg">
          <a
            className="route-link"
            onClick={() => { const s = slugForName(vs); if (s) onSelectStation(s, nameFor(vs)) }}
            title={`Öffne ${nameFor(vs)}`}
          >{nameFor(vs)}</a>
          <span className="route-arrow">›</span>
        </span>
      ))}
      {first.direction_name ? (
        first.via_path && first.via_path.length ? (
          <span className="route-dest">{first.direction_name}</span>
        ) : (
          <a
            className="route-link route-dest"
            onClick={() => { const s = slugForName(first.direction_name!); if (s) onSelectStation(s, first.direction_name!) }}
            title={`Öffne ${first.direction_name} (Zielbahnhof)`}
          >{first.direction_name}</a>
        )
      ) : (
        <span className="route-dest">—</span>
      )}
    </div>
  ) : null

  return (
    <div className="event-view">
      <div className="list-header">
        <Tip tip={`Alle aufgezeichneten Beobachtungen von ${lineLabel} an ${stationName}. Mehrere Zeilen für denselben Zug entstehen, wenn sich Verspätung oder Gleis zwischen den Scans ändern.`}>
          {stationName} — {lineLabel}
        </Tip>
      </div>
      {loading && <div className="loading">Loading…</div>}
      {routeEl}
      <div className="event-scroll">
        <table className="event-table">
          <thead>
            <tr>
              <th><Tip tip="Abfahrt (→) oder Ankunft (←). Zielbahnhof (bei Abfahrt) bzw. Herkunft (bei Ankunft).">Dir</Tip></th>
              <th><Tip tip="Die geplante Zeit laut Fahrplan.">Planned</Tip></th>
              <th><Tip tip="Die aktuelle Prognose/Ist-Zeit. — = noch keine Abweichung gemeldet.">Actual</Tip></th>
              <th><Tip tip="Verspätung = aktuell minus geplant (Sekunden). Rot wenn > 0.">Delay</Tip></th>
              <th><Tip tip="Gleis. Orange 'was X' = Gleiswechsel (geplant war ein anderes Gleis).">Plat</Tip></th>
              <th><Tip tip="Wann der Crawler diese Zeile gelesen hat. Der Abstand zwischen Scraped-Zeilen zeigt, wie sich die Verspätung entwickelt.">Scraped</Tip></th>
              <th><Tip tip="Klick öffnet diese Fahrt über ALLE Bahnhöfe (geplant/tatsächlich an jedem Halt).">Trip</Tip></th>
            </tr>
          </thead>
          <tbody>
            {events.map(e => (
              <tr key={e.id}>
                <td className={e.direction === 'departure' ? 'dep' : 'arr'}>
                  <Tip tip={`${e.direction === 'departure' ? 'Abfahrt' : 'Ankunft'}. ${e.direction_name || 'unbekannt'}`}>
                    {e.direction === 'departure' ? '→' : '←'} {e.direction_name || '—'}
                  </Tip>
                </td>
                <td><Tip tip={`Geplant: ${fmtFull(e.planned_time)}`}>{fmtTime(e.planned_time)}</Tip></td>
                <td>{e.actual_time
                  ? <Tip tip={`Aktuell: ${fmtFull(e.actual_time)}`}>{fmtTime(e.actual_time)}</Tip>
                  : '—'}</td>
                <td className={(e.delay_s ?? 0) > 0 ? 'delayed' : ''}>
                  <Tip tip={e.cancelled
                    ? 'Zug fällt aus'
                    : e.delay_s === null
                      ? 'Keine Abweichung gemeldet'
                      : (e.delay_s > 0
                        ? `${Math.floor(e.delay_s / 60)}m ${e.delay_s % 60}s Verspätung`
                        : 'Pünktlich')}>
                    {e.cancelled ? <span className="delayed">fällt aus</span> : fmtDelay(e.delay_s)}
                  </Tip>
                </td>
                <td>
                  <Tip tip={e.planned_platform && e.planned_platform !== e.platform
                    ? `Aktuelles Gleis ${e.platform}, geplant war Gleis ${e.planned_platform} (Gleiswechsel!)`
                    : e.platform ? `Gleis ${e.platform}` : 'Kein Gleis angegeben'}>
                    {e.platform || '—'}
                    {e.planned_platform && e.planned_platform !== e.platform && (
                      <span className="gleis-change"> (was {e.planned_platform})</span>
                    )}
                  </Tip>
                </td>
                <td className="muted"><Tip tip={`Gelesen am ${fmtFull(e.scraped_at)}`}>{fmtTime(e.scraped_at)}</Tip></td>
                <td>
                  <a
                    className="trip-link"
                    onClick={() => onSelectTrip(e.stop_id)}
                    title="Öffne die Fahrt über alle Bahnhöfe"
                  >Fahrt</a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}