import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { CapacityOverview, CapacityPoolRunning, CapacityResourceView, CapacityShapes, CapacitySKUOptions, Me } from '../api/types'

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
 *   FOUR TABS, one question each, and the Pools tab is ONE LINE per pool.
 *   A POOL LISTS THE CLASSES IT ENFORCES, and a pool without burstable shows
 *   no overcommit and no floor anywhere.
 *   NOTHING IS TYPED THAT CAN BE CHOSEN: SKUs, families, pools and classes are
 *   selects.
 *
 * Effects do not run under renderToString, so every follow-up fetch shows its
 * initial state. Rendering is NOT the validation of this page — a page can
 * render and still white-screen on its first click, which this one once did —
 * so what is pinned here is structure; the clicks are walked in a browser.
 */

const res = (over: Partial<CapacityResourceView>): CapacityResourceView => ({
  resource: 'vcpu', label: 'vCPU', unit: 'vCPU',
  machines: 10, per_machine: 64, reserve: 64, overcommit_ratio: 4,
  guaranteed_floor: 320, floor_free: 0, burstable_envelope: 1024, burstable_room: 768,
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
  guaranteed_floor: 0, floor_free: 0, burstable_envelope: 2048, burstable_room: 0,
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
    { class: 'guaranteed', label: 'Guaranteed', note: 'physically backed at 1:1', requires: 'a fixed allocation' },
    { class: 'burstable', label: 'Burstable', note: 'throttled at the soft wall', requires: 'the host throttling at runtime' },
    { class: 'spot', label: 'Spot', note: 'reclaimed when the room shrinks', requires: 'the right to delete the resource' },
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
          class_mismatches: [{ sku: 'ecs.m7n.xlarge.8', asked: 'spot', counted_as: 'guaranteed', units: 2, resources: 2 }],
          pools: [
            {
              id: 'pa', zone_id: 'za', name: 'm7n-a', machines: 10, classes: ['guaranteed', 'burstable', 'spot'], lead_time_days: 45, source: 'manual', note: 'batch one',
              updated_by: 'ops@nc.example', updated_at: '2026-09-09T08:00:00Z',
              resources: [
                { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', per_machine: 64, reserve: 64, overcommit_ratio: 4, guaranteed_floor: 320 },
                { resource: 'memory_gib', label: 'Memory', unit: 'GiB', per_machine: 512, reserve: 512, overcommit_ratio: 1, guaranteed_floor: 0 },
              ],
              status: 'critical', binding_resource: 'memory_gib', utilisation_pct: 100,
              resources_view: [vcpu, ram],
              placements: [
                // ONE SKU, THREE CLASSES on the same pool — and a family.
                { sku: 'ecs.m7n.2xlarge.8', family: false, class: 'guaranteed', shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'seed', units: 40, resources: 5, matched_skus: [] },
                { sku: 'ecs.m7n.2xlarge.8', family: false, class: 'burstable', shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'seed', units: 32, resources: 4, matched_skus: [] },
                { sku: 'ecs.m7n.2xlarge.8', family: false, class: 'spot', shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'seed', units: 4, resources: 1, matched_skus: [] },
                { sku: 'ecs.s7n.*', family: true, class: 'spot', shape: {}, shape_source: '', units: 3, resources: 3, matched_skus: ['ecs.s7n.2xlarge.2'] },
              ],
              basket: {
                items: [
                  { sku: 'ecs.m7n.2xlarge.8', units: 1, class: 'guaranteed', shape: { vcpu: 8, memory_gib: 64 } },
                  { sku: 'ecs.m7n.2xlarge.8', units: 0.8, class: 'burstable', shape: { vcpu: 8, memory_gib: 64 } },
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
          class_mismatches: [],
          pools: [
            {
              id: 'pb', zone_id: 'zb', name: 'planned-c', machines: 0, classes: ['guaranteed', 'spot'], lead_time_days: 0, source: 'manual', note: '',
              updated_by: 'ops@nc.example', updated_at: '2026-09-09T08:00:00Z',
              resources: [{ resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', per_machine: 0, reserve: 0, overcommit_ratio: 1, guaranteed_floor: 0 }],
              status: 'unset', binding_resource: '', utilisation_pct: null,
              resources_view: [res({ resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', machines: 0, per_machine: 0, reserve: 0, overcommit_ratio: 1, guaranteed_floor: 0, floor_free: 0, burstable_envelope: 0, burstable_room: 0, raw: 0, usable: 0, sellable: 0, guaranteed: 0, burstable: 0, burstable_physical: 0, spot: 0, spot_physical: 0, sold_nominal: 0, remaining: 0, guaranteed_ceiling: 0, physical_used: 0, physical_free: 0, spot_room: 0, spot_reclaim: 0, sized: false, utilisation_pct: null, status: 'unset', history_days: 0 })],
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
    pools_to_order: 1, pools_order_late: 1, placements: 4, shapes: 8, unplaced_skus: 2, unshaped_skus: 1, spot_to_reclaim: 1, class_mismatches: 1,
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
      zones: [...overview.regions[0].zones, { id: 'zc', code: 'me-east-215c', name: '', is_default: false, unplaced_skus: [], class_mismatches: [], pools: [] }],
    },
  ],
}

const skuOptions: CapacitySKUOptions = {
  skus: [
    { sku: 'ecs.m7n.2xlarge.8', in_price_book: true, metered: true, has_shape: true, shape: { vcpu: 8, memory_gib: 64 }, shape_source: 'seed', description: '8 vCPU 64 GB' },
    { sku: 'ecs.s7n.2xlarge.2', in_price_book: true, metered: true, has_shape: true, shape: { vcpu: 8, memory_gib: 16 }, shape_source: 'derived', description: '' },
    { sku: 'nat.1', in_price_book: true, metered: true, has_shape: false, shape: {}, shape_source: '', description: 'NAT gateway' },
  ],
  families: [
    { pattern: 'ecs.*', skus: 2 },
    { pattern: 'ecs.s7n.*', skus: 1 },
  ],
}

const running: CapacityPoolRunning = {
  pool_id: 'pa',
  pool_name: 'm7n-a',
  as_of: '2026-09-09T09:00:00Z',
  resources: [
    { source_id: 's1', resource_id: 'vm-1', name: 'web-1', sku: 'ecs.m7n.2xlarge.8', via: 'ecs.m7n.2xlarge.8', units: 1, unit: 'instance-hour', class: 'guaranteed', class_source: 'default', override_class: '', tag_class: '', asked: '', placed_classes: ['guaranteed', 'burstable', 'spot'], consumes: { vcpu: 8, memory_gib: 64 }, first_seen: '2026-09-01T00:00:00Z', shared_with: [], reclaim: false },
    { source_id: 's1', resource_id: 'vm-9', name: 'batch-9', sku: 'ecs.s7n.2xlarge.2', via: 'ecs.s7n.*', units: 1, unit: 'instance-hour', class: 'spot', class_source: 'tag', override_class: '', tag_class: 'spot', asked: '', placed_classes: ['spot'], consumes: { vcpu: 8, memory_gib: 16 }, first_seen: '2026-09-09T08:00:00Z', shared_with: [], reclaim: true },
  ],
  reclaim: [{ resource: 'memory_gib', label: 'Memory', unit: 'GiB', needed: 64, covered: 16, resources: 1, short: true }],
}

let docs: { overview: CapacityOverview | null; shapes: CapacityShapes | null } = { overview, shapes }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const data =
      path === '/capacity/overview' ? docs.overview : path === '/capacity/shapes' ? docs.shapes : path === '/capacity/skus' ? skuOptions : path === '/capacity/pools/pa/resources' ? running : null
    return { data, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

let who: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign', source: 'config' }] }
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: who, loading: false, refresh: async () => who, logout: async () => {} }),
}))

import { PoolDetail } from '../panels/capacity/PoolDetail'
import { PoolEditor } from '../panels/capacity/PoolEditor'
import { Capacity } from './Capacity'

const clean = (html: string): string => {
  const out = html.replace(/<!-- -->/g, '')
  expect(out).not.toMatch(/NaN|undefined|\[object Object\]/)
  return out
}

/** The page on one of its tabs. */
function render(tab = ''): string {
  return clean(renderToString(createElement(MemoryRouter, { initialEntries: [tab ? `/capacity?tab=${tab}` : '/capacity'] }, createElement(Capacity))))
}

const noAct = { busy: false, error: '', ok: '', run: async () => true, clear: () => {}, setError: () => {} }

/** What opening a pool's row shows. */
function renderDetail(poolIndex: [zone: number, pool: number], canManage = true): string {
  const pool = overview.regions[0].zones[poolIndex[0]].pools[poolIndex[1]]
  return clean(
    renderToString(
      createElement(MemoryRouter, null, createElement(PoolDetail, { pool, kinds: overview.resource_kinds, classes: overview.classes, skus: skuOptions, canManage, act: noAct, onSaved: async () => {}, onPlacements: () => {} })),
    ),
  )
}

function renderEditor(poolIndex: [zone: number, pool: number] | null): string {
  const pool = poolIndex ? overview.regions[0].zones[poolIndex[0]].pools[poolIndex[1]] : null
  return clean(
    renderToString(
      createElement(PoolEditor, { zoneID: 'za', zones: [{ id: 'za', label: 'me-east-215 / me-east-215a' }], pool, kinds: overview.resource_kinds, classes: overview.classes, act: noAct, onClose: () => {}, onSaved: async () => {} }),
    ),
  )
}

describe('Capacity page', () => {
  it('is four tabs, and opens on Pools with ONE LINE per pool', () => {
    const html = render()
    for (const tab of ['Pools', 'Placements', 'Shapes', 'Regions &amp; zones']) expect(html).toContain(`>${tab}<`)
    expect(html).toContain('href="/capacity?tab=regions"')
    // The pools table: a line each, with what the pool ENFORCES, what binds,
    // how full it is and when to order.
    expect(html).toContain('aria-label="Pools"')
    expect(html).toMatch(/<b>m7n-a<\/b>/)
    expect(html).toMatch(/<b>planned-c<\/b>/)
    expect(html).toContain('data-classes="guaranteed,burstable,spot"')
    expect(html).toContain('data-classes="guaranteed,spot"')
    expect(html).toContain('Order was due 45 days ago')
    expect(html).toContain('4,608 / 4,608 GiB')
    // A row is closed until it is opened: none of the drill-in is on the page.
    expect(html).not.toContain('data-pool-detail')
    expect(html).not.toContain('How many more fit')
    // The other tabs' content is not on this one.
    expect(html).not.toContain('SKU shapes')
    expect(html).not.toContain('Place a SKU on a pool')
  })

  it('says what needs attention above the tabs, naming the tab that fixes it', () => {
    const html = render()
    expect(html).toContain('1 pool resource holds more spot than it has room for')
    expect(html).toContain('2 metered SKUs count against nothing')
    expect(html).toContain('1 SKU is running at a class it is not placed at')
    expect(html).toContain('1 metered SKU has no shape')
    expect(html).toContain('>Open Placements<')
    expect(html).toContain('>Open Shapes<')
    expect(html).toContain('Add region eu-west-101')
  })

  it('never reads ok on a pool nobody has sized', () => {
    const html = render()
    expect(html).toMatch(/data-status="unset"[^>]*>(<b>)?—/)
    expect(html).toContain('nothing is sized yet')
  })

  it('shows, when a pool is opened, the floor, the class split, the stranded vCPU, the walls and the order-by date', () => {
    const html = renderDetail([0, 0])
    expect(html).toContain('data-pool-detail="m7n-a"')
    // The floor column exists because the pool enforces burstable.
    expect(html).toContain('>Guaranteed floor<')
    expect(html).toContain('data-floor="320"')
    expect(html).toContain('burstable 768 left of 1,024')
    expect(html).toContain('data-floor="0"')
    expect(html).toContain('192 vCPU stranded')
    expect(html).toContain('64 GiB of spot must be freed')
    expect(html).toContain('2026-07-26')
    expect(html).toContain('Soft wall')
    // ONE basket, with the mix as chips carrying each line's class.
    expect(html).toContain('0 more of this mix')
    expect(html).toContain('limited by Memory')
    expect(html).toMatch(/0\.8 × ecs\.m7n\.2xlarge\.8<\/span><span class="dim">Burstable/)
  })

  it('builds a mix from selects — a SKU list and only the classes the pool enforces — never from typed text', () => {
    const html = renderDetail([0, 0])
    expect(html).toContain('aria-label="Build a mix for m7n-a"')
    expect(html).toMatch(/<select id="mix-sku-pa"/)
    // An unshaped SKU is offered but cannot be picked, and says why.
    expect(html).toMatch(/<option value="nat\.1" disabled="">nat\.1 — NAT gateway · no shape yet/)
    expect(html).not.toContain('sku:units')
    const plain = renderDetail([1, 0])
    // planned-c enforces guaranteed and spot: burstable is not a choice, and
    // there is no ratio or floor column to read.
    expect(plain).not.toMatch(/<option value="burstable"/)
    expect(plain).not.toContain('>Guaranteed floor<')
    expect(plain).toContain('this pool does not enforce burstable')
    expect(plain).toContain('Not measured')
    expect(plain).toContain('enter the machines and the per-machine vector first')
  })

  it('lists what is RUNNING on the pool, each resource’s class and why, and the spot to give back', () => {
    const html = renderDetail([0, 0])
    expect(html).toContain('data-running="m7n-a"')
    expect(html).toMatch(/data-class="guaranteed" data-class-source="default"/)
    expect(html).toContain('nothing says — counted at the most cautious class')
    expect(html).toMatch(/data-class="spot" data-class-source="tag"/)
    expect(html).toContain('through ecs.s7n.*')
    // The operator may say which class a resource was sold as — from the
    // classes its SKU is placed at, and only when there is a choice.
    expect(html).toContain('aria-label="Class web-1 was sold as"')
    expect(html).toMatch(/aria-label="Class batch-9 was sold as"[^>]*disabled=""/)
    // The reclaim list: how much, how much the marked resources cover, and who deletes.
    expect(html).toContain('data-reclaim="memory_gib"')
    expect(html).toContain('Memory: spot holds 64 GiB more than there is room for, and has to give it back.')
    expect(html).toContain('frees only 16 GiB')
    expect(html).toContain('the platform does the deleting')
    expect(html).toContain('>give back<')
    // Read-only: the figures stay, the control goes.
    expect(renderDetail([0, 0], false)).not.toContain('aria-label="Class web-1 was sold as"')
  })

  it('puts every placement in ONE table — the same SKU three times, and a family', () => {
    const html = render('placements')
    expect(html).toContain('aria-label="Placements"')
    expect(html.split('<code>ecs.m7n.2xlarge.8</code>').length - 1).toBe(3)
    expect(html).toContain('aria-label="Remove ecs.m7n.2xlarge.8 as Burstable from m7n-a"')
    expect(html).toContain('aria-label="Remove ecs.m7n.2xlarge.8 as Spot from m7n-a"')
    expect(html).toContain('<code>ecs.s7n.*</code>')
    expect(html).toContain('taking ecs.s7n.2xlarge.2')
    // The form: a SKU select with a family toggle, a pool select, and a class
    // select that waits for the pool — no text box anywhere.
    expect(html).toContain('aria-label="Place a SKU on a pool"')
    expect(html).toContain('>One SKU<')
    expect(html).toContain('>A family<')
    expect(html).toMatch(/<select id="place-sku"/)
    expect(html).toContain('choose the pool first: it decides which classes exist')
    expect(html).not.toMatch(/<input[^>]*placeholder="ecs\.m7n/)
    // What counts against nothing, and what runs at a class it is not placed at.
    expect(html).toContain('data-unplaced="eip"')
    expect(html).toContain('no pool in this zone takes it')
    expect(html).toContain('placed, but no pool it is placed on holds vCPU')
    expect(html).toContain('data-mismatch="ecs.m7n.xlarge.8"')
    expect(html).toContain('says Spot, which this SKU is not placed at here, so it counts as Guaranteed')
  })

  it('keeps shapes on their own tab, the unshaped SKUs first', () => {
    const html = render('shapes')
    expect(html).toContain('SKUs with no shape')
    expect(html).toContain('aria-label="Add a shape for nat.1"')
    expect(html).toContain('SKU shapes')
    expect(html).toContain('aria-label="Edit the shape of evs.ssd.gb"')
    expect(html).not.toContain('aria-label="Pools"')
  })

  it('keeps regions and zones on their own tab, a zone per line', () => {
    const html = render('regions')
    expect(html).toContain('aria-label="Zones of me-east-215"')
    expect(html).toContain('data-zone="me-east-215a"')
    expect(html).toContain('aria-label="Add a pool in me-east-215b"')
    expect(html).toContain('aria-label="Delete zone me-east-215a"')
  })

  it('asks which classes a pool enforces FIRST, and hides overcommit and the floor without burstable', () => {
    const fresh = renderEditor(null)
    expect(fresh).toContain('Classes this pool enforces')
    // A new pool enforces guaranteed and spot: burstable is opted into.
    expect(fresh).toMatch(/<input type="checkbox" checked=""[^>]*\/> <b>Guaranteed<\/b>/)
    expect(fresh).toMatch(/<input type="checkbox"(?! checked)[^>]*\/> <b>Burstable<\/b>/)
    expect(fresh).toContain('the host throttling at runtime')
    expect(fresh).not.toContain('>Overcommit<')
    expect(fresh).not.toContain('>Guaranteed floor<')
    expect(fresh).toContain('no overcommit and no guaranteed floor')
    // The resource is a select over the kinds, with a way to name one's own.
    expect(fresh).toMatch(/<select aria-label="Resource 1"/)
    expect(fresh).toContain('a resource kind of my own…')

    const editing = renderEditor([0, 0])
    expect(editing).toContain('>Overcommit<')
    expect(editing).toContain('>Guaranteed floor<')
    // 576 usable at 4:1 with 320 kept for guaranteed: burstable capped at 1,024.
    expect(editing).toContain('320 kept for guaranteed · burstable capped at 1,024')
    // A class placements still use cannot be unticked, and says why.
    expect(editing).toMatch(/<input type="checkbox"(?=[^>]*checked="")(?=[^>]*disabled="")[^>]*\/> <b>Burstable<\/b>/)
    expect(editing).toContain('SKUs are still placed here as Burstable')
  })

  it('is read-only without capacity.manage: no forms, the permission named, every figure still there', () => {
    const before = who
    who = { ...who, permissions: { sovereign: ['metering.read'] } }
    try {
      const html = render()
      expect(html).toContain('Read-only')
      expect(html).toContain('capacity.manage')
      expect(html).not.toContain('>Add a pool<')
      expect(html).not.toContain('>Edit pool<')
      expect(html).toContain('4,608 / 4,608 GiB')
      const placements = render('placements')
      expect(placements).not.toContain('Place a SKU on a pool')
      expect(placements).not.toContain('>remove<')
      expect(placements).toContain('<code>ecs.s7n.*</code>')
    } finally {
      who = before
    }
  })

  it('offers Add a pool from the page header, and from every zone on the Regions tab', () => {
    const before = docs
    docs = { ...docs, overview: withEmptyZone }
    try {
      expect(render().split('>Add a pool<').length - 1).toBe(1)
      // Three zones, a button each, plus the header's.
      expect(render('regions').split('>Add a pool<').length - 1).toBe(4)
    } finally {
      docs = before
    }
  })

  it('explains what a pool is when no region exists', () => {
    const before = docs
    docs = { ...docs, overview: { ...overview, regions: [], unmapped_regions: [], summary: { ...overview.summary, regions: 0, zones: 0, pools: 0, pools_sized: 0 } } }
    try {
      const html = render()
      expect(html).toContain('No regions yet')
      expect(html).toContain('Add region')
      expect(html).not.toContain('href="/capacity?tab=pools"')
    } finally {
      docs = before
    }
  })
})
