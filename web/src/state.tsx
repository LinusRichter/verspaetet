// Zentraler Zustand: Navigation (Tab + Bahnhof/Linie/Fahrt), Wissensstand
// (Scrub) und Theme. Die Navigation läuft ÜBER die Browser-Historie:
// jede Ansichts-Navigation ist ein history-Eintrag (Zurück/Vor arbeiten),
// Kontext-Switches innerhalb einer Ansicht ersetzen nur den aktuellen
// Eintrag. Der Zustand liegt zusätzlich in der URL (Deep-Links/Teilen).

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
  const qs = p.toString()
  return qs ? `/?${qs}` : '/'
}

function navFromLocation(): { nav: NavOnly; scrub: number | null } {
  const p = new URLSearchParams(window.location.search)
  const tab = (['board', 'journey', 'line', 'day'] as const).includes(p.get('tab') as Tab)
    ? (p.get('tab') as Tab) : 'board'
  const at = p.get('at')
  return {
    nav: {
      tab,
      station: p.get('station'),
      line: p.get('line'),
      run: p.get('run'),
      runDep: p.get('dep') != null ? Number(p.get('dep')) : null,
      stopIdx: p.get('stop') != null ? Number(p.get('stop')) || 0 : 0,
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

  // Beim Start: nur Metainfos (Zeitrahmen des Tages). Kein weiterer Load.
  useEffect(() => {
    let alive = true
    api.meta().then((m) => {
      if (!alive) return
      setMeta(m)
      if (init.current.scrub == null) setScrubRaw(m.now)
    }).catch((e) => setMetaError(String(e)))
    return () => { alive = false }
  }, [])

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
      const s: NavState = { ...navRef.current, scrub: scrubRef.current }
      window.history.replaceState(s, '', urlFor(navRef.current, scrubRef.current))
    }, 400)
  }, [])

  // Browser Zurück/Vor: Zustand aus dem History-Eintrag wiederherstellen
  // (ohne selbst zu pushen).
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
    // Initialen Eintrag mit Zustand versehen (Reload/Deep-Link).
    window.history.replaceState(
      { ...init.current.nav, scrub: init.current.scrub ?? 0 }, '',
      urlFor(init.current.nav, init.current.scrub ?? 0))
    return () => window.removeEventListener('popstate', h)
  }, [])

  // --- Navigation ----------------------------------------------------------

  const goStation = useCallback((station: string) => {
    pushNav({ ...navRef.current, tab: 'board', station })
  }, [pushNav])

  const goLine = useCallback((lineKey: string) => {
    pushNav({ ...navRef.current, tab: 'line', line: lineKey })
  }, [pushNav])

  const goRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    pushNav({ ...navRef.current, tab: 'journey', run: runId, runDep: dep ?? null, stopIdx })
  }, [pushNav])

  const setTab = useCallback((tab: Tab) => {
    pushNav({ ...navRef.current, tab })
  }, [pushNav])

  const selectLine = useCallback((lineKey: string) => {
    replaceNav({ ...navRef.current, line: lineKey, run: null, runDep: null })
  }, [replaceNav])

  const selectRun = useCallback((runId: string, stopIdx = 0, dep?: number) => {
    replaceNav({ ...navRef.current, run: runId, runDep: dep ?? null, stopIdx })
  }, [replaceNav])

  const back = useCallback(() => window.history.back(), [])

  const value = useMemo<Ctx>(() => ({
    meta, metaError, scrub, setScrub, theme, toggleTheme,
    nav, goStation, goLine, goRun, setTab, selectLine, selectRun,
    canGoBack: histLen > 1, back,
  }), [meta, metaError, scrub, setScrub, theme, toggleTheme, nav,
    goStation, goLine, goRun, setTab, selectLine, selectRun, histLen, back])

  return <C.Provider value={value}>{children}</C.Provider>
}
