import { createElement, type ReactElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { FinancePeriodDetail, JournalResponse, ReconciliationRun } from '../api/types'

/**
 * The four Finance pages rendered (DESIGN.md §18): the journal with its
 * account codes and the balance check AS A FIGURE, the reconciliation's four
 * buckets with counts and amounts, the period close with what blocks it, and
 * the account map.
 */

const journal: JournalResponse = {
  period: '2026-08',
  status: 'open',
  journal: {
    period: '2026-08',
    balanced: true,
    total_debit: '1150.000000',
    total_credit: '1150.000000',
    by_currency: [{ currency: 'OMR', debit: '1150.000000', credit: '1150.000000' }],
    lines: [
      { seq: 1, date: '2026-08-31', event: 'invoice', account_key: 'receivable', account_code: '1100', account_name: 'Trade receivables', debit: '1050.000000', credit: '0.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001', memo: 'invoice INV-2026-00001' },
      { seq: 2, date: '2026-08-31', event: 'invoice', account_key: 'revenue.ecs', account_code: '4010', account_name: 'Revenue, compute', debit: '0.000000', credit: '1100.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001', memo: 'revenue, ecs' },
      { seq: 3, date: '2026-08-31', event: 'invoice', account_key: 'discounts', account_code: '4800', account_name: 'Discounts', debit: '100.000000', credit: '0.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001', memo: 'discounts on INV-2026-00001' },
      { seq: 4, date: '2026-08-31', event: 'invoice', account_key: 'tax_payable', account_code: '2200', account_name: 'Tax payable', debit: '0.000000', credit: '50.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'statement', source_id: 'st1', reference: 'INV-2026-00001', memo: 'tax, VAT standard' },
      { seq: 5, date: '2026-08-20', event: 'payment', account_key: 'gateway_clearing', account_code: '1010', account_name: 'Gateway clearing', debit: '1050.000000', credit: '0.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'payment', source_id: '7', reference: 'GW-1', memo: 'payment received against INV-2026-00001' },
      { seq: 6, date: '2026-08-20', event: 'payment', account_key: 'receivable', account_code: '1100', account_name: 'Trade receivables', debit: '0.000000', credit: '1050.000000', currency: 'OMR', customer_slug: 'acme', customer_name: 'ACME LLC', source_kind: 'payment', source_id: '7', reference: 'GW-1', memo: 'payment received against INV-2026-00001' },
    ],
  },
}

const run: ReconciliationRun = {
  id: 'run-1',
  gateway: 'omantel',
  source: 'file',
  file_name: 'august.csv',
  from: '2026-08-01',
  to: '2026-08-31',
  currency: 'OMR',
  matched: 1,
  amount_mismatched: 1,
  missing_in_ledger: 1,
  missing_in_settlement: 1,
  duplicates: 1,
  settled_total: '174.000000',
  ledger_total: '107.000000',
  fee_total: '2.500000',
  ran_at: '2026-09-01T08:00:00Z',
  ran_by: 'cfo@sovereign.example',
  lines: [
    { id: 1, bucket: 'matched', gateway_reference: 'GW-1', settled_amount: '60.000000', ledger_amount: '60.000000', difference: '0.000000', fee: '1.500000', currency: 'OMR', settled_date: '2026-08-05', payment_id: 1, customer_name: 'ACME LLC' },
    { id: 2, bucket: 'amount-mismatch', gateway_reference: 'GW-2', settled_amount: '39.000000', ledger_amount: '40.000000', difference: '-1.000000', fee: '1.000000', currency: 'OMR', settled_date: '2026-08-06', payment_id: 2, customer_name: 'ACME LLC', detail: 'the gateway settled 39.000000 and payment 2 records 40.000000, a difference of -1.000000' },
    { id: 3, bucket: 'missing-in-ledger', gateway_reference: 'GW-8', settled_amount: '12.000000', fee: '0.000000', currency: 'OMR', settled_date: '2026-08-07', detail: 'the gateway settled GW-8 and no payment carries that reference' },
    { id: 4, bucket: 'missing-in-settlement', gateway_reference: 'GW-9', ledger_amount: '7.000000', fee: '0.000000', currency: 'OMR', settled_date: '2026-08-09', payment_id: 4, customer_name: 'Globex', detail: 'payment 4 is recorded and the settlement file does not name GW-9' },
    { id: 5, bucket: 'duplicate', gateway_reference: 'GW-1', settled_amount: '60.000000', fee: '0.000000', currency: 'OMR', settled_date: '2026-08-08', detail: 'the settlement file names GW-1 more than once; the later line was not matched again' },
  ],
}

const periodDetail: FinancePeriodDetail = {
  period: { period: '2026-08', status: 'open' },
  blockers: [
    { kind: 'draft-statement', id: 'st-draft', customer_id: 'c2', customer_name: 'Globex', invoice_number: '', detail: 'statement st-draft for Globex is still a draft' },
    { kind: 'open-dispute', id: 'd1', customer_id: 'c1', customer_name: 'ACME LLC', invoice_number: 'INV-2026-00001', detail: 'ACME LLC disputes INV-2026-00001: the storage line is wrong' },
  ],
  can_close: false,
  total_debit: '1150.000000',
  total_credit: '1150.000000',
  lines: 6,
  balanced: true,
}

const accounts = {
  keys: ['receivable', 'cash', 'gateway_clearing', 'customer_advances', 'tax_payable', 'revenue', 'discounts', 'credit_notes', 'write_offs', 'gateway_fees', 'commission'],
  revenue_key_prefix: 'revenue.',
  mappings: [
    { key: 'receivable', account_code: '1100', description: 'Trade receivables', updated_at: '2026-09-01T00:00:00Z', updated_by: 'migration' },
    { key: 'cash', account_code: '1000', description: 'Cash and bank' },
    { key: 'gateway_clearing', account_code: '1010', description: 'Payment gateway clearing' },
    { key: 'customer_advances', account_code: '2100', description: 'Customer advances and account credit' },
    { key: 'tax_payable', account_code: '', description: 'Tax payable' },
    { key: 'revenue', account_code: '4000', description: 'Revenue' },
    { key: 'discounts', account_code: '4800', description: 'Discounts and allowances' },
    { key: 'credit_notes', account_code: '4900', description: 'Credit notes' },
    { key: 'write_offs', account_code: '6100', description: 'Receivables written off' },
    { key: 'gateway_fees', account_code: '6200', description: 'Payment gateway fees' },
    { key: 'commission', account_code: '6300', description: 'Partner commission' },
    { key: 'revenue.ecs', account_code: '4010', description: 'Revenue, compute' },
  ],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const data =
      path === null
        ? null
        : path.startsWith('/finance/journal')
          ? journal
          : path === '/finance/reconciliations'
            ? { runs: [run] }
            : path?.startsWith('/finance/reconciliation/')
              ? { run }
              : path === '/finance/periods'
                ? { periods: [{ period: '2026-07', status: 'closed', closed_at: '2026-08-03T00:00:00Z', closed_by: 'cfo@sovereign.example', total_debit: '900.000000', total_credit: '900.000000', lines: 12 }] }
                : path?.startsWith('/finance/periods/')
                  ? periodDetail
                  : path === '/finance/accounts'
                    ? accounts
                    : null
    return { data, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

vi.mock('../auth/session', () => ({
  useSession: () => ({
    me: {
      email: 'cfo@sovereign.example',
      role: 'operator',
      permissions: { sovereign: ['metering.read', 'audit.read', 'settings.manage'] },
      roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }],
    },
    loading: false,
    refresh: async () => null,
    logout: async () => {},
  }),
}))

import { FinanceAccounts } from './FinanceAccounts'
import { FinanceJournal } from './FinanceJournal'
import { FinancePeriods } from './FinancePeriods'
import { FinanceReconciliation } from './FinanceReconciliation'

function render(el: () => ReactElement, at: string): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [at] }, createElement(el))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Finance → Journal', () => {
  it('shows the lines with the MAPPED account codes and what each came from', () => {
    const html = render(FinanceJournal, '/finance/journal')
    expect(html).toContain('aria-label="Journal"')
    expect(html).toContain('>1100<')
    expect(html).toContain('>4010<')
    expect(html).toContain('Revenue, compute')
    expect(html).toContain('INV-2026-00001')
    // Every line names the object it traces back to.
    expect(html).toContain('>statement ')
    expect(html).toContain('>payment ')
  })
  it('shows the balance check as a figure, not a claim', () => {
    const html = render(FinanceJournal, '/finance/journal')
    expect(html).toContain('>Total debits<')
    expect(html).toContain('>Total credits<')
    expect(html).toContain('>Balance check<')
    expect(html).toContain('1,150.000 OMR')
    expect(html).toContain('debits equal credits')
  })
  it('offers the CSV download and the period picker', () => {
    const html = render(FinanceJournal, '/finance/journal')
    expect(html).toContain('Download CSV')
    expect(html).toContain('format=csv')
    expect(html).toContain('aria-label="Period"')
    expect(html).toContain('aria-label="Journal by account"')
  })
})

