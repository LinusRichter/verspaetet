import { useState } from 'react'
import { StatsBar } from './components/StatsBar'
import { StationList } from './components/StationList'
import { LineList } from './components/LineList'
import { EventTable } from './components/EventTable'
import { RouteList } from './components/RouteList'
import { TripView } from './components/TripView'
import { TopDelays } from './components/TopDelays'
import { Breadcrumb } from './components/Breadcrumb'
import { HealthBadge } from './components/HealthBadge'

function App() {
  const [slug, setSlug] = useState<string | null>(null)
  const [name, setName] = useState<string | null>(null)
  const [line, setLine] = useState<string | null>(null)
  const [trip, setTrip] = useState<string | null>(null)

  const selectStation = (s: string, n: string) => { setSlug(s); setName(n); setLine(null); setTrip(null) }
  const reset = () => { setSlug(null); setName(null); setLine(null); setTrip(null) }
  const resetLine = () => { setLine(null); setTrip(null) }
  const handleSelectStation = (s: string, n: string) => { setLine(null); setTrip(null); setSlug(s); setName(n) }
  const handleSelectTrip = (stopId: string) => { setTrip(stopId) }

  return (
    <div className="app">
      <div className="top-bar">
        <StatsBar />
        <div className="top-bar-right">
          <HealthBadge />
          <a href={`http://${window.location.hostname}:8081`} target="_blank" rel="noopener noreferrer" className="ui-link">asynqmon</a>
        </div>
      </div>
      <div className="main-layout">
        <TopDelays />
        <div className="content">
          <Breadcrumb stationName={name} lineLabel={line} onReset={reset} onResetLine={resetLine} />
          {!slug && <StationList onSelect={selectStation} />}
          {slug && !line && (
            <>
              <LineList slug={slug} stationName={name!} onSelect={setLine} />
              <RouteList slug={slug} stationName={name!} onSelectLine={setLine} onSelectStation={handleSelectStation} />
            </>
          )}
          {slug && line && !trip && <EventTable slug={slug} lineLabel={line} stationName={name!} onSelectStation={handleSelectStation} onSelectTrip={handleSelectTrip} />}
          {slug && line && trip && <TripView stopId={trip} lineLabel={line} onSelectStation={handleSelectStation} />}
        </div>
      </div>
    </div>
  )
}

export default App