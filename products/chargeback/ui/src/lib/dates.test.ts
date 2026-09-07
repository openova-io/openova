import { describe, expect, it } from 'vitest'
import {
  MAX_HOURLY_DAYS,
  addDays,
  bucketLabel,
  compareLabel,
  compareWindow,
  daysIn,
  defaultGranularity,
  describeWindow,
  fitGranularity,
  hourlyAllowed,
  presetWindow,
  previousWindow,
  shiftDay,
  shiftWindow,
  toExclusive,
  toInclusive,
  windowFromParams,
} from './dates'

const now = new Date(Date.UTC(2026, 8, 7, 10, 0, 0)) // 2026-09-07

describe('presets', () => {
  it('month to date starts on the 1st and ends after today', () => {
    expect(presetWindow('mtd', now)).toEqual({ from: '2026-09-01', to: '2026-09-08' })
  })
  it('last month is the whole previous calendar month, half-open', () => {
    expect(presetWindow('last-month', now)).toEqual({ from: '2026-08-01', to: '2026-09-01' })
  })
  it('7d / 30d include today', () => {
    expect(presetWindow('7d', now)).toEqual({ from: '2026-09-01', to: '2026-09-08' })
    expect(presetWindow('30d', now)).toEqual({ from: '2026-08-09', to: '2026-09-08' })
  })
  it('3m / 6m start on the 1st of the month N-1 months back; ytd on Jan 1', () => {
    expect(presetWindow('3m', now).from).toBe('2026-07-01')
    expect(presetWindow('6m', now).from).toBe('2026-04-01')
    expect(presetWindow('ytd', now).from).toBe('2026-01-01')
  })
  it('crosses a year boundary', () => {
    const jan = new Date(Date.UTC(2027, 0, 3))
    expect(presetWindow('last-month', jan)).toEqual({ from: '2026-12-01', to: '2027-01-01' })
    expect(presetWindow('7d', jan).from).toBe('2026-12-28')
  })
})

describe('window math', () => {
  it('inclusive ↔ exclusive ends are inverses', () => {
    expect(toExclusive('2026-09-30')).toBe('2026-10-01')
    expect(toInclusive('2026-10-01')).toBe('2026-09-30')
    expect(addDays('2026-02-28', 1)).toBe('2026-03-01')
  })
  it('counts days and picks the grain', () => {
    expect(daysIn({ from: '2026-09-01', to: '2026-09-08' })).toBe(7)
    expect(defaultGranularity({ from: '2026-09-01', to: '2026-09-08' })).toBe('day')
    expect(defaultGranularity({ from: '2026-01-01', to: '2026-09-08' })).toBe('month')
  })
  it('describes windows the way people say them', () => {
    expect(describeWindow({ from: '2026-09-01', to: '2026-09-08' })).toBe('1–7 Sep 2026')
    expect(describeWindow({ from: '2026-08-01', to: '2026-09-01' })).toBe('Aug 2026')
    expect(describeWindow({ from: '2026-08-01', to: '2026-09-08' })).toBe('1 Aug – 7 Sep 2026')
    expect(describeWindow({ from: '2025-12-20', to: '2026-01-05' })).toBe('20 Dec 2025 – 4 Jan 2026')
  })
  it('labels buckets', () => {
    expect(bucketLabel('2026-09-07')).toBe('7 Sep')
    expect(bucketLabel('2026-09')).toBe('Sep 26')
    expect(bucketLabel('2026-09-07T14')).toBe('7 Sep 14:00')
    expect(bucketLabel('2026-09-07T00')).toBe('7 Sep 00:00')
  })
  it('allows hourly grain up to 14 days and falls back to daily beyond', () => {
    expect(MAX_HOURLY_DAYS).toBe(14)
    expect(hourlyAllowed({ from: '2026-09-01', to: '2026-09-15' })).toBe(true)
    expect(hourlyAllowed({ from: '2026-09-01', to: '2026-09-16' })).toBe(false)
    expect(fitGranularity('hour', { from: '2026-09-01', to: '2026-09-02' })).toBe('hour')
    expect(fitGranularity('hour', presetWindow('30d', now))).toBe('day')
    expect(fitGranularity('month', presetWindow('30d', now))).toBe('month')
  })
})

