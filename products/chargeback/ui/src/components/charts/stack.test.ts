import { describe, expect, it } from 'vitest'
import explore from '../../api/fixtures/explore.json'
import type { CostGroup, ExploreResult } from '../../api/types'
import { OTHER_COLOR, PALETTE, colorFor, colorForKey } from './palette'
import { forecastTail, seriesFromDaily, seriesFromExplore, stackSeries, topN } from './stack'

const fixture = explore as ExploreResult

describe('stackSeries', () => {
  it('computes cumulative offsets, totals and the extent', () => {
    const st = stackSeries([
      { key: 'a', label: 'A', values: [1, 2] },
      { key: 'b', label: 'B', values: [3, 4] },
    ])
    expect(st.y0).toEqual([
      [0, 0],
      [1, 2],
    ])
    expect(st.y1).toEqual([
      [1, 2],
      [4, 6],
    ])
    expect(st.totals).toEqual([4, 6])
    expect(st.max).toBe(6)
    expect(st.min).toBe(0)
  })
  it('stacks negative values downward from zero', () => {
    const st = stackSeries([
      { key: 'a', label: 'A', values: [5] },
      { key: 'b', label: 'B', values: [-2] },
    ])
    expect(st.y0).toEqual([[0], [0]])
    expect(st.y1).toEqual([[5], [-2]])
    expect(st.totals).toEqual([3])
    expect(st.min).toBe(-2)
    expect(st.max).toBe(5)
  })
  it('treats a short or non-numeric series as zero where it has no value', () => {
    const st = stackSeries([
      { key: 'a', label: 'A', values: [1, 2, 3] },
      { key: 'b', label: 'B', values: [1, NaN] },
    ])
    expect(st.totals).toEqual([2, 2, 3])
  })
  it('is empty for no series', () => {
    expect(stackSeries([])).toEqual({ y0: [], y1: [], totals: [], max: 0, min: 0 })
  })
  it('honours an explicit bucket count', () => {
    const st = stackSeries([{ key: 'a', label: 'A', values: [1, 2, 3] }], 2)
    expect(st.totals).toEqual([1, 2])
  })
})

function g(key: string, total: number, values: number[], extra: Partial<CostGroup> = {}): CostGroup {
  return { key, label: key.toUpperCase(), total, previous: 0, delta_pct: null, share: 0, resources: 1, values, ...extra }
}

describe('topN', () => {
  it('keeps the n largest and folds the rest into other', () => {
    const out = topN([g('a', 10, [5, 5], { previous: 5, share: 0.5 }), g('b', 6, [3, 3], { previous: 3, share: 0.3 }), g('c', 4, [2, 2], { previous: 1, share: 0.2 })], 1)
    expect(out.map((x) => x.key)).toEqual(['a', 'other'])
    const other = out[1]
    expect(other.label).toBe('Other')
    expect(other.total).toBe(10)
    expect(other.previous).toBe(4)
    expect(other.delta_pct).toBeCloseTo(150)
    expect(other.share).toBeCloseTo(0.5)
    expect(other.resources).toBe(2)
    expect(other.values).toEqual([5, 5])
  })
  it('merges an other the API already sent', () => {
    const out = topN([g('a', 10, [10]), g('b', 1, [1]), g('other', 3, [3], { label: 'Other', resources: 4 })], 1)
    expect(out.map((x) => x.key)).toEqual(['a', 'other'])
    expect(out[1].total).toBe(4)
    expect(out[1].values).toEqual([4])
    expect(out[1].resources).toBe(5)
  })
  it('returns the input untouched when nothing needs folding', () => {
    const groups = [g('a', 10, [10]), g('b', 1, [1]), g('other', 3, [3])]
    expect(topN(groups, 5)).toBe(groups)
  })
  it('ranks by total regardless of input order', () => {
    const out = topN([g('small', 1, [1]), g('big', 9, [9]), g('mid', 5, [5])], 2)
    expect(out.map((x) => x.key)).toEqual(['big', 'mid', 'other'])
  })
  it('leaves delta_pct null when the folded previous is zero', () => {
    const out = topN([g('a', 10, [10]), g('b', 1, [1])], 1)
    expect(out[1].delta_pct).toBeNull()
  })
  it('n = 0 folds everything', () => {
    const out = topN([g('a', 10, [10]), g('b', 1, [1])], 0)
    expect(out.map((x) => x.key)).toEqual(['other'])
    expect(out[0].total).toBe(11)
  })
})

describe('palette', () => {
  it('has twelve slots and assigns by key order, skipping other', () => {
    expect(PALETTE).toHaveLength(12)
    expect(colorFor(0)).toBe(PALETTE[0])
    expect(colorFor(12)).toBe(PALETTE[0])
    expect(colorForKey('evs', ['ecs', 'evs'])).toBe(PALETTE[1])
    expect(colorForKey('evs', ['ecs', 'other', 'evs'])).toBe(PALETTE[1])
    expect(colorForKey('other', ['ecs', 'other'])).toBe(OTHER_COLOR)
    expect(colorForKey('nope', ['ecs'])).toBe(OTHER_COLOR)
  })
  // A filter that removes 'ecs' must not repaint 'evs'.
  it('colour follows the entity when the caller keeps the key order', () => {
    const keys = ['ecs', 'evs', 'obs']
    expect(colorForKey('obs', keys)).toBe(colorForKey('obs', keys.filter((k) => k !== 'nothing')))
  })
})

