/**
 * Pure data shaping for the charts — no React, tested in stack.test.ts.
 *
 * seriesFromExplore() is the one function pages call: it maps the wire
 * document of GET /cost/explore (groups + other + buckets + forecast) onto
 * the props StackedBars and LineChart take.
 */
import type { CostGroup, ExploreResult, Forecast, Granularity } from '../../api/types'
import { OTHER_COLOR, OTHER_KEY, colorForKey } from './palette'

export interface Series {
  key: string
  label: string
  /** One value per bucket; a shorter array leaves the trailing buckets undrawn. */
  values: number[]
  color?: string
}

export interface Stacked {
  /** y0[s][b] is where series s starts in bucket b (the cumulative sum below it). */
  y0: number[][]
  /** y1[s][b] is where series s ends in bucket b. */
  y1: number[][]
  /** Net total per bucket. */
  totals: number[]
  /** Highest stacked top (≥ 0). */
  max: number
  /** Lowest stacked bottom (≤ 0); negative values stack downward from zero. */
  min: number
}

function num(v: unknown): number {
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : 0
}

/** stackSeries computes per-bucket cumulative offsets and totals. */
export function stackSeries(series: Series[], bucketCount?: number): Stacked {
  const n = bucketCount ?? Math.max(0, ...series.map((s) => s.values.length))
  const y0: number[][] = []
  const y1: number[][] = []
  const totals = new Array<number>(n).fill(0)
  const up = new Array<number>(n).fill(0)
  const down = new Array<number>(n).fill(0)
  let max = 0
  let min = 0
  for (const s of series) {
    const a0: number[] = []
    const a1: number[] = []
    for (let b = 0; b < n; b++) {
      const v = num(s.values[b])
      if (v >= 0) {
        a0.push(up[b])
        up[b] += v
        a1.push(up[b])
        if (up[b] > max) max = up[b]
      } else {
        a0.push(down[b])
        down[b] += v
        a1.push(down[b])
        if (down[b] < min) min = down[b]
      }
      totals[b] += v
    }
    y0.push(a0)
    y1.push(a1)
  }
  return { y0, y1, totals, max, min }
}

function emptyLike(label: string, n: number): CostGroup {
  return { key: OTHER_KEY, label, total: 0, previous: 0, delta_pct: null, share: 0, resources: 0, values: new Array<number>(n).fill(0) }
}

function fold(into: CostGroup, g: CostGroup): CostGroup {
  const n = Math.max(into.values.length, g.values.length)
  const values: number[] = []
  for (let i = 0; i < n; i++) values.push(num(into.values[i]) + num(g.values[i]))
  const total = into.total + g.total
  const previous = into.previous + g.previous
  return {
    ...into,
    total,
    previous,
    delta_pct: previous > 0 ? ((total - previous) / previous) * 100 : null,
    share: into.share + g.share,
    resources: into.resources + g.resources,
    values,
  }
}

/**
 * topN keeps the n largest groups (by total) and folds the rest into one
 * "other" row, merging with an "other" the API already sent. It returns the
 * input untouched when nothing needs folding, so colours stay put.
 */
export function topN(groups: CostGroup[], n: number): CostGroup[] {
  const own = groups.filter((g) => g.key !== OTHER_KEY)
  const others = groups.filter((g) => g.key === OTHER_KEY)
  const keep = Math.max(0, Math.floor(n))
  if (own.length <= keep && others.length <= 1) return groups
  const sorted = own
    .map((g, i) => ({ g, i }))
    .sort((a, b) => b.g.total - a.g.total || a.i - b.i)
    .map((x) => x.g)
  const head = sorted.slice(0, keep)
  const tail = sorted.slice(keep)
  const width = Math.max(0, ...groups.map((g) => g.values.length))
  let other = emptyLike(others[0]?.label ?? 'Other', width)
  for (const g of [...others, ...tail]) other = fold(other, g)
  if (other.total === 0 && other.values.every((v) => v === 0) && other.resources === 0) return head
  return [...head, other]
}

