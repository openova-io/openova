import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * A DRAFT's purchase-order reference and payment terms (DESIGN.md §8,
 * #6946): edited in a dialog opened from the Invoice block, billing.issue
 * only, and frozen — the control stays and says why — once the statement is
 * issued. The store refuses the write past draft; this pins that the console
 * never offers it as live.
 */

const draft = {
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
  status: 'draft',
  effective_status: 'draft',
  invoice_number: '',
  po_reference: 'PO-2026-0142',
  payment_terms_days: 45,
  issued_at: null,
  created_at: '2026-09-01T00:00:00Z',
  lines: [{ sku: 'ecs.s6.large.2', unit: 'instance-hour', quantity: '744', unit_price: '1.344086', amount: '1000.000', resource_count: 1, source_id: 'src-a' }],
}

let statement: Record<string, unknown> = draft

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/statements/st1' ? statement : path === '/customers/c1/sources' ? { sources: [] } : path === '/statements/st1/disputes' ? { disputes: [] } : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

const operatorMe = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'billing.issue', 'billing.collect'] }, roles: [{ role: 'billing-operator', scope_kind: 'sovereign' }] }
const financeMe = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read', 'audit.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
const ownerMe = {
  email: 'ap@acme.example',
  role: 'customer-admin',
  customer_id: 'c1',
  permissions: { 'customer:c1': ['metering.read', 'account.topup', 'customer.self.manage'] },
  roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: 'c1' }],
  scopes: ['customer:c1'],
}
let me: unknown = operatorMe
vi.mock('../auth/session', () => ({
  useSession: () => ({ me, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { StatementView } from './StatementView'

function render(): string {
  const html = renderToString(
    createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) }))),
  ).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe("a draft's purchase order and payment terms", () => {
  it('shows the Invoice block on a draft with a live Edit for billing.issue', () => {
    me = operatorMe
    statement = draft
    const html = render()
    expect(html).toContain('>Invoice<')
    expect(html).toContain('not yet numbered')
    expect(html).toContain('>Purchase order<')
    expect(html).toContain('PO-2026-0142')
    expect(html).toContain('>Payment terms<')
    expect(html).toContain('net 45')
    // Live: the button carries no disabled attribute and the working title.
    expect(html).toMatch(/<button class="small"[^>]*title="Set the purchase-order reference[^"]*"[^>]*>Edit<\/button>/)
    expect(html).not.toMatch(/<button class="small" disabled=""[^>]*>Edit<\/button>/)
    // The dialog is closed until the row opens it.
    expect(html).not.toContain('id="invoice-terms-form"')
  })

  it('keeps the control on an issued invoice, disabled, and says why in the store’s words', () => {
    me = operatorMe
    statement = { ...draft, status: 'sent', effective_status: 'sent', invoice_number: 'INV-2026-00001', issued_at: '2026-09-01T00:00:00Z', sent_at: '2026-09-01T00:00:00Z', due_at: '2099-10-01T00:00:00Z' }
    const html = render()
    expect(html).toMatch(/<button class="small" disabled=""[^>]*title="The purchase-order reference and payment terms are frozen once a statement is issued\. This one is sent\."[^>]*>Edit<\/button>/)
  })

  it('offers no Edit to a finance viewer or to the customer', () => {
    statement = draft
    me = financeMe
    expect(render()).not.toMatch(/>Edit<\/button>/)
    me = ownerMe
    const html = render()
    expect(html).not.toMatch(/>Edit<\/button>/)
    // The customer still reads the block.
    expect(html).toContain('PO-2026-0142')
    me = operatorMe
  })
})
