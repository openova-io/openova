import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The statement view for an ISSUED invoice under DESIGN.md §9: the tax
 * line frozen at issue, the credit notes it carries, the payments applied
 * from the account, and the Credit note action an operator gets.
 */

const statement = {
  id: 'st1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  customer_slug: 'acme',
  period_start: '2026-08-01',
  period_end: '2026-08-31',
  currency: 'OMR',
  subtotal: '1000.000',
  discount_total: '0',
  tax_rate: '0.05',
  tax: '50.000',
  total: '1050.000',
  status: 'sent',
  effective_status: 'sent',
  invoice_number: 'INV-2026-00001',
  payment_terms_days: 30,
  due_at: '2099-10-01T00:00:00Z',
  issued_at: '2026-09-01T00:00:00Z',
  sent_at: '2026-09-01T00:00:00Z',
  created_at: '2026-09-01T00:00:00Z',
  paid_total: '600.000',
  credited_total: '100.000',
  balance: '350.000',
  tax_snapshot: { rate: '0.05', exempt: false, customer_name: 'ACME LLC', customer_tax_registration_number: 'OM555', seller_legal_name: 'Sovereign LLC', seller_tax_registration_number: 'OM100' },
  credit_notes: [{ id: 'n1', customer_id: 'c1', statement_id: 'st1', invoice_number: 'INV-2026-00001', number: 'CN-2026-00001', kind: 'partial', reason: 'outage credit', currency: 'OMR', subtotal: '95.238', tax_rate: '0.05', tax: '4.762', total: '100.000', applied: '100.000', unapplied: '0', issued_at: '2026-09-05T00:00:00Z', issued_by: 'ops@sovereign.example' }],
  payments: [{ id: 7, amount: '600.000', paid_at: '2026-09-03T00:00:00Z', status: 'received', method: 'transfer', reference: 'TRF-1', recorded_by: 'ops@sovereign.example', allocated: '600.000', allocations: [{ id: 1, statement_id: 'st1', invoice_number: 'INV-2026-00001', amount: '600.000', allocated_at: '2026-09-03T00:00:00Z', allocated_by: 'ops@sovereign.example' }] }],
  lines: [{ sku: 'ecs.s6.large.2', unit: 'instance-hour', quantity: '744', unit_price: '1.344086', amount: '1000.000', resource_count: 1, source_id: 'src-a' }],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/statements/st1' ? statement : path === '/customers/c1/sources' ? { sources: [] } : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

vi.mock('../auth/session', () => ({
  useSession: () => ({ me: { email: 'ops@sovereign.example', role: 'operator' }, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { StatementView } from './StatementView'

describe('StatementView for an issued invoice', () => {
  it('shows the tax line, the credit notes, the allocations and the Credit note action', () => {
    const html = renderToString(createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) })))).replace(/<!-- -->/g, '')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    // The tax the invoice carries, frozen at issue.
    expect(html).toContain('>Tax<')
    expect(html).toContain('5 % on the net subtotal')
    expect(html).toContain('customer registration OM555')
    expect(html).toContain('seller Sovereign LLC OM100')
    // What credit notes took off, and the note itself.
    expect(html).toContain('>Credited<')
    expect(html).toContain('aria-label="Credit notes"')
    expect(html).toContain('CN-2026-00001')
    expect(html).toContain('outage credit')
    expect(html).toContain('100.000 OMR applied to the invoice')
    expect(html).toContain('950.000 OMR can still be credited')
    // The payment applied from the account, under the payment and in its own table.
    expect(html).toContain('600.000 OMR to INV-2026-00001')
    expect(html).toContain('aria-label="Allocations"')
    // The action an operator gets on a sent invoice.
    expect(html).toContain('>Credit note<')
    expect(html).toContain('Record payment')
  })
})
