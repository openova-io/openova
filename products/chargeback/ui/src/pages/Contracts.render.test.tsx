import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Contract, Me, PriceBook } from '../api/types'

/**
 * The contracts surfaces rendered with the documents the Go side produces
 * (DESIGN.md §15.9): the directory with its renewals-due call-out, one
 * contract with its committed-use and allowance lines, the customer page's
 * Contract tab, and the price-book editor's effective-price column with the
 * ladder explained in words.
 *
 * Effects do not run under renderToString, so every follow-up fetch shows
 * its initial state; what these assert is the SHAPE of what an operator
 * reads — the numbers, the words, and the deadline.
 */

const acmeContract: Contract = {
  id: 'ct1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  customer_slug: 'acme',
  name: 'ACME 2026',
  starts_on: '2026-01-01',
  ends_on: '2026-12-31',
  term_months: 12,
  auto_renew: true,
  renewal_notice_days: 30,
  minimum_commitment: '6000',
  currency: 'OMR',
  status: 'active',
  po_reference: 'PO-4411',
  renewal_date: '2027-01-01',
  notice_from: '2026-12-01',
  renewal_count: 0,
  items: [
    { id: 'i1', kind: 'commitment', sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '7440', discount_pct: '30' },
    { id: 'i2', kind: 'allowance', sku: 'eip.traffic_gb', unit: 'gb', quantity: '100', rollover: true },
  ],
}

// A contract INSIDE its notice window, and one already expired.
const globexContract: Contract = {
  id: 'ct2',
  customer_id: 'c2',
  customer_name: 'Globex',
  name: 'Globex 2026',
  starts_on: '2026-01-01',
  ends_on: '2026-12-31',
  term_months: 12,
  auto_renew: false,
  renewal_notice_days: 3650, // always inside its window, whatever today is
  currency: 'OMR',
  status: 'active',
  renewal_date: '2027-01-01',
  notice_from: '2017-01-06',
  items: [],
}

const book: PriceBook = {
  id: 'pb1',
  name: 'Terms 2026',
  scope: 'cloud',
  currency: 'OMR',
  annual_divisor: 8760,
  bill_stopped: 'compute',
  items: [
    {
      sku: 'object_gib',
      unit: 'gb-month',
      unit_price: '0.010',
      description: 'Object storage',
      tier_mode: 'graduated',
      tiers: [
        { up_to: '10240', price: '0.010' },
        { up_to: '102400', price: '0.008' },
        { up_to: null, price: '0.006' },
      ],
    },
    { sku: 'eip.traffic_gb', unit: 'gb', unit_price: '0.05', description: 'Egress', allowance: '50' },
    { sku: 'ecs.a', unit: 'hour', unit_price: '0.50', description: 'Compute' },
  ],
}

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/contracts') return { contracts: [acmeContract, globexContract] }
    if (path === '/contracts/ct1') return acmeContract
    if (path === '/customers') return { customers: [{ id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'fin@acme.example', status: 'active' }] }
    if (path === '/customers/c1/contracts') return { contracts: [acmeContract] }
    if (path === '/pricebooks/pb1') return book
    if (path === '/pricebooks/pb1/coverage') return { customers: [], sources: [], skus_in_use: [], coverage_pct: 0, unpriced_count: 0 }
    if (path.startsWith('/statements?')) return { statements: [] }
    throw new Error(`no fixture for ${path}`)
  }
  return {
    useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
  }
})

const sovereign: Me = {
  email: 'ops@nc.example',
  role: 'sovereign-admin',
  roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }],
  permissions: { sovereign: ['metering.read', 'rating.manage', 'customers.manage', 'billing.issue', 'billing.collect', 'settings.manage', 'audit.read', 'capacity.manage', 'partners.manage'] },
  scopes: ['sovereign'],
}

const readOnly: Me = {
  email: 'fin@nc.example',
  role: 'finance-viewer',
  roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }],
  permissions: { sovereign: ['metering.read', 'audit.read'] },
  scopes: ['sovereign'],
}

let session: Me = sovereign
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: session, loading: false, logout: async () => {}, reload: async () => {} }),
  SessionProvider: ({ children }: { children: unknown }) => children,
}))

import { ContractDetail } from './ContractDetail'
import { Contracts } from './Contracts'
import { PriceBookEdit } from './PriceBookEdit'
import { ContractPanel } from '../panels/ContractPanel'

