// Command-Palette: große Suche über Bahnhöfe und Linien.
// Öffnen: Ctrl+/ oder Shift+/ (oder Header-Button). Linien-Ergebnisse
// springen in den Linien-Tab, Bahnhöfe in die Bahnhofstafel.
// Tastatur: ↑/↓ wählen, Enter öffnet, Esc schließt.

import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { SearchResults } from '../types'
import { useApp } from '../state'
import { debounce, fmt } from '../lib'
import { Spinner } from './bits'

interface Item {
  kind: 'line' | 'station' | 'stationline'
  key: string
  title: string
  ctx: string
  act: () => void
}

export function Palette({ open, onClose }: { open: boolean; onClose: () => void }): ReactNode {
  const { goStation, goLine, setScrub } = useApp()
  const [q, setQ] = useState('')
  const [res, setRes] = useState<SearchResults | null>(null)
  const [loading, setLoading] = useState(false)
  const [sel, setSel] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const seqRef = useRef(0)

  const run = debounce((query: string) => {
    if (query.trim().length < 2) { setRes(null); setLoading(false); return }
    const my = ++seqRef.current
    api.search(query.trim())
      .then((r) => { if (seqRef.current === my) { setRes(r); setLoading(false) } })
      .catch(() => { if (seqRef.current === my) { setRes(null); setLoading(false) } })
  }, 250)

  // Beim Öffnen: zurücksetzen und fokussieren.
  useEffect(() => {
    if (open) {
      setQ('')
      setRes(null)
      setSel(0)
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  const items = useMemo<Item[]>(() => {
    if (!res) return []
    const out: Item[] = []
    for (const l of res.lines) {
      out.push({
        kind: 'line', key: l.key, title: l.label,
        ctx: `${l.ctx} · ${l.n} Fahrt${l.n > 1 ? 'en' : ''}`,
        act: () => { onClose(); goLine(l.key) },
      })
    }
    for (const s of res.stations) {
      out.push({
        kind: 'station', key: s.name, title: s.name,
        ctx: res.time != null ? `Tafel zum Wissensstand ${fmt(res.time)}` : 'Bahnhofstafel',
        act: () => {
          onClose()
          goStation(s.name)
          if (res.time != null) setScrub(res.time)
        },
      })
    }
    for (const l of res.stationLines ?? []) {
      out.push({
        kind: 'stationline', key: `sl-${l.key}`, title: l.label,
        ctx: `hält an ${res.stationName ?? ''}`,
        act: () => { onClose(); goLine(l.key) },
      })
    }
    return out
  }, [res, onClose, goLine, goStation])

  useEffect(() => { setSel(0) }, [q])

  if (!open) return null

  const pick = (i: number) => { items[i]?.act() }

  return (
    <div className="palbackdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="palette">
        <div className="palin">
          <span className="palico">⌕</span>
          <input
            ref={inputRef}
            type="text"
            placeholder="Bahnhof oder Linie suchen…"
            value={q}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => { setQ(e.target.value); setLoading(e.target.value.trim().length >= 2); run(e.target.value) }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(s + 1, items.length - 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)) }
              else if (e.key === 'Enter') { e.preventDefault(); pick(sel) }
              else if (e.key === 'Escape') { e.preventDefault(); onClose() }
            }}
          />
          <span className="palhint">Esc schließt</span>
        </div>
        <div className="palres">
          {loading && <Spinner />}
          {!loading && items.length === 0 && (
            <div className="palempty">
              {q.trim().length < 2 ? 'Tippen für Suche — Linien springen zur Linie, Bahnhöfe zur Tafel.' : 'nichts gefunden'}
            </div>
          )}
          {!loading && items.length > 0 && (
            <>
              {items.some((i) => i.kind === 'line') && <div className="sgroup">Linien → Linien-Ansicht</div>}
              {items.filter((i) => i.kind === 'line').map((i) => {
                const idx = items.indexOf(i)
                return <PalRow key={i.key} item={i} idx={idx} sel={sel} setSel={setSel} pick={pick} />
              })}
              {items.some((i) => i.kind === 'station') && (
                <div className="sgroup">Bahnhöfe → Bahnhofstafel{res?.time != null ? ` · Wissensstand ${fmt(res.time)}` : ''}</div>
              )}
              {items.filter((i) => i.kind === 'station').map((i) => {
                const idx = items.indexOf(i)
                return <PalRow key={i.key} item={i} idx={idx} sel={sel} setSel={setSel} pick={pick} />
              })}
              {items.some((i) => i.kind === 'stationline') && (
                <div className="sgroup">Linien an „{res?.stationName}“ → Linien-Ansicht</div>
              )}
              {items.filter((i) => i.kind === 'stationline').map((i) => {
                const idx = items.indexOf(i)
                return <PalRow key={i.key} item={i} idx={idx} sel={sel} setSel={setSel} pick={pick} />
              })}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function PalRow({ item, idx, sel, setSel, pick }: {
  item: Item; idx: number; sel: number; setSel: (n: number) => void; pick: (i: number) => void
}): ReactNode {
  return (
    <div
      className={`sitem${idx === sel ? ' hi' : ''}`}
      onMouseEnter={() => setSel(idx)}
      onClick={() => pick(idx)}
    >
      <b>{item.title}</b>
      <span className="ctx">{item.ctx}</span>
    </div>
  )
}