describe('Finance → Reconciliation', () => {
  it('shows the four buckets with their counts and amounts', () => {
    const html = render(FinanceReconciliation, '/finance/reconciliation')
    for (const label of ['Matched', 'Amount mismatch', 'Missing in the ledger', 'Missing in the settlement', 'Duplicate reference']) {
      expect(html).toContain(`>${label}<`)
    }
    expect(html).toContain('60.000 OMR')
    expect(html).toContain('7.000 OMR')
  })
  it('shows the row-level view with both figures on a mismatch', () => {
    const html = render(FinanceReconciliation, '/finance/reconciliation')
    expect(html).toContain('aria-label="Reconciliation lines"')
    expect(html).toContain('>Gateway says<')
    expect(html).toContain('>We recorded<')
    expect(html).toContain('39.000 OMR')
    expect(html).toContain('40.000 OMR')
    expect(html).toContain('a difference of -1.000000')
    expect(html).toContain('not matched again')
  })
  it('offers the upload and the gateway fetch, and says nothing is corrected', () => {
    const html = render(FinanceReconciliation, '/finance/reconciliation')
    expect(html).toContain('aria-label="Settlement file"')
    expect(html).toContain('Fetch from the gateway')
    expect(html).toContain('it reports, and you decide')
    expect(html).toContain('aria-label="Reconciliation runs"')
  })
})

