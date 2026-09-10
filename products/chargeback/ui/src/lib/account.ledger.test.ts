import { describe, expect, it } from 'vitest'
import type { AccountEntry, AgingReport, Statement, Suspension } from '../api/types'
import { acceptsCreditNote, agingKPIs, bucketLabel, creditRoom, directoryBalance, entryReference, ledgerRows, oldestDueText, suspensionOutcome, suspensionText } from './account'

const entry = (over: Partial<AccountEntry>): AccountEntry => ({ id: 1, customer_id: 'c', kind: 'invoice', amount: 0, currency: 'OMR', balance: '', entered_at: '2026-01-01T00:00:00Z', ...over })

describe('the ledger with a running balance', () => {
  // Posted out of order on purpose: the running balance follows posting
  // time, and the table shows newest first.
  const entries = [
    entry({ id: 3, kind: 'credit_note', amount: '-100.000000', entered_at: '2026-08-20T00:00:00Z', credit_note_number: 'CN-2026-00001' }),
    entry({ id: 1, kind: 'invoice', amount: 1000, entered_at: '2026-08-01T00:00:00Z', invoice_number: 'INV-2026-00001', statement_id: 'st1' }),
    entry({ id: 2, kind: 'payment', amount: -600, entered_at: '2026-08-10T00:00:00Z', reference: 'TRF-1', payment_id: 7 }),
    entry({ id: 4, kind: 'top_up', amount: -500, entered_at: '2026-09-01T00:00:00Z', reference: 'TRF-2', payment_id: 8 }),
  ]
  it('computes the running balance in posting order and lists newest first', () => {
    const rows = ledgerRows(entries)
    expect(rows.map((r) => r.entry.id)).toEqual([4, 3, 2, 1])
    expect(rows.map((r) => r.running)).toEqual([-200, 300, 400, 1000])
    expect(rows[3]).toMatchObject({ debit: 1000, credit: null })
    expect(rows[2]).toMatchObject({ debit: null, credit: 600 })
  })
  it('prefers the balance the server sent for a line, and carries on from it', () => {
    const rows = ledgerRows([entry({ id: 1, amount: 1000, entered_at: '2026-08-01T00:00:00Z', balance: '1000.000000' }), entry({ id: 2, amount: -600, entered_at: '2026-08-02T00:00:00Z', balance: '250.000000' }), entry({ id: 3, amount: -50, entered_at: '2026-08-03T00:00:00Z' })])
    // The server said 250 after the payment (a write-off the list did not carry); the next line continues from 250.
    expect(rows.map((r) => r.running)).toEqual([200, 250, 1000])
  })
  it('is empty for an empty ledger', () => {
    expect(ledgerRows([])).toEqual([])
  })
  it('names what a line points at', () => {
    expect(entryReference(entries[1])).toBe('INV-2026-00001')
    expect(entryReference(entries[0])).toBe('CN-2026-00001')
    expect(entryReference(entries[2])).toBe('TRF-1')
    expect(entryReference(entry({}))).toBe('')
  })
})

