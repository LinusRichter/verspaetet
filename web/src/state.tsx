// Zentraler Zustand: Navigation (Tab + Bahnhof/Linie/Fahrt + Tag +
// Richtungsfilter), Browser-Historie (Zurück/Vor), Wissensstand (Scrub)
// und Theme. Alles on-demand — nichts wird vorab geladen außer /api/meta.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Meta, Tab } from './types'
import { api } from './api'

export type LineDir = 'all' | 'in' | 'out'

export interface NavState {
  tab: Tab
  station: string | null
  line: string | null
  run: string | null
  runDep: number | null
  stopIdx: number
  scrub: number
  day: string | null       // ISO-Datum (YYYY-MM-DD); null = heute
  lineDir: LineDir         // Richtungsfilter im Line-View (nur mit Stations-Scope)
}

type NavOnly = Omit<NavState, 'scrub'>

interface Ctx {
  meta: Meta | null
  metaError: string | null
  scrub: number
  setScrub: (t: number) => void
  theme: 'dark' | 'light'
  toggleTheme: () => void
  nav: NavOnly
  goStation: (station: string) => void
  goLine: (lineKey: string, station?: string | null) => void
  goRun: (runId: string, stopIdx?: number, dep?: number) => void
  setTab: (tab: Tab) => void
  setDay: (day: string | null) => void
  selectLine: (lineKey: string) => void   // Kontext-Switch ohne History-Eintrag
  selectRun: (runId: string, stopIdx?: number, dep?: number) => void
  selectLineDir: (dir: LineDir) => void
  canGoBack: boolean
  back: () => void
}

const C = createContext<Ctx | null>(null)

export function useApp(): Ctx {
  const c = useContext(C)
  if (!c) throw new Error('useApp outside provider')
  return c
}

// ---------------------------------------------------------------------------
// URL-Kodierung (Deep-Links)
// ---------------------------------------------------------------------------

function urlFor(nav: NavOnly, scrub: number): string {
  const p = new URLSearchParams()
  p.set('tab', nav.tab)
  if (nav.station) p.set('station', nav.station)
  if (nav.line) p.set('line', nav.line)
  if (nav.run) p.set('run', nav.run)
  if (nav.stopIdx) p.set('stop', String(nav.stopIdx))
  if (nav.runDep != null) p.set('dep', String(nav.runDep))
  if (scrub) p.set('at', String(scrub))
  if (nav.day) p.set('day', nav.day)
  if (nav.lineDir !== 'all') p.set('dir', nav.lineDir)
  const qs = p.toString()
  return qs ? `/?${qs}` : '/'
}

function navFromLocation(): { nav: NavOnly; scrub: number | null } {
  const p = new URLSearchParams(window.location.search)
  const tab = (['board', 'journey', 'line', 'day'] as const).includes(p.get('tab') as Tab)
    ? (p.get('tab') as Tab) : 'board'
  const at = p.get('at')
  const dir = p.get('dir')
  return {
    nav: {
      tab,
      station: p.get('station'),
      line: p.get('line'),
      run: p.get('run'),
      runDep: p.get('dep') != null ? Number(p.get('dep')) : null,
      stopIdx: p.get('stop') != null ? Number(p.get('stop')) || 0 : 0,
      day: p.get('day') && /^\d{4}-\d{2}-\d{2}$/.test(p.get('day')!) ? p.get('day') : null,
      lineDir: dir === 'in' || dir === 'out' ? dir : 'all',
    },
    scrub: at != null ? Number(at) : null,
  }
}

function validState(s: unknown): s is NavState {
  const v = s as NavState | null
  return !!v && typeof v === 'object' && typeof v.tab === 'string' &&
    ['board', 'journey', 'line', 'day'].includes(v.tab)
}

// ---------------------------------------------------------------------------

