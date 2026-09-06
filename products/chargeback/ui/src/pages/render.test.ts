import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

// Every configure/bill page rendered once with representative documents in
// place of the API (useQuery is replaced; effects do not run under
// renderToString, so the per-row follow-up fetches show their pending
// state). Catches the failures a typecheck cannot: a wire field read the
// wrong way, a helper fed the wrong shape, "NaN" or "undefined" in the HTML.

vi.mock('../lib/useQuery', () => {
  const customers = {
    customers: [
      { id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'ops@acme.example', price_book_id: 'pb1', billing_mode: 'chargeback', status: 'active' },
      { id: 'c2', slug: 'globex', name: 'Globex', admin_email: 'fin@globex.example', price_book_id: null, billing_mode: 'showback', status: 'active' },
    ],
  }
  const book = {
    id: 'pb1',
    name: 'Standard 2026',
    currency: 'OMR',
    annual_divisor: 8760,
    bill_stopped: 'compute',
    effective_from: '2026-01-01',
    created_at: '2026-01-01T00:00:00Z',
    items: [
      { sku: 'ecs.s6.large.2', unit: 'instance-hour', unit_price: '0.10000000', annual_price: '876.000', description: 'ECS s6.large.2' },
      { sku: 'evs.ssd.gb', unit: 'gb-hour', unit_price: 0.00013699, description: 'EVS SSD' },
    ],
  }
  const coverage = {
    customers: [{ id: 'c1', name: 'ACME LLC', slug: 'acme' }],
    skus_in_use: [
      { sku: 'ecs.s6.large.2', unit: 'instance-hour', quantity_30d: 720, resources: 1, priced: true, unit_price: 0.1 },
      { sku: 'eip', unit: 'hour', quantity_30d: 720, resources: 2, priced: false, unit_price: null },
    ],
    coverage_pct: 50,
    unpriced_count: 1,
  }
  const discounts = {
    discounts: [
      { id: 'd1', customer_id: null, name: 'Launch campaign', kind: 'percent', value: 15, sku: '', starts_at: '2099-01-01T00:00:00Z', ends_at: null, active: true },
      { id: 'd2', customer_id: 'c1', customer_name: 'ACME LLC', name: 'Volume tier', kind: 'fixed', value: 500, sku: 'ecs.s6.large.2', starts_at: null, ends_at: null, active: false },
    ],
  }
  const budgets = {
    budgets: [{ id: 'b1', name: 'Compute ceiling', customer_id: 'c1', amount: 1000, currency: 'OMR', period: 'monthly', thresholds: [50, 80, 100], notify_emails: ['fin@acme.example'], active: true }],
  }
  const settings = { weights: { vcpu: 1, mem_gib: 0.5, pvc_gb: 0.1 }, overhead_policy: 'separate', pool: 'sovereign-cost', manual_amount: 0, currency: 'OMR', sovereign_customer_id: 'c1', updated_at: '2026-09-01T00:00:00Z' }
  const allocation = {
    from: '2026-09-01',
    to: '2026-09-08',
    settings,
    pool: { source: 'sovereign-cost', amount: 1000, currency: 'OMR', customer_id: 'c1', customer_name: 'ACME LLC' },
    rows: [
      { customer_id: 'c2', customer_slug: 'globex', customer_name: 'Globex', tier: 'organization', vcpu_hours: 100, mem_gib_hours: 200, pvc_gb_hours: 300, weight: 230, share: 0.7, allocated_cost: 700, rated_revenue: 900, margin: 200, margin_pct: 22.2 },
      { customer_id: '', customer_slug: 'platform', customer_name: 'Platform overhead', tier: 'platform-overhead', vcpu_hours: 40, mem_gib_hours: 80, pvc_gb_hours: 100, weight: 90, share: 0.3, allocated_cost: 300, rated_revenue: 0, margin: -300, margin_pct: null },
    ],
    share_total: 1,
    totals: { allocated: 1000, revenue: 900, margin: -100 },
  }
  const statement = {
    id: 'st1',
    customer_id: 'c1',
    customer_name: 'ACME LLC',
    customer_slug: 'acme',
    period_start: '2026-08-01',
    period_end: '2026-08-31',
    currency: 'OMR',
    subtotal: '1000.000',
    discount_total: '150.000',
    discount_detail: [{ id: 'd1', name: 'Launch campaign', kind: 'percent', value: 15, sku: '', amount: '150.000' }],
    tax_rate: '0.05',
    tax: '42.500',
    total: '892.500',
    status: 'draft',
    issued_at: null,
    created_at: '2026-09-01T00:00:00Z',
    lines: [
      { sku: 'ecs.s6.large.2', unit: 'instance-hour', quantity: '744', unit_price: '0.10000000', amount: '744.000', resource_count: 1, source_id: 'src-a' },
      { sku: 'evs.ssd.gb', unit: 'gb-hour', quantity: 74400, unit_price: 0.00013699, amount: 256, resource_count: 3, source_id: 'src-b' },
    ],
  }
  const explore = {
    from: '2026-09-01',
    to: '2026-09-08',
    granularity: 'month',
    group_by: 'sku',
    metric: 'cost',
    currency: 'OMR',
    mixed_currency: false,
    buckets: ['2026-09'],
    bucket_has_data: [true],
    groups: [{ key: 'ecs.s6.large.2', label: 'ecs.s6.large.2', total: 400, previous: 380, delta_pct: 5.2, share: 0.8, resources: 1, values: [400] }],
    other: null,
    total: { current: 500, previous: 470, delta_pct: 6.4, resources: 4 },
    totals_by_bucket: [500],
    unpriced: [],
    forecast: null,
  }
  const docFor = (path: string): unknown => {
    if (path === '/customers') return customers
    if (path === '/pricebooks') return { pricebooks: [book] }
    if (path === '/pricebooks/pb1') return book
    if (path === '/pricebooks/pb1/coverage') return coverage
    if (path === '/discounts') return discounts
    if (path === '/budgets') return budgets
    if (path === '/allocation/settings') return settings
    if (path.startsWith('/allocation?')) return allocation
    if (path.startsWith('/statements?')) return { statements: [statement, { ...statement, id: 'st2', customer_id: 'c2', customer_name: 'Globex', customer_slug: 'globex', status: 'issued', issued_at: '2026-09-02T10:00:00Z', discount_total: 0, discount_detail: null }] }
    if (path === '/statements/st1') return statement
    if (path === '/customers/c1/sources') return { sources: [{ id: 'src1', customer_id: 'c1', kind: 'huawei-project', region: 'me-east-215', project_id: 'proj-1', status: 'verified' }] }
    if (path.includes('/cost/explore?')) return explore
    throw new Error(`no fixture for ${path}`)
  }
  return {
    useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
  }
})

