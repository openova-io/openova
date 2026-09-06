import { describe, expect, it } from 'vitest'
import { OTHER_COLOR, PALETTE } from './palette'
import { arcPath, donutLayout } from './Donut'
import { progressState } from './ProgressBar'
import { waterfallLayout } from './Waterfall'

describe('waterfallLayout', () => {
  const steps = [
    { label: 'List', value: 100, kind: 'total' as const },
    { label: 'Discounts', value: -10, kind: 'delta' as const },
    { label: 'Net', value: 90, kind: 'total' as const },
    { label: 'Tax', value: 4.5, kind: 'delta' as const },
    { label: 'Total', value: 94.5, kind: 'total' as const },
  ]
  it('runs totals through list → discount → net → tax → total', () => {
    const { bars, min, max } = waterfallLayout(steps)
    expect(bars.map((b) => [b.start, b.end])).toEqual([
      [0, 100],
      [100, 90],
      [0, 90],
      [90, 94.5],
      [0, 94.5],
    ])
    expect(bars[1].y0).toBe(90)
    expect(bars[1].y1).toBe(100)
    expect(bars[3].y0).toBe(90)
    expect(bars[3].y1).toBe(94.5)
    expect(min).toBe(0)
    expect(max).toBe(100)
    for (const b of bars) expect(b.y0).toBeLessThanOrEqual(b.y1)
  })
  it('lets a delta take the running total below zero', () => {
    const { bars, min } = waterfallLayout([
      { label: 'a', value: 10, kind: 'total' },
      { label: 'b', value: -15, kind: 'delta' },
    ])
    expect(bars[1].end).toBe(-5)
    expect(bars[1].y0).toBe(-5)
    expect(min).toBe(-5)
  })
  it('treats a non-number as zero and is empty for no steps', () => {
    expect(waterfallLayout([{ label: 'x', value: NaN, kind: 'delta' }]).bars[0].end).toBe(0)
    expect(waterfallLayout([])).toEqual({ bars: [], min: 0, max: 0 })
  })
})

describe('donutLayout', () => {
  it('draws big slices, folds slivers into Other, and keeps every slice in the legend', () => {
    const l = donutLayout([
      { key: 'a', label: 'A', value: 50 },
      { key: 'b', label: 'B', value: 30 },
      { key: 'c', label: 'C', value: 19.5 },
      { key: 'd', label: 'D', value: 0.5 },
    ])
    expect(l.total).toBe(100)
    expect(l.arcs.map((a) => a.key)).toEqual(['a', 'b', 'c', 'other'])
    expect(l.arcs[3].folded).toEqual(['d'])
    expect(l.arcs[3].color).toBe(OTHER_COLOR)
    expect(l.arcs[0].color).toBe(PALETTE[0])
    expect(l.legend).toHaveLength(4)
    expect(l.legend[3]).toMatchObject({ key: 'd', share: 0.005, folded: true, color: OTHER_COLOR })
    expect(l.legend[0].folded).toBe(false)
  })
  it('lays arcs end to end around the full circle', () => {
    const l = donutLayout([
      { key: 'a', label: 'A', value: 1 },
      { key: 'b', label: 'B', value: 3 },
    ])
    expect(l.arcs[0].start).toBeCloseTo(-Math.PI / 2)
    expect(l.arcs[1].start).toBeCloseTo(l.arcs[0].end)
    expect(l.arcs[1].end).toBeCloseTo(-Math.PI / 2 + 2 * Math.PI)
    expect(l.arcs[0].share).toBeCloseTo(0.25)
  })
  it('merges an API other with the folded slivers', () => {
    const l = donutLayout([
      { key: 'a', label: 'A', value: 50 },
      { key: 'other', label: 'Other', value: 10 },
      { key: 'd', label: 'D', value: 0.5 },
    ])
    expect(l.arcs.map((a) => a.key)).toEqual(['a', 'other'])
    expect(l.arcs[1].value).toBe(10.5)
    expect(l.arcs[1].folded).toEqual(['other', 'd'])
    expect(l.legend.find((r) => r.key === 'other')?.folded).toBe(false)
  })
  it('lists zero and negative slices without drawing them; all-zero draws nothing', () => {
    const l = donutLayout([
      { key: 'a', label: 'A', value: 5 },
      { key: 'z', label: 'Z', value: 0 },
      { key: 'n', label: 'N', value: -3 },
    ])
    expect(l.arcs.map((a) => a.key)).toEqual(['a'])
    expect(l.legend).toHaveLength(3)
    expect(donutLayout([{ key: 'z', label: 'Z', value: 0 }]).arcs).toEqual([])
  })
})

describe('arcPath', () => {
  it('uses the large-arc flag past 180° and splits a full ring in two', () => {
    expect(arcPath(50, 50, 40, 20, 0, Math.PI / 2)).toContain('A40 40 0 0 1')
    expect(arcPath(50, 50, 40, 20, 0, 1.5 * Math.PI)).toContain('A40 40 0 1 1')
    expect(arcPath(50, 50, 40, 20, 0, 2 * Math.PI).split('M')).toHaveLength(3)
  })
})

describe('progressState', () => {
  it('is ok below every threshold, warn past one, bad at or over 100 %', () => {
    expect(progressState(50, [80])).toBe('ok')
    expect(progressState(85, [80])).toBe('warn')
    expect(progressState(99.9, [50, 80])).toBe('warn')
    expect(progressState(100, [80])).toBe('bad')
    expect(progressState(120, [])).toBe('bad')
    expect(progressState(10, [])).toBe('ok')
    expect(progressState(NaN, [80])).toBe('ok')
  })
})