export interface ForecastTail {
  /** Index of the first forecast bucket; buckets before it are observed. */
  fromIndex: number
  /** One projected value per forecast bucket (fromIndex + i). */
  values: number[]
}

export interface ChartData {
  buckets: string[]
  series: Series[]
  /** True where the API recorded no measurement for the bucket (bucket_has_data=false). */
  missing: boolean[]
  /** Observed totals per bucket, as the API summed them. */
  totals: number[]
  forecast?: ForecastTail
  currency: string
  granularity: Granularity
}

function isoDay(d: Date): string {
  return d.toISOString().slice(0, 10)
}

/**
 * forecastTail projects the rest of the month after `lastBucket` at the
 * API's run rate. The API's month_end is observed + run_rate_daily × the days
 * from today to month end, so the tail is drawn one bucket per remaining day
 * at run_rate_daily and sums to month_end − observed whenever the window's
 * last bucket is yesterday (the MTD case). It returns null for a non-day
 * bucket, a missing forecast, or a bucket already at month end.
 */
export function forecastTail(lastBucket: string, forecast: Forecast | null | undefined): { buckets: string[]; values: number[] } | null {
  if (!forecast || !Number.isFinite(forecast.run_rate_daily)) return null
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(lastBucket)
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2])
  const d = Number(m[3])
  const monthEnd = new Date(Date.UTC(y, mo, 0)) // day 0 of next month = last day of this one
  const buckets: string[] = []
  const values: number[] = []
  for (let day = d + 1; day <= monthEnd.getUTCDate(); day++) {
    buckets.push(isoDay(new Date(Date.UTC(y, mo - 1, day))))
    values.push(forecast.run_rate_daily)
  }
  return buckets.length ? { buckets, values } : null
}

/**
 * seriesFromExplore maps an ExploreResult onto chart props. Groups keep their
 * API order (largest first) and take palette slots by that order; "other"
 * takes the neutral. A forecast, when present, extends the buckets to the
 * month end with a dashed tail.
 */
export function seriesFromExplore(result: ExploreResult): ChartData {
  const groups = result.groups ?? []
  const keys = groups.map((g) => g.key)
  const series: Series[] = groups.map((g) => ({
    key: g.key,
    label: g.label || g.key,
    values: (g.values ?? []).map(num),
    color: colorForKey(g.key, keys),
  }))
  const other = result.other
  if (other && (num(other.total) !== 0 || (other.values ?? []).some((v) => num(v) !== 0))) {
    series.push({ key: OTHER_KEY, label: other.label || 'Other', values: (other.values ?? []).map(num), color: OTHER_COLOR })
  }
  const observed = result.buckets ?? []
  const missing = observed.map((_, i) => result.bucket_has_data?.[i] === false)
  const tail = result.granularity === 'day' && observed.length ? forecastTail(observed[observed.length - 1], result.forecast) : null
  return {
    buckets: tail ? [...observed, ...tail.buckets] : observed,
    series,
    missing,
    totals: (result.totals_by_bucket ?? []).map(num),
    forecast: tail ? { fromIndex: observed.length, values: tail.values } : undefined,
    currency: result.currency,
    granularity: result.granularity,
  }
}

/**
 * seriesFromDaily shapes the Summary.daily / ResourceDetail.daily rows
 * ({day, cost, has_data}) into a single series, with the same forecast tail.
 */
export function seriesFromDaily(
  daily: Array<{ day: string; cost: number; has_data: boolean }>,
  forecast: Forecast | null | undefined,
  currency: string,
  label = 'Cost',
): ChartData {
  const rows = daily ?? []
  const buckets = rows.map((r) => r.day)
  const values = rows.map((r) => num(r.cost))
  const tail = buckets.length ? forecastTail(buckets[buckets.length - 1], forecast) : null
  return {
    buckets: tail ? [...buckets, ...tail.buckets] : buckets,
    series: [{ key: 'cost', label, values, color: colorForKey('cost', ['cost']) }],
    missing: rows.map((r) => r.has_data === false),
    totals: values,
    forecast: tail ? { fromIndex: buckets.length, values: tail.values } : undefined,
    currency,
    granularity: 'day',
  }
}
