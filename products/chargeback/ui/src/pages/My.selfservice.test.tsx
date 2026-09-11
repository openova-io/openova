import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The customer lens of DESIGN.md §16: the invoices list with a Download on
 * every row, and the payment-methods card showing brand, last four and
 * expiry — and never a token.
 */

const CUSTOMER = 'c1'

const statement = {
  id: 'st1',
  customer_id: CUSTOMER,
  customer_name: 'ACME LLC',
  period_start: '2026-08-01',
  period_end: '2026-08-31',
  currency: 'OMR',
  subtotal: '1000.000',
  discount_total: '0',
  tax_rate: '0.05',
  tax: '50.000',
  total: '1050.000',
  status: 'sent',
  invoice_number: 'INV-2026-00001',
  issued_at: '2026-09-01T00:00:00Z',
  created_at: '2026-09-01T00:00:00Z',
}

const disputedStatement = { ...statement, id: 'st2', invoice_number: 'INV-2026-00002', disputed_at: '2026-09-05T00:00:00Z', dispute_reason: 'the storage line is not ours' }

const methods = {
  payment_methods: [
    { id: 'pm1', customer_id: CUSTOMER, gateway: 'omantel', status: 'active', brand: 'visa', last4: '4242', exp_month: 11, exp_year: 2030, label: 'Company card', saved: true, created_at: '2026-09-01T00:00:00Z', confirmed_at: '2026-09-01T00:05:00Z' },
    { id: 'pm2', customer_id: CUSTOMER, gateway: 'omantel', status: 'pending', setup_url: 'https://pay.omantel.example/setup/acme', saved: false, created_at: '2026-09-02T00:00:00Z' },
    { id: 'pm3', customer_id: CUSTOMER, gateway: 'omantel', status: 'removed', brand: 'mastercard', last4: '1111', saved: false, created_at: '2026-08-01T00:00:00Z', removed_at: '2026-08-20T00:00:00Z' },
  ],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = (() => {
      switch (path) {
        case `/customers/${CUSTOMER}/payment-methods`:
          return methods
        case `/customers/${CUSTOMER}`:
          return { id: CUSTOMER, slug: 'acme', name: 'ACME LLC', admin_email: 'ap@acme.example', status: 'active', billing_mode: 'real', gateway_name: 'omantel' }
        case `/customers/${CUSTOMER}/disputes`:
          return { disputes: [{ id: 'd1', statement_id: 'st2', customer_id: CUSTOMER, reason: 'the storage line is not ours', amount: 100, status: 'open', opened_at: '2026-09-05T00:00:00Z' }] }
        case `/customers/${CUSTOMER}/cost/summary`:
          return null
        default:
          return null
      }
    })()
    return { data: doc, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

const owner = {
  email: 'ap@acme.example',
  role: 'customer-admin',
  customer_id: CUSTOMER,
  permissions: { [`customer:${CUSTOMER}`]: ['metering.read', 'account.topup', 'customer.self.manage'] },
  roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: CUSTOMER }],
  scopes: [`customer:${CUSTOMER}`],
}
const viewer = {
  email: 'v@acme.example',
  role: 'customer-viewer',
  customer_id: CUSTOMER,
  permissions: { [`customer:${CUSTOMER}`]: ['metering.read'] },
  roles: [{ role: 'customer-viewer', scope_kind: 'customer', customer_id: CUSTOMER }],
  scopes: [`customer:${CUSTOMER}`],
}

let me: unknown = owner
vi.mock('../auth/session', () => ({
  useSession: () => ({ me, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { MyPaymentMethods, MyStatements } from './My'
import { StatementTable } from '../panels/StatementsPanel'

function render(el: ReturnType<typeof createElement>): string {
  return renderToString(createElement(MemoryRouter, null, el)).replace(/<!-- -->/g, '')
}

describe('the customer’s payment methods (§16)', () => {
  it('shows brand, last four and expiry, offers Add and Remove, and never renders a token', () => {
    me = owner
    const html = render(createElement(MyPaymentMethods))
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Visa ···· 4242')
    expect(html).toContain('11 / 2030')
    expect(html).toContain('Add a payment method')
    expect(html).toContain('Remove')
    // A pending setup sends the payer to the gateway's own page.
    expect(html).toContain('https://pay.omantel.example/setup/acme')
    expect(html).toContain('Finish on the payment page')
    // A removed method is gone from the list.
    expect(html).not.toContain('1111')
    // Nothing that could charge the card is on the page.
    expect(html).not.toMatch(/token/i)
    expect(html).toContain('held by the payment provider')
  })

  it('offers a viewer nothing to change', () => {
    me = viewer
    const html = render(createElement(MyPaymentMethods))
    expect(html).toContain('Visa ···· 4242')
    expect(html).not.toContain('Add a payment method')
    expect(html).not.toContain('>Remove<')
  })
})

describe('the customer’s invoices (§16)', () => {
  it('names the open dispute at the top of the list', () => {
    me = owner
    const html = render(createElement(MyStatements))
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('One invoice is under dispute')
    expect(html).toContain('not chased')
    expect(html).toContain('Download any of them as a PDF')
  })

  it('puts a Download on every row, and marks a disputed invoice', () => {
    me = owner
    const html = render(
      createElement(StatementTable, {
        rows: [statement, disputedStatement],
        canIssue: false,
        onChanged: () => {},
      }),
    )
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('href="/api/v1/statements/st1.pdf"')
    expect(html).toContain('href="/api/v1/statements/st2.pdf"')
    expect(html).toContain('>Download<')
    expect(html).toContain('disputed')
  })
})
