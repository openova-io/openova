import { describe, expect, it } from 'vitest'
import { statementPeriod } from './statements'

describe('statementPeriod', () => {
  // period_end is inclusive on the wire; a whole month must read as that month,
  // not as "1 Aug – 30 Aug" (an off-by-one that misreports the period).
  it('reads a whole calendar month as the month', () => {
    expect(statementPeriod({ period_start: '2026-08-01', period_end: '2026-08-31' })).toBe('Aug 2026')
    expect(statementPeriod({ period_start: '2026-02-01', period_end: '2026-02-28' })).toBe('Feb 2026')
  })
  it('describes a partial period day to day', () => {
    expect(statementPeriod({ period_start: '2026-09-01', period_end: '2026-09-15' })).toBe('1–15 Sep 2026')
  })
  it('accepts timestamps and shows malformed periods as sent', () => {
    expect(statementPeriod({ period_start: '2026-08-01T00:00:00Z', period_end: '2026-08-31T00:00:00Z' })).toBe('Aug 2026')
    expect(statementPeriod({ period_start: '2026-08-31', period_end: '2026-08-01' })).toBe('2026-08-31 → 2026-08-01')
    expect(statementPeriod({ period_start: '', period_end: '' })).toBe('? → ?')
  })
})
