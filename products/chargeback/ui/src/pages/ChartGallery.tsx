import { useState } from 'react'
import explore from '../api/fixtures/explore.json'
import type { ExploreResult } from '../api/types'
import {
  Donut,
  EmptyChart,
  LineChart,
  ProgressBar,
  RankedBars,
  Sparkline,
  StackedBars,
  Waterfall,
  seriesFromExplore,
  type Series,
} from '../components/charts'
import { formatMoney } from '../lib/money'

/**
 * /dev/charts — the visual regression page for src/components/charts. Every
 * chart is drawn with sample data (the Go-generated explore fixture where it
 * fits) so the set can be eyeballed in a browser after any change. Not
 * linked from the navigation on purpose.
 */

const fixture = explore as ExploreResult
const fixtureData = seriesFromExplore(fixture)
const omr = (v: number) => formatMoney(v, 'OMR')
const omrCompact = (v: number) => formatMoney(v, 'OMR', { compact: true })

// A deterministic 30-day window: six services, weekday shape, one day with
// no collection, and a forecast tail. Seeded so the page looks the same on
// every load.
function lcg(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}
function thirtyDays(): ExploreResult {
  const rnd = lcg(7)
  const days: string[] = []
  for (let i = 0; i < 30; i++) days.push(new Date(Date.UTC(2026, 7, 9 + i)).toISOString().slice(0, 10))
  const kinds: [string, string, number][] = [
    ['ecs', 'Elastic Cloud Server', 48],
    ['evs', 'Block storage (EVS)', 12],
    ['obs', 'Object storage (OBS)', 7],
    ['elb', 'Load balancer (ELB)', 4],
    ['rds', 'Relational DB (RDS)', 9],
    ['nat', 'NAT gateway', 2],
  ]
  const missing = 12
  const groups = kinds.map(([key, label, base]) => {
    const values = days.map((d, i) => {
      if (i === missing) return 0
      const weekend = new Date(d).getUTCDay() % 6 === 0 ? 0.82 : 1
      const trend = 1 + i * 0.006
      return Math.round(base * weekend * trend * (0.9 + rnd() * 0.2) * 1000) / 1000
    })
    const total = values.reduce((a, b) => a + b, 0)
    return { key, label, total, previous: total * (0.8 + rnd() * 0.4), delta_pct: null, share: 0, resources: 3, values }
  })
  const totals = days.map((_, i) => groups.reduce((a, g) => a + g.values[i], 0))
  const current = totals.reduce((a, b) => a + b, 0)
  for (const g of groups) g.share = g.total / current
  return {
    from: days[0],
    to: '2026-09-08',
    granularity: 'day',
    group_by: 'kind',
    metric: 'cost',
    currency: 'OMR',
    mixed_currency: false,
    buckets: days,
    bucket_has_data: days.map((_, i) => i !== missing),
    groups,
    other: null,
    total: { current, previous: current * 0.9, delta_pct: 11.1, resources: 18 },
    totals_by_bucket: totals,
    unpriced: [],
    forecast: { month_end: 3200, run_rate_daily: 84.2, trend_daily: 0.4, method: 'run-rate-7d', days_observed: 7, days_in_month: 30, confidence: 'medium' },
  }
}
const month = seriesFromExplore(thirtyDays())

const byCustomer = [
  { key: 'c1', label: 'Sohar Aluminium', value: 1240.5 },
  { key: 'c2', label: 'Omantel Digital', value: 830.25 },
  { key: 'c3', label: 'Bank Muscat Labs', value: 410 },
  { key: 'c4', label: 'Duqm Port Authority — very long customer name to ellipsise', value: 265.75 },
  { key: 'c5', label: 'Nizwa University', value: 92 },
  { key: 'c6', label: 'Tiny tenant A', value: 9.5 },
  { key: 'c7', label: 'Tiny tenant B', value: 4.25 },
]
const total = byCustomer.reduce((a, s) => a + s.value, 0)
const ranked = byCustomer.map((c, i) => ({ key: c.key, label: c.label, value: c.value, share: c.value / total, delta_pct: [12.4, -3.1, 0, null, 148, -0.02, 41][i] }))

const spark = [12, 12.4, 11.9, 13.2, 14.8, 14.1, 15.2, 14.9, 15.6]
const sparkGap = [3, 3.2, NaN, 2.9, 3.4, 3.1]
const sparkFlat = [0, 0, 0, 0, 0]

const expected: Series[] = [{ key: 'actual', label: 'Actual', values: month.series[0].values.slice(0, 30) }]
const bandMin = expected[0].values.map((v) => v * 0.85)
const bandMax = expected[0].values.map((v) => v * 1.15)
// The model still expects a value on the day nothing was collected: the band
// runs through the gap while the actual line breaks.
bandMin[12] = (bandMin[11] + bandMin[13]) / 2
bandMax[12] = (bandMax[11] + bandMax[13]) / 2
expected[0].values[22] = expected[0].values[22] * 1.6 // the anomaly

