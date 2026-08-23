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
  let status: 'ok' | 'warn' | 'err' = 'ok'
  let label = 'healthy'
  if (ago < 0 || ago > 600) {
    status = 'err'
    label = 'no data'
  } else if (ago > 300 || health.recent_runs < 5) {
    status = 'warn'
    label = 'slow'
  }
  const fmtAgo = (s: number) => {
    if (s < 0) return 'never'
    if (s < 60) return `${s}s ago`
    return `${Math.floor(s / 60)}m ago`
  }
  return (
    <span className={`health-badge ${status}`} title={`last scrape: ${fmtAgo(ago)}\nrecent runs (10m): ${health.recent_runs}\nrecent events (10m): ${health.recent_events}`}>
      {label} · {fmtAgo(ago)}
    </span>
  )
}