import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { AgingReport } from '../api/types'

/**
 * The Collections page rendered (DESIGN.md §9.6–§9.7): the aging table
 * with its accessible name and the five buckets, the strip, the run
 * action, and Suspend / Resume offered according to the row's state.
 */

const report: AgingReport = {
  as_of: '2026-09-10T00:00:00Z',
  buckets: ['current', '1-30', '31-60', '61-90', 'over-90'],
  rows: [
    { customer_id: 'c1', customer_name: 'ACME LLC', customer_slug: 'acme', currency: 'OMR', buckets: { current: '100.000000', '31-60': '250.500000' }, total: '350.500000', overdue: '250.500000', oldest_days: 45, invoices: 2, suspended: false, available_credit: '0' },
    { customer_id: 'c2', customer_name: 'Globex', customer_slug: 'globex', currency: 'OMR', buckets: { 'over-90': '80.000000' }, total: '80.000000', overdue: '80.000000', oldest_days: 120, invoices: 1, suspended: true, suspended_at: '2026-09-01T00:00:00Z', suspension_source: 'collections', suspension_reason: 'escalated', available_credit: '15.000000' },
  ],
  totals: { current: '100.000000', '31-60': '250.500000', 'over-90': '80.000000' },
  total: '430.500000',
  overdue: '330.500000',
  invoices: [
    { statement_id: 'st1', invoice_number: 'INV-2026-00001', customer_id: 'c1', customer_name: 'ACME LLC', currency: 'OMR', due_at: '2026-07-27T00:00:00Z', days_past_due: 45, bucket: '31-60', outstanding: '250.500000', status: 'sent' },
    { statement_id: 'st2', invoice_number: 'INV-2026-00002', customer_id: 'c1', customer_name: 'ACME LLC', currency: 'OMR', due_at: '2026-09-20T00:00:00Z', days_past_due: -10, bucket: 'current', outstanding: '100.000000', status: 'sent' },
    { statement_id: 'st3', invoice_number: 'INV-2026-00003', customer_id: 'c2', customer_name: 'Globex', currency: 'OMR', due_at: '2026-05-13T00:00:00Z', days_past_due: 120, bucket: 'over-90', outstanding: '80.000000', status: 'sent' },
  ],
  collections_owner: 'internal',
}

const settings = { discount_rule: 'most-specific', reminder_days: [-3, 0, 7, 14, 30], escalation_days: 45, escalation_action: 'suspend', commercial_provider: 'internal' }

let current: AgingReport = report

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/collections/aging' ? current : path === '/billing-settings' ? settings : null,
    error: '',
    loading: false,
    reload: async () => {},
    setData: () => {},
  }),
}))

// The run and the suspend / resume controls are billing.collect (DESIGN.md
// §10.9): the page is rendered as a sovereign-admin.
vi.mock('../auth/session', () => ({
  useSession: () => ({
    me: { email: 'ops@sovereign.example', role: 'operator', permissions: { sovereign: ['metering.read', 'billing.collect'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] },
    loading: false,
    refresh: async () => null,
    logout: async () => {},
  }),
}))

import { Collections } from './Collections'

function render(): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: ['/collections'] }, createElement(Collections))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Collections renders the aging report', () => {
  it('the table is named, has the five buckets, and totals them', () => {
    const html = render()
    expect(html).toContain('aria-label="Aging"')
    for (const h of ['>Current', '>1–30 days', '>31–60 days', '>61–90 days', '>Over 90', '>Total owed', '>Overdue', '>Oldest due', '>Credit available']) expect(html).toContain(h)
    expect(html).toContain('ACME LLC')
    expect(html).toContain('Globex')
    expect(html).toContain('250.500 OMR')
    expect(html).toContain('45 days overdue')
    expect(html).toContain('120 days overdue')
    // The totals row: current + late buckets and the grand total.
    expect(html).toContain('430.500 OMR')
    expect(html).toContain('330.500 OMR')
  })
  it('the strip answers how much, how much is late, and who', () => {
    const html = render()
    expect(html).toContain('>Total owed<')
    expect(html).toContain('>Overdue<')
    expect(html).toContain('Customers overdue')
    expect(html).toContain('of 2 with an open invoice')
    expect(html).toContain('>Suspended<')
  })
  it('offers the run, and Suspend or Resume according to the row', () => {
    const html = render()
    expect(html).toContain('Run collections now')
    expect(html).toContain('>Suspend<')
    expect(html).toContain('>Resume<')
    expect(html).toContain('badge bad">suspended')
    // The schedule from the settings reads in words in the subtitle.
    expect(html).toContain('3 days before · on the due date · 7, 14, 30 days after')
    expect(html).toContain('escalate 45 days after, suspend')
  })
  it('says so when nothing is owed', () => {
    current = { ...report, rows: [], invoices: [], total: '0', overdue: '0' }
    try {
      const html = render()
      expect(html).toContain('Nothing is owed')
      expect(html).toContain('Every issued invoice is settled.')
      expect(html).toContain('everyone is within terms')
    } finally {
      current = report
    }
  })
})
