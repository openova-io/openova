import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The statement view under DESIGN.md §16: the Download the customer takes
 * away, the Dispute it raises on its own invoice, the banner the dispute
 * puts on it, and the Resolve control an operator with billing.collect gets.
 */

const base = {
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
  paid_total: '0',
  credited_total: '0',
  balance: '1050.000',
  lines: [{ id: 7, sku: 'evs.ssd.gb', unit: 'gb-hour', quantity: '744', unit_price: '1.344086', amount: '1000.000', resource_count: 1, source_id: 'src-a' }],
}

const openDisputeDoc = {
  id: 'd1',
  statement_id: 'st1',
  customer_id: 'c1',
  invoice_number: 'INV-2026-00001',
  reason: 'the storage line is not ours',
  amount: 250,
  currency: 'OMR',
  status: 'open',
  opened_by: 'ap@acme.example',
  opened_at: '2026-09-05T00:00:00Z',
}

let statement: Record<string, unknown> = base
let disputes: unknown[] = []

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = path === '/statements/st1' ? statement : path === '/statements/st1/disputes' ? { disputes } : path === '/customers/c1/sources' ? { sources: [] } : null
    return { data: doc, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

const CUSTOMER = 'c1'
const ownerMe = {
  email: 'ap@acme.example',
  role: 'customer-admin',
  customer_id: CUSTOMER,
  permissions: { [`customer:${CUSTOMER}`]: ['metering.read', 'account.topup', 'customer.self.manage'] },
  roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: CUSTOMER }],
  scopes: [`customer:${CUSTOMER}`],
}
const viewerMe = {
  email: 'v@acme.example',
  role: 'customer-viewer',
  customer_id: CUSTOMER,
  permissions: { [`customer:${CUSTOMER}`]: ['metering.read'] },
  roles: [{ role: 'customer-viewer', scope_kind: 'customer', customer_id: CUSTOMER }],
  scopes: [`customer:${CUSTOMER}`],
}
const operatorMe = { email: 'ops@nc.example', role: 'operator' }

let me: unknown = ownerMe
vi.mock('../auth/session', () => ({
  useSession: () => ({ me, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { StatementView } from './StatementView'

function render(): string {
  return renderToString(
    createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) }))),
  ).replace(/<!-- -->/g, '')
}

describe('the invoice a customer takes away (§16)', () => {
  it('offers the PDF download to the customer that owns it', () => {
    me = ownerMe
    statement = base
    disputes = []
    const html = render()
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('href="/api/v1/statements/st1.pdf"')
    expect(html).toContain('Download PDF')
  })
})

describe('disputing an invoice (§16)', () => {
  it('offers Dispute to an owner and nothing to a viewer', () => {
    me = ownerMe
    statement = base
    disputes = []
    expect(render()).toContain('>Dispute<')

    me = viewerMe
    expect(render()).not.toContain('>Dispute<')
    // The viewer can still take the document away.
    expect(render()).toContain('Download PDF')
  })

  it('does not offer a dispute on a draft, a cancelled invoice, or one already disputed', () => {
    me = ownerMe
    disputes = []
    statement = { ...base, status: 'draft', invoice_number: '', issued_at: null }
    expect(render()).not.toContain('>Dispute<')
    statement = { ...base, status: 'cancelled', cancelled_at: '2026-09-06T00:00:00Z', cancel_reason: 'voided' }
    expect(render()).not.toContain('>Dispute<')
    statement = { ...base, disputed_at: '2026-09-05T00:00:00Z', dispute_reason: 'the storage line is not ours' }
    disputes = [openDisputeDoc]
    expect(render()).not.toContain('>Dispute<')
  })

  it('shows the dispute with its reason, its amount and what it means for collections', () => {
    me = ownerMe
    statement = { ...base, disputed_at: '2026-09-05T00:00:00Z', dispute_reason: 'the storage line is not ours' }
    disputes = [openDisputeDoc]
    const html = render()
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Disputed')
    expect(html).toContain('the storage line is not ours')
    expect(html).toContain('250.000 OMR stays on the balance and is not chased')
    // A customer never gets the operator's control.
    expect(html).not.toContain('Resolve dispute')
  })

  it('gives an operator the Resolve control while one is open, and not otherwise', () => {
    me = operatorMe
    statement = { ...base, disputed_at: '2026-09-05T00:00:00Z', dispute_reason: 'the storage line is not ours' }
    disputes = [openDisputeDoc]
    expect(render()).toContain('Resolve dispute')

    // Resolved: the control goes and the outcome is stated.
    statement = base
    disputes = [{ ...openDisputeDoc, status: 'upheld', resolved_at: '2026-09-07T00:00:00Z', credit_note_id: 'n1' }]
    const html = render()
    expect(html).not.toContain('Resolve dispute')
    expect(html).toContain('upheld')
    expect(html).toContain('a credit note was issued for the disputed amount')
  })
})
