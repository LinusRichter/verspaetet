// Scope-Leiste: Bahnhof › Linie › Fahrt. Zeigt immer, wo man gerade steht
// (auch in der Übersicht); jedes Segment ist klickbar; „←" stellt den
// letzten Navigationszustand inkl. Wissensstand wieder her.

import { useEffect, useState, type ReactNode } from 'react'
import { useApp } from '../state'
import { api } from '../api'
import type { LineInfo } from '../types'
import { fmt } from '../lib'

interface LineInfoExt extends LineInfo { ctx?: string }

export function Breadcrumbs(): ReactNode {
  const { nav, canGoBack, goStation, goLine, back } = useApp()
  const [line, setLine] = useState<LineInfoExt | null>(null)

  useEffect(() => {
    let alive = true
    if (!nav.line) { setLine(null); return }
    api.line(nav.line, nav.station)
      .then((d) => { if (alive) setLine({ ...d.line, ctx: d.ctx }) })
      .catch(() => { if (alive) setLine(null) })
    return () => { alive = false }
  }, [nav.line])

  const parts: ReactNode[] = []
  if (nav.station) {
    parts.push(
      nav.tab === 'board'
        ? <span key="st" className="crumb cur">{nav.station}</span>
        : <span key="st" className="crumb" onClick={() => goStation(nav.station!)}>{nav.station}</span>,
    )
  }
  if ((nav.tab === 'line' || nav.tab === 'journey') && line) {
    parts.push(
      nav.tab === 'line'
        ? <span key="li" className="crumb cur">{line.label}{line.ctx ? ` (${line.ctx})` : ''}</span>
        : <span key="li" className="crumb" onClick={() => goLine(line.key)}>{line.label}</span>,
    )
  }
  if (nav.tab === 'journey' && nav.run) {
    parts.push(<span key="ru" className="crumb cur">Fahrt{nav.runDep != null ? ` ${fmt(nav.runDep)}` : ''}</span>)
  }

  return (
    <div className="crumbbar">
      <button className="mini" onClick={back} disabled={!canGoBack} title="zurück">←</button>
      {parts.length === 0
        ? <span className="muted">kein Kontext — Bahnhof oder Linie suchen</span>
        : parts.reduce<ReactNode[]>((acc, p, i) =>
          i === 0 ? [p] : [...acc, <span key={`sep${i}`} className="crumb-sep">›</span>, p], [])}
    </div>
  )
}
