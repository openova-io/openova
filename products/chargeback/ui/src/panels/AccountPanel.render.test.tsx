import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { AccountDocument, Customer } from '../api/types'

/**
 * The Account tab rendered (DESIGN.md §9). The arithmetic lives in
 * lib/account.test.ts; this asserts the PIXELS: the four figures with the
 * right word under the balance, the ledger with its accessible name and a
 * running balance per line, the credit notes and suspensions, the money-in
 * actions a gateway customer gets, and the empty states.
 */

const account: AccountDocument = {
  customer_id: 'c1',
  currency: 'OMR',
  balance: '-200.000000',
  available_credit: '200.000000',
  outstanding: '0.000000',
  overdue: '0.000000',
  open_invoices: 0,
  account_owner: 'internal',
  entries: [
    { id: 1, customer_id: 'c1', kind: 'invoice', amount: '1000.000000', currency: 'OMR', statement_id: 'st1', invoice_number: 'INV-2026-00001', balance: '1000.000000', entered_at: '2026-08-01T00:00:00Z' },
    { id: 2, customer_id: 'c1', kind: 'payment', amount: '-600.000000', currency: 'OMR', payment_id: 7, reference: 'TRF-1', balance: '400.000000', entered_at: '2026-08-10T00:00:00Z' },
    { id: 3, customer_id: 'c1', kind: 'credit_note', amount: '-400.000000', currency: 'OMR', credit_note_id: 'n1', credit_note_number: 'CN-2026-00001', statement_id: 'st1', balance: '0.000000', entered_at: '2026-08-20T00:00:00Z' },
    { id: 4, customer_id: 'c1', kind: 'top_up', amount: '-200.000000', currency: 'OMR', payment_id: 8, reference: 'TRF-2', balance: '-200.000000', entered_at: '2026-09-01T00:00:00Z' },
  ],
  payments: [
    { id: 7, amount: 600, paid_at: '2026-08-10T00:00:00Z', status: 'received', method: 'transfer', reference: 'TRF-1', allocated: 600, unallocated: 0, allocations: [{ id: 1, statement_id: 'st1', invoice_number: 'INV-2026-00001', amount: 600, allocated_at: '2026-08-10T00:00:00Z' }] },
    { id: 8, amount: 200, paid_at: '2026-09-01T00:00:00Z', status: 'received', method: 'transfer', reference: 'TRF-2', allocated: 0, unallocated: 200, purpose: 'checkout' },
  ],
  credit_notes: [{ id: 'n1', customer_id: 'c1', statement_id: 'st1', invoice_number: 'INV-2026-00001', number: 'CN-2026-00001', kind: 'partial', reason: 'outage credit', currency: 'OMR', subtotal: 380.952381, tax_rate: 0.05, tax: 19.047619, total: 400, applied: 400, unapplied: 0, issued_at: '2026-08-20T00:00:00Z', issued_by: 'ops@sovereign.example' }],
  suspension: null,
  payment_model: 'prepaid',
  auto_apply_credit: false,
  suspend_at_zero: true,
}

const empty: AccountDocument = { ...account, balance: '0', available_credit: '0', outstanding: '0', overdue: '0', entries: [], payments: [], credit_notes: [], payment_model: 'postpaid', suspend_at_zero: false }

