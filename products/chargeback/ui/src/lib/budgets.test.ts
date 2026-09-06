import { describe, expect, it } from 'vitest'
import type { Budget, BudgetStatus } from '../api/types'
import { budgetForm, budgetTone, budgetView, readBudgetStatus, sortBudgetViews, parseEmails, parseThresholds } from './budgets'

const budget = (over: Partial<Budget>): Budget => ({
  id: 'b-1',
  name: 'September',
  customer_id: 'c-1',
  amount: 3000,
  currency: 'OMR',
  period: 'monthly',
  thresholds: [50, 80, 100],
  notify_emails: ['fin@acme.om'],
  active: true,
  ...over,
})

const status = (over: Partial<BudgetStatus>): BudgetStatus => ({
  id: 'b-1',
  name: 'September',
  customer_id: 'c-1',
  amount: 3000,
  currency: 'OMR',
  actual: 104.16,
  forecast: 2712.5,
  pct_actual: 3.47,
  pct_forecast: 90.4,
  status: 'warning',
  thresholds: [
    { pct: 50, crossed: false },
    { pct: 80, crossed: false },
    { pct: 100, crossed: false },
  ],
  ...over,
})

describe('readBudgetStatus', () => {
  it('reads numbers and numeric strings alike (the fixture shape)', () => {
    const r = readBudgetStatus(status({ actual: '104.160000' as unknown as number, amount: '3000.000000' as unknown as number }))
    expect(r.actual).toBeCloseTo(104.16, 6)
    expect(r.amount).toBe(3000)
    expect(r.pctActual).toBeCloseTo(3.47, 6)
  })
  it('derives the percentages when the server omitted them, and keeps a null forecast null', () => {
    const r = readBudgetStatus(status({ pct_actual: undefined as unknown as number, forecast: null, pct_forecast: null, actual: 1500 }))
    expect(r.pctActual).toBe(50)
    expect(r.forecast).toBeNull()
    expect(r.pctForecast).toBeNull()
  })
})

describe('budgetView / tone / sort', () => {
  it('maps status to tone and lists the crossed thresholds', () => {
    const v = budgetView(budget({}), status({ status: 'exceeded', thresholds: [{ pct: 50, crossed: true }, { pct: 80, crossed: true }, { pct: 100, crossed: false }] }))
    expect(v.tone).toBe('bad')
    expect(v.crossed).toEqual([50, 80])
    expect(budgetTone('warning')).toBe('warn')
    expect(budgetTone('ok')).toBe('ok')
  })
  it('a view without a status keeps the budget amount and records the error', () => {
    const v = budgetView(budget({ amount: '250.5' as unknown as number }), null, 'HTTP 500')
    expect(v.amount).toBe(250.5)
    expect(v.actual).toBe(0)
    expect(v.statusError).toBe('HTTP 500')
  })
  it('sorts exceeded > warning > ok, inactive last, then by name', () => {
    const rows = [
      budgetView(budget({ id: 'ok-b', name: 'B' }), status({ status: 'ok' })),
      budgetView(budget({ id: 'off', name: 'A', active: false }), status({ status: 'exceeded' })),
      budgetView(budget({ id: 'warn', name: 'C' }), status({ status: 'warning' })),
      budgetView(budget({ id: 'ok-a', name: 'A' }), status({ status: 'ok' })),
      budgetView(budget({ id: 'over', name: 'Z' }), status({ status: 'exceeded' })),
    ]
    expect(sortBudgetViews(rows).map((v) => v.budget.id)).toEqual(['over', 'warn', 'ok-a', 'ok-b', 'off'])
  })
  it('budgetForm round-trips the lists as comma text', () => {
    expect(budgetForm(budget({ thresholds: [50, 100], notify_emails: ['a@x.om', 'b@x.om'] }))).toEqual({ name: 'September', amount: '3000', currency: 'OMR', thresholds: '50, 100', notify_emails: 'a@x.om, b@x.om', active: true })
  })
})

describe('parseThresholds', () => {
  it('parses a comma list with stray spaces into ascending whole percentages', () => {
    expect(parseThresholds(' 50, 80 ,100 ')).toEqual({ values: [50, 80, 100] })
    expect(parseThresholds('90')).toEqual({ values: [90] })
    expect(parseThresholds('80 100 120')).toEqual({ values: [80, 100, 120] })
  })
  it('rejects an empty list', () => {
    expect(parseThresholds('')).toMatchObject({ values: [], error: expect.stringContaining('At least one') })
  })
  it('rejects non-integers and out-of-range values', () => {
    expect(parseThresholds('50,eighty').error).toContain('eighty')
    expect(parseThresholds('50.5').error).toContain('50.5')
    expect(parseThresholds('0,50').error).toContain('outside')
    expect(parseThresholds('50,-1').error).toBeTruthy()
  })
  // Descending or repeated thresholds would alert out of order or twice;
  // the evaluator records one crossing per threshold, so they must be unique.
  it('rejects descending and duplicate thresholds', () => {
    expect(parseThresholds('80,50').error).toContain('ascending')
    expect(parseThresholds('50,50').error).toContain('ascending')
  })
})

describe('parseEmails', () => {
  it('splits on commas, semicolons and whitespace, lower-cases and de-duplicates', () => {
    expect(parseEmails('Ops@Acme.example, fin@acme.example; ops@acme.example')).toEqual({ values: ['ops@acme.example', 'fin@acme.example'] })
  })
  it('accepts an empty list (no notification)', () => {
    expect(parseEmails('')).toEqual({ values: [] })
  })
  it('names the bad address', () => {
    expect(parseEmails('ok@x.example, not-an-address').error).toContain('not-an-address')
  })
})
