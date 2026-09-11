import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Customer, Me } from '../api/types'

/**
 * The customer's tax block (DESIGN.md §17): where it is registered, whether
 * it is a registered BUSINESS — which is what makes reverse charge apply —
 * and the exemption CERTIFICATE behind the exempt flag.
 *
 * The certificate assertion is the point of the block: an EXPIRED
 * certificate is not an exemption, the rating engine falls back to the
 * standard rate, and an issuer that is not told carries the liability. The
 * dates here are far from today so the suite does not depend on the day it
 * runs.
 */

const base: Customer = {
  id: 'c1',
  slug: 'acme',
  name: 'ACME LLC',
  admin_email: 'fin@acme.example',
  kind: 'external',
  billing_mode: 'chargeback',
  status: 'active',
  charging: 'billed',
  payment_model: 'postpaid',
  payment_method: 'transfer',
  payment_terms_days: 30,
  tax_country: 'AE',
  tax_region: 'Dubai',
  tax_business: true,
  tax_registration_number: 'AE100200300',
}

const expiredCertificate: Customer = {
  ...base,
  tax_exempt: true,
  tax_exempt_reason: 'government entity',
  tax_exemption_number: 'CERT-9001',
  tax_exemption_expires_on: '2020-01-31T00:00:00Z',
  tax_exemption_scan_ref: 'dms://certs/9001.pdf',
}

const validCertificate: Customer = { ...expiredCertificate, tax_exemption_number: 'CERT-9002', tax_exemption_expires_on: '2099-12-31T00:00:00Z' }

let customer: Customer = base

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/billing-settings') return { discount_rule: 'most-specific', commercial_provider: 'internal', tax_country: 'OM' }
    if (path === '/customers/c1') return customer
    if (path === '/customers/c1/sources') return { sources: [{ id: 'src1', customer_id: 'c1', kind: 'huawei-project', region: 'me-east-215', project_id: 'p1', status: 'verified', price_book_id: 'pb1' }] }
    if (path === '/customers/c1/users') return { users: [] }
    if (path === '/customers/c1/cost/summary') return null
    if (path === '/pricebooks') return { pricebooks: [] }
    if (path === '/partners') return { partners: [] }
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
  permissions: { sovereign: ['metering.read', 'customers.manage', 'billing.collect', 'settings.manage', 'customer.self.manage', 'partners.manage'] },
  scopes: ['sovereign'],
}

vi.mock('../auth/session', () => ({
  useSession: () => ({ me: sovereign, loading: false, logout: async () => {}, reload: async () => {} }),
  SessionProvider: ({ children }: { children: unknown }) => children,
}))

import { CustomerDetail } from '../pages/CustomerDetail'
import { SettingsPanel } from './SettingsPanel'

function render(Page: ComponentType, path: string, url = path): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

function panel(c: Customer): string {
  const Page: ComponentType = () => createElement(SettingsPanel, { customer: c, onSaved: () => {} })
  return render(Page, '/customers/c1')
}

describe("the customer's tax block", () => {
  it('carries the country, the region, the registered-business flag and the registration number', () => {
    const html = panel(base)
    expect(html).toContain('Tax country')
    expect(html).toMatch(/aria-label="Tax country"[^>]*value="AE"|value="AE"[^>]*aria-label="Tax country"/)
    expect(html).toMatch(/aria-label="Tax region"[^>]*value="Dubai"|value="Dubai"[^>]*aria-label="Tax region"/)
    expect(html).toContain('Registered business')
    expect(html).toContain('value="AE100200300"')
    // WHY the flag exists, in the sentence that explains reverse charge.
    expect(html).toContain('Reverse charge applies to a registered business in another country and never to a consumer')
    // The rates themselves are the rules page's, and it is linked.
    expect(html).toContain('href="/tax"')
  })

  it('shows the certificate — number, expiry and scan reference — once the customer is exempt', () => {
    const html = panel(validCertificate)
    expect(html).toContain('Certificate number')
    expect(html).toContain('value="CERT-9002"')
    expect(html).toContain('value="2099-12-31"')
    expect(html).toContain('value="dms://certs/9001.pdf"')
    expect(html).toContain('Certificate scan')
  })

  it('marks an EXPIRED certificate expired, and says the standard rate now applies', () => {
    const html = panel(expiredCertificate)
    expect(html).toContain('notice bad')
    expect(html).toContain('CERT-9001')
    expect(html).toContain('expired on 2020-01-31')
    expect(html).toContain('rated at the standard rate until a current certificate is recorded')
  })

  it('does NOT mark a certificate that is still in force', () => {
    const html = panel(validCertificate)
    expect(html).not.toContain('expired on')
    expect(html).not.toContain('notice bad')
    expect(html).toContain('in force through 2099-12-31')
  })

  it('says nothing about a certificate on a customer that is not exempt', () => {
    const html = panel(base)
    expect(html).not.toContain('Certificate number')
    expect(html).not.toContain('expired on')
  })
})

describe('an expired certificate is banner-level on the customer page', () => {
  // "Record a current certificate" is the page banner's own wording — the
  // panel notice never says it — so its presence is the banner and not the
  // form underneath.
  it('banners it, and links to where it is replaced', () => {
    customer = expiredCertificate
    const html = render(CustomerDetail, '/customers/:id', '/customers/c1?tab=settings')
    expect(html).toContain('The exemption certificate')
    expect(html).toContain('CERT-9001')
    expect(html).toContain('expired on 2020-01-31')
    expect(html).toContain('Record a current certificate')
  })

  it('says nothing when the certificate is in force', () => {
    customer = validCertificate
    const html = render(CustomerDetail, '/customers/:id', '/customers/c1?tab=settings')
    expect(html).not.toContain('Record a current certificate')
    expect(html).not.toContain('expired on')
    customer = base
  })
})