import { Allocation } from './Allocation'
import { Budgets } from './Budgets'
import { Discounts } from './Discounts'
import { PriceBookEdit } from './PriceBookEdit'
import { PriceBooks } from './PriceBooks'
import { Statements } from './Statements'
import { StatementView } from './StatementView'

function render(Page: ComponentType, path: string, url = path): string {
  // renderToString separates adjacent text nodes with <!-- -->; the reader never sees them.
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  // No page may leak a JavaScript artefact into what the operator reads.
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('configure + bill pages render their documents', () => {
  it('Price books: list with coverage, customers without a book, actions', () => {
    const html = render(PriceBooks, '/pricebooks')
    expect(html).toContain('Standard 2026')
    expect(html).toContain('Customers without a book')
    expect(html).toContain('billed as running')
    expect(html).toContain('Clone')
    expect(html).toContain('Delete')
  })
  it('Price book detail: settings line, coverage with an unpriced SKU, editable items', () => {
    const html = render(PriceBookEdit, '/pricebooks/:id', '/pricebooks/pb1')
    expect(html).toContain('annual ÷ 8,760 h')
    expect(html).toContain('unpriced')
    expect(html).toContain('Add rate')
    expect(html).toContain('ecs.s6.large.2')
    expect(html).toContain('value="876"')
    expect(html).toContain('value="0.1"')
    expect(html).toContain('Export CSV')
  })
  it('Discounts: scope, derived status, whole-bill vs SKU scope', () => {
    const html = render(Discounts, '/discounts')
    expect(html).toContain('Launch campaign')
    expect(html).toContain('All customers')
    expect(html).toContain('scheduled')
    expect(html).toContain('inactive')
    expect(html).toContain('whole bill')
    expect(html).toContain('15 %')
  })
  it('Budgets: strip row with scope, amount and thresholds', () => {
    const html = render(Budgets, '/budgets')
    expect(html).toContain('Compute ceiling')
    expect(html).toContain('ACME LLC')
    expect(html).toContain('thresholds 50 % · 80 % · 100 %')
    expect(html).toContain('New budget')
  })
  it('Allocation: settings form, pool + margin KPIs, result rows, explainer', () => {
    const html = render(Allocation, '/allocation')
    expect(html).toContain('Globex')
    expect(html).toContain('Platform overhead')
    expect(html).toContain('rated cloud cost of ACME LLC')
    expect(html).toContain('How this is computed')
    expect(html).toContain('this window:')
    expect(html).toContain('Save and recalculate')
  })
  it('Statements: period label, both statuses, draft-only actions', () => {
    const html = render(Statements, '/statements', '/statements?period=2026-08')
    expect(html).toContain('Aug 2026')
    expect(html).toContain('ACME LLC')
    expect(html).toContain('Globex')
    expect(html).toContain('Run period')
    expect(html).toContain('Issue')
    expect(html).toContain('−150.000 OMR')
  })
  it('Statement view: waterfall totals, discount detail, service groups, per-source breakdown', () => {
    const html = render(StatementView, '/statements/:id', '/statements/st1')
    expect(html).toContain('Aug 2026')
    expect(html).toContain('Elastic Cloud Server')
    expect(html).toContain('Block storage (EVS)')
    expect(html).toContain('Net subtotal')
    expect(html).toContain('850.000 OMR')
    expect(html).toContain('Tax 5 %')
    expect(html).toContain('892.500 OMR')
    expect(html).toContain('Launch campaign')
    expect(html).toContain('By cost source')
    expect(html).toContain('src-a')
  })
})
