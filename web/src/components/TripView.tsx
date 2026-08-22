import { useState, useEffect } from 'react'
import { fetchTripStops } from '../api'
import type { TripStop } from '../types'
import { Tip } from './Tip'

interface Props {
  uuid: string
  date: string | null
  lineLabel: string
  onSelectStation: (slug: string, name: string) => void
}

const catColors: Record<string, string> = {
  fern: '#4a9eff', regio: '#4aff4a', s_bahn: '#ff9a4a', u_bahn: '#bb4aff',
  bus: '#888', ersatz: '#ff4a4a', unknown: '#555',
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
function fmtDelay(s: number): string {
  if (s === 0) return ''
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `+${m}m${sec > 0 ? ` ${sec}s` : ''}`
}

export function TripView({ uuid, date, lineLabel, onSelectStation }: Props) {
  const [stops, setStops] = useState<TripStop[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    fetchTripStops(uuid, date || undefined).then(s => { setStops(s); setLoading(false) }).catch(console.error)
  }, [uuid, date])

  const first = stops[0]

  return (
    <div className="event-view">
      <div className="list-header">
        <Tip tip={`Die Haltestellen einer einzelnen Fahrt (${lineLabel}) über alle Bahnhöfe hinweg. Geordnet nach geplanter Zeit. Klick auf einen Bahnhof öffnet dessen Ansicht.`}>
          {lineLabel} — Fahrt {uuid.slice(0, 8)} {date || ''}
        </Tip>
      </div>
      {first && (
        <div className="route-bar">
          <span className="line-badge" style={{ background: catColors[first.line_category] || '#555' }}>
            {first.line_label}
          </span>
          <span className="route-station">{first.station_name}</span>
          <span className="route-arrow">→</span>
          {stops.slice(1).map(s => (
            <span key={s.station_eva} className="route-seg">
              <a
                className="route-link"
                onClick={() => onSelectStation(s.station_slug, s.station_name)}
                title={`Öffne ${s.station_name}`}
              >{s.station_name}</a>
              <span className="route-arrow">›</span>
            </span>
          ))}
        </div>
      )}
      {loading && <div className="loading">Loading…</div>}
      <div className="event-scroll">
        <table className="event-table">
          <thead>
            <tr>
              <th><Tip tip="Bahnhof. Klickbar — öffnet die Ansicht des Bahnhofs.">Station</Tip></th>
              <th><Tip tip="Ankunft (←) oder Abfahrt (→) an diesem Bahnhof.">Dir</Tip></th>
              <th><Tip tip="Die geplante Zeit laut Fahrplan.">Planned</Tip></th>
              <th><Tip tip="Die tatsächliche/aktualisierte Zeit. — = noch keine Abweichung gemeldet.">Actual</Tip></th>
              <th><Tip tip="Verspätung = tatsächlich minus geplant. Rot wenn > 0 (zu spät), blau wenn < 0 (zu früh).">Delay</Tip></th>
              <th><Tip tip="Gleis. Orange 'was X' = Gleiswechsel.">Plat</Tip></th>
              <th><Tip tip="Wann der Crawler diese Beobachtung von der Tafel gelesen hat.">Scraped</Tip></th>
            </tr>
          </thead>
          <tbody>
            {stops.map(s => (
              <tr key={s.station_eva}>
                <td>
                  <a className="route-link" onClick={() => onSelectStation(s.station_slug, s.station_name)}>
                    {s.station_name || s.station_slug}
                  </a>
                </td>
                <td className={s.direction === 'departure' ? 'dep' : 'arr'}>
                  <Tip tip={s.direction === 'departure' ? 'Abfahrt' : 'Ankunft'}>
                    {s.direction === 'departure' ? '→' : '←'}
                  </Tip>
                </td>
                <td><Tip tip={`Geplant: ${fmtFull(s.planned_time)}`}>{fmtTime(s.planned_time)}</Tip></td>
                <td>{s.actual_time
                  ? <Tip tip={`Tatsächlich: ${fmtFull(s.actual_time)}`}>{fmtTime(s.actual_time)}</Tip>
                  : '—'}</td>
                <td className={s.delay_s > 0 ? 'delayed' : s.delay_s < 0 ? 'early' : ''}>
                  <Tip tip={s.delay_s > 0
                    ? `${Math.floor(s.delay_s / 60)}m ${s.delay_s % 60}s Verspätung`
                    : s.delay_s < 0
                      ? `${Math.floor(-s.delay_s / 60)}m zu früh`
                      : 'Pünktlich (keine Abweichung gemeldet)'}>
                    {fmtDelay(s.delay_s)}
                  </Tip>
                </td>
                <td>
                  <Tip tip={s.planned_platform && s.planned_platform !== s.platform
                    ? `Aktuelles Gleis ${s.platform}, geplant war Gleis ${s.planned_platform} (Gleiswechsel!)`
                    : s.platform ? `Gleis ${s.platform}` : 'Kein Gleis angegeben'}>
                    {s.platform || '—'}
                    {s.planned_platform && s.planned_platform !== s.platform && (
                      <span className="gleis-change"> (was {s.planned_platform})</span>
                    )}
                  </Tip>
                </td>
                <td className="muted"><Tip tip={`Gelesen am ${fmtFull(s.scraped_at)}`}>{fmtTime(s.scraped_at)}</Tip></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}