import { describeWindow, toExclusive } from './dates'

const DAY = /^\d{4}-\d{2}-\d{2}$/

/**
 * "Aug 2026" for a statement's period. The wire sends period_end INCLUSIVE
 * (internal/rating/run.go: to − 1 day), so it is widened by a day before
 * describeWindow, which takes an exclusive end. Anything malformed is shown
 * as sent rather than mis-described.
 */
export function statementPeriod(s: { period_start: string; period_end: string }): string {
  const from = (s.period_start ?? '').slice(0, 10)
  const end = (s.period_end ?? '').slice(0, 10)
  if (!DAY.test(from) || !DAY.test(end) || end < from) return `${from || '?'} → ${end || '?'}`
  return describeWindow({ from, to: toExclusive(end) })
}
