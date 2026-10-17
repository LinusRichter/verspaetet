// Wissensstand-Regler. Eigenbau statt <input type=range>: Thumb, Ticks und
// Jetzt-Marker nutzen dieselbe Prozent-Mathematik im selben Container und
// können daher nicht gegeneinander versetzt sein. Tastatur: ←/→ = 5 min.

import { useRef } from 'react'
import { useApp } from '../state'
import { clamp, fmt } from '../lib'

export function ScrubBar(): React.ReactNode {
  const { meta, scrub, setScrub } = useApp()
  const track = useRef<HTMLDivElement>(null)

  if (!meta) return <div className="scrub-row" />

  const span = meta.t1 - meta.t0
  const pos = (t: number) => ((t - meta.t0) / span) * 100
  const state = scrub < meta.now ? 'historisch' : scrub > meta.now ? 'Zukunft (Final-Daten)' : 'jetzt'
  const stateCls = scrub < meta.now ? 'hist' : scrub > meta.now ? 'fut' : ''

  const val = (clientX: number): number => {
    const r = track.current?.getBoundingClientRect()
    if (!r) return scrub
    return meta.t0 + clamp((clientX - r.left) / r.width, 0, 1) * span
  }

  const stepMin = span > 900 ? 120 : 60
  const ticks: number[] = []
  for (let m = Math.ceil(meta.t0 / stepMin) * stepMin; m <= meta.t1; m += stepMin) ticks.push(m)

  return (
    <div className="scrub-row">
      <span className="scrub-label">Wissensstand</span>
      <span className="scrub-time">{fmt(scrub)}</span>
      <span className={`scrub-state ${stateCls}`}>{state}</span>
      <div className="scrub-wrap">
        <div
          ref={track}
          className="scrubtrack"
          tabIndex={0}
          title="Wissensstand ziehen · ←/→ = 5 min"
          onPointerDown={(e) => {
            e.preventDefault()
            track.current?.setPointerCapture(e.pointerId)
            setScrub(Math.round(val(e.clientX) / 5) * 5)
          }}
          onPointerMove={(e) => {
            if (e.buttons) setScrub(Math.round(val(e.clientX) / 5) * 5)
          }}
          onKeyDown={(e) => {
            const s = e.key === 'ArrowLeft' ? -5 : e.key === 'ArrowRight' ? 5 : 0
            if (s) { e.preventDefault(); setScrub(scrub + s) }
          }}
        >
          <div className="rail" />
          <div className="thumb" style={{ left: `${pos(scrub)}%` }} />
        </div>
        <div className="nowmark" style={{ left: `${pos(meta.now)}%` }} title={`jetzt (${fmt(meta.now)})`} />
        <div className="ticks">
          {ticks.map((m) => <span key={m} style={{ left: `${pos(m)}%` }}>{fmt(m)}</span>)}
        </div>
      </div>
      <button className="mini" onClick={() => setScrub(meta.now)}>Jetzt</button>
    </div>
  )
}