describe('Finance → Period close', () => {
  it('names what blocks the close', () => {
    const html = render(FinancePeriods, '/finance/periods')
    expect(html).toContain('aria-label="What blocks the close"')
    expect(html).toContain('statement st-draft for Globex is still a draft')
    expect(html).toContain('disputes INV-2026-00001: the storage line is wrong')
    expect(html).toContain('1 statement is still a draft and 1 invoice is disputed')
  })
  it('offers the close, disabled while something blocks it', () => {
    const html = render(FinancePeriods, '/finance/periods')
    expect(html).toContain('Close August 2026')
    expect(html).toMatch(/Close August 2026<\/button>/)
    expect(html).toContain('disabled=""')
    expect(html).toContain('no statement in it may be issued, re-run, cancelled or credited')
  })
  it('lists the periods already closed with who closed them', () => {
    const html = render(FinancePeriods, '/finance/periods')
    expect(html).toContain('aria-label="Periods"')
    expect(html).toContain('July 2026')
    expect(html).toContain('cfo@sovereign.example')
  })
})

describe('Finance → Account mapping', () => {
  it('shows the key-to-code table and the per-service revenue accounts', () => {
    const html = render(FinanceAccounts, '/finance/accounts')
    expect(html).toContain('aria-label="Account mapping"')
    expect(html).toContain('aria-label="Revenue accounts by service"')
    expect(html).toContain('>receivable<')
    expect(html).toContain('value="1100"')
    expect(html).toContain('revenue.ecs')
  })
  it('says which key has no code, and what that costs', () => {
    const html = render(FinanceAccounts, '/finance/accounts')
    expect(html).toContain('No code is mapped for tax_payable')
    expect(html).toContain('the export is refused')
  })
})
