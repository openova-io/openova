import { describe, expect, it } from 'vitest'
import type { Statement } from '../api/types'
import { acceptsPayment, dueLabel, isStatementStatus, nextStatuses, statementBalance, statementPaid, statementPeriod, statementStatus } from './statements'

const stmt = (over: Partial<Statement>): Statement => ({
  id: 's-1',
  customer_id: 'c-1',
  period_start: '2026-08-01',
  period_end: '2026-08-31',
  currency: 'OMR',
  subtotal: 1000,
  tax_rate: 0,
  tax: 0,
  total: 1000,
  status: 'draft',
  ...over,
})

describe('statementPeriod', () => {
  // period_end is inclusive on the wire; a whole month must read as that month,
  // not as "1 Aug – 30 Aug" (an off-by-one that misreports the period).
  it('reads a whole calendar month as the month', () => {
    expect(statementPeriod({ period_start: '2026-08-01', period_end: '2026-08-31' })).toBe('Aug 2026')
    expect(statementPeriod({ period_start: '2026-02-01', period_end: '2026-02-28' })).toBe('Feb 2026')
  })
  it('describes a partial period day to day', () => {
    expect(statementPeriod({ period_start: '2026-09-01', period_end: '2026-09-15' })).toBe('1–15 Sep 2026')
  })
  it('accepts timestamps and shows malformed periods as sent', () => {
    expect(statementPeriod({ period_start: '2026-08-01T00:00:00Z', period_end: '2026-08-31T00:00:00Z' })).toBe('Aug 2026')
    expect(statementPeriod({ period_start: '2026-08-31', period_end: '2026-08-01' })).toBe('2026-08-31 → 2026-08-01')
    expect(statementPeriod({ period_start: '', period_end: '' })).toBe('? → ?')
  })
})

// The invoice lifecycle (DESIGN.md §8).
describe('statementStatus', () => {
  it('shows the DERIVED status, which is the only place "overdue" comes from', () => {
    expect(statementStatus(stmt({ status: 'sent', effective_status: 'overdue' }))).toBe('overdue')
    expect(statementStatus(stmt({ status: 'sent' }))).toBe('sent')
    // A document written before invoicing carries only `status`.
    expect(statementStatus(stmt({ status: 'issued', effective_status: undefined }))).toBe('issued')
  })
  it('knows which strings are statuses at all, so a URL cannot invent one', () => {
    expect(isStatementStatus('overdue')).toBe(true)
    expect(isStatementStatus('posted')).toBe(false)
    expect(isStatementStatus(null)).toBe(false)
  })
})

describe('statementBalance / statementPaid', () => {
  it('prefers the server balance and otherwise derives it from total − paid', () => {
    expect(statementBalance(stmt({ total: 1000, paid_total: 400, balance: 600 }))).toBe(600)
    expect(statementBalance(stmt({ total: 1000, paid_total: 400 }))).toBe(600)
    expect(statementBalance(stmt({ total: 1000 }))).toBe(1000)
    expect(statementPaid(stmt({ paid_total: '400.000000' }))).toBe(400)
    expect(statementPaid(stmt({}))).toBe(0)
  })
})

describe('acceptsPayment / nextStatuses', () => {
  it('a payment may be recorded from issued, sent and overdue only', () => {
    expect(acceptsPayment(stmt({ status: 'issued' }))).toBe(true)
    expect(acceptsPayment(stmt({ status: 'sent' }))).toBe(true)
    expect(acceptsPayment(stmt({ status: 'sent', effective_status: 'overdue' }))).toBe(true)
    expect(acceptsPayment(stmt({ status: 'draft' }))).toBe(false)
    expect(acceptsPayment(stmt({ status: 'paid' }))).toBe(false)
    expect(acceptsPayment(stmt({ status: 'cancelled' }))).toBe(false)
  })
  it('mirrors the lifecycle the store enforces', () => {
    expect(nextStatuses('draft')).toEqual(['issued', 'cancelled'])
    expect(nextStatuses('issued')).toEqual(['sent', 'paid', 'cancelled'])
    expect(nextStatuses('sent')).toEqual(['paid', 'overdue'])
    expect(nextStatuses('overdue')).toEqual(['paid'])
    expect(nextStatuses('paid')).toEqual([])
    expect(nextStatuses('cancelled')).toEqual([])
  })
})

describe('dueLabel', () => {
  const now = new Date('2026-09-10T12:00:00Z')
  it('counts the days on either side of the due date', () => {
    expect(dueLabel(stmt({ due_at: '2026-09-22T12:00:00Z' }), now)).toBe('due in 12 days')
    expect(dueLabel(stmt({ due_at: '2026-09-11T12:00:00Z' }), now)).toBe('due in 1 day')
    expect(dueLabel(stmt({ due_at: '2026-09-10T12:00:00Z' }), now)).toBe('due today')
    expect(dueLabel(stmt({ due_at: '2026-09-01T12:00:00Z' }), now)).toBe('9 days overdue')
    expect(dueLabel(stmt({ due_at: '2026-09-09T12:00:00Z' }), now)).toBe('1 day overdue')
  })
  it('says nothing when there is no due date, or it is unreadable', () => {
    expect(dueLabel(stmt({}), now)).toBe('')
    expect(dueLabel(stmt({ due_at: 'soon' }), now)).toBe('')
  })
})
