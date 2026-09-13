import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { CapacityOverview, CapacityResourceView, CapacityShapes, Me } from '../api/types'

/**
 * Plan → Capacity rendered with the document the Go integration test derives
 * (DESIGN.md §11) — the founder's worked example: a pool of ten servers at
 * 64 vCPU / 512 GiB with N+1 held back, vCPU 4:1 and RAM 1:1, selling 40
 * guaranteed and 32 burstable m7n.2xlarge with a little spot alongside.
 *
 * What the page has to get right, and what these assertions are for:
 *
 *   RAM BINDS and the pool is full, while 192 physical vCPU are STRANDED —
 *   the main procurement signal, and the case a blended average erases.
 *   THE ORDER-BY DATE, not the wall: 24 days of room against 45 days of lead
 *   time reads "order was due 21 days ago", never "—" and never "in 24 days".
 *   THE CLASS SPLIT, named, because guaranteed growth is a hardware order and
 *   spot growth is a reclaim.
 *   ONE BASKET headroom, not a per-SKU maximum per resource.
 *   THE HONEST EMPTY STATE: an unsized pool reads "not sized", never ok, and
 *   a basket that could not be measured prints its reason rather than a 0.
 *
 * Effects do not run under renderToString, so every follow-up fetch shows its
 * initial state.
 */

const res = (over: Partial<CapacityResourceView>): CapacityResourceView => ({
  resource: 'vcpu', label: 'vCPU', unit: 'vCPU',
  machines: 10, per_machine: 64, reserve: 64, overcommit_ratio: 4,
  raw: 640, usable: 576, sellable: 1344,
  guaranteed: 320, burstable: 256, burstable_physical: 64, spot: 32, spot_physical: 8,
  sold_nominal: 576, remaining: 768, guaranteed_ceiling: 256,
  physical_used: 384, physical_free: 192, stranded: false, spot_room: 768, spot_reclaim: 0,
  sized: true, utilisation_pct: 42.9, status: 'ok', overcommitted: false, over: 0,
  series: [], history_days: 8,
  soft_wall_days: null, soft_wall_date: null, hard_wall_days: null, hard_wall_date: null,
  order_by_days: null, order_by_date: null, order_by_wall: '', late: false,
  ...over,
})

const vcpu = res({
  stranded: true,
  series: [
    { class: 'guaranteed', label: 'Guaranteed', days: [{ day: '2026-09-01', consumed: 256 }, { day: '2026-09-08', consumed: 312 }], growth_per_day: 8 },
    { class: 'burstable', label: 'Burstable', days: [{ day: '2026-09-01', consumed: 256 }, { day: '2026-09-08', consumed: 256 }], growth_per_day: 0 },
  ],
  soft_wall_days: 24, soft_wall_date: '2026-10-03', hard_wall_days: 32, hard_wall_date: '2026-10-11',
  order_by_days: -21, order_by_date: '2026-08-19', order_by_wall: 'soft', late: true,
})

const ram = res({
  resource: 'memory_gib', label: 'Memory', unit: 'GiB',
  per_machine: 512, reserve: 512, overcommit_ratio: 1,
  raw: 5120, usable: 4608, sellable: 4608,
  guaranteed: 2560, burstable: 2048, burstable_physical: 2048, spot: 64, spot_physical: 64,
  sold_nominal: 4608, remaining: 0, guaranteed_ceiling: 2048,
  physical_used: 4608, physical_free: 0, spot_room: 0, spot_reclaim: 64,
  utilisation_pct: 100, status: 'critical',
  series: [{ class: 'guaranteed', label: 'Guaranteed', days: [{ day: '2026-09-01', consumed: 2048 }, { day: '2026-09-08', consumed: 2496 }], growth_per_day: 64 }],
  soft_wall_days: 0, soft_wall_date: '2026-09-09', hard_wall_days: 32, hard_wall_date: '2026-10-11',
  order_by_days: -45, order_by_date: '2026-07-26', order_by_wall: 'soft', late: true,
})

