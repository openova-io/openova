import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { CapacityOverview, Me, SKUFootprints } from '../api/types'

/**
 * Plan → Capacity rendered with the document the Go integration test derives
 * (DESIGN.md §11): the heatmap cell per zone × family coloured at 70 / 85 %,
 * the available amount and time to exhaustion in the cell, the SKU headroom
 * table with the binding family, the footprints, the unmapped-SKU notice
 * with its one-click form, and the read-only variant without
 * capacity.manage. Effects do not run under renderToString, so every
 * follow-up fetch shows its initial state.
 */

const pool = (over: Partial<CapacityOverview['regions'][0]['zones'][0]['pools'][0]>) => ({
  id: 'p', zone_id: 'z', family: 'vcpu', total: 0, reserved: 0, source: 'manual', note: '', updated_by: '', updated_at: '2026-09-09T08:00:00Z',
  label: 'vCPU', unit: 'vCPU', consumed: 0, available: 0, utilisation_pct: null, status: 'unset', clamped: false, overcommit: 0, zone_unknown: 0,
  growth_per_day: null, exhaustion_days: null, history_days: 0, series: [],
  ...over,
})

const overview: CapacityOverview = {
  as_of: '2026-09-09T09:00:00Z',
  sources: 2,
  lagging_sources: 0,
  thresholds: { warn_pct: 70, critical_pct: 85 },
  families: [
    { family: 'vcpu', label: 'vCPU', unit: 'vCPU' },
    { family: 'memory_gib', label: 'Memory', unit: 'GiB' },
    { family: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB' },
    { family: 'block_hdd_gib', label: 'Block HDD', unit: 'GiB' },
    { family: 'object_gib', label: 'Object storage', unit: 'GiB' },
    { family: 'eip_addresses', label: 'Elastic IPs', unit: 'addresses' },
    { family: 'bandwidth_mbps', label: 'Bandwidth', unit: 'Mbps' },
  ],
  regions: [
    {
      id: 'r1',
      code: 'me-east-215',
      name: 'Muscat',
      cloud_source_kind: 'huawei-project',
      zones: [
        {
          id: 'za',
          code: 'me-east-215a',
          name: 'AZ 1',
          is_default: true,
          pools: [
            pool({ id: 'pa-vcpu', zone_id: 'za', family: 'vcpu', total: 20, consumed: 16, available: 4, utilisation_pct: 80, status: 'warn', zone_unknown: 8, growth_per_day: 1.14, exhaustion_days: 3.5, history_days: 8, note: 'two hosts', updated_by: 'ops@nc.example' }),
            pool({ id: 'pa-mem', zone_id: 'za', family: 'memory_gib', label: 'Memory', unit: 'GiB', total: 100, consumed: 80, available: 20, utilisation_pct: 80, status: 'warn', growth_per_day: 0, history_days: 8 }),
            pool({ id: 'pa-ssd', zone_id: 'za', family: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB' }),
            pool({ id: 'pa-hdd', zone_id: 'za', family: 'block_hdd_gib', label: 'Block HDD', unit: 'GiB' }),
            pool({ id: 'pa-obj', zone_id: 'za', family: 'object_gib', label: 'Object storage', unit: 'GiB' }),
            pool({ id: 'pa-eip', zone_id: 'za', family: 'eip_addresses', label: 'Elastic IPs', unit: 'addresses' }),
            pool({ id: 'pa-bw', zone_id: 'za', family: 'bandwidth_mbps', label: 'Bandwidth', unit: 'Mbps' }),
          ],
          skus: [
            { sku: 'ecs.m7n.2xlarge.8', footprint: { vcpu: 8, memory_gib: 64 }, footprint_source: 'seed', consumed_units: 1, resources: 1, headroom_units: 0, binding_family: 'vcpu', cap: null },
            { sku: 'ecs.m7n.xlarge.8', footprint: { vcpu: 4, memory_gib: 32 }, footprint_source: 'seed', consumed_units: 0, resources: 0, headroom_units: 0, binding_family: 'memory_gib', cap: null },
            { sku: 'ecs.c7n.large.2', footprint: { vcpu: 2, memory_gib: 4 }, footprint_source: 'derived', consumed_units: 0, resources: 0, headroom_units: 2, binding_family: 'vcpu', cap: null },
            { sku: 'evs.ssd.gb', footprint: { block_ssd_gib: 1 }, footprint_source: 'seed', consumed_units: 0, resources: 0, headroom_units: null, binding_family: '', cap: null },
          ],
        },
        {
          id: 'zb',
          code: 'me-east-215b',
          name: '',
          is_default: false,
          pools: [
            pool({ id: 'pb-vcpu', zone_id: 'zb', family: 'vcpu' }),
            pool({ id: 'pb-mem', zone_id: 'zb', family: 'memory_gib', label: 'Memory', unit: 'GiB' }),
            pool({ id: 'pb-ssd', zone_id: 'zb', family: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', total: 1000, consumed: 180, available: 820, utilisation_pct: 18, status: 'ok', growth_per_day: 10, exhaustion_days: 82, history_days: 8 }),
            pool({ id: 'pb-hdd', zone_id: 'zb', family: 'block_hdd_gib', label: 'Block HDD', unit: 'GiB' }),
            pool({ id: 'pb-obj', zone_id: 'zb', family: 'object_gib', label: 'Object storage', unit: 'GiB' }),
            pool({ id: 'pb-eip', zone_id: 'zb', family: 'eip_addresses', label: 'Elastic IPs', unit: 'addresses', consumed: 1 }),
            pool({ id: 'pb-bw', zone_id: 'zb', family: 'bandwidth_mbps', label: 'Bandwidth', unit: 'Mbps', total: 5, consumed: 10, available: 0, utilisation_pct: 200, status: 'critical', clamped: true, overcommit: 5, growth_per_day: 0, history_days: 8 }),
          ],
          skus: [{ sku: 'evs.ssd.gb', footprint: { block_ssd_gib: 1 }, footprint_source: 'seed', consumed_units: 180, resources: 1, headroom_units: 320, binding_family: 'cap', cap: 500 }],
        },
      ],
    },
  ],
  unmapped_skus: [{ sku: 'nat.1', unit: 'hour', quantity: 1, resources: 1, regions: ['me-east-215'] }],
  unmapped_regions: [{ region: 'eu-west-101', reason: 'no-region', skus: 1, quantity: 1, resources: 1 }],
  summary: { regions: 1, zones: 2, pools: 14, pools_with_total: 4, pools_warn: 2, pools_critical: 1, pools_below_threshold: 3, skus: 6, unmapped_skus: 1 },
}

const footprints: SKUFootprints = {
  footprints: [
    { sku: 'ecs.m7n.2xlarge.8', families: { vcpu: 8, memory_gib: 64 }, source: 'seed', updated_at: '2026-09-09T00:00:00Z' },
    { sku: 'evs.ssd.gb', families: { block_ssd_gib: 1 }, source: 'seed' },
    { sku: 'nat.2', families: { eip_addresses: 1, bandwidth_mbps: 100 }, source: 'manual' },
  ],
  families: overview.families,
  unseeded_skus: ['elb', 'nat.1', 'vpc'],
}

let docs: { overview: CapacityOverview | null; footprints: SKUFootprints | null } = { overview, footprints }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const data = path === '/capacity/overview' ? docs.overview : path === '/capacity/footprints' ? docs.footprints : null
    return { data, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

let who: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign', source: 'config' }] }
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: who, loading: false, refresh: async () => who, logout: async () => {} }),
}))

import { Capacity } from './Capacity'

function render(): string {
  const html = renderToString(createElement(MemoryRouter, null, createElement(Capacity))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Capacity page', () => {
  it('renders the KPI strip, the heatmap with thresholds, available and exhaustion per cell, and the unmapped notices for a sovereign-admin', () => {
    const html = render()
    // KPIs.
    expect(html).toContain('Pools past 70 %')
    expect(html).toContain('1 critical · 4 of 14 pools sized')
    expect(html).toContain('SKUs without a footprint')
    expect(html).toContain('2026-09-09 09:00Z')
    expect(html).toContain('2 cloud sources')
    // Heatmap: both zones, every family column, the default badge.
    expect(html).toContain('aria-label="Capacity by zone and family"')
    expect(html).toContain('me-east-215a')
    expect(html).toContain('me-east-215b')
    for (const f of ['vCPU', 'Memory', 'Block SSD', 'Block HDD', 'Object storage', 'Elastic IPs', 'Bandwidth']) expect(html).toContain(`>${f}<`)
    expect(html).toContain('badge info">default')
    // Cells coloured by status, with the figures in them.
    expect(html).toContain('heat warn editable')
    expect(html).toContain('heat critical editable')
    expect(html).toContain('heat ok editable')
    expect(html).toContain('heat unset editable')
    expect(html).toContain('80.0 %')
    expect(html).toContain('<b>4</b> vCPU left of 20')
    expect(html).toContain('runs out in 4 days')
    expect(html).toContain('8 zone unknown')
    expect(html).toContain('<b>820</b> GiB left of 1,000')
    expect(html).toContain('runs out in 2.7 months')
    expect(html).toContain('not growing')
    expect(html).toContain('200.0 %')
    expect(html).toContain('badge bad">over')
    expect(html).toContain('no total · <b>1</b> addresses in use')
    expect(html).toContain('click to set')
    // Legend and the filter.
    expect(html).toContain('70 – 85 %')
    expect(html).toContain('aria-label="Region filter"')
    expect(html).toContain('me-east-215 · Muscat')
    // SKU headroom for the first zone: headroom and the binding family.
    expect(html).toContain('SKU headroom in me-east-215a')
    expect(html).toContain('ecs.m7n.2xlarge.8')
    expect(html).toContain('8 vCPU · 64 GiB')
    expect(html).toContain('badge ">vCPU')
    expect(html).toContain('badge ">Memory')
    expect(html).toContain('derived from the name')
    expect(html).toContain('no family of this footprint has a total in this zone yet')
    expect(html).toContain('aria-label="Cap a SKU"')
    // Unmapped SKU with its one-click form; unmapped region with its one-click add.
    expect(html).toContain('aria-label="Unmapped SKUs"')
    expect(html).toContain('nat.1')
    expect(html).toContain('>Add footprint<')
    expect(html).toContain('eu-west-101')
    expect(html).toContain('Add region eu-west-101')
    // Footprints card: the seeded rows, the manual one, the unseeded list.
    expect(html).toContain('aria-label="SKU footprints"')
    expect(html).toContain('badge info">seed')
    expect(html).toContain('badge ">manual')
    expect(html).toContain('no per-unit footprint on the list: elb, nat.1, vpc')
    // Regions card with its forms.
    expect(html).toContain('aria-label="Add a region"')
    expect(html).toContain('>Add zone<')
    expect(html).not.toContain('Read-only')
  })

  it('is read-only without capacity.manage: no forms, no editable cells, the permission named', () => {
    who = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read', 'audit.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
    const html = render()
    expect(html).toContain('capacity.manage')
    expect(html).toContain('Read-only')
    expect(html).not.toContain('editable')
    expect(html).not.toContain('aria-label="Add a region"')
    expect(html).not.toContain('aria-label="Cap a SKU"')
    expect(html).not.toContain('>Add footprint<')
    expect(html).not.toContain('Add region eu-west-101')
    expect(html).not.toContain('click to set')
    // Still every figure.
    expect(html).toContain('80.0 %')
    expect(html).toContain('runs out in 2.7 months')
    expect(html).toContain('SKU headroom in me-east-215a')
  })

  it('explains the static-first model when no region exists', () => {
    who = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    docs = {
      overview: { ...overview, as_of: null, sources: 0, regions: [], unmapped_skus: [], unmapped_regions: [], summary: { regions: 0, zones: 0, pools: 0, pools_with_total: 0, pools_warn: 0, pools_critical: 0, pools_below_threshold: 0, skus: 6, unmapped_skus: 0 } },
      footprints,
    }
    const html = render()
    expect(html).toContain('No regions yet')
    expect(html).toContain('static-first')
    expect(html).toContain('capacity collector')
    expect(html).toContain('aria-label="Add a region"')
    expect(html).toContain('no cloud usage metered yet')
    expect(html).not.toContain('aria-label="Capacity by zone and family"')
    expect(html).toContain('6 SKUs carry a footprint')
  })
})
