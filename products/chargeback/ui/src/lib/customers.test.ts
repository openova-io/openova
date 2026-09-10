import { describe, expect, it } from 'vitest'
import type { Customer, PriceBook, Summary } from '../api/types'
import { commercialDetail, commercialLabel, customerCounts, customerPatch, fieldLabel, filterCustomers, lastStatementText, mtdByCustomer, mtdFor, planLabel, priceBookName, settingsFrom, sourceCounts, sourcesText } from './customers'

const cust = (over: Partial<Customer>): Customer => ({
  id: 'c-x',
  slug: 'x',
  name: 'X',
  admin_email: 'x@example.com',
  billing_mode: 'showback',
  status: 'active',
  ...over,
})

const rows: Customer[] = [
  cust({ id: 'c-1', slug: 'acme', name: 'Acme Trading', admin_email: 'ops@acme.om', status: 'active' }),
  cust({ id: 'c-2', slug: 'bravo', name: 'Bravo', admin_email: 'fin@bravo.om', status: 'pending' }),
  cust({ id: 'c-3', slug: 'charlie', name: 'Charlie Holdings', admin_email: 'cfo@charlie.om', status: 'suspended' }),
]

describe('filterCustomers', () => {
  it('matches name, slug and admin email case-insensitively', () => {
    expect(filterCustomers(rows, 'ACME', 'all').map((c) => c.id)).toEqual(['c-1'])
    expect(filterCustomers(rows, 'bravo', 'all').map((c) => c.id)).toEqual(['c-2'])
    expect(filterCustomers(rows, 'cfo@', 'all').map((c) => c.id)).toEqual(['c-3'])
  })
  it('combines the status filter with the search', () => {
    expect(filterCustomers(rows, '', 'pending').map((c) => c.id)).toEqual(['c-2'])
    expect(filterCustomers(rows, 'a', 'suspended').map((c) => c.id)).toEqual(['c-3'])
    expect(filterCustomers(rows, 'acme', 'suspended')).toEqual([])
  })
  it('an empty search with "all" is the identity', () => {
    expect(filterCustomers(rows, '   ', 'all')).toHaveLength(3)
  })
})

describe('customerCounts', () => {
  it('counts each status and the total', () => {
    expect(customerCounts(rows)).toEqual({ active: 1, pending: 1, suspended: 1, total: 3 })
  })
  it('an unknown status counts towards the total only', () => {
    expect(customerCounts([cust({ status: 'weird' })])).toEqual({ active: 0, pending: 0, suspended: 0, total: 1 })
  })
})

describe('sourceCounts / sourcesText', () => {
  it('reads the list-endpoint aggregates', () => {
    const c = cust({ source_count: 3, verified_source_count: 2 })
    expect(sourceCounts(c)).toEqual({ verified: 2, total: 3 })
    expect(sourcesText(c)).toBe('2/3')
  })
  it('counts embedded rows on the detail document', () => {
    const c = cust({ sources: [{ id: 'a', kind: 'huawei-project', region: 'r', project_id: 'p', status: 'verified', layer: 'cloud' }, { id: 'b', kind: 'huawei-project', region: 'r', project_id: 'q', status: 'pending', layer: 'cloud' }] })
    expect(sourcesText(c)).toBe('1/2')
  })
  it('says "—" when the document did not say, and a bare total when only that is known', () => {
    expect(sourcesText(cust({}))).toBe('—')
    expect(sourcesText(cust({ sources: 4 }))).toBe('4')
  })
})

describe('mtdByCustomer / mtdFor', () => {
  const summary = {
    by_customer: [
      { key: 'c-1', id: 'c-1', label: 'Acme', cost: 100.8, previous: 0, delta_pct: null, share: 0.9, resources: 3 },
      { key: 'c-2', id: 'c-2', label: 'Bravo', cost: '3.36', previous: 0, delta_pct: null, share: 0.1, resources: 1 },
      { key: 'other', label: 'Other', cost: 9, previous: 0, delta_pct: null, share: 0, resources: 0 },
    ],
  } as unknown as Summary
  it('keys by customer id and coerces numeric strings', () => {
    const m = mtdByCustomer(summary)
    expect(mtdFor(m, 'c-1')).toBeCloseTo(100.8, 6)
    expect(mtdFor(m, 'c-2')).toBeCloseTo(3.36, 6)
  })
  it('a customer outside the top-10 is null, never 0 — and "other" is not a customer', () => {
    const m = mtdByCustomer(summary)
    expect(mtdFor(m, 'c-3')).toBeNull()
    expect(mtdFor(m, 'other')).toBeNull()
  })
  it('tolerates a missing summary', () => {
    expect(mtdByCustomer(null).size).toBe(0)
  })
})