const overview: CapacityOverview = {
  as_of: '2026-09-09T09:00:00Z',
  sources: 2,
  lagging_sources: 0,
  thresholds: { warn_pct: 70, critical_pct: 85 },
  classes: [
    { class: 'guaranteed', label: 'Guaranteed', note: 'physically backed at 1:1' },
    { class: 'burstable', label: 'Burstable', note: 'throttled at the soft wall' },
    { class: 'spot', label: 'Spot', note: 'reclaimed when the room shrinks' },
  ],
  resource_kinds: [
    { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', position: 10 },
    { resource: 'memory_gib', label: 'Memory', unit: 'GiB', position: 20 },
    { resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', position: 30 },
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
          unplaced_skus: [],
          pools: [
            {
              id: 'pa', zone_id: 'za', name: 'm7n-a', machines: 10, lead_time_days: 45, source: 'manual', note: 'batch one',
              updated_by: 'ops@nc.example', updated_at: '2026-09-09T08:00:00Z',
              resources: [
                { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', per_machine: 64, reserve: 64, overcommit_ratio: 4 },
                { resource: 'memory_gib', label: 'Memory', unit: 'GiB', per_machine: 512, reserve: 512, overcommit_ratio: 1 },
              ],
              status: 'critical', binding_resource: 'memory_gib', utilisation_pct: 100,
              resources_view: [vcpu, ram],
              placements: [
                { sku: 'ecs.m7n.2xlarge.8', class: 'guaranteed', shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'seed', units: 40, resources: 1 },
                { sku: 'ecs.m7n.2xlarge.8.burst', class: 'burstable', shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'manual', units: 32, resources: 1 },
                { sku: 'ecs.s7n.2xlarge.2', class: 'spot', shape: { vcpu: 8, memory_gib: 16 }, shape_source: 'derived', units: 4, resources: 1 },
              ],
              basket: {
                items: [
                  { sku: 'ecs.m7n.2xlarge.8', units: 1, class: 'guaranteed', shape: { vcpu: 8, memory_gib: 64 } },
                  { sku: 'ecs.m7n.2xlarge.8.burst', units: 0.8, class: 'burstable', shape: { vcpu: 8, memory_gib: 64 } },
                ],
                units: 0,
                reason: '',
                binding_resource: 'memory_gib',
                resources: [
                  { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', per_basket: 14.4, remaining: 768, units: 20 },
                  { resource: 'memory_gib', label: 'Memory', unit: 'GiB', per_basket: 115.2, remaining: 0, units: 0 },
                ],
                unshaped_skus: [],
              },
              zone_unknown: true,
              order_by_days: -45, order_by_date: '2026-07-26', order_by_resource: 'memory_gib', order_by_wall: 'soft', late: true,
            },
          ],
        },
        {
          id: 'zb',
          code: 'me-east-215b',
          name: '',
          is_default: false,
          unplaced_skus: [
            { sku: 'eip', units: 1, reason: 'no-placement', resources: 1 },
            { sku: 'ecs.gpu.large', units: 24, reason: 'resource-unplaced', resource: 'vcpu', resources: 3 },
          ],
          pools: [
            {
              id: 'pb', zone_id: 'zb', name: 'planned-c', machines: 0, lead_time_days: 0, source: 'manual', note: '',
              updated_by: 'ops@nc.example', updated_at: '2026-09-09T08:00:00Z',
              resources: [{ resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', per_machine: 0, reserve: 0, overcommit_ratio: 1 }],
              status: 'unset', binding_resource: '', utilisation_pct: null,
              resources_view: [res({ resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', machines: 0, per_machine: 0, reserve: 0, overcommit_ratio: 1, raw: 0, usable: 0, sellable: 0, guaranteed: 0, burstable: 0, burstable_physical: 0, spot: 0, spot_physical: 0, sold_nominal: 0, remaining: 0, guaranteed_ceiling: 0, physical_used: 0, physical_free: 0, spot_room: 0, spot_reclaim: 0, sized: false, utilisation_pct: null, status: 'unset', history_days: 0 })],
              placements: [],
              basket: { items: [], units: null, reason: 'no resource this mix consumes is sized on this pool: enter the machines and the per-machine vector first', binding_resource: '', resources: [], unshaped_skus: [] },
              zone_unknown: false,
              order_by_days: null, order_by_date: null, order_by_resource: '', order_by_wall: '', late: false,
            },
          ],
        },
      ],
    },
  ],
  unshaped_skus: [{ sku: 'nat.1', unit: 'hour', quantity: 1, resources: 1, regions: ['me-east-215'] }],
  unmapped_regions: [{ region: 'eu-west-101', reason: 'no-region', skus: 1, quantity: 1, resources: 1 }],
  summary: {
    regions: 1, zones: 2, pools: 2, pools_sized: 1, pools_warn: 0, pools_critical: 1, pools_past_threshold: 1,
    pools_to_order: 1, pools_order_late: 1, placements: 3, shapes: 8, unplaced_skus: 2, unshaped_skus: 1, spot_to_reclaim: 1,
  },
}

const shapes: CapacityShapes = {
  shapes: [
    { sku: 'ecs.m7n.2xlarge.8', resources: { vcpu: 8, memory_gib: 64 }, source: 'seed', updated_at: '2026-09-09T00:00:00Z' },
    { sku: 'evs.ssd.gb', resources: { block_ssd_gib: 1 }, source: 'seed' },
    { sku: 'ecs.m7n.2xlarge.8.burst', resources: { vcpu: 8, memory_gib: 64 }, source: 'manual' },
  ],
  resource_kinds: overview.resource_kinds,
  unseeded_skus: ['elb', 'nat.1', 'vpc'],
}

/** The same Sovereign with a zone nobody has put a pool in yet. */
const withEmptyZone: CapacityOverview = {
  ...overview,
  regions: [
    {
      ...overview.regions[0],
      zones: [...overview.regions[0].zones, { id: 'zc', code: 'me-east-215c', name: '', is_default: false, unplaced_skus: [], pools: [] }],
    },
  ],
}

let docs: { overview: CapacityOverview | null; shapes: CapacityShapes | null } = { overview, shapes }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const data = path === '/capacity/overview' ? docs.overview : path === '/capacity/shapes' ? docs.shapes : null
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
  it('renders the pool as a set of machines, the class split, the stranded vCPU, the walls and the order-by date', () => {
    const html = render()

    // The KPI strip: how many pools, how many are past the line, and how many
    // orders are owed — with the late count, which is the actionable one.
    expect(html).toContain('Pools past 70 %')
    expect(html).toContain('1 of 2 sized')
    expect(html).toContain('Orders owed')
    expect(html).toContain('1 already past the order-by date')
    expect(html).toContain('2026-09-09 09:00Z')
    expect(html).toContain('2 cloud sources')

    // THE POOL IS A SET OF MACHINES, and says what one of them holds.
    expect(html).toContain('m7n-a')
    expect(html).toContain('10 machines')
    expect(html).toContain('64 vCPU · 512 GiB')
    expect(html).toContain('45 days to procure')
    expect(html).toContain('me-east-215 / me-east-215a')
    expect(html).toContain('batch one')

    // THE BINDING RESOURCE IS NAMED, and the free vCPU behind it is reported
    // as STRANDED rather than as headroom.
    expect(html).toContain('Binds first')
    expect(html).toContain('badge bad">Memory')
    expect(html).toContain('192 vCPU stranded')
    expect(html).toContain('because Memory ran out')

    // The vector, per resource, with the ratio that produced sellable.
    expect(html).toContain('4:1')
    expect(html).toContain('1:1')
    expect(html).toContain('1,344') // vCPU sellable
    expect(html).toContain('4,608') // RAM usable and sellable
    expect(html).toContain('sellable = guaranteed + (usable − guaranteed) × ratio')

    // THE CLASS SPLIT, named — and the spot that has to be freed, with the
    // line that says whose decision the WHICH is.
    expect(html).toContain('Guaranteed 320')
    expect(html).toContain('Burstable 256')
    expect(html).toContain('Spot 32')
    expect(html).toContain('64 GiB of spot must be freed')
    expect(html).toContain('the platform decides which instances')

    // THE ORDER-BY DATE, and the fact it has passed. Both walls are on the
    // row so the reader can see which one drove it.
    expect(html).toContain('2026-07-26')
    expect(html).toContain('45 days ago')
    expect(html).toContain('2026-08-19')
    expect(html).toContain('21 days ago')
    expect(html).toContain('Soft wall 2026-10-03')
    expect(html).toContain('8 vCPU a day')
    expect(html).toContain('not growing')

    // ONE BASKET, not a per-SKU maximum. The answer is 0 and the resource
    // that produced it is named.
    expect(html).toContain('How many more fit')
    expect(html).toContain('0 more of this mix')
    expect(html).toContain('limited by Memory')
    expect(html).toContain('1 × ecs.m7n.2xlarge.8')
    expect(html).toContain('0.8 × ecs.m7n.2xlarge.8.burst')

    // The placements, with the class on each — the same shape at two classes
    // is two SKUs, and the page shows exactly that.
    expect(html).toContain('ecs.m7n.2xlarge.8.burst')
    expect(html).toContain('badge ok">Guaranteed')
    expect(html).toContain('badge warn">Burstable')
    expect(html).toContain('badge info">Spot')
    expect(html).toContain('derived from the name')

    // Usage that landed nowhere, BY NAME rather than summed away.
    expect(html).toContain('Metered here, counted against nothing')
    expect(html).toContain('no pool in this zone takes it')
    expect(html).toContain('placed, but no pool it is placed on holds vCPU')
    expect(html).toContain('SKUs with no shape')
    expect(html).toContain('nat.1')
    expect(html).toContain('Add region eu-west-101')

    // The shapes card and the editors.
    expect(html).toContain('SKU shapes')
    expect(html).toContain('badge info">seed')
    expect(html).toContain('no per-unit shape on the list: elb, nat.1, vpc')
    expect(html).toContain('Add region')
    expect(html).toContain('Add zone')
    expect(html).not.toContain('Read-only')
  })

  it('never reads ok on a pool nobody has sized, and says WHY the basket has no number', () => {
    const html = render()
    expect(html).toContain('planned-c')
    // The unsized resource says it is unsized; it does not print 0 % or "ok".
    expect(html).toContain('not sized — enter the machines and what one holds')
    expect(html).toContain('heat unset')
    // And the basket prints the server's reason in words, never a figure.
    expect(html).toContain('Not measured')
    expect(html).toContain('no resource this mix consumes is sized on this pool')
    expect(html).toContain('Nothing is placed on this pool yet')
  })

  it('is read-only without capacity.manage: no forms, the permission named, every figure still there', () => {
    who = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read', 'audit.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
    const html = render()
    expect(html).toContain('capacity.manage')
    expect(html).toContain('Read-only')
    expect(html).not.toContain('>Edit pool<')
    expect(html).not.toContain('aria-label="Add a region"')
    expect(html).not.toContain('>Place<')
    expect(html).not.toContain('Add region eu-west-101')
    // Still every figure.
    expect(html).toContain('192 vCPU stranded')
    expect(html).toContain('0 more of this mix')
    expect(html).toContain('2026-07-26')
  })

  // DEFECT 2, the founder on 0.1.42: "where is the add pool". It existed, at
  // the very bottom of the page inside the regions-and-zones administration
  // block, and the person who commissioned it could not find it. These hold
  // that it is now offered WHERE THE POOLS ARE — and still offered where it
  // always was, so nobody's muscle memory breaks and there is one dialog.
  it('offers Add a pool from the page header, from every zone, and from a zone that has none', () => {
    who = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    docs = { overview: withEmptyZone, shapes }
    const html = render()

    // The page header carries the action, where this console's page-level
    // actions live.
    expect(html).toContain('<div class="actions"><button class="small primary">Add a pool</button></div>')

    // Every zone has its own strip, naming the zone and how many pools are on
    // it, with the button beside the pools it adds to.
    expect(html).toContain('aria-label="Zone me-east-215a"')
    expect(html).toContain('aria-label="Zone me-east-215c"')
    expect(html).toContain('1 pool')

    // A ZONE WITH NO POOLS still offers one, in the space its pools would
    // occupy — the case where there is no pool card to look beside.
    expect(html).toContain('no pools yet')
    expect(html).toContain('No pools in this zone')
    expect(html).toContain('A pool is a set of identical machines somebody bought')

    // One control per zone plus the page header, and the zones block below
    // keeps its own entry: four ways in, no second way of creating a pool.
    expect(html.split('>Add a pool<').length - 1).toBe(5)
    expect(html).toContain('Add a pool — me-east-215c')

    docs = { overview, shapes }
  })

  it('offers none of them without capacity.manage', () => {
    who = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read', 'audit.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
    docs = { overview: withEmptyZone, shapes }
    const html = render()
    expect(html).not.toContain('Add a pool')
    // The zone is still there, still counted, still honest about being empty.
    expect(html).toContain('aria-label="Zone me-east-215c"')
    expect(html).toContain('No pools in this zone')
    docs = { overview, shapes }
  })

  it('explains what a pool is when no region exists', () => {
    who = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    docs = {
      overview: {
        ...overview,
        as_of: null,
        sources: 0,
        regions: [],
        unshaped_skus: [],
        unmapped_regions: [],
        summary: { ...overview.summary, regions: 0, zones: 0, pools: 0, pools_sized: 0, pools_critical: 0, pools_past_threshold: 0, pools_to_order: 0, pools_order_late: 0, unplaced_skus: 0, unshaped_skus: 0, spot_to_reclaim: 0 },
      },
      shapes,
    }
    const html = render()
    expect(html).toContain('No regions yet')
    expect(html).toContain('a POOL for each set of identical machines')
    expect(html).toContain('aria-label="Add region"')
    expect(html).toContain('no cloud usage metered yet')
    expect(html).toContain('no pool is growing towards a wall yet')
    expect(html).not.toContain('m7n-a')
  })
})
