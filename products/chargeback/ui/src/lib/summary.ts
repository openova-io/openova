import type { Summary } from '../api/types'

/**
 * Readers over the summary document (#6867). Every page KPI goes through
 * here, and `summary.test.ts` runs these readers over the Go-generated
 * fixture ui/src/api/fixtures/summary.json — so a key renamed on either side
 * fails a test instead of rendering a zero, which is what happened on hw307.
 */

export interface OverviewKPIs {
  currency: string
  mtd: number
  mtdDays: number
  forecastMonthEnd: number | null
  forecastConfidence: string | null
  forecastMethod: string | null
  lastMonth: number
  lastMonthPeriod: string
  prevMTD: number
  momDeltaPct: number | null
  avgDaily: number
  resourcesLive: number
  unpricedCount: number
  customersActive: number
  customersPending: number
  customersSuspended: number
  sourcesVerified: number
  sourcesFailed: number
  sourcesPending: number
  lastCollectedAt: string | null
  draftStatements: number
  issuedStatements: number
}

const n = (v: unknown): number => {
  const x = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(x) ? x : 0
}

export function readKPIs(s: Summary): OverviewKPIs {
  return {
    currency: s.currency || '',
    mtd: n(s.mtd?.cost),
    mtdDays: n(s.mtd?.days),
    forecastMonthEnd: s.forecast ? n(s.forecast.month_end) : null,
    forecastConfidence: s.forecast?.confidence ?? null,
    forecastMethod: s.forecast?.method ?? null,
    lastMonth: n(s.last_month?.cost),
    lastMonthPeriod: s.last_month?.period ?? '',
    prevMTD: n(s.prev_mtd?.cost),
    momDeltaPct: s.mom_delta_pct === null || s.mom_delta_pct === undefined ? null : n(s.mom_delta_pct),
    avgDaily: n(s.avg_daily_30d),
    resourcesLive: n(s.resources_live),
    unpricedCount: Array.isArray(s.unpriced_skus) ? s.unpriced_skus.length : 0,
    customersActive: n(s.customers?.active),
    customersPending: n(s.customers?.pending),
    customersSuspended: n(s.customers?.suspended),
    sourcesVerified: n(s.sources?.verified),
    sourcesFailed: n(s.sources?.failed),
    sourcesPending: n(s.sources?.pending),
    lastCollectedAt: s.last_collected_at ?? null,
    draftStatements: n(s.statements?.draft),
    issuedStatements: n(s.statements?.issued),
  }
}

/** The 30-day daily series as chart points (has_data=false days are kept, flagged). */
export function readDaily(s: Summary): { buckets: string[]; values: number[]; missing: boolean[] } {
  const daily = Array.isArray(s.daily) ? s.daily : []
  return {
    buckets: daily.map((d) => d.day),
    values: daily.map((d) => n(d.cost)),
    missing: daily.map((d) => !d.has_data),
  }
}

export function readGroups(rows: Summary['by_kind'] | Summary['by_customer'] | undefined) {
  return (Array.isArray(rows) ? rows : []).map((g) => ({
    key: g.key,
    label: g.label || g.name || g.key,
    value: n(g.cost),
    previous: n(g.previous),
    delta_pct: g.delta_pct === null || g.delta_pct === undefined ? null : n(g.delta_pct),
    share: n(g.share),
    resources: n(g.resources),
    id: g.id,
  }))
}
