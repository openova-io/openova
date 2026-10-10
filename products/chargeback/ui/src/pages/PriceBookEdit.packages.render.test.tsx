import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { packages } from '../panels/estimate/fixture'

/**
 * The Packages tab of a price book, rendered (DESIGN.md §22.5): the book's
 * plan items as columns with their price, shape, guaranteed floors and the
 * Recommended badge; the STEP-UP CHECK row under them, green where the rule
 * holds and red where it does not; the features as rows GROUPED under their
 * group heading; each cell a chip in its kind's words — Included, Optional
 * with its add-on price and the "included from XL" hint, a quantity with its
 * overage, a level's label with its purchasable next level, "from M" on a
 * teaser, Not offered; a feature not yet in the book reading not offered
 * everywhere; the floor strip with its own Edit / Delete; "Add feature",
 * "Add floor item" and a row's Edit / Delete. The default tab is still the
 * items, untouched.
 */

const features = {
  features: [
    { id: 'f0', key: 'ssl', name: 'Unlimited free SSL', blurb: 'Certificates for every site, renewed for you', kind: 'boolean', group: 'floor', teaser: false, sort_order: 0 },
    { id: 'f1', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', group: 'capacity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', teaser: false, sort_order: 1 },
    { id: 'f2', key: 'disk', name: 'Disk', blurb: '', kind: 'quantity', group: 'capacity', unit: 'GB', addon_sku: 'k8s.pvc_gb', teaser: false, sort_order: 2 },
    { id: 'f3', key: 'ai_seo', name: 'AI SEO ready', blurb: '', kind: 'boolean', group: 'features', addon_sku: 'addon.ai_seo', teaser: false, sort_order: 3 },
    { id: 'f4', key: 'gitea_iac', name: 'Gitea + IaC', blurb: '', kind: 'access', group: 'access', teaser: false, sort_order: 4 },
    { id: 'f5', key: 'vuln_dashboard', name: 'Vulnerability dashboard', blurb: '', kind: 'boolean', group: 'ops', teaser: true, sort_order: 5 },
    { id: 'f6', key: 'dedicated_ip', name: 'Dedicated IP address', blurb: 'A public IPv4 reserved for your Organization', kind: 'boolean', group: 'scope', addon_sku: 'addon.dedicated_ip', teaser: false, sort_order: 6 },
    { id: 'f7', key: 'backup', name: 'Backup', blurb: 'Scheduled backups of your sites and databases', kind: 'boolean', group: 'resilience', addon_sku: 'addon.backup', teaser: false, sort_order: 7 },
    { id: 'f8', key: 'dr_topology', name: 'DR topology', blurb: '', kind: 'level', group: 'resilience', levels: ['single region', 'active-passive'], addon_sku: 'addon.dr', teaser: false, sort_order: 8 },
    { id: 'f9', key: 'sla', name: 'SLA', blurb: 'What we promise on availability', kind: 'level', group: 'service', levels: ['99.5 %', '99.9 %'], teaser: false, sort_order: 9 },
  ],
  groups: packages.groups,
}

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
  const docFor = (path: string) => {
    if (path === '/pricebooks/pb1') return book
    if (path === '/pricebooks/pb1/coverage') return coverage
    if (path === '/pricebooks/pb1/packages') return packages
    if (path === '/features') return features
    throw new Error(`no fixture for ${path}`)
  }
  return { useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }) }
})

import { CellModal, FeatureModal } from '../components/PackagesPanel'
import { PriceBookEdit } from './PriceBookEdit'

function render(url: string): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path: '/pricebooks/:id', element: createElement(PriceBookEdit) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

const between = (html: string, from: string, to: string) => html.slice(html.indexOf(from), html.indexOf(to, html.indexOf(from)))

