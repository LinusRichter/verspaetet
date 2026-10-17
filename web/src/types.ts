// Domain-Typen der Data-UI. Zahlen sind Minuten des Tages (Berlin), sobald
// es Zeitpunkte sind — identisch zur Semantik der API-Endpunkte.

export type Tab = 'board' | 'journey' | 'line' | 'day'

export interface Meta {
  day: string       // Anzeige-Label des geladenen Tages
  t0: number        // Zeitstrahl-Start (min)
  t1: number        // Zeitstrahl-Ende (min)
  now: number       // „jetzt" (min); bei Historie-Tagen = Datenende
}

export interface LineInfo {
  key: string       // eindeutige Linien-Identität (z.B. cat+label+Bereich)
  label: string     // öffentliches Label („RE 7", „S 6")
  cat: string       // Kategorie (ICE, RE, RB, S, …)
  ctx?: string      // Bereichs-Kontext bei mehrfach belegten Labels
}

export interface Snap {
  at: number        // Wissensstand (min)
  prog: number | null // Verspätung in min; null = keine Prognose
  ist: boolean      // tatsächlich geschehen
  cancelled: boolean
  plat: string
}

export interface Stop {
  name: string
  planned: number
  finalD: number    // letzte bekannte Verspätung
  actual: number    // planned + finalD (bzw. +2 bei Entfall)
  cancelled: boolean
  platFrom: string  // planmäßiges Gleis
  platTo: string    // letztes bekanntes Gleis
  platKnown: number // ab wann Gleiswechsel bekannt (min; 1e9 = kein Wechsel)
  snaps: Snap[]
}

export interface Run {
  id: string        // Trip-Schlüssel (stop_id-Präfix)
  lineKey: string
  dep: number       // Startzeit (min)
  cancelled: boolean
  stops: Stop[]
}

export interface SearchStation { name: string; n: number }
export interface SearchLine extends LineInfo { n: number; ctx: string }
export interface SearchResults { stations: SearchStation[]; lines: SearchLine[] }

export interface BoardRow {
  runId: string
  stopIdx: number
  lineKey: string
  planned: number
  known: boolean
  delay: number | null
  ist: boolean
  cancelled: boolean
  plat: string
  platFrom: string
  platChanged: boolean
  dest: string
  via: string
}

export interface BoardData {
  rows: BoardRow[]
  lineKeys: string[]   // Linien, die an dieser Station halten
  lines: LineInfo[]    // deren Metadaten
}

export interface LineRunStop { name: string; finalD: number; cancelled: boolean }
export interface LineRun { id: string; dep: number; cancelled: boolean; finalD: number; stops: LineRunStop[] }

export interface AltLine {
  key: string
  label: string
  cat: string
  runId: string   // sinnvollste Fahrt (nahe „jetzt − 100 min") für Direktsprünge
  dep: number
}

export interface JourneyData {
  run: Run
  line: LineInfo & { evu: string }
  altLines: AltLine[]
  altScoped: boolean // true = auf den Bahnhofskontext gefiltert
}

export interface LineData {
  line: LineInfo
  ctx?: string       // Disambiguierung (z.B. „Hagen Hbf ⇢ Köln Hbf")
  runs: LineRun[]
  stats: { n: number; cx: number; p50: number; p90: number; max: number; punc: number; gc: number }
  altLines: AltLine[]
  altScoped: boolean
}

export interface DayKpis { runs: number; punc: number; avg: number; cx: number; gc: number; snaps: number }
export interface HourBar { h: number; avg: number; n: number }
export interface LeaderRow { key: string; label: string; n: number; p50: number; p90: number; max: number; punc: number; cx: number }
export interface TopRow { runId: string; label: string; dep: number; from: string; to: string; finalD: number }
export interface DayData {
  kpis: DayKpis
  hours: HourBar[]
  leaderboard: LeaderRow[]
  top: TopRow[]
}
