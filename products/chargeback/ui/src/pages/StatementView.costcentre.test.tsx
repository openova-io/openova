import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * What an invoice says about COST CENTRES under DESIGN.md §19: the same
 * invoice read by a second dimension, whose net, tax and total columns add up
 * to the invoice itself. The unassigned bucket is a visible row, never a
 * silence, and the page says which of the two the reader is looking at.
 */

// 1,000 list, 100 off, 900 net, 45 tax, 945 total — split 5 : 3 : 2 across
// two named centres and the bucket nothing named.
const base = {
  id: 'st1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  customer_slug: 'acme',
  period_start: '2026-09-01',
  period_end: '2026-09-30',
  currency: 'OMR',
  subtotal: '900.000',
  discount_total: '100.000',
  tax_rate: '0.05',
  tax: '45.000',
  total: '945.000',
  status: 'issued',
  effective_status: 'issued',
  invoice_number: 'INV-2026-00007',
  payment_terms_days: 30,
  due_at: '2099-10-01T00:00:00Z',
  issued_at: '2026-10-01T00:00:00Z',
  created_at: '2026-10-01T00:00:00Z',
  paid_total: '0',
  credited_total: '0',
  balance: '945.000',
  lines: [{ id: 7, sku: 'k8s.vcpu', unit: 'vcpu-hour', quantity: '744', unit_price: '1.344086', amount: '1000.000', resource_count: 3, source_id: 'src-a' }],
  cost_centre_lines: [
    { code: 'ENG', name: 'Engineering', usage: '500.000000', list: '500.000000', discount: '50.000000', net: '450.000000', tax: '22.500000', total: '472.500000' },
    { code: 'RES', name: 'Research', usage: '300.000000', list: '300.000000', discount: '30.000000', net: '270.000000', tax: '13.500000', total: '283.500000' },
    { code: '(unassigned)', name: 'Unassigned', usage: '200.000000', list: '200.000000', discount: '20.000000', net: '180.000000', tax: '9.000000', total: '189.000000' },
  ],
}

let statement: Record<string, unknown> = base

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = path === '/statements/st1' ? statement : path === '/statements/st1/disputes' ? { disputes: [] } : path === '/customers/c1/sources' ? { sources: [] } : null
    return { data: doc, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

const operatorMe = { email: 'ops@nc.example', role: 'operator' }
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: operatorMe, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { StatementView } from './StatementView'

function render(): string {
  const html = renderToString(
    createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) }))),
  ).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the cost-centre breakdown on an invoice (§19)', () => {
  it('shows one row per centre with its share and every money column', () => {
    statement = base
    const html = render()
    expect(html).toContain('By cost centre')
    expect(html).toContain('ENG')
    expect(html).toContain('Engineering')
    expect(html).toContain('RES')
    expect(html).toContain('472.500 OMR')
    expect(html).toContain('283.500 OMR')
    expect(html).toContain('189.000 OMR')
    // Each row's share of the invoice, as a percentage.
    expect(html).toContain('50 %')
    expect(html).toContain('30 %')
    expect(html).toContain('20 %')
  })

  it('shows the UNASSIGNED bucket as a row, and says nothing named it', () => {
    statement = base
    const html = render()
    expect(html).toContain('(unassigned)')
    expect(html).toContain('no rule and no override named it')
  })

  it('states that the rows add up to the invoice, and that a centre is not a charge', () => {
    statement = base
    const html = render()
    expect(html).toContain('These rows add up to the invoice exactly')
    expect(html).toContain('900.000 OMR')
    expect(html).toContain('45.000 OMR')
    expect(html).toContain('945.000 OMR')
    expect(html).toContain('it is not a charge')
    expect(html).not.toContain('the breakdown and the invoice disagree')
  })

  it('says so when the rows do NOT add up — the defect is shown, not rounded away', () => {
    statement = {
      ...base,
      cost_centre_lines: [{ code: 'ENG', name: 'Engineering', usage: '500.000000', list: '500.000000', discount: '50.000000', net: '450.000000', tax: '22.500000', total: '472.500000' }],
    }
    const html = render()
    expect(html).toContain('the breakdown and the invoice disagree')
    expect(html).not.toContain('These rows add up to the invoice exactly')
  })

  it('renders nothing at all for a statement rated before cost centres existed', () => {
    statement = { ...base, cost_centre_lines: undefined }
    const html = render()
    expect(html).not.toContain('By cost centre')
    // And the rest of the invoice is untouched.
    expect(html).toContain('INV-2026-00007')
    expect(html).toContain('945.000 OMR')
  })
})
