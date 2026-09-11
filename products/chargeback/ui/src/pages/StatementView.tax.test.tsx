import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * What an invoice says about tax under DESIGN.md §17: the summary BY RATE —
 * one row per rule that applied, frozen at issue — and the e-invoice state,
 * including one that stopped short of submission with the reason shown in
 * full. The signing KEY is never shown and never asked for.
 */

// 1,000 net: 800 of compute at 5 %, 200 of storage zero-rated. Two rates on
// one invoice, and their taxes add up to the 40.000 the invoice carries.
const base = {
  id: 'st1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  customer_slug: 'acme',
  period_start: '2026-08-01',
  period_end: '2026-08-31',
  currency: 'OMR',
  subtotal: '1000.000',
  discount_total: '0',
  tax_rate: '0.04',
  tax: '40.000',
  total: '1040.000',
  status: 'sent',
  effective_status: 'sent',
  invoice_number: 'INV-2026-00001',
  payment_terms_days: 30,
  due_at: '2099-10-01T00:00:00Z',
  issued_at: '2026-09-01T00:00:00Z',
  created_at: '2026-09-01T00:00:00Z',
  paid_total: '0',
  credited_total: '0',
  balance: '1040.000',
  lines: [{ id: 7, sku: 'k8s.vcpu', unit: 'vcpu-hour', quantity: '744', unit_price: '1.075269', amount: '800.000', resource_count: 1, source_id: 'src-a', tax_category: 'compute', tax_rule_id: 'tr1' }],
  tax_lines: [
    { rule_id: 'tr1', rule_name: 'Oman VAT standard', kind: 'standard', category: 'compute', rate: '0.0500', base: '800.000', tax: '40.000' },
    { rule_id: 'tr2', rule_name: 'Storage zero-rated', kind: 'zero_rated', category: 'storage', rate: '0.0000', base: '200.000', tax: '0.000', note: 'Zero-rated supply under Article 93 of the VAT Law.' },
  ],
  tax_snapshot: {
    rate: '0.04',
    exempt: false,
    customer_name: 'ACME LLC',
    customer_country: 'OM',
    seller_country: 'OM',
    audit: ['customer rate override 0.0400 applied instead of 0.0500'],
  },
}

const archived = {
  profile: 'zatca',
  invoice_number: 'INV-2026-00001',
  state: 'not_submitted',
  hash: 'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9',
  signature: 'MEUCIQD…',
  signature_algorithm: 'RSA-SHA256',
  key_id: 'sovereign-2026',
  submit_reason: 'the tax authority has published no submission endpoint for this profile; the document is signed and archived here',
  built_at: '2026-09-01T00:10:00Z',
}

let statement: Record<string, unknown> = base

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = path === '/statements/st1' ? statement : path === '/statements/st1/disputes' ? { disputes: [] } : path === '/customers/c1/sources' ? { sources: [] } : null
    return { data: doc, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

const operatorMe = { email: 'ops@nc.example', role: 'operator' }
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: operatorMe, loading: false, refresh: async () => null, logout: async () => {} }),
}))

import { StatementView } from './StatementView'

