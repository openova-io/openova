import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Me } from '../api/types'

/**
 * The partner surfaces rendered with the documents the API sends
 * (DESIGN.md §11): the directory with its tiers, the margin table, and the
 * retail-rule editor with its derived book and its below-buy warning. What a
 * typecheck cannot catch — a wire field read the wrong way, a helper fed the
 * wrong shape, "NaN" in the HTML — fails here.
 */

const partners = {
  partners: [
    {
      id: 'p1',
      slug: 'resell-co',
      name: 'Resell Co',
      tier_id: 't1',
      tier_name: 'Gold',
      bill_to: 'partner',
      status: 'active',
      contact_email: 'ap@resell.example',
      party_customer_id: 'party-1',
      customer_count: 2,
      balance: 147,
      available_credit: 0,
      has_retail_rule: true,
    },
    {
      id: 'p2',
      slug: 'agent-co',
      name: 'Agent Co',
      tier_id: null,
      bill_to: 'customer',
      status: 'active',
      contact_email: 'ap@agent.example',
      party_customer_id: 'party-2',
      customer_count: 1,
      commission_pct: 20,
      balance: -20,
      available_credit: 20,
      has_retail_rule: false,
    },
  ],
}

const tiers = {
  tiers: [
    {
      id: 't1',
      name: 'Gold',
      description: '30 % off list',
      partners: 1,
      discounts: [{ id: 'd1', customer_id: null, name: 'Gold 30 %', kind: 'percent', value: 30, sku: '', starts_at: null, ends_at: null, active: true, tier_id: 't1' }],
    },
    { id: 't2', name: 'Silver', description: '', partners: 0, discounts: [] },
  ],
}

const margin = {
  partner_id: 'p1',
  period: '2026-08',
  currency: 'OMR',
  rows: [
    { customer_id: 'c1', customer_name: 'Alpha', service: 'ecs', customer_net: 90, partner_buy: 70, margin: 20, margin_pct: 22.22 },
    { customer_id: 'c2', customer_name: 'Bravo', service: 'evs', customer_net: 10, partner_buy: 10, margin: 0, margin_pct: 0 },
  ],
  totals: { customer_id: '', customer_name: '', service: '', customer_net: 100, partner_buy: 80, margin: 20, margin_pct: 20 },
}

const retail = {
  partner_id: 'p1',
  bill_to: 'partner',
  retail_rule: { partner_id: 'p1', base: 'buy', markup_pct: 5, overrides: [{ scope: 'service', key: 'evs', markup_pct: -20 }], updated_at: '2026-09-11T08:00:00Z' },
  books: [
    {
      book: {
        id: 'rb1',
        name: 'Resell Co · retail · NC list 2026',
        scope: 'cloud',
        currency: 'OMR',
        annual_divisor: 8760,
        bill_stopped: 'compute',
        partner_id: 'p1',
        derived_from_rule: true,
        derived_from_book_id: 'b1',
        items: [
          { sku: 'ecs.m7n.xlarge.8', unit: 'instance-hour', unit_price: 73.5 },
          { sku: 'evs.ssd.gb', unit: 'gb-hour', unit_price: 5.6 },
        ],
      },
      list_book_id: 'b1',
      list_book_name: 'NC list 2026',
      below_buy: [{ sku: 'evs.ssd.gb', unit: 'gb-hour', list_unit_price: 10, buy_unit_price: 7, retail_unit_price: 5.6 }],
    },
  ],
  below_buy: [{ sku: 'evs.ssd.gb', unit: 'gb-hour', list_unit_price: 10, buy_unit_price: 7, retail_unit_price: 5.6 }],
}

const partnerCustomers = {
  customers: [
    { id: 'c1', slug: 'alpha', name: 'Alpha', admin_email: 'a@alpha.example', billing_mode: 'real', status: 'active', balance: 94.5, last_statement_period: '2026-08' },
    { id: 'c2', slug: 'bravo', name: 'Bravo', admin_email: 'b@bravo.example', billing_mode: 'real', status: 'active', balance: 0 },
  ],
}

const partnerStatements = {
  statements: [
    {
      id: 'w1',
      customer_id: 'party-1',
      customer_name: 'Resell Co',
      period_start: '2026-08-01',
      period_end: '2026-08-31',
      currency: 'OMR',
      subtotal: 70,
      tax_rate: 0.05,
      tax: 3.5,
      total: 73.5,
      status: 'issued',
      issued_at: '2026-09-01T08:00:00Z',
      invoice_number: 'INV-2026-00004',
      statement_kind: 'wholesale',
      partner_id: 'p1',
      buy_total: 70,
      margin_total: 20,
    },
  ],
}

