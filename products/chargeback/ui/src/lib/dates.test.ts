import { describe, expect, it } from 'vitest'
import { addDays, bucketLabel, daysIn, defaultGranularity, describeWindow, presetWindow, toExclusive, toInclusive, windowFromParams } from './dates'

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
