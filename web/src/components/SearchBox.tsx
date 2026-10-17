// Globale Suche: Bahnhöfe und Linien, debounced, Ergebnisse on demand.
// Doppelte Linien-Labels werden über den Bereich (ctx) disambiguiert.

import { useState, type ReactNode } from 'react'
import { api } from '../api'
import type { SearchResults } from '../types'
import { useApp } from '../state'
import { debounce } from '../lib'
import { Spinner } from './bits'

export function SearchBox(): ReactNode {
  const { goStation, goLine } = useApp()
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [res, setRes] = useState<SearchResults | null>(null)

  const run = debounce((query: string) => {
    if (query.trim().length < 2) { setRes(null); setLoading(false); return }
    api.search(query.trim())
      .then((r) => { setRes(r); setLoading(false) })
      .catch(() => { setRes(null); setLoading(false) })
  }, 250)

  const onChange = (v: string) => {
    setQ(v)
    if (v.trim().length < 2) { setRes(null); setOpen(false); return }
    setLoading(true)
    setOpen(true)
    run(v)
  }

  return (
    <div className="searchbox">
      <input
        type="text"
        placeholder="Suche: Bahnhof oder Linie"
        value={q}
        autoComplete="off"
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => q.trim().length >= 2 && setOpen(true)}
      />
      <span className="sic">⌕</span>
      {open && (
        <div className="searchres">
          {loading && <Spinner />}
          {!loading && res && (
            <>
              {res.lines.length > 0 && <div className="sgroup">Linien</div>}
              {res.lines.map((l) => (
                <div key={l.key} className="sitem" onClick={() => { setOpen(false); goLine(l.key) }}>
                  <b>{l.label}</b>
                  <span className="ctx">{l.ctx} · {l.n} Fahrt{l.n > 1 ? 'en' : ''}</span>
                </div>
              ))}
              {res.stations.length > 0 && <div className="sgroup">Bahnhöfe</div>}
              {res.stations.map((s) => (
                <div key={s.name} className="sitem" onClick={() => { setOpen(false); goStation(s.name) }}>
                  <b>{s.name}</b>
                  <span className="ctx">{s.n} Abfahrten</span>
                </div>
              ))}
              {res.lines.length === 0 && res.stations.length === 0 && (
                <div className="sitem muted">nichts gefunden</div>
              )}
            </>
          )}
          {!loading && !res && <div className="sitem muted">mindestens 2 Zeichen…</div>}
        </div>
      )}
      {open && <div className="searchbackdrop" onClick={() => setOpen(false)} />}
    </div>
  )
}