describe('the Packages tab of a price book', () => {
  it('draws the columns with price, shape, guaranteed floors and the Recommended badge, and the step-up row green and red', () => {
    const html = render('/pricebooks/pb1?tab=packages')
    expect(html).toContain('aria-label="Package matrix"')
    // The columns, cheapest first, priced per month, with the shape.
    const heads = [...html.matchAll(/data-testid="pkg-head-(plan\.[a-z]+)">/g)].map((m) => m[1])
    expect(heads).toEqual(['plan.s', 'plan.m', 'plan.l', 'plan.xl'])
    const m = between(html, 'data-testid="pkg-head-plan.m"', 'data-testid="pkg-head-plan.l"')
    expect(m).toContain('<div class="pkg-name">M<span class="badge ok pkg-recommended">Recommended</span></div>')
    expect(m).toContain('9.000 OMR / month')
    expect(m).toContain('<div class="tiny pkg-shape">2 vCPU · 4 GB · 50 GB disk</div>')
    expect(m).toContain('<div class="tiny muted pkg-guaranteed">0.33 vCPU · 1.33 GB guaranteed</div>')
    expect(m).toContain('aria-label="Settings of M"')
    const s = between(html, 'data-testid="pkg-head-plan.s"', 'data-testid="pkg-head-plan.m"')
    expect(s).not.toContain('Recommended')
    expect(s).toContain('1 vCPU · 2 GB · 25 GB disk')
    // The step-up check: holds on S (green) and M, fails on L (red), the top package has none.
    expect(html).toMatch(/data-testid="step-up-plan\.s"[^>]*>gap 4\.000 · bundled add-ons 8\.000 OMR ✓</)
    expect(html).toMatch(/class="small num ok" data-testid="step-up-plan\.s"/)
    expect(html).toMatch(/class="small num bad" data-testid="step-up-plan\.l"[^>]*>gap 14\.000 · bundled add-ons 3\.500 OMR ✗ raise add-on prices</)
    expect(html).toMatch(/class="small num muted" data-testid="step-up-plan\.xl"[^>]*>top package</)
  })

  it('groups the rows under their heading and words every kind of cell', () => {
    const html = render('/pricebooks/pb1?tab=packages')
    // The group headings, in the document's order, each before its rows.
    const order = [...html.matchAll(/data-testid="(group-[a-z]+|feature-row-[a-z_]+)"/g)].map((m) => m[1])
    expect(order).toEqual([
      'group-capacity',
      'feature-row-bandwidth',
      'feature-row-disk',
      'group-features',
      'feature-row-ai_seo',
      'group-access',
      'feature-row-gitea_iac',
      'group-ops',
      'feature-row-vuln_dashboard',
      'group-scope',
      'feature-row-dedicated_ip',
      'group-resilience',
      'feature-row-backup',
      'feature-row-dr_topology',
      'group-service',
      'feature-row-sla',
    ])
    expect(html).toMatch(/<tr class="pkg-group" data-testid="group-ops"><th colSpan="6">Managed operations<\/th><\/tr>/)
    // A quantity with its overage under the chip.
    expect(html).toMatch(/data-testid="cell-bandwidth-plan\.s"><span>50 Mbps<\/span><span class="tiny muted">hard cap<\/span>/)
    expect(html).toMatch(/data-testid="cell-bandwidth-plan\.l"><span>250 Mbps<\/span><span class="tiny muted">metered<\/span>/)
    // Optional with its add-on price and the hint; the chip's class is the state.
    expect(html).toMatch(/class="pkg-cell optional" aria-label="Backup on M"[^>]*><span>\+ 1\.500 OMR \/ month<\/span><span class="tiny muted">included from XL<\/span>/)
    expect(html).toMatch(/class="pkg-cell included" aria-label="Backup on XL"[^>]*><span>Included<\/span>/)
    // A level: its label, and the purchasable next level with its price on S.
    expect(html).toMatch(/data-testid="cell-dr_topology-plan\.s"><span>single region<\/span><span class="tiny muted">active-passive \+ 8\.000 OMR \/ month<\/span>/)
    expect(html).toMatch(/data-testid="cell-dr_topology-plan\.xl"><span>active-passive<\/span>/)
    // An access door, with its note.
    expect(html).toMatch(/class="pkg-cell included" aria-label="Gitea \+ IaC on M"[^>]*><span>Included<\/span><span class="tiny muted">read<\/span>/)
    expect(html).toMatch(/class="pkg-cell not_offered" aria-label="Gitea \+ IaC on S"/)
    // A teaser says where it starts, with a dashed chip.
    expect(html).toMatch(/class="pkg-cell teaser" aria-label="Vulnerability dashboard on S"[^>]*><span>from M<\/span>/)
    // Not offered.
    expect(html).toMatch(/class="pkg-cell not_offered" aria-label="Dedicated IP address on L"[^>]*><span>Not offered<\/span>/)
    // A feature with no cell in this book yet reads not offered everywhere and says so.
    const sla = html.slice(html.indexOf('data-testid="feature-row-sla"'))
    expect(sla).toContain('not in this book yet')
    expect(sla).toContain('level · 99.5 % → 99.9 %')
    expect((sla.slice(0, sla.indexOf('</tr>')).match(/pkg-cell not_offered/g) ?? []).length).toBe(4)
    // The floor: a strip with the item, its blurb and its own actions; never a row.
    const floor = html.slice(html.indexOf('data-testid="floor-strip"'))
    expect(floor).toContain('<b>Unlimited free SSL</b>')
    expect(floor).toContain('Certificates for every site, renewed for you')
    expect(floor).toContain('>Edit<')
    expect(html).not.toContain('data-testid="feature-row-ssl"')
    // The actions.
    expect(html).toContain('>Add feature<')
    expect(html).toContain('>Add floor item<')
    expect(html).toContain('>Delete<')
    // The tab strip names both tabs and marks this one.
    expect(html).toMatch(/class="tab active" aria-current="page"[^>]*><span>Packages<\/span>/)
    expect(html).not.toContain('>Add item<')
  })

  it('keeps the items as the default tab, with the matrix off the page', () => {
    const html = render('/pricebooks/pb1')
    expect(html).toContain('>Add item<')
    expect(html).toContain('addon.backup')
    expect(html).not.toContain('aria-label="Package matrix"')
    expect(html).toMatch(/class="tab active" aria-current="page"[^>]*><span>Items<\/span>/)
    expect(html).toMatch(/<span>Packages<\/span><span class="count">4<\/span>/)
  })
})

