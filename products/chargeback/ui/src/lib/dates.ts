// Date-window helpers for the cost pages (#6867).
//
// The API takes half-open windows [from, to) in whole UTC days. Pickers show
// an INCLUSIVE end date; `toExclusive` converts. Presets follow the cloud
// consoles' conventions: MTD starts on the 1st, "last month" is the previous
// calendar month, YTD starts on 1 January.

export type Preset = '7d' | '30d' | 'mtd' | 'last-month' | '3m' | '6m' | 'ytd' | 'custom'

export const PRESETS: ReadonlyArray<{ value: Preset; label: string }> = [
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
  { value: 'mtd', label: 'Month to date' },
  { value: 'last-month', label: 'Last month' },
  { value: '3m', label: 'Last 3 months' },
  { value: '6m', label: 'Last 6 months' },
  { value: 'ytd', label: 'Year to date' },
  { value: 'custom', label: 'Custom' },
]

export interface Window {
  /** inclusive, YYYY-MM-DD */
  from: string
  /** EXCLUSIVE, YYYY-MM-DD — what the API takes */
  to: string
}

export function iso(d: Date): string {
  return d.toISOString().slice(0, 10)
}

export function utc(y: number, m0: number, d: number): Date {
  return new Date(Date.UTC(y, m0, d))
}

export function parseDay(s: string): Date {
  const [y, m, d] = s.split('-').map(Number)
  return utc(y, m - 1, d)
}

export function addDays(s: string, n: number): string {
  const d = parseDay(s)
  return iso(utc(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() + n))
}

/** Inclusive picker end → exclusive API end. */
export function toExclusive(inclusiveEnd: string): string {
  return addDays(inclusiveEnd, 1)
}
/** Exclusive API end → inclusive picker end. */
export function toInclusive(exclusiveEnd: string): string {
  return addDays(exclusiveEnd, -1)
}

/** The window a preset means, relative to `now` (UTC). */
export function presetWindow(p: Preset, now = new Date()): Window {
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth()
  const today = utc(y, m, now.getUTCDate())
  const tomorrow = iso(utc(y, m, now.getUTCDate() + 1))
  switch (p) {
    case '7d':
      return { from: iso(utc(y, m, today.getUTCDate() - 6)), to: tomorrow }
    case '30d':
      return { from: iso(utc(y, m, today.getUTCDate() - 29)), to: tomorrow }
    case 'mtd':
      return { from: iso(utc(y, m, 1)), to: tomorrow }
    case 'last-month':
      return { from: iso(utc(y, m - 1, 1)), to: iso(utc(y, m, 1)) }
    case '3m':
      return { from: iso(utc(y, m - 2, 1)), to: tomorrow }
    case '6m':
      return { from: iso(utc(y, m - 5, 1)), to: tomorrow }
    case 'ytd':
      return { from: iso(utc(y, 0, 1)), to: tomorrow }
    case 'custom':
      return { from: iso(utc(y, m, today.getUTCDate() - 29)), to: tomorrow }
  }
}

/** Day grain up to ~93 days, month grain beyond — the AWS/Azure default. */
export function defaultGranularity(w: Window): 'day' | 'month' {
  return daysIn(w) > 93 ? 'month' : 'day'
}

export function daysIn(w: Window): number {
  return Math.round((parseDay(w.to).getTime() - parseDay(w.from).getTime()) / 86_400_000)
}

/** Human window text: "1–7 Sep 2026" / "Aug 2026" / "1 Aug – 7 Sep 2026". */
export function describeWindow(w: Window): string {
  const a = parseDay(w.from)
  const b = parseDay(toInclusive(w.to))
  const mon = (d: Date) => d.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })
  const isWholeMonth = a.getUTCDate() === 1 && iso(utc(a.getUTCFullYear(), a.getUTCMonth() + 1, 0)) === iso(b)
  if (isWholeMonth) return `${mon(a)} ${a.getUTCFullYear()}`
  if (a.getUTCFullYear() === b.getUTCFullYear() && a.getUTCMonth() === b.getUTCMonth()) {
    return `${a.getUTCDate()}–${b.getUTCDate()} ${mon(a)} ${a.getUTCFullYear()}`
  }
  if (a.getUTCFullYear() === b.getUTCFullYear()) {
    return `${a.getUTCDate()} ${mon(a)} – ${b.getUTCDate()} ${mon(b)} ${a.getUTCFullYear()}`
  }
  return `${a.getUTCDate()} ${mon(a)} ${a.getUTCFullYear()} – ${b.getUTCDate()} ${mon(b)} ${b.getUTCFullYear()}`
}

/** Short bucket label for axes: "7 Sep" for days, "Sep 26" for months. */
export function bucketLabel(bucket: string): string {
  if (bucket.length === 7) {
    const [y, m] = bucket.split('-').map(Number)
    return `${utc(y, m - 1, 1).toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })} ${String(y).slice(2)}`
  }
  const d = parseDay(bucket)
  return `${d.getUTCDate()} ${d.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })}`
}

/** Read window + granularity from URL search params, falling back to a preset. */
export function windowFromParams(params: URLSearchParams, fallback: Preset = '30d', now = new Date()): { window: Window; preset: Preset } {
  const from = params.get('from')
  const to = params.get('to')
  const preset = (params.get('preset') as Preset | null) ?? (from && to ? 'custom' : fallback)
  if (preset === 'custom' && from && to && /^\d{4}-\d{2}-\d{2}$/.test(from) && /^\d{4}-\d{2}-\d{2}$/.test(to) && to > from) {
    return { window: { from, to }, preset: 'custom' }
  }
  const p = preset === 'custom' ? fallback : preset
  return { window: presetWindow(p, now), preset: p }
}
