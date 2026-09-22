import { useEffect, useState } from 'react'
import { fetchOpsQueues, fetchOpsFailed, fetchOpsCollection, fetchOpsExports, fetchOpsHealth } from './api'
import type { OpsQueue, OpsFailedTask, OpsCollection, OpsExportChunk, OpsHealth } from './types'

function Card({ title, hint, children, wide }: { hint?: string; children: React.ReactNode; title: string; wide?: boolean }) {
  return (
    <div className={`card ${wide ? 'card-wide' : ''}`}>
      <h2>{title}{hint && <Info text={hint} />}</h2>
      {children}
    </div>
  )
}

// Hover tooltip (title attr — zero JS cost). Content lives with the data it explains.
function Info({ text }: { text: string }) {
  return <span className="info" title={text}>?</span>
}

function Metric({ label, value, warn, hint }: { value: string | number; label: string; warn?: boolean; hint?: string }) {
  return (
    <div className={`metric ${warn ? 'metric-warn' : ''}`} title={hint}>
      <b>{value}</b>
      <span>{label}{hint && <Info text={hint} />}</span>
    </div>
  )
}

const QUEUE_HINTS: Record<string, string> = {
  queue: 'Task-Typ der Warteschlange in Redis. "default" = Stations-Fetches (board:fetch).',
  pending: 'Tasks in der Warteschlange, noch nicht angefangen. Kurz nach einem Tick normal (~180); dauerhaft hoch = Worker hinkt nach.',
  active: 'Tasks, die der Worker GERADE verarbeitet (IRIS-Request läuft). Sollte ≈ Worker-Konkurrenz (20) oder weniger sein.',
  scheduled: 'Tasks mit geplanter Startzeit in der Zukunft. Bei uns unüblich (Scheduler enqueued sofort) — hier sollte immer 0 stehen.',
  retry: 'Fehlgeschlagene Tasks, die automatisch erneut versucht werden (max. 3x, exponentielles Backoff). Kurz nach IRIS-429s normal; hier bleiben sie NICHT liegen.',
  archived: 'Fehlgeschlagene Tasks, deren alle Retries verbraucht sind — werden NICHT mehr versucht. Das sind die echten Verluste pro Tick.',
  processed: 'Alle je abgeschlossenen Tasks (erfolgreich + fehlgeschlagen) seit Scheduler-Start.',
  failed: 'Fehlgeschlagene Versuche insgesamt. Enthält auch Retries, die später klappten — also nicht 1:1 verlorene Daten.',
  latency: 'Zeitstempel des letzten archivierten Fehlers (UTC) — "latency" hier = wie alt die letzte Fehllage ist.',
}