function render(): string {
  const html = renderToString(
    createElement(MemoryRouter, { initialEntries: ['/statements/st1'] }, createElement(Routes, null, createElement(Route, { path: '/statements/:id', element: createElement(StatementView) }))),
  ).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the tax summary by rate (§17)', () => {
  it('shows one row per rule — both rates, both bases, and the note the rule makes the invoice carry', () => {
    statement = base
    const html = render()
    expect(html).toContain('Tax summary')
    expect(html).toContain('2 rules applied · 2 rates')
    // Both rates, as percentages; the wire carried fractions.
    expect(html).toContain('5 %')
    expect(html).toContain('0 %')
    // The rate CELL is the percentage; the fraction never reaches the table.
    expect(html).toMatch(/<td class="nowrap">5 %<span class="sub">Oman VAT standard<\/span><\/td>/)
    expect(html).toMatch(/<td class="nowrap">0 %<span class="sub">Storage zero-rated<\/span><\/td>/)
    expect(html).toContain('Oman VAT standard')
    expect(html).toContain('Storage zero-rated')
    expect(html).toContain('Zero-rated')
    // The taxable base each rate covers, and the tax on it.
    expect(html).toContain('800.000 OMR')
    expect(html).toContain('200.000 OMR')
    expect(html).toContain('Zero-rated supply under Article 93 of the VAT Law.')
  })

  it('the rows add up to the tax on the invoice, and say so', () => {
    statement = base
    const html = render()
    expect(html).toContain('These rows add up to the 40.000 OMR of tax on this invoice.')
    expect(html).not.toContain('the summary and the total disagree')
    // The base the summary covers is the net it was taxed on.
    expect(html).toContain('1,000.000 OMR')
  })

  it('says so plainly when they do NOT add up', () => {
    statement = { ...base, tax: '55.000', total: '1055.000', balance: '1055.000' }
    const html = render()
    expect(html).toContain('the summary and the total disagree')
  })

  it('records why, where the rules alone did not decide it', () => {
    statement = base
    const html = render()
    expect(html).toContain('Why, where the rules alone did not decide it')
    expect(html).toContain('customer rate override 0.0400 applied instead of 0.0500')
  })

  it('shows no summary block on a statement rated before the rules existed', () => {
    statement = { ...base, tax_lines: null, tax_snapshot: { rate: '0.04', exempt: false, customer_name: 'ACME LLC' } }
    const html = render()
    expect(html).not.toContain('Tax summary')
  })
})

describe('the e-invoice (§17)', () => {
  it('renders not_submitted WITH its reason in full, and offers the signed XML', () => {
    statement = { ...base, einvoice: archived }
    const html = render()
    expect(html).toContain('E-invoice')
    expect(html).toContain('Not submitted')
    expect(html).toContain('the tax authority has published no submission endpoint for this profile; the document is signed and archived here')
    // The download points at the archival XML, which the server sends as an
    // attachment.
    expect(html).toContain('href="/api/v1/statements/st1/einvoice.xml"')
    expect(html).toContain('Download the signed XML')
    expect(html).toContain('href="/api/v1/statements/st1/einvoice"')
  })

  it('shows the hash and the signature algorithm, and no key material', () => {
    statement = { ...base, einvoice: archived }
    const html = render()
    expect(html).toContain('b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9')
    expect(html).toContain('RSA-SHA256')
    expect(html).toContain('sovereign-2026')
    expect(html).toContain('the signing key stays on the server; it is never shown here and never asked for')
    // The signature blob itself is in the archived document, not on the page.
    expect(html).not.toContain('MEUCIQD')
    expect(html).not.toContain('PRIVATE KEY')
  })

  it('a submitted document names its authority reference, and the moment it was accepted', () => {
    statement = { ...base, einvoice: { ...archived, state: 'submitted', submit_reason: '', submit_reference: 'ZATCA-88123', submitted_at: '2026-09-01T00:12:00Z' } }
    const html = render()
    expect(html).toContain('Submitted')
    expect(html).toContain('ZATCA-88123')
    expect(html).toContain('Accepted by the tax authority')
    expect(html).not.toContain('Not submitted')
  })

  it('offers no archival copy before one exists', () => {
    statement = { ...base, einvoice: { ...archived, state: 'built', hash: '', signature: '', signature_algorithm: '', key_id: '', submit_reason: '' } }
    const html = render()
    expect(html).toContain('E-invoice')
    expect(html).not.toContain('Download the signed XML')
    expect(html).toContain('No archival copy yet')
  })

  it('says nothing at all where no profile is configured', () => {
    statement = base
    expect(render()).not.toContain('E-invoice')
  })
})
