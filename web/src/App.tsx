import { useEffect, useState, type ReactNode } from 'react'
import { AppProvider, useApp } from './state'
import type { Tab } from './types'
import { ScrubBar } from './components/ScrubBar'
import { Breadcrumbs } from './components/Breadcrumbs'
import { Board } from './components/Board'
import { Journey } from './components/Journey'
import { LineView } from './components/LineView'
import { DayView } from './components/DayView'
import { Palette } from './components/Palette'
import { ErrorBox } from './components/bits'

const TABS: { id: Tab; label: string }[] = [
  { id: 'board', label: 'Bahnhofstafel' },
  { id: 'journey', label: 'Zuglauf' },
  { id: 'line', label: 'Linie' },
  { id: 'day', label: 'Übersicht' },
]

function Shell(): ReactNode {
  const { metaError, nav, setTab, theme, toggleTheme } = useApp()
  const [palOpen, setPalOpen] = useState(false)

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  // Ctrl+/ oder Shift+/ öffnet die Palette.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.shiftKey) && e.key === '/') {
        e.preventDefault()
        setPalOpen(true)
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [])

  return (
    <div className="app">
      <header>
        <div className="hdr-row">
          <div className="brand">verspaetet</div>
          <nav className="tabs">
            {TABS.map((t) => (
              <button key={t.id} className={`tab${nav.tab === t.id ? ' active' : ''}`} onClick={() => setTab(t.id)}>
                {t.label}
              </button>
            ))}
          </nav>
          <div className="hdr-right">
            <button className="mini" onClick={() => setPalOpen(true)} title="Suche (Ctrl+/ oder Shift+/)">
              ⌕ Suche
            </button>
            <button className="mini" onClick={toggleTheme}>{theme === 'dark' ? '☀ hell' : '🌙 dunkel'}</button>
          </div>
        </div>
        {metaError ? <div className="scrub-row"><ErrorBox msg={`Meta: ${metaError}`} /></div> : <ScrubBar />}
      </header>
      <Breadcrumbs />
      <main>
        {nav.tab === 'board' && <Board />}
        {nav.tab === 'journey' && <Journey />}
        {nav.tab === 'line' && <LineView />}
        {nav.tab === 'day' && <DayView />}
      </main>
      <footer>
        Fahrten ≙ stop_id-Gruppen · Snapshots ≙ stop_events-Rows (scraped_at = Wissensstand) ·
        alle Ansichten werden on demand geladen · Suche: Ctrl+/ oder Shift+/
      </footer>
      <Palette open={palOpen} onClose={() => setPalOpen(false)} />
    </div>
  )
}

export default function App(): ReactNode {
  return (
    <AppProvider>
      <Shell />
    </AppProvider>
  )
}