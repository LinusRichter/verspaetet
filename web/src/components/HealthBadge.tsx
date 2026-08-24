import { useState, useEffect } from 'react'
import { fetchHealth } from '../api'
import type { Health } from '../types'

export function HealthBadge() {
  const [health, setHealth] = useState<Health | null>(null)
  useEffect(() => {
    const poll = () => fetchHealth().then(setHealth).catch(console.error)
    poll()
    const id = setInterval(poll, 30000)
    return () => clearInterval(id)
  }, [])
  if (!health) return <span className="health-badge loading">…</span>

  const ago = health.last_scrape_ago_s
  const rate = health.fetch_rate
  let status: 'ok' | 'warn' | 'err' = 'ok'
  let label = 'healthy'
  if (ago < 0 || ago > 600 || rate < 0.1) {
    status = 'err'
    label = 'failing'
  } else if (ago > 300 || rate < 0.5) {
    status = 'warn'
    label = 'degraded'
  }
  const fmtAgo = (s: number) => {
    if (s < 0) return 'never'
    if (s < 60) return `${s}s ago`
    return `${Math.floor(s / 60)}m ago`
  }
  const fmtPct = (r: number) => `${Math.round(r * 100)}%`
  return (
    <span className={`health-badge ${status}`} title={`fetch rate: ${fmtPct(rate)}\nrecent runs (10m): ${health.recent_runs}\nexpected: ~${health.expected_runs / 3}\nrecent events (10m): ${health.recent_events}\nlast scrape: ${fmtAgo(ago)}`}>
      {label} · {fmtPct(rate)} · {fmtAgo(ago)}
    </span>
  )
}