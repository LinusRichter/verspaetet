// Bahnhofstafel: suchbares Stationsfeld, Linien des Bahnhofs als Chips,
// Abfahrten zum Wissensstand. Lädt ausschließlich on demand.

import { useEffect, useRef, useState, type ReactNode } from 'react'
import { api } from '../api'
import type { BoardData, SearchStation } from '../types'
import { useApp } from '../state'
import { debounce, fmt } from '../lib'
import { catColor, palette } from '../theme'
import { Chip, Empty, ErrorBox, Loading } from './bits'

export function Board(): ReactNode {
  const { nav, scrub, meta, goStation, goLine, goRun, theme } = useApp()
  const p = palette(theme)
  const [q, setQ] = useState('')
  const [sug, setSug] = useState<SearchStation[]>([])
  const [sugOpen, setSugOpen] = useState(false)
  const [data, setData] = useState<BoardData | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const box = useRef<HTMLDivElement>(null)

  // Stationsvorschläge (debounced, on demand, ab 2 Zeichen; veraltete
  // Antworten werden verworfen — Sequenznummer vergleicht)
  const seqRef = useRef(0)
  const runSearch = debounce((query: string) => {
    if (query.trim().length < 2) { setSug([]); return }
    const my = ++seqRef.current
    api.search(query.trim())
      .then((r) => { if (seqRef.current === my) setSug(r.stations.slice(0, 10)) })
      .catch(() => { if (seqRef.current === my) setSug([]) })
  }, 200)

  useEffect(() => {
    const h = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setSugOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])

  // Abfahrten laden, sobald Bahnhof oder Wissensstand sich ändert
  useEffect(() => {
    if (!nav.station) { setData(null); return }
    let alive = true
    const key = ++loadKeyCounter
    api.board(nav.station, scrub)
      .then((d) => { if (alive && loadKeyCounter === key) { setData(d); setErr(null) } })
      .catch((e) => { if (alive && loadKeyCounter === key) setErr(String(e)) })
    return () => { alive = false }
  }, [nav.station, scrub])

  const loading = data == null && !err && !!nav.station

  return (
    <section className="view-active">
      <div className="vhead">
        <div className="stcombo" ref={box}>
          <input
            placeholder="Bahnhof suchen…"
            value={q}
            autoComplete="off"
            onChange={(e) => {
              setQ(e.target.value)
              setSugOpen(true)
              runSearch(e.target.value)
            }}
            onFocus={() => { setSugOpen(true); if (!q) runSearch('') }}
          />
          {sugOpen && sug.length > 0 && (
            <div className="stlist">
              {sug.map((s) => (
                <div key={s.name} className="stitem"
                  onClick={() => { setQ(s.name); setSugOpen(false); goStation(s.name) }}>
                  <span>{s.name}</span><span className="n">{s.n}</span>
                </div>
              ))}
            </div>
          )}
        </div>
        {meta && <span className="muted">{meta.day}</span>}
        <span className="hint">Zeile klicken → Zuglauf · Linie klicken → Linien-Ansicht</span>
      </div>

      {!nav.station && <Empty hint="Bahnhof suchen — dann erscheinen die Abfahrten zum Wissensstand." />}

      {nav.station && (
        <>
          {data && (
            <div className="chipsline">
              <span className="muted chipslabel">Linien hier:</span>
              {data.lines.map((l) => (
                <button key={l.key} className="lchip" onClick={() => goLine(l.key)}>
                  <span className="dot" style={{ background: catColor(l.cat, p) }} />{l.label}
                </button>
              ))}
            </div>
          )}
          {err && <ErrorBox msg={err} />}
          {loading && <Loading note={`Abfahrten ${nav.station} …`} />}
          {data && (
            <table className="tbl">
              <thead>
                <tr>
                  <th className="t">Plan</th><th className="t">Ist/Prog</th><th className="t">±</th>
                  <th>Linie</th><th>Richtung</th><th>über</th><th className="t">Gl.</th>
                </tr>
              </thead>
              <tbody>
                {data.rows.map((r) => {
                  const st = { known: r.known, ist: r.ist, delay: r.delay, cancelled: r.cancelled, plat: r.plat }
                  const line = data.lines.find((l) => l.key === r.lineKey)
                  return (
                    <tr key={`${r.runId}-${r.stopIdx}`} className={`brow${r.planned < scrub - 2 ? ' dim' : ''}`}
                      onClick={() => goRun(r.runId, r.stopIdx)}>
                      <td className="t">{fmt(r.planned)}</td>
                      <td className="t">{r.known && r.delay != null ? fmt(r.planned + r.delay) : '–'}</td>
                      <td className="t"><Chip st={st} /></td>
                      <td>
                        <span className="linelink" onClick={(e) => { e.stopPropagation(); goLine(r.lineKey) }}>
                          <span className="dot" style={{ background: catColor(line?.cat ?? '', p) }} />{line?.label ?? r.lineKey}
                        </span>
                      </td>
                      <td>{r.dest || '–'}</td>
                      <td className="muted">{r.via || '–'}</td>
                      <td className="t">
                        {r.plat || '–'}
                        {r.platChanged && <span className="pchg" title={`Gleiswechsel (war Gl. ${r.platFrom})`}>⇄</span>}
                      </td>
                    </tr>
                  )
                })}
                {data.rows.length === 0 && (
                  <tr><td colSpan={7} className="muted" style={{ textAlign: 'center', padding: 20 }}>
                    keine Abfahrten im Fenster (−2,5 h … +4 h um Wissensstand)
                  </td></tr>
                )}
              </tbody>
            </table>
          )}
        </>
      )}
    </section>
  )
}

let loadKeyCounter = 0
