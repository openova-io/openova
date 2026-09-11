import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Me } from '../api/types'

/**
 * The partner lens's Analyse pages, rendered as a partner owner sees them
 * (DESIGN.md §13.5) — the founder's reason for resellers: "so the resellers
 * could see the cost analysis of their customers".
 *
 * What is being proved is that the SAME explorer and resource list the
 * operator reads come up for a partner, over the Sovereign-wide documents
 * (which the server confines to the partner's customers), with the customer
 * dimension offered and every link landing inside /partner — and without the
 * provider's own configuration, which is not a partner's to open.
 */

const explore = {
  from: '2026-09-01',
  to: '2026-09-08',
  granularity: 'day',
  group_by: 'customer',
  metric: 'cost',
  currency: 'OMR',
  mixed_currency: false,
  buckets: ['2026-09-01', '2026-09-02'],
  bucket_has_data: [true, true],
  groups: [
    { key: 'c-1', label: 'Alpha', total: 20, previous: 10, delta_pct: 100, share: 0.6667, resources: 1, values: [12, 8] },
    { key: 'c-2', label: 'Bravo', total: 10, previous: 10, delta_pct: 0, share: 0.3333, resources: 1, values: [5, 5] },
  ],
  other: null,
  total: { current: 30, previous: 20, delta_pct: 50, resources: 2 },
  totals_by_bucket: [17, 13],
  unpriced: [],
  not_sold_per_use: [],
  forecast: null,
  compare: { from: '2026-08-25', to: '2026-09-01', label: 'previous period' },
  unconverted: [],
}

const dimensions = {
  from: '2026-09-01',
  to: '2026-09-08',
  dimensions: {
    customer: [
      { key: 'c-1', label: 'Alpha' },
      { key: 'c-2', label: 'Bravo' },
    ],
    kind: [{ key: 'ecs', label: 'Elastic Cloud Server' }],
    region: [{ key: 'me-east-215', label: 'me-east-215' }],
    sku: [{ key: 'ecs.m7n.xlarge.8', label: 'ecs.m7n.xlarge.8' }],
    source: [],
    resource: [],
    tier: [],
    namespace: [],
    enterprise_project: [],
  },
  tag_keys: [],
}

const resources = {
  rows: [
    {
      source_id: 'src-1', resource_id: 'vm-alpha', kind: 'ecs', name: 'alpha web', region: 'me-east-215',
      customer_id: 'c-1', customer_name: 'Alpha', status: 'live',
      first_seen: '2026-09-01T00:00:00Z', last_seen: '2026-09-02T00:00:00Z', deleted_at: null,
      cost: 20, currency: 'OMR', lines: [{ sku: 'ecs.m7n.xlarge.8', unit: 'instance-hour', quantity: 20, cost: 20 }],
    },
    {
      source_id: 'src-2', resource_id: 'vm-bravo', kind: 'ecs', name: 'bravo web', region: 'me-east-215',
      customer_id: 'c-2', customer_name: 'Bravo', status: 'live',
      first_seen: '2026-09-01T00:00:00Z', last_seen: '2026-09-02T00:00:00Z', deleted_at: null,
      cost: 10, currency: 'OMR', lines: [],
    },
  ],
  total: 2,
  sum_cost: 30,
  limit: 50,
  offset: 0,
  currency: 'OMR',
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const doc = (): unknown => {
      if (path === null) return null
      if (path.startsWith('/cost/explore')) return explore
      if (path.startsWith('/cost/dimensions')) return dimensions
      if (path.startsWith('/resources')) return resources
      // A customer-scoped path here would mean the partner lens pinned
      // itself to its own party account instead of spanning its customers.
      throw new Error(`the partner lens asked for ${path}`)
    }
    return { data: doc(), error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

const partner: Me = {
  email: 'ap@resell.example',
  role: 'partner-owner',
  customer_id: 'party-1',
  permissions: { 'partner:p-1': ['metering.read', 'account.topup', 'partner.self.manage'] },
  roles: [{ role: 'partner-owner', scope_kind: 'partner', partner_id: 'p-1', partner_name: 'Resell Co', customer_ids: ['c-1', 'c-2'] }],
  scopes: ['partner:p-1'],
}
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: partner, loading: false, refresh: async () => partner, logout: async () => {} }),
}))

import { CostExplorer } from './CostExplorer'
import { Resources } from './Resources'

function render(Page: ComponentType, path: string, url = path): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('the partner lens Analyse pages', () => {
  it('Cost explorer: its customers grouped, totalled and exportable', () => {
    const html = render(CostExplorer, '/partner/explore', '/partner/explore?group_by=customer&preset=30d')
    // The real explorer, not a partner-only imitation.
    expect(html).toContain('Cost explorer')
    expect(html).toContain('aria-label="Explorer controls"')
    // Grouping BY CUSTOMER is offered — this is the whole point for a
    // reseller, and `lens.operator` alone would have hidden it.
    expect(html).toContain('>Customer</option>')
    // Its two customers, with their figures.
    expect(html).toContain('Alpha')
    expect(html).toContain('Bravo')
    expect(html).toContain('Group by')
    // The Sovereign-wide export path; the server scopes it.
    expect(html).toMatch(/href="[^"]*\/cost\/export\.csv\?/)
    // The provider's own configuration is not a partner's to open.
    expect(html).not.toContain('/pricebooks')
  })

  it('Resources: a Customer column whose links stay inside the partner lens', () => {
    const html = render(Resources, '/partner/resources', '/partner/resources?preset=30d')
    expect(html).toContain('>Customer<')
    expect(html).toContain('alpha web')
    expect(html).toContain('bravo web')
    // Every customer link is the partner's page for it, never the
    // Sovereign's /customers/<id>, which the shell would bounce.
    expect(html).toContain('href="/partner/customers/c-1"')
    expect(html).toContain('href="/partner/customers/c-2"')
    expect(html).not.toMatch(/href="\/customers\//)
    // The export is the Sovereign-wide list, which the server scopes — not
    // the partner's party account's.
    expect(html).toMatch(/href="[^"]*\/api\/v1\/resources\.csv\?/)
  })
})