const docs: Record<string, unknown> = {
  '/customers/c1/account': account,
  '/customers/c1/suspensions': { suspensions: [{ id: 1, customer_id: 'c1', action: 'suspend', source: 'wallet', reason: 'balance reached zero', ok: true, actor: 'wallet', at: '2026-07-01T00:00:00Z' }, { id: 2, customer_id: 'c1', action: 'resume', source: 'operator', ok: false, error: 'organization not found', actor: 'ops@sovereign.example', at: '2026-07-02T00:00:00Z' }] },
  '/customers/c1/payment-intents': { intents: [{ id: 'pi1', customer_id: 'c1', purpose: 'checkout', amount: 200, currency: 'OMR', gateway: 'stripe', status: 'awaiting-transfer', reference: 'pi_123', pay_url: 'https://pay.example/pi_123', created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z' }] },
  '/customers/c2/account': empty,
  '/customers/c2/suspensions': { suspensions: [] },
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path ? (docs[path] ?? null) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

import { AccountPanel } from './AccountPanel'

const customer = (over: Partial<Customer>): Customer => ({ id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'ops@acme.example', billing_mode: 'chargeback', charging: 'billed', payment_model: 'prepaid', payment_method: 'transfer', status: 'active', ...over })

function render(c: Customer): string {
  const html = renderToString(createElement(MemoryRouter, null, createElement(AccountPanel, { customerId: c.id, customer: c, currency: 'OMR' }))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('AccountPanel renders the account', () => {
  it('shows the four figures, with the balance worded as credit', () => {
    const html = render(customer({}))
    expect(html).toContain('>Balance<')
    expect(html).toContain('200.000 OMR')
    expect(html).toContain('in credit')
    expect(html).toContain('Credit available')
    expect(html).toContain('>Owed<')
    expect(html).toContain('0 open invoices')
    expect(html).toContain('>Overdue<')
  })
  it('the ledger has its accessible name, every kind, the references and a running balance', () => {
    const html = render(customer({}))
    expect(html).toContain('aria-label="Account ledger"')
    expect(html).toContain('>Invoice<')
    expect(html).toContain('>Payment<')
    expect(html).toContain('>Credit note<')
    expect(html).toContain('>Top-up<')
    expect(html).toContain('INV-2026-00001')
    expect(html).toContain('CN-2026-00001')
    expect(html).toContain('TRF-2')
    // Where the payment went, under its ledger line.
    expect(html).toContain('600.000 OMR to INV-2026-00001')
    expect(html).toContain('200.000 OMR on account')
    // The running balance after the first invoice, and the credit at the end.
    expect(html).toContain('1,000.000 OMR')
    expect(html).toContain('owes')
  })
  it('lists the credit notes and the suspensions with the platform refusal', () => {
    const html = render(customer({}))
    expect(html).toContain('aria-label="Credit notes"')
    expect(html).toContain('outage credit')
    expect(html).toContain('400.000 OMR applied to the invoice')
    expect(html).toContain('aria-label="Suspensions"')
    expect(html).toContain('suspended · wallet')
    expect(html).toContain('balance reached zero')
    expect(html).toContain('resumed · operator')
    expect(html).toContain('the platform refused: organization not found')
  })
  it('offers Top up and Apply credit; the prepaid wallet line says it suspends at zero', () => {
    const html = render(customer({}))
    expect(html).toContain('>Top up<')
    expect(html).toContain('Apply credit')
    expect(html).toContain('suspends at zero')
    // Nothing to apply credit to: the button is there but disabled, and says why.
    expect(html).toMatch(/disabled=""[^>]*title="No open invoice to apply it to"|title="No open invoice to apply it to"[^>]*disabled=""/)
    expect(html).not.toContain('Checkout with')
  })
  it('a gateway customer gets a checkout button and its intents', () => {
    const html = render(customer({ payment_method: 'gateway', gateway_name: 'stripe' }))
    expect(html).toContain('Checkout with Stripe')
    expect(html).toContain('aria-label="Payment intents"')
    expect(html).toContain('awaiting transfer')
    expect(html).toContain('pi_123')
    expect(html).toContain('payment page')
  })
  it('states each absence with a sentence', () => {
    const html = render(customer({ id: 'c2', payment_model: 'postpaid' }))
    expect(html).toContain('No account activity yet')
    expect(html).toContain('The first issued invoice or top-up opens the ledger.')
    expect(html).toContain('No credit notes')
    expect(html).toContain('Never suspended at the platform')
    expect(html).toContain('>settled<')
    expect(html).not.toContain('suspends at zero')
  })
})
