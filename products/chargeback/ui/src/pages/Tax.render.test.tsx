import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { BillingSettings, Me, TaxCategoryRule, TaxRule } from '../api/types'

/**
 * Configure → Tax rendered with the documents the Go side produces
 * (DESIGN.md §17): the rules with their rate as a PERCENTAGE and their
 * validity, the SKU-to-category placements, the Sovereign's own
 * registration cross-linked to Billing rather than re-edited here, and the
 * rule form — including what a kind that charges nothing does to the rate.
 *
 * Effects do not run under renderToString, so each surface shows its
 * initial state; what these assert is what an operator READS.
 */

// Dates are deliberately far from today so the phase a rule is in does not
// depend on the day the suite runs.
const vat: TaxRule = {
  id: 'tr1',
  name: 'Oman VAT standard',
  country: 'OM',
  region: '',
  category: '',
  rate: '0.0500',
  kind: 'standard',
  note: '',
  effective_from: '2020-01-01',
  effective_to: '',
}

const storage: TaxRule = {
  id: 'tr2',
  name: 'Storage zero-rated',
  country: 'OM',
  region: '',
  category: 'storage',
  rate: '0.0000',
  kind: 'zero_rated',
  note: 'Zero-rated supply under Article 93 of the VAT Law.',
  effective_from: '2020-01-01',
  effective_to: '',
}

// A rate that CHANGED ON A DATE: the old band has ended, the new one has
// not started. Both are on the book, and the table says which is which.
const oldRate: TaxRule = {
  id: 'tr3',
  name: 'Oman VAT (pre-2020)',
  country: 'OM',
  region: '',
  category: '',
  rate: '0.0000',
  kind: 'standard',
  note: '',
  effective_from: '2016-01-01',
  effective_to: '2020-01-01',
}

const future: TaxRule = {
  id: 'tr4',
  name: 'Oman VAT 2099',
  country: 'OM',
  region: '',
  category: '',
  rate: '0.0750',
  kind: 'standard',
  note: '',
  effective_from: '2099-01-01',
  effective_to: '',
}

const reverse: TaxRule = {
  id: 'tr5',
  name: 'Cross-border business',
  country: 'AE',
  region: '',
  category: '',
  rate: '0.0000',
  kind: 'reverse_charge',
  note: 'Reverse charge: the recipient is liable to account for the tax on this supply.',
  effective_from: '2020-01-01',
  effective_to: '',
}

const categories: TaxCategoryRule[] = [
  { sku: 'evs.*', category: 'storage', note: 'the whole block-storage family' },
  { sku: 'k8s.vcpu', category: 'compute' },
]

const settings: BillingSettings = {
  discount_rule: 'most-specific',
  invoice_prefix: 'INV',
  credit_note_prefix: 'CN',
  commercial_provider: 'internal',
  tax_rate: '0.050000',
  tax_registration_number: 'OM100200300',
  tax_country: 'OM',
  legal_name: 'Sovereign LLC',
  address: 'Muscat',
  reminder_days: [-3, 0, 7, 14, 30],
  escalation_days: 45,
  escalation_action: 'notify',
}

let billing: BillingSettings = settings

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/tax/rules') return { rules: [vat, storage, oldRate, future, reverse], kinds: ['standard', 'zero_rated', 'exempt', 'reverse_charge', 'out_of_state'] }
    if (path === '/tax/categories') return { categories }
    if (path === '/billing-settings') return billing
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
  permissions: { sovereign: ['metering.read', 'rating.manage', 'customers.manage', 'billing.issue', 'billing.collect', 'settings.manage', 'audit.read'] },
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

import { Tax, TaxCategoryModal, TaxRuleModal } from './Tax'
import { navFor } from '../layout/Shell'

