import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { CostCentre, CostCentreReport, CostCentreResource, CostCentreRule } from '../api/types'

/**
 * Cost centres in the console (DESIGN.md §19), rendered with the documents
 * the Go side actually produces. What these assert is what a reader SEES: the
 * centres and their rules, the period split by centre, the unassigned bucket
 * as a visible row rather than a silence, and the sentence that says the rows
 * add up to the invoice.
 *
 * Effects do not run under renderToString, so each surface shows its initial
 * state.
 */

const centres: CostCentre[] = [
  { id: 'cc1', customer_id: 'c1', code: 'ENG', name: 'Engineering', active: true, rules: 1, resources: 1 },
  { id: 'cc2', customer_id: 'c1', code: 'RES', name: 'Research', active: true, rules: 1, resources: 0 },
  { id: 'cc3', customer_id: 'c1', code: 'OLD', name: 'Retired line', active: false, rules: 0, resources: 0 },
]

const rules: CostCentreRule[] = [
  { id: 'r1', customer_id: 'c1', cost_centre_id: 'cc1', code: 'ENG', name: 'Engineering', tag_key: 'team', tag_value: 'platform', priority: 100 },
  { id: 'r2', customer_id: 'c1', cost_centre_id: 'cc2', code: 'RES', name: 'Research', tag_key: 'team', tag_value: 'research', priority: 100 },
]

const overrides: CostCentreResource[] = [{ customer_id: 'c1', resource_id: 'srv-4', cost_centre_id: 'cc2', code: 'RES', name: 'Research', set_by: 'ops@nc.example' }]

// The frozen breakdown of a real invoice: 10 / 20 / 10 of usage, the
// statement's own net, discount and tax apportioned across them.
const report: CostCentreReport = {
  customer_id: 'c1',
  period: '2026-09',
  currency: 'OMR',
  source: 'statement',
  statement_id: 'st1',
  status: 'issued',
  lines: [
    { code: 'RES', name: 'Research', usage: '20.000000', list: '20.000000', discount: '1.400000', net: '18.600000', tax: '0.930000', total: '19.530000' },
    { code: 'ENG', name: 'Engineering', usage: '10.000000', list: '10.000000', discount: '0.700000', net: '9.300000', tax: '0.465000', total: '9.765000' },
    { code: '(unassigned)', name: 'Unassigned', usage: '10.000000', list: '10.000000', discount: '0.700000', net: '9.300000', tax: '0.465000', total: '9.765000' },
  ],
  totals: { usage: '40.000000', list: '40.000000', discount: '2.800000', net: '37.200000', tax: '1.860000', total: '39.060000' },
  invoice: { subtotal: '37.200000', discount_total: '2.800000', tax: '1.860000', total: '39.060000', agrees: true },
}

let doc: CostCentreReport = report

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path.endsWith('/cost-centres')) return { cost_centres: centres, unassigned: { code: '(unassigned)', name: 'Unassigned' } }
    if (path.endsWith('/cost-centres/rules')) return { rules, tag_key_rule: '^[A-Za-z0-9_.:/@-]{1,128}$', default_priority: 100 }
    if (path.endsWith('/cost-centres/resources')) return { resources: overrides }
    if (path.includes('/cost-centres/report')) return doc
    throw new Error(`no fixture for ${path}`)
  }
  return {
    useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
  }
})

import { CostCentresPanel } from './CostCentresPanel'

function render(canManage: boolean): string {
  const html = renderToString(createElement(MemoryRouter, null, createElement(CostCentresPanel, { customerId: 'c1', canManage, currency: 'OMR' }))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the cost-centre page', () => {
  it('lists the centres, their rules and the per-resource overrides', () => {
    doc = report
    const html = render(true)
    expect(html).toContain('ENG')
    expect(html).toContain('Engineering')
    expect(html).toContain('RES')
    // A retired label is shown as inactive rather than hidden: it still
    // carries every figure it ever did.
    expect(html).toContain('Retired line')
    expect(html).toContain('inactive')
    // A rule reads as the tag it matches, in the order it is matched.
    expect(html).toContain('team = platform')
    expect(html).toContain('team = research')
    // The override names the resource it pins and who pinned it, and a
    // resource can be PINNED here and not only unpinned.
    expect(html).toContain('srv-4')
    expect(html).toContain('ops@nc.example')
    expect(html).toContain('Pin a resource')
    // And the page says in as many words what a cost centre is NOT.
    expect(html).toContain('not a sub-account')
  })

  it('shows the period split by centre, with the unassigned bucket VISIBLE', () => {
    doc = report
    const html = render(true)
    expect(html).toContain('By cost centre')
    expect(html).toContain('(unassigned)')
    expect(html).toContain('tag the resources or add a rule')
    // Every money column of the biggest row, as the reader sees it.
    expect(html).toContain('19.530 OMR')
    expect(html).toContain('9.765 OMR')
    expect(html).toContain('18.600 OMR')
    // The claim the whole design rests on, stated on the page.
    expect(html).toContain('The rows add up to the invoice exactly')
    expect(html).toContain('it never changes it')
  })

  it('says so when the rows do NOT add up, rather than rounding it away', () => {
    doc = { ...report, totals: { ...report.totals, total: '39.000000' }, invoice: { ...report.invoice!, agrees: false } }
    const html = render(true)
    expect(html).toContain('the breakdown and the invoice disagree')
    expect(html).not.toContain('The rows add up to the invoice exactly')
  })

  it('reads an unrated period as usage, and claims no invoice', () => {
    doc = {
      customer_id: 'c1',
      period: '2026-10',
      currency: 'OMR',
      source: 'usage',
      lines: [{ code: '(unassigned)', name: 'Unassigned', usage: '5.000000', list: '0.000000', discount: '0.000000', net: '0.000000', tax: '0.000000', total: '0.000000' }],
      totals: { usage: '5.000000', list: '0.000000', discount: '0.000000', net: '0.000000', tax: '0.000000', total: '0.000000' },
    }
    const html = render(true)
    expect(html).toContain('has not been rated yet')
    expect(html).not.toContain('The rows add up to the invoice exactly')
  })

  it('offers no edit at all without customers.manage, and says which permission', () => {
    doc = report
    const html = render(false)
    expect(html).toContain('customers.manage')
    expect(html).not.toContain('New cost centre')
    expect(html).not.toContain('New rule')
    expect(html).not.toContain('Pin a resource')
    // Reading is unaffected: the whole breakdown is still there.
    expect(html).toContain('By cost centre')
    expect(html).toContain('19.530 OMR')
  })
})