describe('the editors', () => {
  const book = { id: 'pb1', name: 'OpenOva plans', scope: 'platform', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute', created_at: '2026-01-01T00:00:00Z', items: [] } as never
  const dr = features.features[8] as never
  const drDoc = packages.features.find((f) => f.key === 'dr_topology')!

  it('edits a level cell with the level select, the next-level tick and its price', () => {
    const html = renderToString(createElement(CellModal, { book, doc: packages, row: { feature: dr, cells: drDoc.cells, inBook: true }, planSku: 'plan.s', currency: 'OMR', onClose: () => {}, onSaved: () => {} })).replace(/<!-- -->/g, '')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('data-testid="cell-editor-level"')
    expect(html).toContain('DR topology on S')
    // The states a level offers: at a level, or not offered — never a bare "optional".
    expect(html).toMatch(/<button[^>]*aria-pressed="true"[^>]*>At a level<\/button>/)
    expect(html).not.toMatch(/>Optional</)
    // The level select with the labels, single region selected.
    expect(html).toMatch(/<select aria-label="Level"[^>]*><option value="0" selected="">single region<\/option><option value="1">active-passive<\/option><\/select>/)
    // The next level is purchasable on S: ticked, named, priced.
    expect(html).toMatch(/<input type="checkbox"[^>]*aria-label="Next level purchasable"[^>]*checked=""[^>]*\/> active-passive purchasable as an add-on/)
    expect(html).toContain('Next level (OMR / month)')
    expect(html).toMatch(/<input type="number"[^>]*value="8\.000"/)
    expect(html).toContain('>Remove cell<')
  })

  it('edits a level cell at the top with nothing above it, and a quantity cell with its overage', () => {
    const html = renderToString(createElement(CellModal, { book, doc: packages, row: { feature: dr, cells: drDoc.cells, inBook: true }, planSku: 'plan.xl', currency: 'OMR', onClose: () => {}, onSaved: () => {} })).replace(/<!-- -->/g, '')
    expect(html).toMatch(/<option value="1" selected="">active-passive<\/option>/)
    expect(html).toMatch(/<input type="checkbox"[^>]*disabled=""[^>]*aria-label="Next level purchasable"[^>]*\/> top level — nothing above it/)
    const bw = features.features[1] as never
    const bwDoc = packages.features.find((f) => f.key === 'bandwidth')!
    const q = renderToString(createElement(CellModal, { book, doc: packages, row: { feature: bw, cells: bwDoc.cells, inBook: true }, planSku: 'plan.s', currency: 'OMR', onClose: () => {}, onSaved: () => {} })).replace(/<!-- -->/g, '')
    expect(q).toContain('data-testid="cell-editor-quantity"')
    expect(q).toMatch(/<input type="number"[^>]*aria-label="Included quantity"[^>]*value="50"/)
    expect(q).toMatch(/<select aria-label="Overage"[^>]*><option value="metered">Metered<\/option><option value="hard_cap" selected="">Hard cap<\/option><option value="unlimited">Unlimited<\/option><\/select>/)
    expect(q).not.toMatch(/>Optional</)
  })

  it('edits a feature with its group, kind, levels one per line and the teaser flag', () => {
    const html = renderToString(createElement(FeatureModal, { feature: dr, groups: packages.groups!, onClose: () => {}, onSaved: () => {} })).replace(/<!-- -->/g, '')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('Edit DR topology')
    expect(html).toMatch(/<select aria-label="Group"[^>]*><option value="floor">Floor — on every package<\/option><option value="capacity">Capacity<\/option>.*<option value="resilience" selected="">Resilience<\/option>/)
    expect(html).toMatch(/<select aria-label="Kind"[^>]*>.*<option value="level" selected="">Level<\/option>/)
    expect(html).toMatch(/<textarea rows="4" aria-label="Levels"[^>]*>single region\nactive-passive<\/textarea>/)
    expect(html).toMatch(/<input type="checkbox" aria-label="Teaser"/)
    expect(html).toContain('addon.dr')
    // A floor item: boolean, no kind, no add-on, no teaser.
    const floor = renderToString(createElement(FeatureModal, { feature: null, floor: true, groups: packages.groups!, onClose: () => {}, onSaved: () => {} })).replace(/<!-- -->/g, '')
    expect(floor).toContain('Add floor item')
    expect(floor).toMatch(/<option value="floor" selected="">/)
    expect(floor).not.toContain('aria-label="Kind"')
    expect(floor).not.toContain('aria-label="Teaser"')
  })
})
