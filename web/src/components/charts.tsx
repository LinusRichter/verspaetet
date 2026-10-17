// SVG-Charts, pure Props — keine Chart-Library nötig.

import type { ReactNode } from 'react'
import type { LineRun, Meta, Run, HourBar } from '../types'
import type { Palette } from '../theme'
import { delayHex } from '../theme'
import { fmt } from '../lib'

/** Zuglauf: Verspätungs-Entwicklung — X = Uhrzeit, Y = min, 1 Kurve = 1 Halt. */
export function EvolutionChart({ run, selIdx, meta, scrub, onScrub, p }: {
  run: Run; selIdx: number; meta: Meta; scrub: number
  onScrub: (t: number) => void; p: Palette
}): ReactNode {
  const W = 1000, H = 230, ML = 38, MR = 14, MT = 12, MB = 26
  const maxD = Math.max(15, ...run.stops.map((s) => s.finalD)) + 3
  const x = (t: number) => ML + ((t - meta.t0) / (meta.t1 - meta.t0)) * (W - ML - MR)
  const y = (d: number) => H - MB - (d / maxD) * (H - MT - MB)
  const dStep = maxD > 30 ? 10 : 5
  const span = meta.t1 - meta.t0
  const stepMin = span > 900 ? 120 : 60
  const ticks: number[] = []
  for (let m = Math.ceil(meta.t0 / stepMin) * stepMin; m <= meta.t1; m += stepMin) ticks.push(m)

  const click = (e: React.MouseEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    const xIn = ((e.clientX - r.left) / r.width) * W
    onScrub(Math.round((meta.t0 + Math.max(0, Math.min(1, (xIn - ML) / (W - ML - MR))) * span) / 5) * 5)
  }

  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: 'auto', display: 'block' }} onClick={click}>
      <text x={ML - 6} y={MT + 8} textAnchor="end" fontSize={10} fill={p.label}>min</text>
      {Array.from({ length: Math.floor(maxD / dStep) + 1 }, (_, i) => i * dStep).map((d) => (
        <g key={d}>
          <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={p.grid} />
          {d % (dStep * 2) === 0 && <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>}
        </g>
      ))}
      {ticks.map((m) => (
        <g key={m}>
          <line x1={x(m)} y1={H - MB} x2={x(m)} y2={H - MB + 4} stroke={p.axis} />
          <text x={x(m)} y={H - MB + 16} textAnchor="middle" fontSize={10} fill={p.label}>{fmt(m)}</text>
        </g>
      ))}
      <line x1={x(meta.now)} y1={MT} x2={x(meta.now)} y2={H - MB} stroke={p.now} strokeDasharray="4 3" opacity={0.65} />
      <text x={x(meta.now) + 4} y={MT + 10} fontSize={10} fill={p.now}>jetzt</text>
      {run.stops.map((st, i) => {
        const pts = st.snaps.filter((sn) => sn.prog != null).map((sn) => `${x(sn.at).toFixed(1)},${y(sn.prog!).toFixed(1)}`)
        const last = st.snaps[st.snaps.length - 1]
        return (
          <g key={i}>
            {pts.length > 1 && (
              <polyline points={pts.join(' ')} fill="none" stroke={i === selIdx ? p.polySel : p.poly} strokeWidth={i === selIdx ? 2 : 1} />
            )}
            {last?.ist && (
              <rect x={x(last.at) - 2.5} y={y(st.finalD) - 2.5} width={5} height={5} fill={delayHex(p, st.finalD)}>
                <title>{`${st.name} · Ist +${st.finalD} ${fmt(st.actual)}`}</title>
              </rect>
            )}
            {st.cancelled && last && <text x={x(last.at) + 3} y={y(0) - 4} fontSize={10} fill={p.bad}>✕</text>}
          </g>
        )
      })}
      <line x1={x(scrub)} y1={MT} x2={x(scrub)} y2={H - MB} stroke={p.polySel} strokeWidth={2} />
    </svg>
  )
}

