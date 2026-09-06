import { describe, expect, it } from 'vitest'
import { fitLabel, linearTicks, niceMax, niceTicks, shortBucket, xLabelEvery } from './scale'

describe('niceTicks', () => {
  // A flat-zero series that WAS measured still needs a baseline to sit on.
  it('max 0 → a baseline, not a divide-by-zero', () => {
    expect(niceTicks(0)).toEqual([0, 1])
  })
  it('max 1', () => {
    expect(niceTicks(1)).toEqual([0, 0.25, 0.5, 0.75, 1])
  })
  it('max 7', () => {
    expect(niceTicks(7)).toEqual([0, 2, 4, 6, 8])
  })
  // The fixture window total. A 50-step would leave the top third empty;
  // the tie-break prefers the finer step.
  it('max 104.16', () => {
    expect(niceTicks(104.16)).toEqual([0, 25, 50, 75, 100, 125])
  })
  it('max 2780.01', () => {
    expect(niceTicks(2780.01)).toEqual([0, 1000, 2000, 3000])
  })
  it('max 1e6', () => {
    expect(niceTicks(1e6)).toEqual([0, 250000, 500000, 750000, 1000000])
  })
  it('never leaves floating-point residue on a tick', () => {
    expect(niceTicks(0.3)).toEqual([0, 0.1, 0.2, 0.3])
    expect(niceTicks(0.007)).toEqual([0, 0.002, 0.004, 0.006, 0.008])
  })
  it('always starts at 0, ascends, and covers max', () => {
    for (const m of [0.004, 0.9, 3, 49, 99.99, 100, 12345.678, 7e7]) {
      const t = niceTicks(m)
      expect(t[0]).toBe(0)
      expect(t[t.length - 1]).toBeGreaterThanOrEqual(m)
      for (let i = 1; i < t.length; i++) expect(t[i]).toBeGreaterThan(t[i - 1])
      expect(t.length).toBeGreaterThanOrEqual(3)
      expect(t.length).toBeLessThanOrEqual(8)
    }
  })
  it('honours the count hint', () => {
    expect(niceTicks(100, 3)).toEqual([0, 50, 100])
  })
  it('NaN, negative and Infinity fall back to the baseline', () => {
    expect(niceTicks(NaN)).toEqual([0, 1])
    expect(niceTicks(-5)).toEqual([0, 1])
    expect(niceTicks(Infinity)).toEqual([0, 1])
  })
})

describe('niceMax', () => {
  it('is the top tick', () => {
    expect(niceMax(104.16)).toBe(125)
    expect(niceMax(0)).toBe(1)
  })
})

describe('linearTicks', () => {
  it('is niceTicks for a non-negative domain', () => {
    expect(linearTicks(0, 7)).toEqual([0, 2, 4, 6, 8])
    expect(linearTicks(3, 7)).toEqual([0, 2, 4, 6, 8])
  })
  it('extends below zero with the same step and keeps 0 as a tick', () => {
    expect(linearTicks(-5, 0)).toEqual([-5, -4, -3, -2, -1, 0])
    expect(linearTicks(-30, 104.16)).toEqual([-50, -25, 0, 25, 50, 75, 100, 125])
  })
})

describe('xLabelEvery', () => {
  it('labels every bucket when they fit', () => {
    expect(xLabelEvery(7, 700)).toBe(1)
  })
  it('thins as buckets get denser', () => {
    expect(xLabelEvery(30, 600)).toBe(3)
    expect(xLabelEvery(365, 900)).toBe(20)
  })
  it('is never 0', () => {
    expect(xLabelEvery(0, 100)).toBe(1)
    expect(xLabelEvery(10, 0)).toBeGreaterThanOrEqual(1)
  })
})

describe('shortBucket', () => {
  it('abbreviates day and month buckets and leaves others alone', () => {
    expect(shortBucket('2026-09-01')).toBe('1 Sep')
    expect(shortBucket('2026-12-31')).toBe('31 Dec')
    expect(shortBucket('2026-09')).toBe('Sep 2026')
    expect(shortBucket('ecs')).toBe('ecs')
  })
})

describe('fitLabel', () => {
  it('leaves text that fits alone', () => {
    expect(fitLabel('1 Sep', 60)).toBe('1 Sep')
  })
  it('truncates with an ellipsis inside the budget', () => {
    const out = fitLabel('Elastic Cloud Server', 60)
    expect(out.endsWith('…')).toBe(true)
    expect(out.length * 6.7).toBeLessThanOrEqual(60)
    expect(out).toBe('Elastic…')
  })
  it('degrades to a lone ellipsis', () => {
    expect(fitLabel('anything', 5)).toBe('…')
  })
})
