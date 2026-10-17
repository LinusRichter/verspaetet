// Kleine UI-Bausteine.

import type { ReactNode } from 'react'
import { delayCls } from '../lib'
import type { StopState } from '../lib'

export function Spinner(): ReactNode {
  return <div className="spin" aria-label="lädt" />
}

export function Loading({ note }: { note?: string }): ReactNode {
  return (
    <div className="loading">
      <Spinner />
      {note && <div className="loading-note">{note}</div>}
    </div>
  )
}

export function ErrorBox({ msg }: { msg: string }): ReactNode {
  return <div className="errorbox">Fehler: {msg}</div>
}

export function Empty({ hint }: { hint: string }): ReactNode {
  return <div className="empty">{hint}</div>
}

export function Card({ value, label }: { value: ReactNode; label: string }): ReactNode {
  return (
    <div className="card">
      <b>{value}</b>
      <span className="lbl">{label}</span>
    </div>
  )
}

/** Verspätungs-Chip: farbcodiert, Titel erklärt Ist/Prognose. */
export function Chip({ st }: { st: StopState }): ReactNode {
  if (!st.known) return <span className="chip unknown" title="noch keine Prognose">–</span>
  if (st.cancelled) return <span className="chip cx" title="Halt entfällt">✕ entfällt</span>
  if (st.delay == null) return <span className="chip unknown">–</span>
  const lbl = st.delay < 0 ? `\u2212${Math.abs(st.delay)}` : `+${st.delay}`
  const ttl = `${st.ist ? 'Ist' : 'Prognose'}: ${lbl} min`
  return <span className={`chip ${delayCls(st.delay)}`} title={ttl}>{lbl}</span>
}