export function AppProvider({ children }: { children: ReactNode }) {
  const init = useRef(navFromLocation())
  const [meta, setMeta] = useState<Meta | null>(null)
  const [metaError, setMetaError] = useState<string | null>(null)
  const [scrub, setScrubRaw] = useState<number>(init.current.scrub ?? 0)
  const [theme, setTheme] = useState<'dark' | 'light'>(() =>
    (localStorage.getItem('verspaetet-theme') as 'dark' | 'light') ?? 'dark')
  const [nav, setNav] = useState<NavOnly>(init.current.nav)
  const [histLen, setHistLen] = useState<number>(window.history.length)

  const navRef = useRef(nav)
  navRef.current = nav
  const scrubRef = useRef(scrub)
  scrubRef.current = scrub
  const dayRef = useRef(nav.day)
  dayRef.current = nav.day

  // Meta pro TAG laden (heute = Default). Historische Tage: now = Tagesende.
  // Scrub springt beim Tageswechsel auf den Tages-„jetzt"-Stand — außer der
  // Deep-Link hatte einen expliziten Wissensstand (?at=).
  const scrubDaySet = useRef<string | null | undefined>(undefined)
  useEffect(() => {
    let alive = true
    const day = nav.day
    api.meta(day).then((m) => {
      if (!alive) return
      setMeta(m)
      setMetaError(null)
      const key = day ?? 'today'
      if (init.current.scrub == null && scrubDaySet.current !== key) {
        scrubDaySet.current = key
        setScrubRaw(m.now)
      }
    }).catch((e) => { if (alive) setMetaError(String(e)) })
    return () => { alive = false }
  }, [nav.day])

  const toggleTheme = useCallback(() => {
    setTheme((th) => {
      const next = th === 'dark' ? 'light' : 'dark'
      localStorage.setItem('verspaetet-theme', next)
      document.documentElement.dataset.theme = next
      return next
    })
  }, [])

  // --- History-Synchronisation --------------------------------------------

  const pushNav = useCallback((next: NavOnly) => {
    const s: NavState = { ...next, scrub: scrubRef.current }
    window.history.pushState(s, '', urlFor(next, s.scrub))
    setHistLen(window.history.length)
    setNav(next)
  }, [])

  const replaceNav = useCallback((next: NavOnly) => {
    const s: NavState = { ...next, scrub: scrubRef.current }
    window.history.replaceState(s, '', urlFor(next, s.scrub))
    setNav(next)
  }, [])

  // Scrub-Ziehen darf die Historie nicht fluten: nur den aktuellen Eintrag
  // debounced aktualisieren.
  const replaceScrubSoon = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const setScrub = useCallback((t: number) => {
    setScrubRaw(t)
    clearTimeout(replaceScrubSoon.current)
    replaceScrubSoon.current = setTimeout(() => {
      window.history.replaceState(
        { ...navRef.current, scrub: scrubRef.current }, '',
        urlFor(navRef.current, scrubRef.current))
    }, 400)
  }, [])

  useEffect(() => {
    const h = (e: PopStateEvent) => {
      if (validState(e.state)) {
        const { scrub: at, ...rest } = e.state
        setScrubRaw(at)
        setNav(rest)
      }
      setHistLen(window.history.length)
    }
    window.addEventListener('popstate', h)
    window.history.replaceState(
      { ...init.current.nav, scrub: init.current.scrub ?? 0 }, '',
      urlFor(init.current.nav, init.current.scrub ?? 0))
    return () => window.removeEventListener('popstate', h)
  }, [])

  // --- Navigation ----------------------------------------------------------

  const goStation = useCallback((station: string) => {
    pushNav({ ...navRef.current, tab: 'board', station })
  }, [pushNav])

  const goLine = useCallback((lineKey: string, station?: string | null) => {
    pushNav({ ...navRef.current, tab: 'line', line: lineKey, station: station !== undefined ? station : navRef.current.station, run: null, runDep: null })
  }, [pushNav])

  const goRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    pushNav({ ...navRef.current, tab: 'journey', run: runId, runDep: dep ?? null, stopIdx })
  }, [pushNav])

  const setTab = useCallback((tab: Tab) => {
    pushNav({ ...navRef.current, tab })
  }, [pushNav])

  const setDay = useCallback((day: string | null) => {
    pushNav({ ...navRef.current, day, lineDir: 'all' })
  }, [pushNav])

  const selectLine = useCallback((lineKey: string) => {
    replaceNav({ ...navRef.current, line: lineKey, run: null, runDep: null })
  }, [replaceNav])

  const selectRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    replaceNav({ ...navRef.current, run: runId, runDep: dep ?? null, stopIdx })
  }, [replaceNav])

  const selectLineDir = useCallback((dir: LineDir) => {
    replaceNav({ ...navRef.current, lineDir: dir })
  }, [replaceNav])

  const back = useCallback(() => window.history.back(), [])

  const value = useMemo<Ctx>(() => ({
    meta, metaError, scrub, setScrub, theme, toggleTheme,
    nav, goStation, goLine, goRun, setTab, setDay, selectLine, selectRun, selectLineDir,
    canGoBack: histLen > 1, back,
  }), [meta, metaError, scrub, setScrub, theme, toggleTheme, nav,
    goStation, goLine, goRun, setTab, setDay, selectLine, selectRun, selectLineDir, histLen, back])

  return <C.Provider value={value}>{children}</C.Provider>
}