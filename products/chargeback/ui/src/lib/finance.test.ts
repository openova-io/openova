import { describe, expect, it } from 'vitest'
import type { JournalBatch, JournalLine, ReconciliationRun } from '../api/types'
import { accountTotals, balanceOf, blockerSummary, bucketSummaries, closeState, eventLabel, lineDifference, needsAttention, periodLabel, recentPeriods, splitMappings, unmappedKeys } from './finance'

const lines: JournalLine[] = [
  { seq: 1, date: '2026-08-31', event: 'invoice', account_key: 'receivable', account_code: '1100', account_name: 'Trade receivables', debit: '1050.000000', credit: '0.000000', currency: 'OMR', customer_slug: 'acme', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001' },
  { seq: 2, date: '2026-08-31', event: 'invoice', account_key: 'revenue.ecs', account_code: '4010', account_name: 'Revenue, compute', debit: '0.000000', credit: '1100.000000', currency: 'OMR', customer_slug: 'acme', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001' },
  { seq: 3, date: '2026-08-31', event: 'invoice', account_key: 'discounts', account_code: '4800', account_name: 'Discounts', debit: '100.000000', credit: '0.000000', currency: 'OMR', customer_slug: 'acme', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001' },
  { seq: 4, date: '2026-08-31', event: 'invoice', account_key: 'tax_payable', account_code: '2200', account_name: 'Tax payable', debit: '0.000000', credit: '50.000000', currency: 'OMR', customer_slug: 'acme', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001' },
]

const batch: JournalBatch = {
  period: '2026-08',
  lines,
  total_debit: '1150.000000',
  total_credit: '1150.000000',
  by_currency: [{ currency: 'OMR', debit: '1150.000000', credit: '1150.000000' }],
  balanced: true,
}

describe('the balance check reads as a figure, not a claim', () => {
  it('reports both sides and the difference', () => {
    const b = balanceOf(batch)
    expect(b.debit).toBe(1150)
    expect(b.credit).toBe(1150)
    expect(b.difference).toBe(0)
    expect(b.balanced).toBe(true)
    expect(b.lines).toBe(4)
    expect(b.currency).toBe('OMR')
  })
  it('a batch whose sides differ is not balanced whatever the flag says', () => {
    const b = balanceOf({ ...batch, total_credit: '1100.000000', balanced: true })
    expect(b.difference).toBe(50)
    expect(b.balanced).toBe(false)
  })
  it('an absent batch reads as zero rather than NaN', () => {
    const b = balanceOf(null)
    expect(b.debit).toBe(0)
    expect(b.credit).toBe(0)
    expect(b.balanced).toBe(false)
  })
})

describe('account totals group the journal the way a ledger is posted', () => {
  it('one row per account, in code order, summing both sides', () => {
    const totals = accountTotals(lines)
    expect(totals.map((t) => t.code)).toEqual(['1100', '2200', '4010', '4800'])
    expect(totals.find((t) => t.code === '1100')?.debit).toBe(1050)
    expect(totals.find((t) => t.code === '4010')?.credit).toBe(1100)
    expect(totals.reduce((n, t) => n + t.lines, 0)).toBe(4)
  })
})

describe('the four buckets', () => {
  const run: ReconciliationRun = {
    id: 'r1',
    gateway: 'omantel',
    source: 'file',
    matched: 1,
    amount_mismatched: 1,
    missing_in_ledger: 1,
    missing_in_settlement: 1,
    duplicates: 1,
    settled_total: '174.000000',
    ledger_total: '107.000000',
    fee_total: '2.500000',
    ran_at: '2026-09-01T00:00:00Z',
    lines: [
      { bucket: 'matched', gateway_reference: 'GW-1', settled_amount: '60.000000', ledger_amount: '60.000000', difference: '0.000000', fee: '1.500000', currency: 'OMR' },
      { bucket: 'amount-mismatch', gateway_reference: 'GW-2', settled_amount: '39.000000', ledger_amount: '40.000000', difference: '-1.000000', fee: '1.000000', currency: 'OMR' },
      { bucket: 'missing-in-ledger', gateway_reference: 'GW-8', settled_amount: '12.000000', fee: '0.000000', currency: 'OMR' },
      { bucket: 'missing-in-settlement', gateway_reference: 'GW-9', ledger_amount: '7.000000', fee: '0.000000', currency: 'OMR' },
      { bucket: 'duplicate', gateway_reference: 'GW-1', settled_amount: '60.000000', fee: '0.000000', currency: 'OMR' },
    ],
  }
  it('each bucket carries its count and the money in it', () => {
    const s = bucketSummaries(run)
    expect(s.map((b) => b.value)).toEqual(['matched', 'amount-mismatch', 'missing-in-ledger', 'missing-in-settlement', 'duplicate'])
    expect(s.map((b) => b.count)).toEqual([1, 1, 1, 1, 1])
    expect(s.find((b) => b.value === 'matched')?.amount).toBe(60)
    // The money of a payment the gateway never named is OURS, not theirs.
    expect(s.find((b) => b.value === 'missing-in-settlement')?.amount).toBe(7)
  })
  it('counts what a human still has to act on', () => {
    expect(needsAttention(run)).toBe(4)
    expect(needsAttention({ ...run, amount_mismatched: 0, missing_in_ledger: 0, missing_in_settlement: 0, duplicates: 0 })).toBe(0)
    expect(needsAttention(null)).toBe(0)
  })
  it('a mismatch reports settled minus recorded', () => {
    expect(lineDifference(run.lines![1])).toBe(-1)
    expect(lineDifference(run.lines![2])).toBe(null)
  })
  it('an empty run reads as zeroes rather than blanks', () => {
    for (const b of bucketSummaries(null)) {
      expect(b.count).toBe(0)
      expect(b.amount).toBe(0)
    }
  })
})

describe('what blocks a close', () => {
  it('names the drafts and the disputes', () => {
    expect(blockerSummary([{ kind: 'draft-statement', id: 'a' }])).toBe('1 statement is still a draft')
    expect(blockerSummary([{ kind: 'draft-statement', id: 'a' }, { kind: 'draft-statement', id: 'b' }, { kind: 'open-dispute', id: 'c' }])).toBe('2 statements are still drafts and 1 invoice is disputed')
    expect(blockerSummary([])).toBe('')
  })
  it('the close state says whether it can and why not', () => {
    expect(closeState({ period: { period: '2026-08', status: 'open' }, blockers: [], can_close: true, balanced: true })).toEqual({ can: true, why: 'nothing is outstanding' })
    expect(closeState({ period: { period: '2026-08', status: 'closed', closed_by: 'cfo@x.example' }, blockers: [], can_close: false })).toEqual({ can: false, why: 'closed by cfo@x.example' })
    expect(closeState({ period: { period: '2026-08', status: 'open' }, blockers: [{ kind: 'open-dispute', id: 'd' }], can_close: false })).toEqual({ can: false, why: '1 invoice is disputed' })
    expect(closeState({ period: { period: '2026-08', status: 'open' }, blockers: [], can_close: false, balanced: false, balance_error: 'does not balance: 50.000000' })).toEqual({ can: false, why: 'does not balance: 50.000000' })
  })
})

describe('the period picker', () => {
  it('offers whole months, newest first', () => {
    const list = recentPeriods(new Date(Date.UTC(2026, 8, 11)), 3)
    expect(list).toEqual(['2026-09', '2026-08', '2026-07'])
  })
  it('names a month in words, and leaves anything else alone', () => {
    expect(periodLabel('2026-08')).toBe('August 2026')
    expect(periodLabel('2026-13')).toBe('2026-13')
    expect(periodLabel('')).toBe('')
  })
})

describe('the account map', () => {
  const mappings = [
    { key: 'receivable', account_code: '1100' },
    { key: 'revenue', account_code: '4000' },
    { key: 'revenue.ecs', account_code: '4010' },
    { key: 'tax_payable', account_code: '' },
  ]
  it('splits the fixed keys from the per-service revenue ones', () => {
    const { fixed, services } = splitMappings(mappings, ['receivable', 'revenue', 'tax_payable'])
    expect(fixed.map((m) => m.key)).toEqual(['receivable', 'revenue', 'tax_payable'])
    expect(services.map((m) => m.key)).toEqual(['revenue.ecs'])
  })
  it('names the keys with no code, which is what makes a batch refuse', () => {
    expect(unmappedKeys(mappings)).toEqual(['tax_payable'])
  })
})

describe('event labels', () => {
  it('names every event the journal emits', () => {
    for (const k of ['invoice', 'payment', 'top_up', 'advance_applied', 'credit_note', 'write_off', 'refund', 'gateway_fee']) {
      expect(eventLabel(k)).not.toBe(k)
    }
    expect(eventLabel('something-new')).toBe('something-new')
  })
})
