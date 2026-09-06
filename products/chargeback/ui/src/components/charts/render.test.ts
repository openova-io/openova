import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import explore from '../../api/fixtures/explore.json'
import type { ExploreResult } from '../../api/types'
import { Donut } from './Donut'
import { LineChart } from './LineChart'
import { ProgressBar } from './ProgressBar'
import { RankedBars } from './RankedBars'
import { Sparkline } from './Sparkline'
import { StackedBars } from './StackedBars'
import { Waterfall } from './Waterfall'
import { seriesFromExplore } from './stack'

// Server-rendered structure of every chart: no DOM, no browser. These pin the
// render contract (marks drawn, hatches, accessible names, the empty state)
// — the interactive layer (tooltip, hover) is walked on /dev/charts.
const data = seriesFromExplore(explore as ExploreResult)
const count = (html: string, re: RegExp) => (html.match(re) ?? []).length

describe('StackedBars render', () => {
  const html = renderToStaticMarkup(createElement(StackedBars, { ...data, title: 'Cost by service', totalsLine: true }))
  it('draws one segment per non-zero value, forecast bars hatched, and a legend', () => {
    // 7 buckets × 3 series
    expect(count(html, /class="chart-mark"[^>]*fill="#/g)).toBe(21)
    // 23 forecast buckets hatched + dashed
    expect(count(html, /fill="url\(#[^"]*-fc\)"/g)).toBe(23)
    expect(html).toContain('stroke-dasharray="3 2"')
    expect(html).toContain('>Forecast<')
    expect(html).toContain('>Other<')
    expect(html).toContain('aria-label="Cost by service: 7 buckets from 1 Sep to 7 Sep, 3 series, total 104.16, forecast to 30 Sep."')
  })
  it('never draws a missing bucket as zero: hatched column, no segments', () => {
    const missing = data.missing.slice()
    missing[3] = true
    const h = renderToStaticMarkup(createElement(StackedBars, { ...data, missing, forecast: undefined, buckets: data.buckets.slice(0, 7) }))
    expect(count(h, /fill="url\(#[^"]*-nd\)"/g)).toBe(1)
    expect(count(h, /class="chart-mark"[^>]*fill="#/g)).toBe(18)
    expect(h).toContain('>No data<')
  })
  it('hides the legend for a lone series and shows it with two', () => {
    const one = renderToStaticMarkup(createElement(StackedBars, { buckets: ['2026-08', '2026-09'], series: [{ key: 'a', label: 'A', values: [1, 2] }] }))
    expect(one).not.toContain('chart-legend')
    const two = renderToStaticMarkup(
      createElement(StackedBars, { buckets: ['2026-08'], series: [{ key: 'a', label: 'A', values: [1] }, { key: 'b', label: 'B', values: [1] }] }),
    )
    expect(two).toContain('chart-legend')
  })
  it('renders the empty state, not an axis, when there is nothing to draw', () => {
    for (const props of [
      { buckets: [], series: [] },
      { buckets: ['2026-09-01'], series: [{ key: 'a', label: 'A', values: [0] }], missing: [true] },
    ]) {
      const h = renderToStaticMarkup(createElement(StackedBars, props))
      expect(h).toContain('No data in this window.')
      expect(h).not.toContain('<svg')
    }
  })
  it('keeps clickable segments reachable from the keyboard', () => {
    const h = renderToStaticMarkup(createElement(StackedBars, { ...data, onBarClick: () => {} }))
    expect(count(h, /tabindex="0"/g)).toBe(21)
    expect(count(h, /role="button"/g)).toBe(21)
  })
})

describe('LineChart render', () => {
  it('draws a line per series, a total line and a dashed forecast', () => {
    const h = renderToStaticMarkup(createElement(LineChart, { ...data, title: 'Daily cost' }))
    expect(count(h, /stroke-width="2"[^>]*stroke-linejoin="round"/g)).toBeGreaterThanOrEqual(3)
    expect(h).toContain('stroke-dasharray="5 4"')
    expect(h).toContain('<title>Total</title>')
    expect(h).toContain('>Forecast (total)<')
    expect(h).toContain('aria-label="Daily cost: 7 buckets from 1 Sep to 7 Sep, 3 series, forecast to 30 Sep."')
  })
  it('breaks the line at a missing bucket instead of dipping to zero', () => {
    const h = renderToStaticMarkup(
      createElement(LineChart, { buckets: ['2026-09-01', '2026-09-02', '2026-09-03'], series: [{ key: 'a', label: 'A', values: [1, 0, 1] }], missing: [false, true, false] }),
    )
    // Two M commands: two runs.
    const d = /<path d="([^"]*)" fill="none" stroke="#2a78d6"/.exec(h)?.[1] ?? ''
    expect(count(d, /M/g)).toBe(2)
    expect(h).toContain('No data (gap)')
  })
  it('draws the expected band for a single series only', () => {
    const band = { min: [1, 1, 1], max: [3, 3, 3] }
    const one = renderToStaticMarkup(createElement(LineChart, { buckets: ['a', 'b', 'c'], series: [{ key: 'x', label: 'X', values: [2, 2, 2] }], band }))
    expect(one).toContain('fill-opacity="0.12"')
    expect(one).toContain('Expected range')
    const two = renderToStaticMarkup(
      createElement(LineChart, { buckets: ['a', 'b', 'c'], series: [{ key: 'x', label: 'X', values: [2, 2, 2] }, { key: 'y', label: 'Y', values: [1, 1, 1] }], band }),
    )
    expect(two).not.toContain('Expected range')
  })
  it('renders the empty state for no data', () => {
    expect(renderToStaticMarkup(createElement(LineChart, { buckets: [], series: [] }))).toContain('No data in this window.')
  })
})

describe('Donut render', () => {
  it('draws arcs with a surface gap, the total in the centre, and every slice in the legend', () => {
    const h = renderToStaticMarkup(
      createElement(Donut, {
        slices: [
          { key: 'a', label: 'A', value: 60 },
          { key: 'b', label: 'B', value: 39.5 },
          { key: 'c', label: 'C', value: 0.5 },
        ],
        caption: 'Total',
      }),
    )
    expect(count(h, /<path class="chart-mark"/g)).toBe(3)
    expect(count(h, /stroke="#ffffff" stroke-width="2"/g)).toBe(3)
    expect(h).toContain('>100<')
    expect(count(h, /role="listitem"/g)).toBe(3)
    expect(h).toContain('item folded')
    expect(h).toContain('0.5 %')
  })
  it('falls back to the compact figure when the full one will not fit the hole', () => {
    const h = renderToStaticMarkup(createElement(Donut, { slices: [{ key: 'a', label: 'A', value: 104.16 }], size: 140, thickness: 18, format: (v) => `${v.toFixed(3)} OMR`, compactFormat: (v) => `${Math.round(v)} OMR` }))
    expect(h).toContain('>104 OMR<')
  })
  it('renders the empty state when everything is zero', () => {
    expect(renderToStaticMarkup(createElement(Donut, { slices: [{ key: 'a', label: 'A', value: 0 }] }))).toContain('No data in this window.')
  })
})

describe('RankedBars render', () => {
  const rows = [
    { key: 'a', label: 'A', value: 100, share: 0.8, delta_pct: 12.4 },
    { key: 'b', label: 'B', value: 25, share: 0.2, delta_pct: null },
  ]
  it('scales bars to the max and colours deltas by direction', () => {
    const h = renderToStaticMarkup(createElement(RankedBars, { rows }))
    expect(h).toContain('width="100"')
    expect(h).toContain('width="25"')
    expect(h).toContain('delta bad')
    expect(h).toContain('▲ +12.4 %')
    expect(h).toContain('80.0 %')
    expect(h).toContain('>—<')
    expect(h).not.toContain('<button')
  })
  it('keeps a fixed max comparable and makes rows buttons when clickable', () => {
    const h = renderToStaticMarkup(createElement(RankedBars, { rows, max: 400, onClick: () => {}, upIsGood: true }))
    expect(h).toContain('width="25"')
    expect(h).toContain('width="6.25"')
    expect(count(h, /<button/g)).toBe(2)
    expect(h).toContain('delta good')
  })
  it('renders the empty state for no rows', () => {
    expect(renderToStaticMarkup(createElement(RankedBars, { rows: [] }))).toContain('No data in this window.')
  })
})

describe('Sparkline render', () => {
  it('renders nothing for no values and a dot on the last point otherwise', () => {
    expect(renderToStaticMarkup(createElement(Sparkline, { values: [] }))).toBe('')
    expect(renderToStaticMarkup(createElement(Sparkline, { values: [NaN] }))).toBe('')
    const h = renderToStaticMarkup(createElement(Sparkline, { values: [1, 2, 3], label: 'x' }))
    expect(h).toContain('<circle')
    expect(h).toContain('aria-label="x: trend 1 to 3, peak 3"')
  })
})

describe('Waterfall render', () => {
  it('draws a bar per step, connectors between them and a value on every cap', () => {
    const h = renderToStaticMarkup(
      createElement(Waterfall, {
        steps: [
          { label: 'List', value: 100, kind: 'total' },
          { label: 'Discounts', value: -10, kind: 'delta' },
          { label: 'Net', value: 90, kind: 'total' },
        ],
      }),
    )
    expect(count(h, /<rect class="chart-mark"/g)).toBe(3)
    expect(count(h, /stroke="#94a3b8" stroke-width="1"/g)).toBe(2)
    expect(h).toContain('>100<')
    expect(h).toContain('>−10<')
    expect(h).toContain('fill="#15803d"')
  })
})

describe('ProgressBar render', () => {
  it('extends the scale for an overrun and keeps the budget line visible', () => {
    const h = renderToStaticMarkup(createElement(ProgressBar, { value: 120, max: 100, thresholds: [80], label: 'X' }))
    expect(h).toContain('120 %')
    expect(h).toContain('fill="#b91c1c"')
    expect(h).toContain('stroke="#0f172a" stroke-width="1.5"')
    expect(h).toContain('over budget')
  })
  it('draws markers and thresholds', () => {
    const h = renderToStaticMarkup(createElement(ProgressBar, { value: 50, max: 100, thresholds: [50, 80], markers: [{ label: 'Forecast', value: 90 }] }))
    expect(h).toContain('>80 %<')
    expect(h).toContain('Forecast 90')
    expect(h).toContain('fill="#b45309"')
  })
  it('refuses to draw a budget without an amount', () => {
    expect(renderToStaticMarkup(createElement(ProgressBar, { value: 5, max: 0 }))).toContain('No budget amount.')
  })
})