function render(Page: ComponentType, path: string, url = path): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Configure → Contracts', () => {
  it('lists the term, the minimum, the status and the renewal date', () => {
    session = sovereign
    const html = render(Contracts, '/contracts')
    expect(html).toContain('ACME 2026')
    expect(html).toContain('ACME LLC')
    expect(html).toContain('12 months')
    expect(html).toContain('2026-01-01')
    expect(html).toContain('2026-12-31')
    // The monthly minimum, formatted as money, and the renewal date.
    expect(html).toContain('6,000.000 OMR')
    expect(html).toContain('2027-01-01')
    expect(html).toContain('PO-4411')
    expect(html).toContain('New contract')
  })

  it('calls out the renewals due inside their notice window, and says what happens', () => {
    session = sovereign
    const html = render(Contracts, '/contracts')
    expect(html).toContain('Renewals due')
    // Globex does NOT auto-renew and is inside its window: the page says so
    // in those words, because that is the one thing on the page to act on.
    expect(html).toContain('Globex 2026')
    expect(html).toContain('expires unless it is renewed')
  })

  it('hides the write control from a principal without customers.manage', () => {
    session = readOnly
    const html = render(Contracts, '/contracts')
    expect(html).toContain('ACME 2026')
    expect(html).not.toContain('New contract')
    session = sovereign
  })
})

describe('one contract', () => {
  it('shows the minimum, the lines in words and the renewal', () => {
    session = sovereign
    const html = render(ContractDetail, '/contracts/:id', '/contracts/ct1')
    expect(html).toContain('ACME 2026')
    expect(html).toContain('Monthly minimum')
    expect(html).toContain('6,000.000 OMR')
    expect(html).toContain('a period whose net falls below it carries a true-up line')
    // The committed-use line and the allowance line, each explained.
    expect(html).toContain('ecs.m7n.2xlarge.8')
    expect(html).toContain('7440 instance-hour of ecs.m7n.2xlarge.8 committed each period at 30 % off list; anything above it at list.')
    expect(html).toContain('100 gb of eip.traffic_gb included each period; carried into the next period if unused.')
    expect(html).toContain('Issue SLA credit')
    expect(html).toContain('Edit lines')
  })

  it('a read-only principal gets the record without the controls', () => {
    session = readOnly
    const html = render(ContractDetail, '/contracts/:id', '/contracts/ct1')
    expect(html).toContain('ACME 2026')
    expect(html).not.toContain('Issue SLA credit')
    expect(html).not.toContain('Edit lines')
    session = sovereign
  })
})

describe("the customer page's Contract tab", () => {
  it('puts the agreement where the customer is', () => {
    session = sovereign
    const Page: ComponentType = () =>
      createElement(ContractPanel, {
        customer: { id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'fin@acme.example', kind: 'external', billing_mode: 'chargeback', status: 'active', charging: 'billed', payment_terms_days: 30 },
        canManage: true,
      })
    const html = render(Page, '/customers/c1')
    expect(html).toContain('ACME 2026')
    expect(html).toContain('Monthly minimum 6,000.000 OMR')
    expect(html).toContain('a period below it carries a true-up line')
    expect(html).toContain('Renews for another 12 months on 2027-01-01')
    expect(html).toContain('7440 instance-hour of ecs.m7n.2xlarge.8 committed each period')
  })
})

describe('the price-book item editor', () => {
  it('explains a tiered item IN WORDS, band by band, under its row', () => {
    session = sovereign
    const html = render(PriceBookEdit, '/pricebooks/:id', '/pricebooks/pb1')
    expect(html).toContain('Effective price')
    expect(html).toContain('Graduated · 3 bands')
    // The sentence, exactly as rating.ExplainItem produces it in Go.
    expect(html).toContain('Each band rates at its own price: up to 10240 at 0.01 OMR per gb-month, 10240-102400 at 0.008 OMR per gb-month, above 102400 at 0.006 OMR per gb-month.')
    // The allowance item says what is included and that it lapses.
    expect(html).toContain('50 gb included')
    expect(html).toContain('The first 50 gb each period are included; unused allowance lapses at the end of the period; the rest at 0.05 OMR per gb.')
    // An item with no shape is not dressed up as one.
    expect(html).toContain('flat rate')
    expect(html).toContain('Add tiers or allowance')
    expect(html).toContain('Edit shapes')
  })
})
