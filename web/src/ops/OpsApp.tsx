import { useEffect, useState } from 'react'
import { fetchOpsQueues, fetchOpsFailed, fetchOpsCollection, fetchOpsExports, fetchOpsHealth } from './api'
import type { OpsQueue, OpsFailedTask, OpsCollection, OpsExportChunk, OpsHealth } from './types'

function useInterval(fn: () => void, ms: number) {
  useEffect(() => {
    fn()
    const id = setInterval(fn, ms)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
}

function Card({ title, children, wide }: { children: React.ReactNode; title: string; wide?: boolean }) {
  return (
    <div className={`card ${wide ? 'card-wide' : ''}`}>
      <h2>{title}</h2>
      {children}
    </div>
  )
}

function Metric({ label, value, warn }: { value: string | number; label: string; warn?: boolean }) {
  return (
    <div className={`metric ${warn ? 'metric-warn' : ''}`}>
      <b>{value}</b>
      <span>{label}</span>
    </div>
  )
}

function QueueTable({ queues }: { queues: OpsQueue[] }) {
  if (!queues.length) return <div className="muted">no queues</div>
  return (
    <table>
      <thead>
        <tr>
          <th>queue</th><th>pending</th><th>active</th><th>scheduled</th><th>retry</th><th>archived</th><th>processed</th><th>failed</th><th>latency</th>
        </tr>
      </thead>
      <tbody>
        {queues.map(q => (
          <tr key={q.queue} className={q.retry + q.archived > 0 ? 'row-warn' : ''}>
            <td>{q.queue}</td>
            <td>{q.pending}</td>
            <td>{q.active}</td>
            <td>{q.scheduled}</td>
            <td>{q.retry}</td>
            <td>{q.archived}</td>
            <td>{q.processed}</td>
            <td>{q.failed}</td>
            <td className="muted" title={q.last_error}>{q.last_error ? q.last_failed_at.slice(11, 19) : '—'}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function FailedTable({ tasks }: { tasks: OpsFailedTask[] }) {
  if (!tasks.length) return <div className="muted">no failed tasks 🎉</div>
  return (
    <table>
      <thead>
        <tr><th>time</th><th>type</th><th>payload</th><th>retries</th><th>error</th></tr>
      </thead>
      <tbody>
        {tasks.slice(0, 25).map(t => (
          <tr key={t.id}>
            <td className="muted">{t.last_failed_at.slice(11, 19)}</td>
            <td>{t.type}</td>
            <td className="mono">{t.payload}</td>
            <td>{t.retries}</td>
            <td className="err-text" title={t.last_error}>{t.last_error.slice(0, 90)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function CollectionPanel({ c, h }: { c: OpsCollection | null; h: OpsHealth | null }) {
  if (!c || !h) return <div className="muted">loading…</div>
  return (
    <div className="metric-grid">
      <Metric value={c.events_per_minute.toFixed(0)} label="events/min" />
      <Metric value={`${(h.fetch_rate * 100).toFixed(0)}%`} label="fetch rate" warn={h.fetch_rate < 0.9} />
      <Metric value={`${c.stations_scraped_last_hour}/${c.total_stations}`} label="stations 1h" />
      <Metric value={c.no_iris_stations} label="no_iris" />
      <Metric value={c.pending_stations} label="pending names" warn={c.pending_stations > 0} />
      <Metric value={`${c.punctual_pct.toFixed(1)}%`} label="punctual 1h" />
      <Metric value={`${c.cancelled_pct.toFixed(1)}%`} label="cancelled 1h" />
      <Metric value={c.avg_snaps_per_stop.toFixed(2)} label="snaps/stop" />
      <Metric value={c.db_size} label="db size" />
      <Metric value={c.oldest_event.slice(0, 16).replace('T', ' ')} label="dataset start" />
    </div>
  )
}

function ExportsPanel({ chunks }: { chunks: OpsExportChunk[] }) {
  if (!chunks.length) return <div className="muted">no chunks yet (first export: Oct 4)</div>
  return (
    <table>
      <thead><tr><th>chunk</th><th>files</th><th>size</th></tr></thead>
      <tbody>
        {chunks.map(c => (
          <tr key={c.name}>
            <td>{c.name}</td>
            <td>{c.files}</td>
            <td>{(c.total_bytes / 1024 / 1024).toFixed(1)} MB</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export default function OpsApp() {
  const [queues, setQueues] = useState<OpsQueue[]>([])
  const [failed, setFailed] = useState<OpsFailedTask[]>([])
  const [collection, setCollection] = useState<OpsCollection | null>(null)
  const [exports, setExports] = useState<OpsExportChunk[]>([])
  const [health, setHealth] = useState<OpsHealth | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [ts, setTs] = useState<string>('')

  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const [q, f, c, e, hh] = await Promise.all([
          fetchOpsQueues(), fetchOpsFailed(), fetchOpsCollection(), fetchOpsExports(), fetchOpsHealth(),
        ])
        if (!alive) return
        setQueues(q); setFailed(f); setCollection(c); setExports(e); setHealth(hh)
        setError(null)
        setTs(new Date().toLocaleTimeString('de-DE'))
      } catch (e) {
        if (alive) setError(String(e))
      }
    }
    load()
    const id = setInterval(load, 15_000)
    return () => { alive = false; clearInterval(id) }
  }, [])

  return (
    <div className="ops">
      <div className="top-bar">
        <span className="ops-title">verspaetet <b>ops</b></span>
        <span className="muted">
          {error ? <span className="err-text">API ERROR: {error}</span> : `refreshed ${ts} · auto 15s`}
          {' · '}
          <a href="/" className="ui-link">data ui</a>
        </span>
      </div>

      <div className="ops-grid">
        <Card title="collection" wide>
          <CollectionPanel c={collection} h={health} />
        </Card>

        <Card title="queues">
          <QueueTable queues={queues} />
        </Card>

        <Card title="failed / archived tasks (latest)">
          <FailedTable tasks={failed} />
        </Card>

        <Card title="monthly exports">
          <ExportsPanel chunks={exports} />
        </Card>
      </div>
    </div>
  )
}

// small helper hook (unused yet, kept for charts)
export { useInterval }