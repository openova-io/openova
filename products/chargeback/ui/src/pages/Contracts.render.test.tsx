import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Contract, ContractItem, Me, PriceBook } from '../api/types'

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
    { id: 'i3', kind: 'allowance', sku: 'evs.ssd.gb', unit: 'gb-hour', quantity: '744000' },
    { id: 'i4', kind: 'spend', sku: '', quantity: '0', amount: '1000', discount_pct: '50' },
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

// What the customer's books price — the line dialog's SKU select.
const acmeSKUs = {
  skus: [
    { sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', unit_price: '0.50', description: 'ECS m7n.2xlarge.8', price_book_id: 'pb1', price_book_name: 'Terms 2026', currency: 'OMR' },
    { sku: 'evs.ssd.gb', unit: 'gb-hour', unit_price: '0.00022831', description: 'SSD per GB', price_book_id: 'pb1', price_book_name: 'Terms 2026', currency: 'OMR' },
  ],
}

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/contracts') return { contracts: [acmeContract, globexContract] }
    if (path === '/contracts/ct1') return acmeContract
    if (path === '/customers') return { customers: [{ id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'fin@acme.example', status: 'active' }] }
    if (path === '/customers/c1/contracts') return { contracts: [acmeContract] }
    if (path === '/customers/c1/skus') return acmeSKUs
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

import { ContractDetail, LineEditor } from './ContractDetail'
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
  it('shows the floor, the lines in words and the renewal', () => {
    session = sovereign
    const html = render(ContractDetail, '/contracts/:id', '/contracts/ct1')
    expect(html).toContain('ACME 2026')
    // The floor is the LARGER of the 6,000 minimum and the 1,000 spend
    // commitment, and the page says so rather than adding them up.
    expect(html).toContain('Monthly floor')
    expect(html).toContain('6,000.000 OMR')
    expect(html).toContain('the larger of the minimum (6,000.000 OMR) and the spend commitment (1,000.000 OMR); the two do not add up')
    // The committed-use line and the allowance line, each explained.
    expect(html).toContain('ecs.m7n.2xlarge.8')
    expect(html).toContain('7440 instance-hour of ecs.m7n.2xlarge.8 committed each period at 30 % off list; anything above it at list.')
    expect(html).toContain('100 gb of eip.traffic_gb included each period; carried into the next period if unused.')
    expect(html).toContain('1000 OMR committed each period, whatever is used, for 50 % off everything on the bill; a period below it carries a true-up line.')
    expect(html).toContain('Issue SLA credit')
    // The spend commitment counts as a committed line: the commitment and the spend line.
    expect(html).toMatch(/Committed lines.*?>2</s)
  })

  it('the lines table shows the three kinds with their human readings, and every row has Edit and Delete', () => {
    session = sovereign
    const html = render(ContractDetail, '/contracts/:id', '/contracts/ct1')
    // The three kinds are on the table.
    expect(html).toContain('data-line-kind="commitment"')
    expect(html).toContain('data-line-kind="allowance"')
    expect(html).toContain('data-line-kind="spend"')
    // The raw quantity stays — it is what the engine uses — and the reading
    // sits under it: 7,440 instance-hours is about ten servers all month,
    // 744,000 GB-hours about a terabyte always on. A unit that is not per
    // hour (100 gb) gets no reading, and a spend line shows its amount.
    expect(html).toContain('7,440')
    expect(html).toContain('≈ 10.2 servers all month')
    expect(html).toContain('744,000')
    expect(html).toContain('≈ 1,019 GB always on')
    expect(html.match(/data-reading/g)?.length).toBe(2)
    expect(html).toContain('whole bill')
    expect(html).toContain('1,000.000 OMR')
    // Row-level CRUD (#6946): Edit and Delete on each of the four rows, and
    // one add button per kind — no whole-list editor.
    expect(html.match(/aria-label="Edit /g)?.length).toBe(4)
    expect(html.match(/aria-label="Delete /g)?.length).toBe(4)
    expect(html).toContain('aria-label="Edit spend commitment"')
    expect(html).toContain('aria-label="Delete ecs.m7n.2xlarge.8 commitment"')
    expect(html).toContain('aria-label="Edit evs.ssd.gb allowance"')
    expect(html).toContain('Add committed use')
    expect(html).toContain('Add allowance')
    expect(html).toContain('Add spend commitment')
    expect(html).not.toContain('Edit lines')
  })

  it('a read-only principal gets the record without the controls', () => {
    session = readOnly
    const html = render(ContractDetail, '/contracts/:id', '/contracts/ct1')
    expect(html).toContain('ACME 2026')
    expect(html).toContain('≈ 10.2 servers all month')
    expect(html).not.toContain('Issue SLA credit')
    expect(html).not.toContain('Add committed use')
    expect(html).not.toContain('aria-label="Edit ')
    expect(html).not.toContain('aria-label="Delete ')
    session = sovereign
  })
})

describe('the one-line dialog', () => {
  const dialog = (kind: 'commitment' | 'allowance' | 'spend', item: ContractItem | null) => {
    const Page: ComponentType = () => createElement(LineEditor, { contract: acmeContract, kind, item, onClose: () => {}, onSaved: () => {} })
    return render(Page, '/contracts/ct1')
  }

  it('chooses the SKU from what the customer’s books price, fills the unit, and shows the list and resulting prices live', () => {
    session = sovereign
    const html = dialog('commitment', acmeContract.items![0])
    expect(html).toContain('Edit ecs.m7n.2xlarge.8 commitment')
    // A SELECT, never a text box, and both priced SKUs are on it.
    expect(html).toMatch(/<select[^>]*aria-label="SKU"/)
    expect(html).toContain('ecs.m7n.2xlarge.8 — ECS m7n.2xlarge.8')
    expect(html).toContain('evs.ssd.gb — SSD per GB')
    expect(html).toContain('priced by Terms 2026')
    // The unit and the list price come from the book and are read-only.
    expect(html).toMatch(/aria-label="Unit"[^>]*value="instance-hour"|value="instance-hour"[^>]*aria-label="Unit"/)
    expect(html).toMatch(/readonly/i)
    expect(html).toMatch(/value="0\.5"/)
    // 30 % off a list of 0.50 is 0.35 — shown under the percentage, live.
    expect(html).toContain('→ 0.35 OMR per instance-hour')
    // The human reading sits under the quantity field.
    expect(html).toContain('≈ 10.2 servers all month')
    expect(html).toContain('7440 instance-hour of ecs.m7n.2xlarge.8 committed each period at 30 % off list')
  })

  it('an allowance offers the carry-over and no rate; a spend commitment asks for the amount and the percentage only', () => {
    session = sovereign
    const allowance = dialog('allowance', acmeContract.items![2])
    expect(allowance).toContain('Edit evs.ssd.gb allowance')
    expect(allowance).toContain('Carry the unused part into the next period')
    expect(allowance).not.toContain('Percent off list')
    expect(allowance).toContain('≈ 1,019 GB always on')

    const spend = dialog('spend', null)
    expect(spend).toContain('Add spend commitment')
    expect(spend).toContain('Amount per period (OMR)')
    expect(spend).toContain('aria-label="Spend discount percent"')
    expect(spend).not.toContain('aria-label="SKU"')
    expect(spend).not.toContain('Quantity per period')
    expect(spend).toContain('whether it uses anything or not')
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