describe('priceBookName', () => {
  const books: PriceBook[] = [{ id: 'pb-1', name: 'Standard', scope: 'cloud', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute' }]
  it('joins by id and returns null for none / unknown', () => {
    expect(priceBookName(books, 'pb-1')).toBe('Standard')
    expect(priceBookName(books, 'pb-9')).toBeNull()
    expect(priceBookName(books, null)).toBeNull()
  })
})

describe('lastStatementText', () => {
  it('prefers the list aggregate and shows the month', () => {
    expect(lastStatementText(cust({ last_statement_period: '2026-08-01' }))).toBe('2026-08')
  })
  it('falls back to the embedded summary with its status', () => {
    expect(lastStatementText(cust({ last_statement: { period_start: '2026-07-01', status: 'issued' } }))).toBe('2026-07 (issued)')
    expect(lastStatementText(cust({}))).toBe('—')
  })
})

describe('commercialLabel / commercialDetail', () => {
  it('names the commercial position instead of a mode word (DESIGN.md §8)', () => {
    expect(commercialLabel(cust({ charging: 'informational' }))).toBe('informational')
    expect(commercialLabel(cust({ charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' }))).toBe('prepaid · Stripe')
    expect(commercialLabel(cust({ charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer' }))).toBe('postpaid · transfer')
    expect(commercialLabel(cust({ charging: 'billed', payment_model: 'postpaid', payment_method: 'internal' }))).toBe('internal recharge')
  })
  it('falls back to the derived billing_mode for a document without the new fields', () => {
    expect(commercialLabel(cust({ billing_mode: 'showback' }))).toBe('informational')
  })
  it('shows the terms under an invoiced customer only', () => {
    expect(commercialDetail(cust({ charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer', payment_terms_days: 45, po_reference: 'PO-1' }))).toBe('net 45 · PO-1')
    expect(commercialDetail(cust({ charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer', payment_terms_days: 0 }))).toBe('due on receipt')
    expect(commercialDetail(cust({ charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' }))).toBe('')
    expect(commercialDetail(cust({ charging: 'informational' }))).toBe('')
  })
})

describe('customerPatch', () => {
  const orig = cust({ name: 'Acme', admin_email: 'ops@acme.om', charging: 'informational', price_book_id: 'pb-1', start_date: '2026-01-01', status: 'active', org_slug: null })
  it('sends only the fields that changed', () => {
    const form = { ...settingsFrom(orig), name: 'Acme Trading', charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer' }
    expect(customerPatch(orig, form)).toEqual({ name: 'Acme Trading', charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer' })
  })
  it('an untouched form sends nothing', () => {
    expect(customerPatch(orig, settingsFrom(orig))).toEqual({})
  })
  it('never sends billing_mode — it is derived server-side (DESIGN.md §8)', () => {
    const form = { ...settingsFrom(orig), charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' }
    expect(Object.keys(customerPatch(orig, form))).not.toContain('billing_mode')
  })
  it('sends the payment terms as a number, so 0 means due on receipt', () => {
    const billed = cust({ charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer', payment_terms_days: 30 })
    expect(customerPatch(billed, { ...settingsFrom(billed), payment_terms_days: '0' })).toEqual({ payment_terms_days: 0 })
  })
  it('turning charging off drops the fields that then mean nothing', () => {
    const billed = cust({ charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' })
    expect(customerPatch(billed, { ...settingsFrom(billed), charging: 'informational' })).toEqual({ charging: 'informational' })
  })
  it('moving off the gateway drops the gateway name', () => {
    const billed = cust({ charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' })
    const form = { ...settingsFrom(billed), payment_method: 'transfer', gateway_name: '' }
    expect(customerPatch(billed, form)).toEqual({ payment_method: 'transfer' })
  })
  it('a cleared price book is sent as "" (clear), lowercases the email, trims', () => {
    const form = { ...settingsFrom(orig), admin_email: '  Finance@Acme.om ' }
    expect(customerPatch(orig, form)).toEqual({ admin_email: 'finance@acme.om' })
  })
})

describe('fieldLabel', () => {
  it('names the known settings fields as the form labels them', () => {
    expect(fieldLabel('price_book_id')).toBe('price book')
    expect(fieldLabel('payment_method')).toBe('payment method')
    expect(fieldLabel('po_reference')).toBe('purchase order')
    expect(fieldLabel('admin_email')).toBe('admin email')
    expect(fieldLabel('start_date')).toBe('start date')
    expect(fieldLabel('org_slug')).toBe('Organization slug')
    expect(fieldLabel('plan_slug')).toBe('plan')
    expect(fieldLabel('name')).toBe('name')
    expect(fieldLabel('status')).toBe('status')
  })
  // "saved price book_id" on hw307: a string pattern to String.replace swaps
  // only the first underscore. Every underscore of an unknown key must go.
  it('drops every underscore of a key it does not know', () => {
    expect(fieldLabel('some_new_field')).toBe('some new field')
    expect(fieldLabel('a_b_c_d')).toBe('a b c d')
  })
  it('composes the saved notice from the patch keys', () => {
    expect(['price_book_id', 'status'].map(fieldLabel).join(', ')).toBe('price book, status')
  })
  it('does not read the label table through its prototype', () => {
    expect(fieldLabel('constructor')).toBe('constructor')
    expect(fieldLabel('__proto__')).toBe('  proto  ')
  })
})

// Flexi is not a plan size: it is uncapped and billed off its meters, so the
// header must not print "FLEXI plan" as though it were a tier.
describe('planLabel', () => {
  it('names a sized plan by its size', () => {
    expect(planLabel('m')).toBe('M plan')
    expect(planLabel('XL')).toBe('XL plan')
  })
  it('says pay per use for flexi', () => {
    expect(planLabel('flexi')).toBe('pay per use')
    expect(planLabel(' FLEXI ')).toBe('pay per use')
  })
  it('says nothing for a customer with no plan', () => {
    expect(planLabel('')).toBe('')
    expect(planLabel(null)).toBe('')
    expect(planLabel(undefined)).toBe('')
  })
})
