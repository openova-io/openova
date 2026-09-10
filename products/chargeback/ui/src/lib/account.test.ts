import { describe, expect, it } from 'vitest'
import type { AccountEntry, AgingReport, CreditNote, Payment, Statement } from '../api/types'
import { allocationText, balanceWord, bucketFor, canAllocate, creditNoteEffect, entryColumns, entryLabel, openInvoices, overdueShare, parseReminderDays, paymentStatus, reminderScheduleText } from './account'

const money = (v: number, cur: string) => `${v.toFixed(3)} ${cur}`

describe('the ledger', () => {
  it('splits the signed amount into debit and credit columns', () => {
    const e = (amount: number | string): AccountEntry => ({ id: 1, customer_id: 'c', kind: 'invoice', amount, currency: 'OMR', balance: 0, entered_at: '2026-01-01T00:00:00Z' })
    expect(entryColumns(e(1000))).toEqual({ debit: 1000, credit: null })
    expect(entryColumns(e('-250.500000'))).toEqual({ debit: null, credit: 250.5 })
    expect(entryColumns(e(0))).toEqual({ debit: null, credit: null })
  })
  it('names every kind', () => {
    expect(entryLabel('credit_note')).toBe('Credit note')
    expect(entryLabel('top_up')).toBe('Top-up')
    expect(entryLabel('something_new')).toBe('something new')
  })
  it('reads the balance sign', () => {
    expect(balanceWord(250)).toBe('owes')
    expect(balanceWord('-100.000000')).toBe('in credit')
    expect(balanceWord(0)).toBe('settled')
  })
})

describe('payments', () => {
  const pay = (over: Partial<Payment>): Payment => ({ id: 7, amount: 600, paid_at: '2026-02-10T00:00:00Z', status: 'received', ...over })
  it('says where a payment went and what is left on account', () => {
    const p = pay({ allocated: 450, unallocated: 150, allocations: [{ id: 1, statement_id: 'a', invoice_number: 'INV-2026-00001', amount: 300, allocated_at: '' }, { id: 2, statement_id: 'b', invoice_number: 'INV-2026-00002', amount: 150, allocated_at: '' }] })
    expect(allocationText(p, 'OMR', money)).toBe('300.000 OMR to INV-2026-00001, 150.000 OMR to INV-2026-00002, 150.000 OMR on account')
    expect(canAllocate(p)).toBe(true)
  })
  it('a fully allocated or refunded payment cannot be allocated further', () => {
    expect(allocationText(pay({ allocated: 600, unallocated: 0, allocations: [{ id: 1, statement_id: 'a', amount: 600, allocated_at: '' }] }), 'OMR', money)).toBe('600.000 OMR to invoice')
    expect(canAllocate(pay({ unallocated: 0 }))).toBe(false)
    expect(canAllocate(pay({ status: 'refunded', unallocated: 600 }))).toBe(false)
    expect(paymentStatus(pay({ status: 'received' }))).toBe('settled')
    expect(paymentStatus(pay({ status: 'refunded' }))).toBe('refunded')
  })
  it('lists the open invoices oldest due first', () => {
    const s = (id: string, status: string, due: string, balance: number): Statement => ({ id, customer_id: 'c', period_start: '2026-01-01', period_end: '2026-01-31', currency: 'OMR', subtotal: 1, tax_rate: 0, tax: 0, total: 1, status, due_at: due, balance })
    const rows = [s('late', 'sent', '2026-03-01', 10), s('paid', 'paid', '2026-01-01', 0), s('old', 'issued', '2026-02-01', 5), s('draft', 'draft', '', 1)]
    expect(openInvoices(rows).map((r) => r.id)).toEqual(['old', 'late'])
  })
  it('describes a credit note', () => {
    const n = (applied: number, unapplied: number): CreditNote => ({ id: 'n', customer_id: 'c', statement_id: 's', number: 'CN-2026-00001', kind: 'partial', currency: 'OMR', subtotal: 1, tax_rate: 0, tax: 0, total: applied + unapplied, applied, unapplied, issued_at: '' })
    expect(creditNoteEffect(n(50, 0), 'OMR', money)).toBe('50.000 OMR applied to the invoice')
    expect(creditNoteEffect(n(0, 30), 'OMR', money)).toBe('30.000 OMR on account')
    expect(creditNoteEffect(n(20, 30), 'OMR', money)).toBe('20.000 OMR applied to the invoice, 30.000 OMR on account')
  })
})

describe('the aging report', () => {
  // The same boundaries the server applies (DESIGN.md §9.6).
  it('buckets are exact at every boundary day', () => {
    expect([0, 1, 30, 31, 60, 61, 90, 91].map(bucketFor)).toEqual(['current', '1-30', '1-30', '31-60', '31-60', '61-90', '61-90', 'over-90'])
    expect(bucketFor(-5)).toBe('current')
  })
  it('overdue share is a fraction of the total, null with nothing owed', () => {
    const rep = (total: number, overdue: number): AgingReport => ({ as_of: '', buckets: [], rows: [], totals: {}, total, overdue, invoices: [], collections_owner: 'internal' })
    expect(overdueShare(rep(200, 50))).toBeCloseTo(0.25)
    expect(overdueShare(rep(0, 0))).toBeNull()
  })
  it('renders and parses the reminder schedule', () => {
    expect(reminderScheduleText([-3, 0, 7, 14, 30])).toBe('3 days before · on the due date · 7, 14, 30 days after')
    expect(reminderScheduleText([-1])).toBe('1 day before')
    expect(reminderScheduleText([])).toBe('no reminders')
    expect(parseReminderDays('30, -3, 7,0, 14, 7')).toEqual({ values: [-3, 0, 7, 14, 30] })
    expect(parseReminderDays('3 days').error).toContain('not a whole number')
    expect(parseReminderDays('4000').error).toContain('out of range')
  })
})