describe('seriesFromExplore on the Go-generated fixture', () => {
  const d = seriesFromExplore(fixture)
  it('maps groups + other to three series in palette order', () => {
    expect(d.series.map((s) => s.key)).toEqual(['ecs', 'evs', 'other'])
    expect(d.series.map((s) => s.label)).toEqual(['Elastic Cloud Server', 'Block storage (EVS)', 'Other'])
    expect(d.series.map((s) => s.color)).toEqual([PALETTE[0], PALETTE[1], OTHER_COLOR])
    expect(d.series[0].values).toEqual(fixture.groups[0].values)
    expect(d.series[2].values).toEqual(fixture.other?.values)
  })
  it('keeps the seven observed buckets and their totals', () => {
    expect(d.buckets.slice(0, 7)).toEqual(fixture.buckets)
    expect(d.totals).toEqual(fixture.totals_by_bucket)
    expect(d.missing).toEqual([false, false, false, false, false, false, false])
    expect(d.currency).toBe('OMR')
    expect(d.granularity).toBe('day')
  })
  it('extends the buckets to month end with a run-rate tail that reconciles with month_end', () => {
    expect(d.forecast).toBeDefined()
    expect(d.forecast!.fromIndex).toBe(7)
    expect(d.forecast!.values).toHaveLength(23)
    expect(d.buckets).toHaveLength(30)
    expect(d.buckets[7]).toBe('2026-09-08')
    expect(d.buckets[29]).toBe('2026-09-30')
    for (const v of d.forecast!.values) expect(v).toBeCloseTo(fixture.forecast!.run_rate_daily, 12)
    const tail = d.forecast!.values.reduce((a, b) => a + b, 0)
    expect(fixture.total.current + tail).toBeCloseTo(fixture.forecast!.month_end, 6)
  })
  it('drops other when it is null or empty', () => {
    expect(seriesFromExplore({ ...fixture, other: null }).series.map((s) => s.key)).toEqual(['ecs', 'evs'])
    const zero = { ...fixture.other!, total: 0, values: [0, 0, 0, 0, 0, 0, 0] }
    expect(seriesFromExplore({ ...fixture, other: zero }).series).toHaveLength(2)
  })
  it('has no tail without a forecast or at month grain', () => {
    const none = seriesFromExplore({ ...fixture, forecast: null })
    expect(none.forecast).toBeUndefined()
    expect(none.buckets).toHaveLength(7)
    const monthly = seriesFromExplore({ ...fixture, granularity: 'month', buckets: ['2026-08', '2026-09'] })
    expect(monthly.forecast).toBeUndefined()
    expect(monthly.buckets).toEqual(['2026-08', '2026-09'])
  })
  it('marks bucket_has_data=false as missing rather than zero', () => {
    const has = [...fixture.bucket_has_data]
    has[2] = false
    const m = seriesFromExplore({ ...fixture, bucket_has_data: has })
    expect(m.missing[2]).toBe(true)
    expect(m.missing.filter(Boolean)).toHaveLength(1)
  })
  it('does not draw a thirteenth colour: twelve groups plus other stay distinct', () => {
    const groups = Array.from({ length: 12 }, (_, i) => g(`k${i}`, 12 - i, [1]))
    const many = seriesFromExplore({ ...fixture, groups, other: g('other', 1, [1]) })
    const colours = new Set(many.series.map((s) => s.color))
    expect(colours.size).toBe(13)
  })
})

describe('forecastTail', () => {
  const fc = { ...fixture.forecast!, run_rate_daily: 2 }
  it('is null at month end, for non-day buckets, or without a forecast', () => {
    expect(forecastTail('2026-09-30', fc)).toBeNull()
    expect(forecastTail('2026-09', fc)).toBeNull()
    expect(forecastTail('2026-09-07', null)).toBeNull()
  })
  it('walks to the end of the month, leap years included', () => {
    expect(forecastTail('2028-02-27', fc)).toEqual({ buckets: ['2028-02-28', '2028-02-29'], values: [2, 2] })
    expect(forecastTail('2026-12-30', fc)).toEqual({ buckets: ['2026-12-31'], values: [2] })
  })
})

describe('seriesFromDaily', () => {
  it('shapes summary daily rows into one series with gaps and a tail', () => {
    const d = seriesFromDaily(
      [
        { day: '2026-09-28', cost: 1, has_data: true },
        { day: '2026-09-29', cost: 0, has_data: false },
      ],
      { ...fixture.forecast!, run_rate_daily: 3 },
      'OMR',
    )
    expect(d.series).toHaveLength(1)
    expect(d.series[0].values).toEqual([1, 0])
    expect(d.missing).toEqual([false, true])
    expect(d.buckets).toEqual(['2026-09-28', '2026-09-29', '2026-09-30'])
    expect(d.forecast).toEqual({ fromIndex: 2, values: [3] })
  })
})