const users = {
  users: [
    { id: 'b1', subject_email: 'ap@resell.example', role: 'partner-owner', scope_kind: 'partner', partner_id: 'p1', granted_by: 'ops@nc.example', granted_at: '2026-09-11T08:00:00Z' },
    { id: 'b2', subject_email: 'v@resell.example', role: 'partner-viewer', scope_kind: 'partner', partner_id: 'p1', granted_at: '2026-09-11T08:05:00Z' },
  ],
}

const pricebooks = {
  pricebooks: [
    {
      id: 'b1',
      name: 'NC list 2026',
      scope: 'cloud',
      currency: 'OMR',
      annual_divisor: 8760,
      bill_stopped: 'compute',
      items: [
        { sku: 'ecs.m7n.xlarge.8', unit: 'instance-hour', unit_price: 100 },
        { sku: 'evs.ssd.gb', unit: 'gb-hour', unit_price: 10 },
      ],
    },
  ],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = (): unknown => {
      if (path === null) return null
      if (path === '/partners') return partners
      if (path === '/partners/tiers') return tiers
      if (path === '/partners/p1') return partners.partners[0]
      if (path === '/partners/p1/customers') return partnerCustomers
      if (path === '/partners/p1/statements') return partnerStatements
      if (path === '/partners/p1/retail-book') return retail
      if (path === '/partners/p1/users') return users
      if (path === '/pricebooks') return pricebooks
      if (path.startsWith('/partners/p1/margin')) return margin
      // The agent: no rule, no book, and the note that says why.
      if (path === '/partners/p2/retail-book')
        return { partner_id: 'p2', bill_to: 'customer', retail_rule: null, books: [], below_buy: [], note: 'This partner is an agent: we invoice its end customers at our own books and credit the partner a commission, so there is no retail book.' }
      throw new Error(`no fixture for ${path}`)
    }
    return { data: doc(), error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

let who: Me = {
  email: 'ops@nc.example',
  role: 'operator',
  permissions: { sovereign: ['metering.read', 'partners.manage', 'partner.self.manage', 'settings.manage', 'billing.collect'] },
  roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign', source: 'config' }],
}
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: who, loading: false, refresh: async () => who, logout: async () => {} }),
}))

import { Partners } from './Partners'
import { PartnerCustomers, PartnerMargin, PartnerStatements, PartnerUsers, RetailRuleEditor } from './PartnerDetail'

function render(Page: ComponentType, url = '/partners'): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Page))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Configure → Partners', () => {
  it('lists both models, what sets each buy price, and which way the balance runs', () => {
    const html = render(Partners)
    expect(html).toContain('Resell Co')
    expect(html).toContain('Agent Co')
    // The model is named in words, not as the wire value.
    expect(html).toContain('>Resell<')
    expect(html).toContain('>Agent<')
    expect(html).toContain('invoiced the wholesale statement')
    expect(html).toContain('credited a commission')
    // What sets the buy price: a tier, or a commission percentage.
    expect(html).toContain('Gold')
    expect(html).toContain('20 % commission')
    // A balance says which way it runs, never a bare signed number.
    expect(html).toContain('owes us')
    expect(html).toContain('we owe')
    expect(html).toContain('147.000')
    // The retail book column: derived, or a warning that there is no rule.
    expect(html).toContain('derived')
    // An agent has no retail book at all.
    expect(html).toContain('an agent&#x27;s customers are billed by us, at our own books')
  })

  it('shows the tiers with the percentages that make them', () => {
    const html = render(Partners)
    expect(html).toContain('aria-label="Partner tiers"')
    expect(html).toContain('30 %')
    expect(html).toContain('all SKUs')
    // A tier with no discount takes nothing off list, and says so.
    expect(html).toContain('none — this tier takes nothing off list')
    expect(html).toContain('the same engine, and the same combination rule, as a customer')
  })

  it('offers create only with partners.manage', () => {
    expect(render(Partners)).toContain('New partner')
    const was = who
    who = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
    const html = render(Partners)
    expect(html).not.toContain('New partner')
    expect(html).not.toContain('New tier')
    // It still reads the directory.
    expect(html).toContain('Resell Co')
    who = was
  })
})

