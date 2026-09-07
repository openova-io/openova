import { describe, expect, it } from 'vitest'
import explore from '../api/fixtures/explore.json'
import type { ExploreResult, Forecast } from '../api/types'
import { forecastHint, forecastNote, weekdayFactors } from './forecast'

const fixture = (explore as ExploreResult).forecast!

const seasonal: Forecast = {
  ...fixture,
  method: 'weekday-seasonal',
  days_observed: 21,
  confidence: 'medium',
  weekday_factors: { Sun: 0.58, Sat: 0.58, Mon: 1.17, Tue: 1.17, Wed: 1.17, Thu: 1.17, Fri: 1.17 },
}

describe('forecastNote', () => {
  it('says the method, the days observed and the confidence in words', () => {
    expect(forecastNote(seasonal)).toBe('weekday-seasonal · 21 days · medium confidence')
  })
  it('reads the Go fixture (7 flat days → run-rate-7d+trend, medium)', () => {
    expect(forecastNote(fixture)).toBe('run-rate-7d+trend · 7 days · medium confidence')
  })
  it('handles one day and a missing forecast', () => {
    expect(forecastNote({ ...fixture, method: 'run-rate-1d', days_observed: 1, confidence: 'low' })).toBe('run-rate-1d · 1 day · low confidence')
    expect(forecastNote(null)).toBe('')
  })
})

describe('weekdayFactors', () => {
  it('orders the wire map Mon…Sun', () => {
    expect(weekdayFactors(seasonal).map((x) => x.day)).toEqual(['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'])
    expect(weekdayFactors(seasonal)[5]).toEqual({ day: 'Sat', factor: 0.58 })
  })
  it('is empty when the method applied no shape (fixture has no weekday_factors)', () => {
    expect(fixture.weekday_factors).toBeUndefined()
    expect(weekdayFactors(fixture)).toEqual([])
    expect(weekdayFactors(null)).toEqual([])
  })
})

describe('forecastHint', () => {
  it('lists the weekday factors for the seasonal method', () => {
    const h = forecastHint(seasonal)
    expect(h).toContain('shaped by the weekday factors')
    expect(h).toContain('Weekday factors: Mon ×1.17 · Tue ×1.17 · Wed ×1.17 · Thu ×1.17 · Fri ×1.17 · Sat ×0.58 · Sun ×0.58')
  })
  it('describes the run-rate methods without factors', () => {
    expect(forecastHint(fixture)).toContain('7-day run rate + trend')
    expect(forecastHint(fixture)).not.toContain('Weekday factors')
    expect(forecastHint({ ...fixture, method: 'run-rate-3d' })).toContain('flat')
    expect(forecastHint(null)).toContain('Complete days so far')
  })
})
