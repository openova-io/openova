import type { Forecast } from '../api/types'

/**
 * Readers over the month-end forecast object (#6867 follow-up). The API
 * labels every forecast with the method it used, how many complete days it
 * saw and a confidence; the KPI cards say those in words, and the overview
 * card lists the weekly shape the weekday-seasonal method applied.
 */

/** Display order for weekday factors; the wire map has no order of its own. */
export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const

/** "weekday-seasonal · 21 days · medium confidence" */
export function forecastNote(f: Forecast | null | undefined): string {
  if (!f) return ''
  const days = Number(f.days_observed)
  const parts = [f.method || 'forecast']
  if (Number.isFinite(days) && days > 0) parts.push(`${days} day${days === 1 ? '' : 's'}`)
  if (f.confidence) parts.push(`${f.confidence} confidence`)
  return parts.join(' · ')
}

/** Weekday factors in Mon…Sun order; empty when the method applied no shape. */
export function weekdayFactors(f: Forecast | null | undefined): Array<{ day: string; factor: number }> {
  const wf = f?.weekday_factors
  if (!wf || typeof wf !== 'object') return []
  const out: Array<{ day: string; factor: number }> = []
  for (const day of WEEKDAYS) {
    const v = Number(wf[day])
    if (Number.isFinite(v)) out.push({ day, factor: v })
  }
  return out
}

/**
 * Tooltip for the forecast KPI: what the number is made of, plus the weekday
 * factors (× the overall mean) when the weekday-seasonal method applied them.
 * Lines are joined with "\n" — a title attribute renders them as such.
 */
export function forecastHint(f: Forecast | null | undefined): string {
  const base = 'Complete days so far + the projected cost of each remaining day'
  if (!f) return base
  const lines = [base]
  const method = typeof f.method === 'string' ? f.method : ''
  if (method === 'weekday-seasonal') lines.push('Projection: mean of the last 28 days + trend, shaped by the weekday factors')
  else if (method.endsWith('+trend')) lines.push('Projection: 7-day run rate + trend per day')
  else lines.push('Projection: daily run rate, flat')
  const wf = weekdayFactors(f)
  if (wf.length) lines.push(`Weekday factors: ${wf.map((x) => `${x.day} ×${x.factor.toFixed(2)}`).join(' · ')}`)
  return lines.join('\n')
}