export function ChartGallery() {
  const [clicked, setClicked] = useState<string>('')
  return (
    <div className="gallery">
      <h1>Chart gallery</h1>
      <p className="muted">
        Visual regression page for <code>src/components/charts</code>. Every chart below draws sample data; hover, tab and arrow through them. Last click:{' '}
        <code>{clicked || '—'}</code>
      </p>

      <h2>StackedBars — the explore fixture (2 services + other, 7 days, forecast tail to month end)</h2>
      <div className="card">
        <StackedBars {...fixtureData} format={omr} totalsLine title="Cost by service" onBarClick={(b, k) => setClicked(`bucket ${b} (${fixtureData.buckets[b]}), series ${k}`)} />
      </div>

      <h2>StackedBars — 30 days, six services, one day without data, forecast</h2>
      <div className="card">
        <StackedBars {...month} format={omr} title="Daily cost by service" height={260} />
      </div>

      <h2>StackedBars — monthly buckets, single series (legend auto-hidden)</h2>
      <div className="card">
        <StackedBars
          buckets={['2026-03', '2026-04', '2026-05', '2026-06', '2026-07', '2026-08', '2026-09']}
          series={[{ key: 'cost', label: 'Cost', values: [2100, 2350.5, 2280, 2610.25, 2780.012, 2955, 446.4] }]}
          format={omr}
          height={180}
        />
      </div>

      <div className="grid2">
        <div>
          <h2>LineChart — area, forecast, gap</h2>
          <div className="card">
            <LineChart {...month} area format={omr} title="Daily cost" />
          </div>
        </div>
        <div>
          <h2>LineChart — expected vs actual band</h2>
          <div className="card">
            <LineChart
              buckets={month.buckets.slice(0, 30)}
              series={expected}
              band={{ min: bandMin, max: bandMax, label: 'Expected range' }}
              missing={month.missing}
              format={omr}
              title="Anomaly view"
              onPointClick={(i) => setClicked(`point ${i} (${month.buckets[i]})`)}
            />
          </div>
        </div>
      </div>

      <div className="grid2">
        <div>
          <h2>Donut — cost by customer (two slivers folded into Other)</h2>
          <div className="card">
            <Donut slices={byCustomer} format={omr} compactFormat={omrCompact} caption="MTD cost" title="Cost by customer" onSliceClick={(k) => setClicked(`slice ${k}`)} />
          </div>
        </div>
        <div>
          <h2>Donut — by service with an API "other"</h2>
          <div className="card">
            <Donut
              slices={[...fixture.groups.map((g) => ({ key: g.key, label: g.label, value: g.total })), { key: 'other', label: 'Other', value: fixture.other?.total ?? 0 }]}
              format={omr}
              compactFormat={omrCompact}
              caption="Window total"
              size={140}
              thickness={18}
            />
          </div>
        </div>
      </div>

      <h2>RankedBars — with share and delta chips (cost: up is bad)</h2>
      <div className="card">
        <RankedBars rows={ranked} format={omr} onClick={(k) => setClicked(`ranked ${k}`)} title="Cost by customer" />
      </div>
      <h2>RankedBars — fixed max for comparability, savings (up is good), no share</h2>
      <div className="card">
        <RankedBars
          rows={[
            { key: 'a', label: 'Stopped ECS still billed', value: 120, delta_pct: 30 },
            { key: 'b', label: 'Idle EVS volumes', value: 45, delta_pct: -12 },
          ]}
          max={300}
          format={omr}
          upIsGood
          showShare={false}
        />
      </div>

      <h2>Sparkline — table cells</h2>
      <div className="card">
        <table>
          <thead>
            <tr>
              <th>Resource</th>
              <th>Trend (9 days)</th>
              <th className="num">Cost</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>ecs-web-01</td>
              <td>
                <Sparkline values={spark} label="ecs-web-01" />
              </td>
              <td className="num">{omr(15.6)}</td>
            </tr>
            <tr>
              <td>evs-data-02 (a gap)</td>
              <td>
                <Sparkline values={sparkGap} label="evs-data-02" color="#eb6834" />
              </td>
              <td className="num">{omr(3.1)}</td>
            </tr>
            <tr>
              <td>flat zero (measured)</td>
              <td>
                <Sparkline values={sparkFlat} label="flat" />
              </td>
              <td className="num">{omr(0)}</td>
            </tr>
            <tr>
              <td>no data (renders nothing)</td>
              <td>
                <Sparkline values={[]} label="empty" />
              </td>
              <td className="num">—</td>
            </tr>
          </tbody>
        </table>
      </div>

      <h2>Waterfall — statement: list → discounts → net → tax → total</h2>
      <div className="card">
        <Waterfall
          steps={[
            { label: 'List', value: 2780.012, kind: 'total' },
            { label: 'Discounts', value: -278.001, kind: 'delta' },
            { label: 'Net', value: 2502.011, kind: 'total' },
            { label: 'Tax 5 %', value: 125.101, kind: 'delta' },
            { label: 'Total', value: 2627.112, kind: 'total' },
          ]}
          format={omr}
          title="Statement 2026-08"
        />
      </div>

      <h2>ProgressBar — budgets</h2>
      <div className="card stack">
        <ProgressBar label="Platform · September" value={1860} max={3000} thresholds={[50, 80]} markers={[{ label: 'Forecast', value: 2750 }]} format={omr} />
        <ProgressBar label="Sohar Aluminium (threshold crossed)" value={1240.5} max={1400} thresholds={[50, 80]} markers={[{ label: 'Forecast', value: 1510 }]} format={omr} />
        <ProgressBar label="Omantel Digital (over budget)" value={830.25} max={700} thresholds={[80]} format={omr} />
        <ProgressBar label="No thresholds, no markers" value={92} max={500} format={omr} />
      </div>

      <h2>Empty states</h2>
      <div className="grid2">
        <div className="card">
          <StackedBars buckets={[]} series={[]} />
        </div>
        <div className="card">
          <LineChart buckets={['2026-09-01', '2026-09-02']} series={[{ key: 'a', label: 'A', values: [0, 0] }]} missing={[true, true]} />
        </div>
        <div className="card">
          <Donut slices={[{ key: 'a', label: 'A', value: 0 }]} />
        </div>
        <div className="card">
          <EmptyChart message="No cost sources verified yet." hint="Add a source under Customers → Sources." />
        </div>
      </div>
    </div>
  )
}