describe('aging labels and figures', () => {
  it('labels every bucket and passes an unknown one through', () => {
    expect(['current', '1-30', '31-60', '61-90', 'over-90'].map(bucketLabel)).toEqual(['Current', '1–30 days', '31–60 days', '61–90 days', 'Over 90'])
    expect(bucketLabel('later')).toBe('later')
  })
  it('says how late the oldest invoice is', () => {
    expect(oldestDueText(0)).toBe('nothing overdue')
    expect(oldestDueText(1)).toBe('1 day overdue')
    expect(oldestDueText(45)).toBe('45 days overdue')
  })
  it('reads the strip figures from the report, counting only customers with something overdue', () => {
    const rep: AgingReport = {
      as_of: '2026-09-10T00:00:00Z',
      buckets: ['current', '1-30'],
      rows: [
        { customer_id: 'a', customer_name: 'A', customer_slug: 'a', currency: 'OMR', buckets: { current: '100.000000' }, total: '100.000000', overdue: '0', oldest_days: 0, invoices: 1, suspended: false, available_credit: '0' },
        { customer_id: 'b', customer_name: 'B', customer_slug: 'b', currency: 'OMR', buckets: { '1-30': '250.500000' }, total: '250.500000', overdue: '250.500000', oldest_days: 12, invoices: 2, suspended: true, available_credit: '10' },
      ],
      totals: {},
      total: '350.500000',
      overdue: '250.500000',
      invoices: [
        { statement_id: 's1', customer_id: 'a', customer_name: 'A', currency: 'OMR', due_at: '2026-09-20T00:00:00Z', days_past_due: -10, bucket: 'current', outstanding: '100', status: 'sent' },
        { statement_id: 's2', customer_id: 'b', customer_name: 'B', currency: 'OMR', due_at: '2026-08-29T00:00:00Z', days_past_due: 12, bucket: '1-30', outstanding: '250.5', status: 'sent' },
        { statement_id: 's3', customer_id: 'b', customer_name: 'B', currency: 'OMR', due_at: '2026-09-10T00:00:00Z', days_past_due: 0, bucket: 'current', outstanding: '0', status: 'sent' },
      ],
      collections_owner: 'internal',
    }
    expect(agingKPIs(rep)).toEqual({ total: 350.5, overdue: 250.5, customers: 2, customersOverdue: 1, invoices: 3, currency: 'OMR' })
    expect(agingKPIs(null)).toEqual({ total: 0, overdue: 0, customers: 0, customersOverdue: 0, invoices: 0, currency: '' })
  })
})

describe('the directory balance', () => {
  it('is null when the document did not carry one, never 0', () => {
    expect(directoryBalance(undefined)).toBeNull()
    expect(directoryBalance('')).toBeNull()
    expect(directoryBalance('abc')).toBeNull()
  })
  it('keeps the accounting sign: positive owed, negative in credit', () => {
    expect(directoryBalance(250)).toBe(250)
    expect(directoryBalance('-100.500000')).toBe(-100.5)
    expect(directoryBalance(0)).toBe(0)
  })
})

describe('suspensions', () => {
  it('describes the platform suspension the customer is under', () => {
    expect(suspensionText({ suspended_at: '2026-09-01T10:00:00Z', source: 'collections', reason: '45 days overdue' })).toBe('suspended at the platform by collections since 2026-09-01 — 45 days overdue')
    expect(suspensionText({ suspended_at: '', source: '' })).toBe('suspended at the platform')
  })
  it('reads an executed suspend / resume and the platform refusal', () => {
    const s = (over: Partial<Suspension>): Suspension => ({ id: 1, customer_id: 'c', action: 'suspend', source: 'operator', ok: true, at: '', ...over })
    expect(suspensionOutcome(s({ reason: 'no response' }))).toEqual({ label: 'suspended · operator', ok: true, detail: 'no response' })
    expect(suspensionOutcome(s({ action: 'resume', source: 'wallet' }))).toEqual({ label: 'resumed · wallet', ok: true, detail: '' })
    expect(suspensionOutcome(s({ ok: false, error: 'organization not found' })).detail).toBe('the platform refused: organization not found')
  })
})

describe('credit notes on an invoice', () => {
  const st = (over: Partial<Statement>): Statement => ({ id: 's', customer_id: 'c', period_start: '2026-08-01', period_end: '2026-08-31', currency: 'OMR', subtotal: 1000, tax_rate: 0.05, tax: 50, total: 1050, status: 'sent', ...over })
  it('room is the total less what is already credited', () => {
    expect(creditRoom(st({}))).toBe(1050)
    expect(creditRoom(st({ credited_total: '300.000000' }))).toBe(750)
    expect(creditRoom(st({ credited_total: 1050 }))).toBe(0)
  })
  it('an issued, sent, paid or overdue invoice with room accepts one; a draft, a cancelled or a fully credited one does not', () => {
    expect(acceptsCreditNote(st({ status: 'issued' }))).toBe(true)
    expect(acceptsCreditNote(st({ status: 'paid' }))).toBe(true)
    expect(acceptsCreditNote(st({ status: 'sent', effective_status: 'overdue' }))).toBe(true)
    expect(acceptsCreditNote(st({ status: 'draft' }))).toBe(false)
    expect(acceptsCreditNote(st({ status: 'cancelled' }))).toBe(false)
    expect(acceptsCreditNote(st({ status: 'sent', credited_total: 1050 }))).toBe(false)
  })
})
