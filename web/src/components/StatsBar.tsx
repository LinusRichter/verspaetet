import { useState, useEffect } from 'react'
import { fetchStats } from '../api'
import type { Stats } from '../types'

export function StatsBar() {
  const [stats, setStats] = useState<Stats | null>(null)
  useEffect(() => { fetchStats().then(setStats).catch(console.error) }, [])
  if (!stats) return <div className="stats-bar">Loading…</div>
  const fmtDelay = (s: number) => s > 0 ? `${Math.floor(s / 60)}m ${s % 60}s` : '—'
  return (
    <div className="stats-bar">
      <span><b>{stats.stations}</b> stations</span>
      <span><b>{stats.stop_events}</b> events</span>
      <span><b className="delayed">{stats.delayed}</b> delayed</span>
      <span>avg <b>{fmtDelay(stats.avg_delay_s)}</b></span>
      <span>max <b className="delayed">{fmtDelay(stats.max_delay_s)}</b></span>
    </div>
  )
}