/** Linie: Spaghetti — X = Haltposition, Y = Endverspätung, 1 Linie = 1 Fahrt. */
export function SpaghettiChart({ runs, p }: { runs: LineRun[]; p: Palette }): ReactNode {
  const W = 1000, H = 250, ML = 38, MR = 14, MT = 12, MB = 52
  const live = runs.filter((r) => !r.cancelled)
  const n = runs[0]?.stops.length ?? 1
  const maxD = Math.max(15, ...live.map((r) => r.finalD)) + 3
  const x = (i: number) => ML + (n === 1 ? 0 : (i / (n - 1)) * (W - ML - MR))
  const y = (d: number) => H - MB - (d / maxD) * (H - MT - MB)
  const dStep = maxD > 30 ? 10 : 5
  let worst: LineRun | undefined, best: LineRun | undefined
  for (const r of live) {
    if (!worst || r.finalD > worst.finalD) worst = r
    if (!best || r.finalD < best.finalD) best = r
  }
  const every = Math.max(1, Math.round(n / 14))

  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: 'auto', display: 'block' }}>
      {Array.from({ length: Math.floor(maxD / dStep) + 1 }, (_, i) => i * dStep).map((d) => (
        <g key={d}>
          <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={p.grid} />
          {d % (dStep * 2) === 0 && <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>}
        </g>
      ))}
      {runs[0]?.stops.map((st, i) => {
        if (i % every !== 0 && i !== n - 1) return null
        const xx = x(i)
        return (
          <g key={i}>
            <line x1={xx} y1={MT} x2={xx} y2={H - MB} stroke={p.grid} />
            <text x={xx} y={H - MB + 12} textAnchor="end" fontSize={10} fill={p.label}
              transform={`rotate(-38 ${xx} ${H - MB + 12})`}>{st.name.slice(0, 16)}</text>
          </g>
        )
      })}
      {live.map((r) => {
        const col = r === worst ? p.bad : r === best ? p.ok : p.poly
        const w = r === worst || r === best ? 2 : 1
        const pts = r.stops.map((st, i) => `${x(i).toFixed(1)},${y(st.finalD).toFixed(1)}`).join(' ')
        return <polyline key={r.id} points={pts} fill="none" stroke={col} strokeWidth={w} opacity={r === worst || r === best ? 1 : 0.7}>
          <title>{`Fahrt ${fmt(r.dep)} → +${r.finalD}`}</title>
        </polyline>
      })}
    </svg>
  )
}

/** Übersicht: Ø Endverspätung nach Abfahrtsstunde. */
export function HourProfile({ hours, p }: { hours: HourBar[]; p: Palette }): ReactNode {
  const W = 1000, H = 170, ML = 38, MR = 14, MT = 12, MB = 24
  const mx = Math.max(4, ...hours.map((h) => h.avg))
  const x = (i: number) => ML + (hours.length === 1 ? 0.5 : (i / (hours.length - 1)) * (W - ML - MR))
  const y = (v: number) => H - MB - (v / mx) * (H - MT - MB)
  const gstep = mx > 16 ? 5 : 2
  const bw = Math.max(6, ((W - ML - MR) / hours.length) * 0.55)

  return (
    <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: 'auto', display: 'block' }}>
      {Array.from({ length: Math.floor(mx / gstep) + 1 }, (_, i) => i * gstep).map((d) => (
        <g key={d}>
          <line x1={ML} y1={y(d)} x2={W - MR} y2={y(d)} stroke={p.grid} />
          <text x={ML - 6} y={y(d) + 3} textAnchor="end" fontSize={10} fill={p.label}>{d}</text>
        </g>
      ))}
      {hours.map((h, i) => (
        <g key={h.h}>
          <rect x={x(i) - bw / 2} y={y(h.avg)} width={bw} height={H - MB - y(h.avg)}
            fill={h.avg >= 8 ? p.bad : h.avg >= 4 ? p.orange : p.ok} opacity={0.85}>
            <title>{`ab ${fmt(h.h * 60)} · Ø +${h.avg} min · ${h.n} Fahrten`}</title>
          </rect>
          {h.h % 2 === 0 && <text x={x(i)} y={H - MB + 14} textAnchor="middle" fontSize={9} fill={p.label}>{h.h}</text>}
        </g>
      ))}
    </svg>
  )
}
