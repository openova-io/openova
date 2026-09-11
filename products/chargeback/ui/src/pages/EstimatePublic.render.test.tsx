import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The public calculator page (DESIGN.md §11) rendered with the catalog in
 * place of the API: the list prices, the plans, the pay-per-use rates and
 * the footer that says what these prices are. It must render with no
 * session — the page never reads one — and `?embed=1` must drop the header
 * so the marketplace can frame it.
 */

const catalog = {
  price_book: { id: 'pb1', name: 'NC list 2026', updated_at: '2026-09-01T00:00:00Z' },
  currency: 'OMR',
  tax_rate: '0.0500',
  regions: ['me-east-215-a'],
  skus: [
    { sku: 'ecs.s6.large.2', service: 'ecs', unit: 'instance-hour', unit_price: '0.10000000', monthly: '73.000000', description: 'General computing 2 vCPU 4 GB' },
    { sku: 'evs.ssd.gb', service: 'evs', unit: 'gb-hour', unit_price: '0.00013699', monthly: '0.100003', description: 'SSD block storage per GB' },
  ],
  plans: [{ slug: 'm', name: 'M', sku: 'plan.m', unit: 'plan-hour', unit_price: '0.01232877', monthly: '9.000002', vcpu: 4, memory_gib: 8 }],
  payg: [{ sku: 'k8s.vcpu', unit: 'vcpu-hour', unit_price: '0.00273973', monthly: '2.000003' }],
  hours_per_month: 730,
  list_prices: true,
  notice: 'List prices. Taxes are shown separately. A negotiated price, a discount or a partner rate is never part of this estimate; contact us for a proposal.',
  generated_at: '2026-09-11T10:00:00Z',
}

const estimate = {
  id: 'e1',
  lines: [{ sku: 'ecs.s6.large.2', unit: 'instance-hour', quantity: '1', hours: '730', months: 1, rated_quantity: '730.000000', unit_price: '0.10000000', amount: '73.000000' }],
  currency: 'OMR',
  region: 'me-east-215-a',
  subtotal: '73.000000',
  tax_rate: '0.0500',
  tax: '3.650000',
  total: '76.650000',
  monthly: '76.650000',
  yearly: '919.800000',
  price_book: catalog.price_book,
  list_prices: true,
  lead: false,
  created_at: '2026-09-11T10:00:00Z',
  valid_until: '2026-10-11T10:00:00Z',
  share_url: 'https://billing.t99.omani.works/estimate/e1',
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/public/catalog' ? catalog : path === '/public/estimates/e1' ? estimate : null,
    error: '',
    loading: false,
    reload: async () => {},
    setData: () => {},
  }),
}))

import { EstimatePublic } from './EstimatePublic'

function render(entry: string, path = '/estimate') {
  return renderToString(
    createElement(MemoryRouter, { initialEntries: [entry] }, createElement(Routes, null, createElement(Route, { path, element: createElement(EstimatePublic) }))),
  ).replace(/<!-- -->/g, '')
}

describe('the public calculator page', () => {
  it('shows the list prices, the plans and the pay-per-use rates, with the price book and the notice in the footer', () => {
    const html = render('/estimate')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Cost calculator')
    expect(html).toContain('ecs.s6.large.2')
    expect(html).toContain('73.000 OMR')
    expect(html).toContain('M</strong>')
    expect(html).toContain('9.000 OMR')
    expect(html).toContain('k8s.vcpu')
    expect(html).toContain('NC list 2026')
    expect(html).toContain('prices as of')
    expect(html).toContain('A negotiated price, a discount or a partner rate is never part of this estimate')
    // The tax rate is its own line, named as a percentage.
    expect(html).toContain('Tax (5.0 %)')
    // Nothing of the console: no sidebar, no sign-out, no operator menu.
    expect(html).not.toContain('Sign out')
    expect(html).not.toContain('aside')
    expect(html).not.toContain('Cost explorer')
    // An empty cart says what to do, and quotes no total.
    expect(html).toContain('Add a plan or a service to see what a month costs.')
  })

  it('drops the header in embed mode and keeps the prices', () => {
    const html = render('/estimate?embed=1')
    expect(html).not.toContain('<h1>')
    expect(html).not.toContain('Cost calculator')
    expect(html).toContain('ecs.s6.large.2')
    expect(html).toContain('NC list 2026')
  })

  it('renders a shared estimate read-only, with its validity and the book it was priced from', () => {
    const html = render('/estimate/e1', '/estimate/:id')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Cost estimate')
    expect(html).toContain('730.000000')
    expect(html).toContain('76.650 OMR')
    expect(html).toContain('919.800 OMR')
    expect(html).toContain('valid until')
    expect(html).toContain('NC list 2026')
    // Read-only: no cart controls on a shared estimate.
    expect(html).not.toContain('Share estimate')
    expect(html).not.toContain('Send me this estimate')
  })
})
