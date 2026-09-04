import { useState, useEffect } from 'react'
import { fetchTripStops } from '../api'
import type { TripStop } from '../types'
import { Tip } from './Tip'

interface Props {
  stopId: string
  lineLabel: string
  onSelectStation?: (slug: string, name: string) => void
}

const catColors: Record<string, string> = {
  ICE: '#4a9eff', IC: '#4a9eff', EC: '#4a9eff', TGV: '#4a9eff', RJ: '#4a9eff', ECE: '#4a9eff',
  RE: '#4aff4a', RB: '#4aff4a', ME: '#4aff4a', IRE: '#4aff4a', MEX: '#4aff4a',
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

export function TripView({ stopId, lineLabel }: Props) {
  const [stops, setStops] = useState<TripStop[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    fetchTripStops(stopId).then(s => { setStops(s); setLoading(false) }).catch(console.error)
  }, [stopId])

  const first = stops[0]

  return (
    <div className="event-view">
      <div className="list-header">
        <Tip tip={`Die Haltestellen einer einzelnen Fahrt (${lineLabel}) über alle Bahnhöfe hinweg. Geordnet nach geplanter Zeit.`}>
          {lineLabel} — Fahrt (Tages-ID {stopId.split('-').slice(-2).join('-')})
        </Tip>
      </div>
      {first && (
        <div className="route-bar">
          <span className="line-badge" style={{ background: catColors[first.line_category] || '#555' }}>
            {first.line_category}{first.train_number ? ` ${first.train_number}` : ''}
          </span>
          <span className="route-station">{first.station_name}</span>
          <span className="route-arrow">→</span>
          {stops.slice(1).map(s => (
            <span key={s.station_eva} className="route-seg">
              <span className="route-link">{s.station_name}</span>
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
              <th><Tip tip="Bahnhof.">Station</Tip></th>
              <th><Tip tip="Die geplante Zeit laut Fahrplan.">Planned</Tip></th>
              <th><Tip tip="Die aktuelle Prognose/Ist-Zeit. — = noch keine Abweichung gemeldet.">Actual</Tip></th>
              <th><Tip tip="Verspätung = aktuell minus geplant. Rot wenn > 0 (zu spät), blau wenn < 0 (zu früh).">Delay</Tip></th>
              <th><Tip tip="Gleis. Orange 'was X' = Gleiswechsel.">Plat</Tip></th>
              <th><Tip tip="Wann der Crawler diese Beobachtung gelesen hat.">Scraped</Tip></th>
            </tr>
          </thead>
          <tbody>
            {stops.map(s => (
              <tr key={s.station_eva}>
                <td>{s.station_name}</td>
                <td><Tip tip={`Geplant: ${fmtFull(s.planned_time)}`}>{fmtTime(s.planned_time)}</Tip></td>
                <td>{s.actual_time
                  ? <Tip tip={`Aktuell: ${fmtFull(s.actual_time)}`}>{fmtTime(s.actual_time)}</Tip>
                  : '—'}</td>
                <td className={(s.delay_s ?? 0) > 0 ? 'delayed' : (s.delay_s ?? 0) < 0 ? 'early' : ''}>
                  <Tip tip={s.delay_s === null
                    ? 'Keine Abweichung gemeldet'
                    : (s.delay_s > 0
                      ? `${Math.floor(s.delay_s / 60)}m ${s.delay_s % 60}s Verspätung`
                      : s.delay_s < 0
                        ? `${Math.floor(-s.delay_s / 60)}m zu früh`
                        : 'Pünktlich')}>
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