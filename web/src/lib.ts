// Kleine pure Helfer, überall benutzt.

export function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** Minuten des Tages → „HH:MM". Negativ-/Überlauf-sicher (Mitternachtsfahrten). */
export function fmt(m: number): string {
  const v = ((m % 1440) + 1440) % 1440
  return pad(Math.floor(v / 60)) + ':' + pad(v % 60)
}

export function clamp(v: number, a: number, b: number): number {
  return Math.max(a, Math.min(b, v))
}

export function pct(arr: number[], p: number): number {
  if (!arr.length) return 0
  const s = [...arr].sort((a, b) => a - b)
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]
}

/** Wissen über einen Halt zum Zeitpunkt t (letzte Snapshot-Row ≤ t). */
export interface StopState {
  known: boolean
  ist: boolean
  delay: number | null
  cancelled: boolean
  plat: string
}

export interface SnapLike {
  at: number
  prog: number | null
  ist: boolean
  cancelled: boolean
  plat: string
}

export function stateAt(stop: { snaps: SnapLike[]; platFrom: string }, t: number): StopState {
  let best: SnapLike | null = null
  for (const sn of stop.snaps) {
    if (sn.at <= t) best = sn
    else break
  }
  if (!best) return { known: false, ist: false, delay: null, cancelled: false, plat: stop.platFrom }
  return { known: true, ist: best.ist, delay: best.prog, cancelled: best.cancelled, plat: best.plat }
}

export function delayCls(d: number | null): string {
  if (d == null) return 'unknown'
  if (d >= 10) return 'bad'
  if (d >= 6) return 'orange'
  if (d >= 3) return 'warn'
  return 'ok'
}

export function runStateAt(run: { cancelled: boolean; stops: { name: string; planned: number; actual: number }[] }, t: number): string {
  const st = run.stops
  if (run.cancelled) return 'Fahrt entfallen'
  if (t < st[0].planned - 4) return 'noch nicht abgefahren'
  if (t >= st[st.length - 1].actual) return 'Fahrt beendet'
  for (let i = 0; i < st.length - 1; i++) {
    if (t >= st[i].planned && t < st[i + 1].planned) return `unterwegs ${st[i].name} → ${st[i + 1].name}`
  }
  return 'im Ziel'
}

/** Tausenderpunkte nach deutscher Art: 42587 → „42.587". */
export function fmtNum(n: number): string {
  return n.toLocaleString('de-DE')
}

export function debounce<A extends unknown[]>(fn: (...a: A) => void, ms: number): (...a: A) => void {
  let t: ReturnType<typeof setTimeout> | undefined
  return (...a: A) => {
    clearTimeout(t)
    t = setTimeout(() => fn(...a), ms)
  }
}
