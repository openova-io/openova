import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * What a statement shaped by a CONTRACT shows (DESIGN.md §15.9): the true-up
 * line, named and explained where it is charged, and the SLA credit note
 * beside the reason it answers.
 *
 * Both are things a customer is charged or credited for without having used
 * anything, so both have to be readable on the document itself — a shortfall
 * that appeared only in the totals, or a credit with no stated cause, is the
 * shape of a bill nobody can check.
 */

const statement = {
  id: 'st1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  customer_slug: 'acme',
  period_start: '2026-08-01',
  period_end: '2026-08-31',
  currency: 'OMR',
  // 4,319.08 of usage lifted to the 6,000 monthly minimum by the true-up.
  subtotal: '6000.000000',
  discount_total: '0',
  discount_detail: null,
  tax_rate: '0.05',
  tax: '300.000000',
  total: '6300.000000',
  status: 'issued',
  invoice_number: 'INV-2026-0001',
  issued_at: '2026-09-01T08:00:00Z',
  created_at: '2026-09-01T00:00:00Z',
  contract_id: 'ct1',
  contract_name: 'ACME 2026',
  credited_total: '630.000000',
  paid_total: '0',
  balance: '5670.000000',
  credit_notes: [
    {
      id: 'cn1',
      customer_id: 'c1',
      statement_id: 'st1',
      number: 'CN-2026-0001',
      kind: 'partial',
      reason: 'availability 99.2 % against the 99.9 % commitment for August',
      currency: 'OMR',
      subtotal: '600.000000',
      tax_rate: '0.05',
      tax: '30.000000',
      total: '630.000000',
      applied: '630.000000',
      unapplied: '0',
      issued_at: '2026-09-05T09:00:00Z',
      issued_by: 'ops@nc.example',
      contract_id: 'ct1',
      contract_name: 'ACME 2026',
      sla_pct: '10',
      measured_availability: '99.2',
    },
  ],
  lines: [
    { sku: 'object_gib', unit: 'gb-month', quantity: '51200', unit_price: '0.00840000', amount: '430.080000', resource_count: 1, source_id: 'src-a' },
    { sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '10000', unit_price: '0.38840000', amount: '3884.000000', resource_count: 4, source_id: 'src-a' },
    { sku: 'eip.traffic_gb', unit: 'gb', quantity: '150', unit_price: '0.03333333', amount: '5.000000', resource_count: 1, source_id: 'src-a' },
    // The shortfall against the contract's monthly minimum, as a LINE.
    { sku: 'true-up', unit: 'period', quantity: '1', unit_price: '1680.920000', amount: '1680.920000', resource_count: 0 },
  ],
}

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/statements/st1') return statement
    if (path === '/customers/c1/sources') return { sources: [{ id: 'src-a', customer_id: 'c1', kind: 'huawei-project', region: 'me-east-215', project_id: 'proj-acme', status: 'verified' }] }
    // The statement view also asks whether this invoice is disputed (§16).
    // An undisputed invoice is the case these two assertions are about, so the
    // fixture answers with no dispute rather than widening the throw: a path
    // nobody taught this test about must still fail loudly.
    if (path === '/statements/st1/disputes') return { disputes: [] }
    throw new Error(`no fixture for ${path}`)
  }
  return {
    useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
  }
})

import { StatementView } from './StatementView'

function render(): string {
  const html = renderToString(
    createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) }))),
  ).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('a statement shaped by a contract', () => {
  it('shows the true-up as a named line, explained where it is charged', () => {
    const html = render()
    expect(html).toContain('true-up')
    expect(html).toContain('the shortfall against the contract&#x27;s monthly minimum')
    // It gets its own heading rather than being filed under "Other" beside a
    // storage line.
    expect(html).toContain('Contract minimum (true-up)')
    expect(html).toContain('1,680.920')
    // The lines the shapes produced are on the bill at what they rated, not
    // at the book's flat rate: a tiered 51,200 GiB is 430.080, never 512.
    expect(html).toContain('430.080')
    expect(html).toContain('3,884.000')
    // And the subtotal is the minimum the contract set, to the last unit.
    expect(html).toContain('6,000.000 OMR')
  })

  it('shows the SLA credit with its reason, its percentage and the availability measured', () => {
    const html = render()
    expect(html).toContain('CN-2026-0001')
    expect(html).toContain('availability 99.2 % against the 99.9 % commitment for August')
    expect(html).toContain('SLA credit under ACME 2026')
    expect(html).toContain('10 % of the period')
    expect(html).toContain('availability measured 99.2 %')
    expect(html).toContain('630.000')
  })
})
