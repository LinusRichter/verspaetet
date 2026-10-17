import { useEffect, type ReactNode } from 'react'
import { AppProvider, useApp } from './state'
import type { Tab } from './types'
import { ScrubBar } from './components/ScrubBar'
import { SearchBox } from './components/SearchBox'
import { Breadcrumbs } from './components/Breadcrumbs'
import { Board } from './components/Board'
import { Journey } from './components/Journey'
import { LineView } from './components/LineView'
import { DayView } from './components/DayView'
import { ErrorBox } from './components/bits'

const TABS: { id: Tab; label: string }[] = [
  { id: 'board', label: 'Bahnhofstafel' },
  { id: 'journey', label: 'Zuglauf' },
  { id: 'line', label: 'Linie' },
  { id: 'day', label: 'Übersicht' },
]

function Shell(): ReactNode {
  const { metaError, nav, setTab, theme, toggleTheme } = useApp()

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

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
          <SearchBox />
          <div className="hdr-right">
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
        alle Ansichten werden on demand geladen
      </footer>
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