describe('the margin table', () => {
  it('shows customer net, partner buy, the derived margin and its percentage', () => {
    const Page: ComponentType = () => createElement(PartnerMargin, { partnerId: 'p1' })
    const html = render(Page, '/partners/p1?tab=margin&period=2026-08')
    expect(html).toContain('aria-label="Margin"')
    expect(html).toContain('Customer net')
    expect(html).toContain('Partner buy')
    expect(html).toContain('Alpha')
    expect(html).toContain('90.000')
    expect(html).toContain('70.000')
    expect(html).toContain('20.000')
    expect(html).toContain('22.2 %')
    // A zero margin is shown as zero, not as an absence.
    expect(html).toContain('Bravo')
    expect(html).toContain('0.0 %')
    // The totals line.
    expect(html).toContain('100.000')
    expect(html).toContain('80.000')
    expect(html).toContain('every figure comes from the statements of the period')
  })
})

describe('the retail rule editor', () => {
  it('seeds from the stored rule and shows the derived book read-only', () => {
    const Page: ComponentType = () => createElement(RetailRuleEditor, { partner: partners.partners[0] })
    const html = render(Page, '/partners/p1?tab=retail%20rule')
    // The stored rule is what the form shows before anything is touched.
    expect(html).toContain('aria-label="Markup percent"')
    expect(html).toContain('value="5"')
    expect(html).toMatch(/aria-pressed="true"[^>]*>Buy price</)
    // The override, with its own markup.
    expect(html).toContain('value="evs"')
    expect(html).toContain('value="-20"')
    // The derived book, named for what it derives from and read-only.
    expect(html).toContain('Resell Co · retail · NC list 2026')
    expect(html).toContain('derived from NC list 2026 · read-only')
    expect(html).toContain('73.50000000')
    // The below-buy warning, naming the SKU.
    expect(html).toContain('below the buy price')
    expect(html).toContain('evs.ssd.gb')
    // The preview computed from the list book: list 100 → buy 70 → retail 73.5.
    expect(html).toContain('aria-label="Retail preview"')
    expect(html).toContain('Save and re-derive')
  })

  it('is read-only for a partner viewer', () => {
    const was = who
    who = {
      email: 'v@resell.example',
      role: 'partner-viewer',
      permissions: { 'partner:p1': ['metering.read'] },
      roles: [{ role: 'partner-viewer', scope_kind: 'partner', partner_id: 'p1', partner_name: 'Resell Co', customer_ids: ['party-1', 'c1'] }],
      scopes: ['partner:p1'],
    }
    const Page: ComponentType = () => createElement(RetailRuleEditor, { partner: partners.partners[0] })
    const html = render(Page, '/partners/p1?tab=retail%20rule')
    expect(html).not.toContain('Save and re-derive')
    expect(html).toContain('An owner of this partner edits the rule')
    who = was
  })

  it('says why an agent has no retail book', () => {
    const Page: ComponentType = () => createElement(RetailRuleEditor, { partner: partners.partners[1] })
    const html = renderToString(createElement(MemoryRouter, null, createElement(Page))).replace(/<!-- -->/g, '')
    expect(html).toContain('agent')
    expect(html).not.toContain('Save and re-derive')
  })
})

describe('the partner tabs', () => {
  it('lists the partner’s customers and its own statements', () => {
    const C: ComponentType = () => createElement(PartnerCustomers, { partnerId: 'p1' })
    const cust = render(C, '/partners/p1?tab=customers')
    expect(cust).toContain('Alpha')
    expect(cust).toContain('Bravo')
    expect(cust).toContain('aria-label="Partner customers"')

    const S: ComponentType = () => createElement(PartnerStatements, { partnerId: 'p1' })
    const stmts = render(S, '/partners/p1?tab=statements')
    expect(stmts).toContain('aria-label="Partner statements"')
    expect(stmts).toContain('wholesale')
    expect(stmts).toContain('INV-2026-00004')
    expect(stmts).toContain('73.500')
    expect(stmts).toContain('20.000')
    expect(stmts).toContain('a wholesale statement is what the partner pays us')
  })

  it('lists the partner users with the role each holds', () => {
    const U: ComponentType = () => createElement(PartnerUsers, { partnerId: 'p1' })
    const html = render(U, '/partners/p1?tab=users')
    expect(html).toContain('ap@resell.example')
    expect(html).toContain('Partner owner')
    expect(html).toContain('Partner viewer')
    expect(html).toContain('aria-label="Add a partner user"')
  })
})
