import { describe, expect, it } from 'vitest'
import { toDailySeries } from './TrendChart'

describe('toDailySeries', () => {
  // group_by=day returns one row per (day, sku). Taking rows as-is would draw
  // a sawtooth of whichever SKU sorted last instead of a spend trend.
  it('sums every SKU within a day', () => {
    const s = toDailySeries([
      { key: '2026-09-01', quantity: '10' },
      { key: '2026-09-01', quantity: '5' },
      { key: '2026-09-02', quantity: '3' },
    ])
    expect(s).toEqual([
      { label: '2026-09-01', value: 15 },
      { label: '2026-09-02', value: 3 },
    ])
  })

  it('orders chronologically regardless of row order', () => {
    const s = toDailySeries([
      { key: '2026-09-03', quantity: '1' },
      { key: '2026-09-01', quantity: '2' },
    ])
    expect(s.map((p) => p.label)).toEqual(['2026-09-01', '2026-09-03'])
  })

  // A day the API did not return was not measured. Back-filling a zero would
  // draw a dip that never happened.
  it('does not invent missing days', () => {
    const s = toDailySeries([
      { key: '2026-09-01', quantity: '1' },
      { key: '2026-09-05', quantity: '1' },
    ])
    expect(s).toHaveLength(2)
  })

  it('ignores unparseable quantities rather than rendering NaN', () => {
    const s = toDailySeries([
      { key: '2026-09-01', quantity: 'not-a-number' },
      { key: '2026-09-01', quantity: '4' },
    ])
    expect(s).toEqual([{ label: '2026-09-01', value: 4 }])
  })

  it('is empty for no rows, so the caller can render nothing', () => {
    expect(toDailySeries([])).toEqual([])
  })
})

describe('field naming', () => {
  // The API names the column `key` on the wire; the client type calls it
  // `day`. Accepting only one would leave the chart silently empty after a
  // rename on either side — the failure looks like "no usage", not a bug.
  it('accepts either day or key', () => {
    expect(toDailySeries([{ day: '2026-09-01', quantity: '2' }])).toEqual([
      { label: '2026-09-01', value: 2 },
    ])
    expect(toDailySeries([{ key: '2026-09-01', quantity: '2' }])).toEqual([
      { label: '2026-09-01', value: 2 },
    ])
  })
})