function QueueTable({ queues }: { queues: OpsQueue[] }) {
  if (!queues.length) return <div className="muted">no queues</div>
  return (
    <table>
      <thead>
        <tr>
          <th title={QUEUE_HINTS.queue}>queue</th>
          <th title={QUEUE_HINTS.pending}>pending</th>
          <th title={QUEUE_HINTS.active}>active</th>
          <th title={QUEUE_HINTS.scheduled}>scheduled</th>
          <th title={QUEUE_HINTS.retry}>retry</th>
          <th title={QUEUE_HINTS.archived}>archived</th>
          <th title={QUEUE_HINTS.processed}>processed</th>
          <th title={QUEUE_HINTS.failed}>failed</th>
          <th title={QUEUE_HINTS.latency}>last fail</th>
        </tr>
      </thead>
      <tbody>
        {queues.map(q => (
          <tr key={q.queue} className={q.retry + q.archived > 0 ? 'row-warn' : ''}>
            <td>{q.queue}</td>
            <td title={QUEUE_HINTS.pending}>{q.pending}</td>
            <td title={QUEUE_HINTS.active}>{q.active}</td>
            <td title={QUEUE_HINTS.scheduled}>{q.scheduled}</td>
            <td title={QUEUE_HINTS.retry}>{q.retry}</td>
            <td title={QUEUE_HINTS.archived}>{q.archived}</td>
            <td title={QUEUE_HINTS.processed}>{q.processed}</td>
            <td title={QUEUE_HINTS.failed}>{q.failed}</td>
            <td className="muted" title={q.last_error || 'no recent failures'}>
              {q.last_error ? q.last_failed_at.replace('T', ' ').slice(0, 19) : '—'}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function ageOf(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  const m = Math.floor(ms / 60000)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function FailedTable({ tasks }: { tasks: OpsFailedTask[] }) {
  if (!tasks.length) return <div className="muted">no failed tasks 🎉</div>
  return (
    <table>
      <thead>
        <tr>
          <th title="Wann der Task zuletzt fehlschlug (UTC). 'age' zeigt dir, ob er frisch ist oder schon alt.">last fail</th>
          <th title="retry = Worker probiert es bald wieder; archived = alle Versuche verbraucht, Task liegt still">state</th>
          <th>payload</th>
          <th title="Wie viele Versuche schon gemacht wurden (von max. 3)">tries</th>
          <th>error</th>
        </tr>
      </thead>
      <tbody>
        {tasks.slice(0, 20).map(t => (
          <tr key={t.id}>
            <td className="muted" title={t.last_failed_at}>{t.last_failed_at.replace('T', ' ').slice(0, 19)} · {ageOf(t.last_failed_at)}</td>
            <td><span className={`state-pill ${t.state === 'archived' ? 'pill-archived' : 'pill-retry'}`}>{t.state}</span></td>
            <td className="mono">{t.payload}</td>
            <td>{t.retries}/3</td>
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
      <Metric
        value={c.events_per_minute.toFixed(0)}
        label="events/min"
        hint="Erfolgreich gespeicherte Beobachtungen pro Minute (letzte Stunde gemittelt). Ein Event = ein Zug an einer Station in einem Snapshot — Züge, deren Werte sich seit letztem Snapshot nicht änderten, werden dedupliziert und erscheinen nicht erneut. Nur erfolgreiche Speicherungen zählen — Fehlversuche fehlen hier."
      />
      <Metric
        value={`${(h.fetch_rate * 100).toFixed(0)}%`}
        label="fetch rate"
        warn={h.fetch_rate < 0.9}
        hint="Anteil der Stationen, die in den letzten 10 min erwartungsgemäß gescrapt wurden (100 % = jeder geplante Tick lief). Einbrüche deuten auf Scheduler-/Redis-/Rate-Limit-Probleme."
      />
      <Metric
        value={`${c.stations_scraped_last_hour}/${c.total_stations}`}
        label="stations 1h"
        hint="Von wie vielen Stationen wir in der letzten Stunde Boards geholt haben. Sollte ≈ total minus no_iris sein. Kleiner = Sammellücken."
      />
      <Metric
        value={c.no_iris_stations}
        label="no_iris"
        hint="Stationen, deren EVA bei IRIS permanent 400 (nicht gefunden) meldet — meist Halte ohne IRIS-Betriebsstelle. Werden bewusst nicht mehr abgefragt."
      />
      <Metric
        value={c.pending_stations}
        label="pending names"
        warn={c.pending_stations > 0}
        hint="Bahnhofsnamen aus Fahrplan-Daten, die (noch) keiner Station zugeordnet sind. Wachsen lassen ist ok; 1x täglich per StaDa-Call auflösen."
      />
      <Metric
        value={`${c.punctual_pct.toFixed(1)}%`}
        label="punctual 1h"
        hint="Anteil der Beobachtungen der letzten Stunde mit kommunizierter Verspätung < 2 min (nur Züge mit Ist-Zeit). Plausibilitäts-Indikator: Werte um ~75–85 % entsprechen bekannter DB-Realität."
      />
      <Metric
        value={`${c.cancelled_pct.toFixed(1)}%`}
        label="cancelled 1h"
        hint="Anteil der Beobachtungen der letzten Stunde, deren Zug als ausgefallen gemeldet war."
      />
      <Metric
        value={c.avg_snaps_per_stop.toFixed(2)}
        label="snaps/stop 1h"
        hint="Ø Snapshots pro (Zug, Station, Richtung) der letzten Stunde — die Verspätungs-Evolution pro Halte. > 1 = Züge werden mehrfach beobachtet (der Kernwert des Datensatzes)."
      />
      <Metric
        value={c.db_size}
        label="db size"
        hint="Gesamtgröße der Postgres-DB (Daten + Indizes). Wächst ~20 GB/Monat bei aktueller Cadence."
      />
      <Metric
        value={c.oldest_event.slice(0, 16).replace('T', ' ')}
        label="dataset start"
        hint="Älteste gespeicherte Beobachtung. Danach sollte die Zeit NIE mehr springen — sonst gab es eine Lücke (steht dann als Downtime im nächsten Export)."
      />
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

  const healthy = health && health.fetch_rate >= 0.9 && error === null

  return (
    <div className="ops">
      <div className="top-bar">
        <span className="ops-title">verspaetet <b>ops</b></span>
        <span className="muted">
          <span className={`health-dot ${healthy ? 'dot-ok' : 'dot-warn'}`} />
          {error ? <span className="err-text">API ERROR: {error}</span> : `refreshed ${ts} · auto 15s`}
          {' · '}
          <a href="/" className="ui-link">data ui</a>
        </span>
      </div>

      <div className="ops-grid">
        <Card title="collection" wide hint="Live-Zahlen der Datensammlung. Alle Werte beziehen sich auf die letzte Stunde (außer fetch rate: 10 min, und db size: aktuell).">
          <CollectionPanel c={collection} h={health} />
        </Card>

        <Card title="queues" hint="asynq-Warteschlangen in Redis. Der Scheduler legt ~180 Tasks pro Minute in 'default'; 20 Worker verarbeiten sie parallel. Für Details einzelner Tasks: docker compose --profile asynqmon up asynqmon (Port 8081).">
          <QueueTable queues={queues} />
        </Card>

        <Card title="failed / archived tasks (latest)" hint="Neueste fehlgeschlagene Fetches über beide Zustände hinweg (retry = wird erneut versucht, archived = aufgegeben). Die meisten hier sichtbaren Fehler sind Altlasten oder kurzzeitige IRIS-429s, die später erfolgreich retryt wurden.">
          <FailedTable tasks={failed} />
        </Card>

        <Card title="monthly exports" hint="Fertige Datensatz-Chunks im Parquet-Format. Am 4. des Monats wird der Vormonat exportiert und zu HuggingFace hochgeladen.">
          <ExportsPanel chunks={exports} />
        </Card>
      </div>
    </div>
  )
}