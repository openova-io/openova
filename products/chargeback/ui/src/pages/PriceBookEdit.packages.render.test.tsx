import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { packages } from '../panels/estimate/fixture'

/**
 * The Packages tab of a price book, rendered (DESIGN.md §22): the book's
 * plan items as columns with their price per month, every feature as a row,
 * each cell a chip in one of the THREE states — Included, Optional with its
 * add-on price and the "included from XL" hint, Not offered — a quantity
 * feature showing how much is included, a feature not yet in the book
 * reading not offered everywhere, "Add feature" and a row's Edit / Delete.
 * The default tab is still the items, untouched.
 */

vi.mock('../lib/useQuery', () => {
  const book = {
    id: 'pb1',
    name: 'OpenOva plans',
    scope: 'platform',
    currency: 'OMR',
    annual_divisor: 8760,
    bill_stopped: 'compute',
    created_at: '2026-01-01T00:00:00Z',
    items: [
      { sku: 'plan.s', unit: 'plan-hour', unit_price: '0.00684932', annual_price: '60' },
      { sku: 'plan.m', unit: 'plan-hour', unit_price: '0.01232877', annual_price: '108' },
      { sku: 'plan.l', unit: 'plan-hour', unit_price: '0.02191781', annual_price: '192' },
      { sku: 'plan.xl', unit: 'plan-hour', unit_price: '0.04109589', annual_price: '360' },
      { sku: 'addon.backup', unit: 'plan-hour', unit_price: '0.00205479', annual_price: '18' },
    ],
  }
  const coverage = { customers: [], sources: [], skus_in_use: [], coverage_pct: 100, unpriced_count: 0, not_sold_count: 0 }
  const features = {
    features: [
      { id: 'f1', key: 'ssl', name: 'Unlimited free SSL', blurb: 'Certificates for every site, renewed for you', kind: 'boolean', sort_order: 1 },
      { id: 'f2', key: 'backup', name: 'Backup', blurb: 'Daily backups of your sites and databases, kept 30 days', kind: 'boolean', addon_sku: 'addon.backup', sort_order: 2 },
      { id: 'f3', key: 'dedicated_ip', name: 'Dedicated IP address', blurb: 'A public address of your own', kind: 'boolean', addon_sku: 'addon.dedicated_ip', sort_order: 3 },
      { id: 'f4', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', sort_order: 4 },
      { id: 'f5', key: 'sso', name: 'SSO', blurb: 'One sign-in for every application', kind: 'boolean', sort_order: 5 },
    ],
  }
  const docFor = (path: string) => {
    if (path === '/pricebooks/pb1') return book
    if (path === '/pricebooks/pb1/coverage') return coverage
    if (path === '/pricebooks/pb1/packages') return packages
    if (path === '/features') return features
    throw new Error(`no fixture for ${path}`)
  }
  return { useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }) }
})

import { PriceBookEdit } from './PriceBookEdit'

function render(url: string): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path: '/pricebooks/:id', element: createElement(PriceBookEdit) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the Packages tab of a price book', () => {
  it('draws the matrix: one column per plan item with its price, one row per feature, the three states as chips', () => {
    const html = render('/pricebooks/pb1?tab=packages')
    expect(html).toContain('aria-label="Package matrix"')
    // The columns, cheapest first, priced per month.
    const heads = [...html.matchAll(/<th class="pkg-head"><div>([A-Z]+)<\/div><div class="small muted num">([0-9.]+) OMR \/ month/g)].map((m) => [m[1], m[2]])
    expect(heads).toEqual([
      ['S', '5.000'],
      ['M', '9.000'],
      ['L', '16.000'],
      ['XL', '30.000'],
    ])
    // Included.
    expect(html).toMatch(/data-testid="cell-ssl-plan\.s"><span>Included<\/span>/)
    // Optional with its add-on price and the hint; the chip's class is the state.
    expect(html).toMatch(/class="pkg-cell optional" aria-label="Backup on M"[^>]*><span>\+ 1\.500 OMR \/ month<\/span><span class="tiny muted">included from XL<\/span>/)
    expect(html).toMatch(/class="pkg-cell included" aria-label="Backup on XL"[^>]*><span>Included<\/span>/)
    // Not offered.
    expect(html).toMatch(/class="pkg-cell not_offered" aria-label="Dedicated IP address on L"[^>]*><span>Not offered<\/span>/)
    // A quantity feature shows how much is included.
    expect(html).toMatch(/data-testid="cell-bandwidth-plan\.s"><span>50 Mbps<\/span>/)
    expect(html).toMatch(/data-testid="cell-bandwidth-plan\.xl"><span>1000 Mbps<\/span>/)
    // A feature with no cell in this book yet reads not offered everywhere and says so.
    const sso = html.slice(html.indexOf('data-testid="feature-row-sso"'))
    expect(sso).toContain('not in this book yet')
    expect((sso.slice(0, sso.indexOf('</tr>')).match(/pkg-cell not_offered/g) ?? []).length).toBe(4)
    // The row actions and the add button.
    expect(html).toContain('>Add feature<')
    expect(html).toContain('>Edit<')
    expect(html).toContain('>Delete<')
    // The tab strip names both tabs and marks this one.
    expect(html).toMatch(/class="tab active" aria-current="page"[^>]*><span>Packages<\/span>/)
    // The items table is not on this tab.
    expect(html).not.toContain('>Add item<')
  })

  it('keeps the items as the default tab, with the matrix off the page', () => {
    const html = render('/pricebooks/pb1')
    expect(html).toContain('>Add item<')
    expect(html).toContain('addon.backup')
    expect(html).not.toContain('aria-label="Package matrix"')
    expect(html).toMatch(/class="tab active" aria-current="page"[^>]*><span>Items<\/span>/)
    // The tab's count is the book's plan items — four packages.
    expect(html).toMatch(/<span>Packages<\/span><span class="count">4<\/span>/)
  })
})
