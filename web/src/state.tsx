// Zentraler Zustand: Navigation (Tab + Bahnhof/Linie/Fahrt), History-Stack
// für „zurück", Wissensstand (Scrub) und Theme. Alles on-demand — nichts
// wird vorab geladen außer /api/meta.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Meta, Tab } from './types'
import { api } from './api'

export interface NavState {
  tab: Tab
  station: string | null
  line: string | null
  run: string | null
  runDep: number | null
  stopIdx: number
  scrub: number
}

interface Ctx {
  meta: Meta | null
  metaError: string | null
  scrub: number
  setScrub: (t: number) => void
  theme: 'dark' | 'light'
  toggleTheme: () => void
  nav: Omit<NavState, 'scrub'>
  goStation: (station: string) => void
  goLine: (lineKey: string) => void
  goRun: (runId: string, stopIdx?: number, dep?: number) => void
  setTab: (tab: Tab) => void
  selectLine: (lineKey: string) => void   // Kontext-Switch ohne History-Eintrag
  selectRun: (runId: string, stopIdx?: number, dep?: number) => void
  canGoBack: boolean
  back: () => void
}

const C = createContext<Ctx | null>(null)

export function useApp(): Ctx {
  const c = useContext(C)
  if (!c) throw new Error('useApp outside provider')
  return c
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [meta, setMeta] = useState<Meta | null>(null)
  const [metaError, setMetaError] = useState<string | null>(null)
  const [scrub, setScrubRaw] = useState(0)
  const [theme, setTheme] = useState<'dark' | 'light'>(() =>
    (localStorage.getItem('verspaetet-theme') as 'dark' | 'light') ?? 'dark')
  const [nav, setNav] = useState<Omit<NavState, 'scrub'>>({ tab: 'board', station: null, line: null, run: null, runDep: null, stopIdx: 0 })
  const hist = useRef<NavState[]>([])
  const [histLen, setHistLen] = useState(0)

  // Beim Start: nur Metainfos (Zeitrahmen des Tages). Kein weiterer Load.
  useEffect(() => {
    let alive = true
    api.meta().then((m) => { if (alive) { setMeta(m); setScrubRaw(m.now) } }).catch((e) => setMetaError(String(e)))
    return () => { alive = false }
  }, [])

  const scrubRef = useRef(scrub)
  scrubRef.current = scrub

  const setScrub = useCallback((t: number) => setScrubRaw(t), [])

  const toggleTheme = useCallback(() => {
    setTheme((th) => {
      const next = th === 'dark' ? 'light' : 'dark'
      localStorage.setItem('verspaetet-theme', next)
      document.documentElement.dataset.theme = next
      return next
    })
  }, [])

  const push = useCallback((cur: Omit<NavState, 'scrub'>) => {
    hist.current.push({ ...cur, scrub: scrubRef.current })
    if (hist.current.length > 60) hist.current.shift()
    setHistLen(hist.current.length)
  }, [])

  const apply = useCallback((s: NavState) => {
    setScrubRaw(s.scrub)
    setNav({ tab: s.tab, station: s.station, line: s.line, run: s.run, runDep: s.runDep, stopIdx: s.stopIdx })
  }, [])

  const goStation = useCallback((station: string) => {
    setNav((cur) => { push(cur); return { ...cur, tab: 'board', station } })
  }, [push])

  const goLine = useCallback((lineKey: string) => {
    setNav((cur) => { push(cur); return { ...cur, tab: 'line', line: lineKey } })
  }, [push])

  const goRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    setNav((cur) => { push(cur); return { ...cur, tab: 'journey', run: runId, runDep: dep ?? null, stopIdx } })
  }, [push])

  const setTab = useCallback((tab: Tab) => {
    setNav((cur) => ({ ...cur, tab }))
  }, [])

  const selectLine = useCallback((lineKey: string) => {
    setNav((cur) => ({ ...cur, line: lineKey, run: null }))
  }, [])

  const selectRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    setNav((cur) => ({ ...cur, run: runId, runDep: dep ?? null, stopIdx }))
  }, [])

  const back = useCallback(() => {
    const s = hist.current.pop()
    setHistLen(hist.current.length)
    if (s) apply(s)
  }, [apply])

  const value = useMemo<Ctx>(() => ({
    meta, metaError, scrub, setScrub, theme, toggleTheme,
    nav, goStation, goLine, goRun, setTab, selectLine, selectRun,
    canGoBack: histLen > 0, back,
  }), [meta, metaError, scrub, setScrub, theme, toggleTheme, nav, goStation, goLine, goRun, setTab, selectLine, selectRun, histLen, back])

  return <C.Provider value={value}>{children}</C.Provider>
}
