import type { Budget, BudgetStatus } from '../api/types'

/**
 * Budget readers (#6867). Money fields cross the wire as store.Decimal,
 * which is a bare JSON number today and was a numeric string in an earlier
 * fixture — both are read here so a ProgressBar never gets NaN.
 */

const n = (v: unknown): number => {
  const x = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(x) ? x : 0
}

export interface BudgetView {
  budget: Budget
  status: BudgetStatus | null
  /** what failed when the status could not be read */
  statusError: string
  amount: number
  actual: number
  forecast: number | null
  pctActual: number
  pctForecast: number | null
  tone: 'ok' | 'warn' | 'bad'
  crossed: number[]
}

export function readBudgetStatus(raw: BudgetStatus): { actual: number; amount: number; forecast: number | null; pctActual: number; pctForecast: number | null } {
  const amount = n(raw.amount)
  const actual = n(raw.actual)
  const forecast = raw.forecast === null || raw.forecast === undefined ? null : n(raw.forecast)
  const pctActual = raw.pct_actual === undefined || raw.pct_actual === null ? (amount > 0 ? (actual / amount) * 100 : 0) : n(raw.pct_actual)
  const pctForecast = raw.pct_forecast === null || raw.pct_forecast === undefined ? (forecast !== null && amount > 0 ? (forecast / amount) * 100 : null) : n(raw.pct_forecast)
  return { actual, amount, forecast, pctActual, pctForecast }
}

export function budgetTone(status: string | null | undefined): 'ok' | 'warn' | 'bad' {
  return status === 'exceeded' ? 'bad' : status === 'warning' ? 'warn' : 'ok'
}

export function budgetView(budget: Budget, status: BudgetStatus | null, statusError = ''): BudgetView {
  if (!status) {
    return { budget, status: null, statusError, amount: n(budget.amount), actual: 0, forecast: null, pctActual: 0, pctForecast: null, tone: 'ok', crossed: [] }
  }
  const r = readBudgetStatus(status)
  return {
    budget,
    status,
    statusError: '',
    amount: r.amount || n(budget.amount),
    actual: r.actual,
    forecast: r.forecast,
    pctActual: r.pctActual,
    pctForecast: r.pctForecast,
    tone: budgetTone(status.status),
    crossed: (Array.isArray(status.thresholds) ? status.thresholds : []).filter((t) => t.crossed).map((t) => n(t.pct)),
  }
}

/** Exceeded first, then warning, then ok; inactive last; by name within. */
export function sortBudgetViews(rows: BudgetView[]): BudgetView[] {
  const rank = (v: BudgetView) => (v.budget.active ? 0 : 10) + (v.tone === 'bad' ? 0 : v.tone === 'warn' ? 1 : 2)
  return [...rows].sort((a, b) => rank(a) - rank(b) || a.budget.name.localeCompare(b.budget.name))
}

export function budgetForm(b: Budget): { name: string; amount: string; currency: string; thresholds: string; notify_emails: string; active: boolean } {
  return {
    name: b.name,
    amount: String(n(b.amount)),
    currency: b.currency,
    thresholds: (Array.isArray(b.thresholds) ? b.thresholds : []).join(', '),
    notify_emails: (Array.isArray(b.notify_emails) ? b.notify_emails : []).join(', '),
    active: b.active,
  }
}
