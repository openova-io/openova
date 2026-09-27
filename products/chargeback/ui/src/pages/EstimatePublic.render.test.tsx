import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { catalog } from '../panels/estimate/fixture'

/**
 * The public calculator page (DESIGN.md §12.5) rendered with the catalog in
 * place of the API: the service catalogue by product family, the empty
 * estimate that says what to do, and the footer that says what these
 * prices are. It must render with no session — the page never reads one —
 * and `?embed=1` must drop the header so the marketplace can frame it.
 * The click-through (configurator, add, edit, remove) is
 * EstimatePublic.dom.test.tsx.
 */

const estimate = {
  id: 'e1',
  lines: [{ sku: 'ecs.s7n.xlarge.4', unit: 'instance-hour', quantity: '1', hours: '730', months: 1, rated_quantity: '730.000000', unit_price: '0.09138567', amount: '66.711539' }],
  currency: 'OMR',
  region: 'me-east-215-a',
  subtotal: '66.711539',
  tax_rate: '0.0500',
  tax: '3.335577',
  total: '70.047116',
  monthly: '70.047116',
  yearly: '840.565392',
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
  it('shows the service catalogue by product family, an empty estimate that says what to do, and the price book in the footer', () => {
    const html = render('/estimate')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Cost calculator')
    // Families in product order, each service with a friendly name, a
    // one-line description, its "from" figure and a Configure button.
    const at = (s: string) => html.indexOf(s)
    expect(['Compute', 'Storage', 'Networking', 'Databases', 'Containers', 'Platform plans', 'Other services'].map((f) => at(`<h2>${f}</h2>`))).toEqual(expect.arrayContaining([expect.any(Number)]))
    expect(at('<h2>Compute</h2>')).toBeGreaterThan(-1)
    expect(at('<h2>Compute</h2>')).toBeLessThan(at('<h2>Storage</h2>'))
    expect(at('<h2>Storage</h2>')).toBeLessThan(at('<h2>Networking</h2>'))
    expect(at('<h2>Networking</h2>')).toBeLessThan(at('<h2>Databases</h2>'))
    expect(at('<h2>Databases</h2>')).toBeLessThan(at('<h2>Containers</h2>'))
    expect(at('<h2>Containers</h2>')).toBeLessThan(at('<h2>Platform plans</h2>'))
    expect(at('<h2>Platform plans</h2>')).toBeLessThan(at('<h2>Other services</h2>'))
    for (const name of ['Elastic Cloud Server', 'Auto Scaling', 'Block storage', 'Backup', 'Elastic IP', 'Load balancer', 'NAT gateway', 'RDS for MySQL', 'CCE cluster', 'Kubernetes capacity', 'Platform plans', 'OBS']) {
      expect(html).toContain(`aria-label="Configure ${name}"`)
    }
    expect(html).toContain('Virtual servers — general purpose')
    // Each family card opens with its glyph and a one-line description.
    for (const fam of ['compute', 'storage', 'networking', 'databases', 'containers', 'plans', 'other']) {
      expect(html).toContain(`class="fam-glyph" data-family="${fam}"`)
    }
    expect(html).toContain('Servers, and the scaling that keeps up with demand.')
    expect(html).toContain('Disks, backups and images, priced per GB.')
    // The totals sit in large type above the items, empty until priced.
    expect(html).toContain('data-testid="estimate-kpis"')
    expect(html).toContain('<span data-testid="total-monthly">—</span>')
    expect(html).not.toContain('data-testid="breakdown"')
    expect(html).toContain('from 11.676 OMR / month')
    expect(html).toContain('from 0.035 OMR / month per GB')
    expect(html).toContain('from 5.000 OMR / month')
    // The companion storage is asked for inside the engine, not listed.
    expect(html).not.toContain('Configure RDS storage')
    // No SKU is a label on the page.
    expect(html).not.toContain('ecs.s7n.small.1')
    expect(html).not.toContain('rds.mysql.c7')
    // The region applies to every item and sits above both columns.
    expect(html).toContain('id="estimate-region"')
    expect(html).toContain('me-east-215-b')
    // An empty estimate says what to do, and quotes no total.
    expect(html).toContain('Choose a service on the left to start an estimate')
    expect(html).toContain('Tax (5.0 %)')
    expect(html).toContain('12 months')
    expect(html).toContain('NC list 2026')
    expect(html).toContain('prices as of')
    expect(html).toContain('A negotiated price, a discount or a partner rate is never part of this estimate')
    // The share link and the lead capture are still there.
    expect(html).toContain('Share estimate')
    expect(html).toContain('Send me this estimate')
    // Nothing of the console: no sidebar, no sign-out, no operator menu.
    expect(html).not.toContain('Sign out')
    expect(html).not.toContain('aside')
    expect(html).not.toContain('Cost explorer')
  })

  it('drops the header in embed mode and keeps the catalogue', () => {
    const html = render('/estimate?embed=1')
    expect(html).not.toContain('<h1>')
    expect(html).not.toContain('Cost calculator')
    expect(html).toContain('aria-label="Configure Elastic Cloud Server"')
    expect(html).toContain('NC list 2026')
  })

  it('renders a shared estimate read-only, named from the catalog, with its validity and the book it was priced from', () => {
    const html = render('/estimate/e1', '/estimate/:id')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Cost estimate')
    expect(html).toContain('General purpose · 4 vCPU · 16 GB')
    expect(html).toContain('ecs.s7n.xlarge.4')
    expect(html).toContain('730.000000')
    expect(html).toContain('70.047 OMR')
    expect(html).toContain('840.565 OMR')
    expect(html).toContain('valid until')
    expect(html).toContain('NC list 2026')
    // Read-only: no controls on a shared estimate.
    expect(html).not.toContain('Share estimate')
    expect(html).not.toContain('Send me this estimate')
    expect(html).not.toContain('Configure ')
  })
})
