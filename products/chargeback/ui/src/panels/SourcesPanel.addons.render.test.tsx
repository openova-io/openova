import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { CostSource } from '../api/types'
import { packages } from './estimate/fixture'

/**
 * The Add-ons control on the Sources tab, rendered (DESIGN.md §22): a
 * platform source shows the add-ons it has taken and a Manage button; a
 * cloud source shows none; the modal lists the optional features of the
 * customer's package with their price and the "included from XL" hint, the
 * taken one ticked, and says what is being taken.
 */

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/pricebooks/plans/packages' ? packages : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

import { AddonsModal, SourcesPanel } from './SourcesPanel'

const platform: CostSource = { id: 's-plat', customer_id: 'c1', kind: 'openova-org', layer: 'platform', price_book_id: 'plans', price_book_name: 'OpenOva plans', region: '', project_id: 'nizwa', status: 'verified', addons: ['backup'] }
const cloud: CostSource = { id: 's-cloud', customer_id: 'c1', kind: 'huawei-project', layer: 'cloud', price_book_id: 'cloud-1', region: 'me-east-215', project_id: 'proj-a', status: 'verified', addons: [] }

function render(el: ReturnType<typeof createElement>): string {
  const html = renderToString(createElement(MemoryRouter, null, el)).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the Add-ons control', () => {
  it('shows a platform source’s add-ons and the Manage button, and nothing on a cloud source', () => {
    const html = render(createElement(SourcesPanel, { customerId: 'c1', sources: [platform, cloud], books: [], canManage: true, canRotate: true, onChanged: () => {}, planSlug: 'm' }))
    expect(html).toContain('>Add-ons<')
    const plat = html.slice(html.indexOf('nizwa'), html.indexOf('proj-a'))
    expect(plat).toContain('>backup<')
    expect(plat).toContain('>Manage add-ons<')
    const cl = html.slice(html.indexOf('proj-a'))
    expect(cl).not.toContain('Manage add-ons')
  })

  it('a read-only lens sees the add-ons taken but no Manage button', () => {
    const html = render(createElement(SourcesPanel, { customerId: 'c1', sources: [platform], books: [], canManage: false, canRotate: false, onChanged: () => {}, planSlug: 'm' }))
    expect(html).toContain('>backup<')
    expect(html).not.toContain('Manage add-ons')
  })

  it('the modal lists the package’s optional features with price and hint, the taken one ticked, and says what is taken', () => {
    const html = render(createElement(AddonsModal, { customerId: 'c1', source: platform, planSlug: 'm', onClose: () => {}, onDone: () => {} }))
    expect(html).toContain('Add-ons — nizwa')
    expect(html).toContain('<b>M</b> package offers these')
    // Backup: ticked, priced, with the hint. Dedicated IP: offered, not ticked.
    const backup = html.slice(html.indexOf('aria-label="Backup"'), html.indexOf('aria-label="Dedicated IP address"'))
    expect(backup).toContain('checked=""')
    expect(backup).toContain('+ 1.500 OMR / month')
    expect(backup).toContain('included from XL')
    const ip = html.slice(html.indexOf('aria-label="Dedicated IP address"'))
    expect(ip.slice(0, ip.indexOf('</label>'))).not.toContain('checked=""')
    expect(ip).toContain('+ 2.000 OMR / month')
    // What the package includes is not on offer.
    expect(html).not.toContain('aria-label="Unlimited free SSL"')
    expect(html).not.toContain('aria-label="Bandwidth"')
    expect(html).toContain('Taking: Backup (+ 1.500 OMR / month)')
    expect(html).toContain('>Save add-ons<')
  })

  it('warns when the customer is on no sized package', () => {
    const html = render(createElement(AddonsModal, { customerId: 'c1', source: platform, planSlug: '', onClose: () => {}, onDone: () => {} }))
    expect(html).toContain('not on a sized package')
  })
})