describe('compare windows', () => {
  it('previousWindow is the same length, ending where the window starts', () => {
    expect(previousWindow({ from: '2026-09-01', to: '2026-09-08' })).toEqual({ from: '2026-08-25', to: '2026-09-01' })
    expect(previousWindow({ from: '2026-03-01', to: '2026-04-01' })).toEqual({ from: '2026-01-29', to: '2026-03-01' })
  })
  it('shiftDay clamps to the end of a shorter month', () => {
    expect(shiftDay('2026-03-31', -1, 'months')).toBe('2026-02-28')
    expect(shiftDay('2028-03-31', -1, 'months')).toBe('2028-02-29')
    expect(shiftDay('2026-08-31', 1, 'months')).toBe('2026-09-30')
    expect(shiftDay('2026-09-07', -1, 'months')).toBe('2026-08-07')
    expect(shiftDay('2026-01-15', -1, 'months')).toBe('2025-12-15')
    expect(shiftDay('2028-02-29', -1, 'years')).toBe('2027-02-28')
    expect(shiftDay('2026-09-07', -1, 'years')).toBe('2025-09-07')
  })
  it('shiftWindow moves a window by a month, keeping whole months whole', () => {
    expect(shiftWindow({ from: '2026-09-01', to: '2026-09-08' }, -1, 'months')).toEqual({ from: '2026-08-01', to: '2026-08-08' })
    // Whole March → whole February.
    expect(shiftWindow({ from: '2026-03-01', to: '2026-04-01' }, -1, 'months')).toEqual({ from: '2026-02-01', to: '2026-03-01' })
    // 31 Mar (one day) → 28 Feb (one day), never an empty window.
    expect(shiftWindow({ from: '2026-03-31', to: '2026-04-01' }, -1, 'months')).toEqual({ from: '2026-02-28', to: '2026-03-01' })
    expect(shiftWindow({ from: '2026-03-30', to: '2026-03-31' }, -1, 'months')).toEqual({ from: '2026-02-28', to: '2026-03-01' })
    // 29–31 Mar collapses onto 28 Feb: still a one-day window, not zero.
    const w = shiftWindow({ from: '2026-03-29', to: '2026-04-01' }, -1, 'months')
    expect(w).toEqual({ from: '2026-02-28', to: '2026-03-01' })
    expect(daysIn(w)).toBe(1)
  })
  it('shiftWindow by a year crosses leap days', () => {
    expect(shiftWindow({ from: '2028-02-01', to: '2028-03-01' }, -1, 'years')).toEqual({ from: '2027-02-01', to: '2027-03-01' })
    expect(shiftWindow({ from: '2026-09-01', to: '2026-09-08' }, -1, 'years')).toEqual({ from: '2025-09-01', to: '2025-09-08' })
  })
  it('compareWindow resolves each mode', () => {
    const w = { from: '2026-09-01', to: '2026-09-08' }
    expect(compareWindow(w, 'previous')).toBeNull()
    expect(compareWindow(w, 'last-month')).toEqual({ from: '2026-08-01', to: '2026-08-08' })
    expect(compareWindow(w, 'last-year')).toEqual({ from: '2025-09-01', to: '2025-09-08' })
    expect(compareWindow(w, 'custom', { from: '2026-01-01', to: '2026-02-01' })).toEqual({ from: '2026-01-01', to: '2026-02-01' })
    expect(compareWindow(w, 'custom', null)).toBeNull()
  })
  it('labels modes for the KPI note', () => {
    expect(compareLabel('previous')).toBe('previous period')
    expect(compareLabel('last-month')).toBe('same period last month')
    expect(compareLabel('last-year')).toBe('same period last year')
    expect(compareLabel('custom')).toBe('custom period')
  })
})

describe('windowFromParams', () => {
  it('uses explicit from/to as custom', () => {
    const r = windowFromParams(new URLSearchParams('from=2026-09-01&to=2026-09-04'), '30d', now)
    expect(r).toEqual({ window: { from: '2026-09-01', to: '2026-09-04' }, preset: 'custom' })
  })
  it('falls back on a malformed custom window', () => {
    const r = windowFromParams(new URLSearchParams('from=2026-09-10&to=2026-09-01'), '7d', now)
    expect(r.preset).toBe('7d')
    expect(r.window).toEqual(presetWindow('7d', now))
  })
  it('honours a preset param', () => {
    expect(windowFromParams(new URLSearchParams('preset=last-month'), '30d', now).window).toEqual({ from: '2026-08-01', to: '2026-09-01' })
  })
})