function render(Page: ComponentType, path = '/tax', url = path): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Configure → Tax lists the rules', () => {
  it('shows each rule with its rate as a PERCENTAGE, what it applies to, its kind and its validity', () => {
    session = sovereign
    billing = settings
    const html = render(Tax)
    expect(html).toContain('Oman VAT standard')
    // The wire carries 0.0500; the reader is shown 5 %.
    expect(html).toContain('5 %')
    expect(html).not.toContain('0.0500')
    expect(html).toContain('OM · every region · every category')
    expect(html).toContain('OM · every region · storage')
    // Each rule's validity, and the phase it is in on the day this runs.
    expect(html).toContain('from 2020-01-01')
    expect(html).toContain('2016-01-01 → 2020-01-01')
    expect(html).toContain('in force')
    expect(html).toContain('ended')
    expect(html).toContain('scheduled')
    // The kinds, as a reader knows them rather than as the column stores them.
    expect(html).toContain('Zero-rated')
    expect(html).toContain('Reverse charge')
    expect(html).not.toContain('zero_rated')
    // The note is what gets printed on the invoice, so it is on the row.
    expect(html).toContain('Zero-rated supply under Article 93 of the VAT Law.')
    expect(html).toContain('Reverse charge: the recipient is liable to account for the tax on this supply.')
  })

  it('lists the SKU placements, saying which row is a whole family', () => {
    session = sovereign
    billing = settings
    const html = render(Tax)
    expect(html).toContain('Categories by SKU')
    expect(html).toContain('evs.*')
    expect(html).toContain('every SKU starting with this prefix')
    expect(html).toContain('k8s.vcpu')
    expect(html).toContain('this SKU exactly')
    expect(html).toContain('the whole block-storage family')
  })

  it("cross-links the Sovereign's own identity to Billing instead of editing it twice", () => {
    session = sovereign
    billing = settings
    const html = render(Tax)
    expect(html).toContain('This Sovereign')
    expect(html).toContain('href="/billing"')
    expect(html).toContain('OM100200300')
    expect(html).toContain('Sovereign LLC')
    // The registration country is shown; it is not a second input here.
    expect(html).toContain('Registration country')
    expect(html).not.toContain('aria-label="Registration country"')
  })

  it('warns when no registration country is configured, because reverse charge cannot then be decided', () => {
    session = sovereign
    billing = { ...settings, tax_country: '' }
    const html = render(Tax)
    expect(html).toContain('not configured')
    expect(html).toContain('no cross-border determination is made')
    billing = settings
  })

  it('renders for a principal without settings.manage, and offers it nothing to change', () => {
    session = readOnly
    billing = settings
    const html = render(Tax)
    expect(html).toContain('Oman VAT standard')
    expect(html).toContain('5 %')
    expect(html).toContain('Read-only')
    expect(html).not.toContain('New rule')
    expect(html).not.toContain('Place a SKU')
    expect(html).not.toContain('>Delete<')
    session = sovereign
  })

  it('is on the Sovereign lens beside Billing, and on no customer lens', () => {
    expect(navFor(sovereign).find(([title]) => title === 'Configure')?.[1].map(([, label]) => label)).toContain('Tax')
    expect(navFor(readOnly).find(([title]) => title === 'Configure')?.[1].map(([, label]) => label)).toContain('Tax')
    const owner: Me = {
      email: 'owner@acme.example',
      role: 'customer-admin',
      customer_id: 'c1',
      permissions: { 'customer:c1': ['metering.read', 'customer.self.manage'] },
      roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: 'c1' }],
      scopes: ['customer:c1'],
    }
    for (const [, items] of navFor(owner)) expect(items.map(([, label]) => label)).not.toContain('Tax')
  })
})

describe('the rule form', () => {
  it('opens a saved rule with its rate as a percentage and its dates in place', () => {
    session = sovereign
    const Page: ComponentType = () => createElement(TaxRuleModal, { rule: vat, categories: ['compute', 'storage'], onClose: () => {}, onDone: () => {} })
    const html = render(Page)
    expect(html).toContain('Edit rule — Oman VAT standard')
    expect(html).toMatch(/aria-label="Rate"[^>]*value="5"|value="5"[^>]*aria-label="Rate"/)
    expect(html).toContain('value="OM"')
    expect(html).toContain('value="2020-01-01"')
    // The categories a SKU row placed are offered, not typed from memory.
    expect(html).toContain('value="storage"')
    // An existing rule is SAVED; only a new one is created.
    expect(html).toContain('>Save<')
    expect(html).not.toContain('Create rule')
  })

  it('a kind that charges nothing holds the rate at 0 and disables the field', () => {
    session = sovereign
    const Page: ComponentType = () => createElement(TaxRuleModal, { rule: reverse, categories: [], onClose: () => {}, onDone: () => {} })
    const html = render(Page)
    expect(html).toMatch(/aria-label="Rate"[^>]*value="0"|value="0"[^>]*aria-label="Rate"/)
    expect(html).toContain('disabled')
    expect(html).toContain('A reverse charge rule charges nothing, so its rate is 0.')
    // The note is the sentence the invoice must carry, and it is filled in.
    expect(html).toContain('Reverse charge: the recipient is liable to account for the tax on this supply.')
  })

  it('a new rule starts standard, with the empty end date explained as open-ended and EXCLUSIVE', () => {
    session = sovereign
    const Page: ComponentType = () => createElement(TaxRuleModal, { categories: [], defaultCountry: 'OM', onClose: () => {}, onDone: () => {} })
    const html = render(Page)
    expect(html).toContain('New tax rule')
    expect(html).toContain('value="OM"')
    expect(html).toContain('Empty = open-ended. The end day is EXCLUDED')
    expect(html).toContain('The only kind that carries a rate above 0.')
  })
})

describe('the SKU placement form', () => {
  it('says a prefix ending in * covers the whole family', () => {
    session = sovereign
    const Page: ComponentType = () => createElement(TaxCategoryModal, { categories: ['storage'], onClose: () => {}, onDone: () => {} })
    const html = render(Page)
    expect(html).toContain('Place a SKU in a category')
    expect(html).toContain('a PREFIX ending in * (evs.* — the whole storage family)')
    expect(html).toContain('The longest matching prefix wins, so evs.ssd.* beats evs.*.')
  })
})
