import { describe, expect, it } from 'vitest'
import type { CapacityBasket, CapacityOverview, CapacityPoolView, CapacityResourceKind } from '../api/types'
import {
  DEFAULT_POOL_CLASSES,
  asOfLabel,
  basketAnswer,
  basketQuery,
  basketSummary,
  bindingOf,
  classLabel,
  floorEffect,
  formatAmount,
  formatDays,
  formatRatio,
  formatUtilisation,
  heatClass,
  isFamily,
  isSized,
  kindOf,
  orderedClasses,
  parseBasketQuery,
  parsePoolForm,
  poolFingerprint,
  parseShapeForm,
  poolRows,
  reserveForMachines,
  shapeSummary,
  sortedResourceKeys,
  sparkPath,
  utilisationStatus,
  vectorSummary,
  zoneRows,
} from './capacity'

/**
 * The display arithmetic of Plan → Capacity (DESIGN.md §11), pinned against
 * the same figures the Go tests derive.
 *
 * The property that runs through all of it: A FIGURE READ FROM A FIELD THAT
 * IS STRUCTURALLY EMPTY MUST NEVER RENDER AS GOOD NEWS. An unsized pool is
 * `unset`, not `ok`; a basket with nothing measurable returns its reason, not
 * a number; a negative order-by is "21 days ago", not "—" and not 0.
 */

const kinds: CapacityResourceKind[] = [
  { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', position: 10 },
  { resource: 'memory_gib', label: 'Memory', unit: 'GiB', position: 20 },
  { resource: 'gpu_cards', label: 'GPU cards', unit: 'cards', position: 1000 },
]

describe('resource kinds are data, not an enum', () => {
  it('reads a kind from the server, falls back to the built-ins, and still answers for a key nobody named', () => {
    expect(kindOf('vcpu', kinds).label).toBe('vCPU')
    expect(kindOf('block_ssd_gib', kinds).unit).toBe('GiB') // built-in fallback
    expect(kindOf('fpga_slots', kinds)).toEqual({ resource: 'fpga_slots', label: 'fpga_slots', unit: '', position: 1000 })
  })

  it('orders resources by the catalogue, then by key', () => {
    expect(sortedResourceKeys(['gpu_cards', 'memory_gib', 'accel', 'vcpu'], kinds)).toEqual(['vcpu', 'memory_gib', 'accel', 'gpu_cards'])
  })

  it('names the three classes', () => {
    expect(classLabel('guaranteed')).toBe('Guaranteed')
    expect(classLabel('spot')).toBe('Spot')
    expect(classLabel('whatever')).toBe('whatever')
  })
})

describe('status never reads ok on an empty field', () => {
  it('classifies a percentage of sellable, and unset without one', () => {
    expect(utilisationStatus(null)).toBe('unset')
    expect(utilisationStatus(undefined)).toBe('unset')
    expect(utilisationStatus(Number.NaN)).toBe('unset')
    expect(utilisationStatus(0)).toBe('ok')
    expect(utilisationStatus(69.9)).toBe('ok')
    expect(utilisationStatus(70)).toBe('warn')
    expect(utilisationStatus(84.9)).toBe('warn')
    expect(utilisationStatus(85)).toBe('critical')
    expect(utilisationStatus(220)).toBe('critical')
    expect(utilisationStatus(50, { warn_pct: 40, critical_pct: 45 })).toBe('critical')
  })

  it('colours a cell by status, and an unknown status as unset', () => {
    expect(heatClass('warn')).toBe('heat warn')
    expect(heatClass('unset')).toBe('heat unset')
    expect(heatClass('something-else')).toBe('heat unset')
  })

  it('renders a missing percentage as an em dash, never as 0 %', () => {
    expect(formatUtilisation(null)).toBe('—')
    expect(formatUtilisation(80)).toBe('80.0 %')
    expect(formatUtilisation(100)).toBe('100.0 %')
  })
})

describe('amounts, ratios and vectors', () => {
  it('formats exact decimals with grouping and no trailing zeros', () => {
    expect(formatAmount('4608.000000')).toBe('4,608')
    expect(formatAmount(0)).toBe('0')
    expect(formatAmount(null)).toBe('—')
    expect(formatAmount('')).toBe('—')
    expect(formatAmount('1.5')).toBe('1.5')
  })

  it('reads a ratio the way an operator says it', () => {
    expect(formatRatio('4.000000')).toBe('4:1')
    expect(formatRatio('1.500000')).toBe('1.5:1')
    expect(formatRatio(0)).toBe('1:1') // no ratio is 1:1, never 0:1
    expect(formatRatio(null)).toBe('1:1')
  })

  it('says what one machine holds, and what one unit of a SKU consumes', () => {
    expect(
      vectorSummary(
        [
          { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', per_machine: 64, reserve: 64, overcommit_ratio: 4, guaranteed_floor: 0 },
          { resource: 'memory_gib', label: 'Memory', unit: 'GiB', per_machine: 512, reserve: 512, overcommit_ratio: 1, guaranteed_floor: 0 },
        ],
        kinds,
      ),
    ).toBe('64 vCPU · 512 GiB')
    expect(vectorSummary([], kinds)).toBe('')
    expect(shapeSummary({ memory_gib: 64, vcpu: 8 }, kinds)).toBe('8 vCPU · 64 GiB')
    expect(shapeSummary({ vcpu: 0 }, kinds)).toBe('')
    expect(shapeSummary(null, kinds)).toBe('')
  })
})

describe('the order-by date is the one that matters', () => {
  it('says how far off a wall is, and says a date has PASSED rather than hiding it', () => {
    expect(formatDays(null)).toBe('—')
    expect(formatDays(0)).toBe('today')
    expect(formatDays(24)).toBe('24 days')
    expect(formatDays(1)).toBe('1 day')
    expect(formatDays(82)).toBe('2.7 months')
    expect(formatDays(800)).toBe('2.2 years')
    // THE CASE THE WHOLE FEATURE EXISTS FOR: the lead time is longer than the
    // time left, so the order is already late. Rendering this as "—" or as 0
    // would hide the only reading that requires action today.
    expect(formatDays(-21)).toBe('21 days ago')
    expect(formatDays(-45)).toBe('45 days ago')
  })

  it('renders the measured hour', () => {
    expect(asOfLabel('2026-09-09T09:00:00Z')).toBe('2026-09-09 09:00Z')
    expect(asOfLabel(null)).toBe('')
    expect(asOfLabel('not a date')).toBe('not a date')
  })
})

describe('a basket answers with a number or with a reason, never with a silent zero', () => {
  const measured: CapacityBasket = {
    items: [{ sku: 'ecs.m7n.2xlarge.8', units: 1, class: 'guaranteed', shape: { vcpu: 8, memory_gib: 64 } }],
    units: 24,
    reason: '',
    binding_resource: 'vcpu',
    resources: [],
    unshaped_skus: [],
  }

  it('reports the count and the binding resource when it was measured', () => {
    expect(basketAnswer(measured, kinds)).toEqual({ units: 24, text: '24 more', binding: 'vCPU' })
  })

  it('reports ZERO as zero — a full pool is a measurement, not an absence', () => {
    expect(basketAnswer({ ...measured, units: 0, binding_resource: 'memory_gib' }, kinds)).toEqual({ units: 0, text: '0 more', binding: 'Memory' })
  })

  it('reports the REASON, in words, when nothing could be measured', () => {
    const unmeasured: CapacityBasket = { ...measured, units: null, binding_resource: '', reason: 'no resource this mix consumes is sized on this pool' }
    expect(basketAnswer(unmeasured, kinds)).toEqual({ units: null, text: 'no resource this mix consumes is sized on this pool', binding: '' })
    // And with no basket at all, still words rather than a figure.
    expect(basketAnswer(null, kinds).units).toBeNull()
  })

  it('round-trips a mix through the query form the server parses', () => {
    expect(basketQuery([{ sku: 'ecs.m7n.2xlarge.8', units: '2' }, { sku: 'evs.ssd.gb', units: 100 }])).toBe('ecs.m7n.2xlarge.8:2,evs.ssd.gb:100')
    expect(basketQuery([{ sku: ' ', units: 1 }, { sku: 'x', units: 0 }])).toBe('')
    expect(parseBasketQuery('ecs.m7n.2xlarge.8:2, evs.ssd.gb')).toEqual([
      { sku: 'ecs.m7n.2xlarge.8', units: '2', class: '' },
      { sku: 'evs.ssd.gb', units: '1', class: '' },
    ])
    expect(parseBasketQuery(' , : , ')).toEqual([])
  })

  it('says a mix in words', () => {
    expect(basketSummary(measured.items)).toBe('1 × ecs.m7n.2xlarge.8')
    expect(basketSummary([])).toBe('')
  })
})

describe('the pool editor refuses what the server refuses, before the round trip', () => {
  const good = {
    name: ' m7n-a ',
    machines: '10',
    lead_time_days: '45',
    note: 'batch one',
    classes: ['spot', 'Burstable', 'guaranteed'],
    resources: [
      { resource: 'VCPU', per_machine: '64', reserve: '64', overcommit_ratio: '4', guaranteed_floor: '200' },
      { resource: 'memory_gib', per_machine: '512', reserve: '512', overcommit_ratio: '1', guaranteed_floor: '' },
    ],
  }

  it('builds the body, lower-casing and trimming the keys', () => {
    const { body, error } = parsePoolForm(good)
    expect(error).toBe('')
    expect(body).toEqual({
      name: 'm7n-a',
      machines: '10',
      classes: ['guaranteed', 'burstable', 'spot'],
      lead_time_days: 45,
      note: 'batch one',
      resources: [
        { resource: 'vcpu', per_machine: '64', reserve: '64', overcommit_ratio: '4', guaranteed_floor: '200' },
        { resource: 'memory_gib', per_machine: '512', reserve: '512', overcommit_ratio: '1', guaranteed_floor: '0' },
      ],
    })
  })

  it('sends ratio 1 and floor 0 when the pool does not enforce burstable, whatever the form still holds', () => {
    // The editor hides both columns once burstable is unticked; what was typed
    // before must not reach the API, which would refuse it.
    const { body, error } = parsePoolForm({ ...good, classes: ['guaranteed', 'spot'] })
    expect(error).toBe('')
    expect(body?.classes).toEqual(['guaranteed', 'spot'])
    expect(body?.resources.map((r) => [r.overcommit_ratio, r.guaranteed_floor])).toEqual([['1', '0'], ['1', '0']])
  })

  it('refuses a pool that enforces nothing, and a floor above what is usable', () => {
    expect(parsePoolForm({ ...good, classes: [] }).error).toMatch(/at least one class/)
    // 10 machines x 64 less a reserve of 64 = 576 usable.
    const over = parsePoolForm({ ...good, resources: [{ ...good.resources[0], guaranteed_floor: '577' }] })
    expect(over.body).toBeNull()
    expect(over.error).toMatch(/guaranteed floor is 577 but only 576 is usable/)
    expect(parsePoolForm({ ...good, resources: [{ ...good.resources[0], guaranteed_floor: '576' }] }).error).toBe('')
  })

  it('defaults a blank reserve to 0 and a blank ratio to 1 — never to nothing', () => {
    const { body } = parsePoolForm({ ...good, resources: [{ resource: 'vcpu', per_machine: '64', reserve: '', overcommit_ratio: '', guaranteed_floor: '' }] })
    expect(body?.resources[0]).toEqual({ resource: 'vcpu', per_machine: '64', reserve: '0', overcommit_ratio: '1', guaranteed_floor: '0' })
  })

  it('refuses a pool with no name, no resource, a bad number or a zero ratio', () => {
    expect(parsePoolForm({ ...good, name: '  ' }).error).toMatch(/name the pool/)
    expect(parsePoolForm({ ...good, resources: [] }).error).toMatch(/a machine with nothing in it is not capacity/)
    expect(parsePoolForm({ ...good, resources: [{ resource: '  ', per_machine: '1', reserve: '', overcommit_ratio: '', guaranteed_floor: '' }] }).error).toMatch(/at least one resource/)
    expect(parsePoolForm({ ...good, machines: '-1' }).error).toMatch(/machines must be a non-negative number/)
    expect(parsePoolForm({ ...good, machines: 'lots' }).error).toMatch(/machines must be/)
    expect(parsePoolForm({ ...good, lead_time_days: '3.5' }).error).toMatch(/whole number of days/)
    expect(parsePoolForm({ ...good, resources: [{ resource: 'vcpu', per_machine: 'x', reserve: '', overcommit_ratio: '', guaranteed_floor: '' }] }).error).toMatch(/per machine/)
    expect(parsePoolForm({ ...good, resources: [{ resource: 'vcpu', per_machine: '1', reserve: '-2', overcommit_ratio: '', guaranteed_floor: '' }] }).error).toMatch(/reserve/)
    expect(parsePoolForm({ ...good, resources: [{ resource: 'vcpu', per_machine: '1', reserve: '', overcommit_ratio: '0', guaranteed_floor: '' }] }).error).toMatch(/positive number/)
    expect(parsePoolForm({ ...good, resources: [good.resources[0], { ...good.resources[0], resource: 'vcpu' }] }).error).toMatch(/listed twice/)
    // Every refusal returns NO body: a half-built pool never reaches the API.
    expect(parsePoolForm({ ...good, name: '' }).body).toBeNull()
  })

  it('N+1 is one machine’s worth of the resource', () => {
    expect(reserveForMachines('512', 1)).toBe('512')
    expect(reserveForMachines('64', 2)).toBe('128')
    expect(reserveForMachines('', 1)).toBe('0')
    expect(reserveForMachines('64', 0)).toBe('0')
  })
})

describe('the shape editor', () => {
  it('drops blanks and zeros (they remove the resource) and lower-cases the keys', () => {
    expect(parseShapeForm({ VCPU: '8', memory_gib: '64', block_ssd_gib: '', eip_addresses: '0' })).toEqual({ resources: { vcpu: '8', memory_gib: '64' }, error: '' })
    expect(parseShapeForm({})).toEqual({ resources: {}, error: '' })
  })

  it('names the resource in the refusal', () => {
    expect(parseShapeForm({ vcpu: '-1' }).error).toMatch(/^vcpu:/)
    expect(parseShapeForm({ gpu_cards: 'two' }).error).toMatch(/^gpu_cards:/)
  })
})

describe('reading the overview', () => {
  const pool = (over: Partial<CapacityPoolView>): CapacityPoolView => ({
    id: 'p',
    zone_id: 'z',
    name: 'm7n-a',
    machines: 10,
    classes: ['guaranteed', 'spot'],
    lead_time_days: 45,
    source: 'manual',
    note: '',
    updated_by: '',
    updated_at: '2026-09-09T08:00:00Z',
    resources: [],
    status: 'unset',
    binding_resource: '',
    utilisation_pct: null,
    resources_view: [],
    placements: [],
    basket: { items: [], units: null, reason: 'nothing is selling on this pool yet', binding_resource: '', resources: [], unshaped_skus: [] },
    zone_unknown: false,
    order_by_days: null,
    order_by_date: null,
    order_by_resource: '',
    order_by_wall: '',
    late: false,
    ...over,
  })

  const ov = {
    as_of: '2026-09-09T09:00:00Z',
    sources: 1,
    lagging_sources: 0,
    thresholds: { warn_pct: 70, critical_pct: 85 },
    classes: [],
    resource_kinds: kinds,
    regions: [
      {
        id: 'r1',
        code: 'me-east-215',
        name: 'Muscat',
        cloud_source_kind: 'huawei-project',
        zones: [
          { id: 'za', code: 'me-east-215a', name: 'AZ 1', is_default: true, pools: [pool({ id: 'pa' }), pool({ id: 'pb', name: 'm7n-b' })], unplaced_skus: [], class_mismatches: [] },
          { id: 'zb', code: 'me-east-215b', name: '', is_default: false, pools: [], unplaced_skus: [], class_mismatches: [] },
        ],
      },
      { id: 'r2', code: 'eu-west-101', name: '', cloud_source_kind: 'huawei-project', zones: [{ id: 'zc', code: 'eu-west-101a', name: '', is_default: true, pools: [pool({ id: 'pc' })], unplaced_skus: [], class_mismatches: [] }] },
    ],
    unshaped_skus: [],
    unmapped_regions: [],
    summary: {
      regions: 2, zones: 3, pools: 3, pools_sized: 0, pools_warn: 0, pools_critical: 0, pools_past_threshold: 0,
      pools_to_order: 0, pools_order_late: 0, placements: 0, shapes: 6, unplaced_skus: 0, unshaped_skus: 0, spot_to_reclaim: 0, class_mismatches: 0,
    },
  } satisfies CapacityOverview

  it('flattens zones and pools in the server’s order, and filters by region', () => {
    expect(zoneRows(ov).map((r) => r.zone.code)).toEqual(['me-east-215a', 'me-east-215b', 'eu-west-101a'])
    expect(zoneRows(ov, 'eu-west-101').map((r) => r.zone.code)).toEqual(['eu-west-101a'])
    expect(poolRows(ov).map((r) => r.pool.id)).toEqual(['pa', 'pb', 'pc'])
    expect(poolRows(ov, 'me-east-215').map((r) => r.pool.id)).toEqual(['pa', 'pb'])
    // A zone with no pools contributes no rows but is still a zone: the page
    // shows it under Regions so an operator can add one.
    expect(poolRows(ov).some((r) => r.zone.code === 'me-east-215b')).toBe(false)
    expect(zoneRows(null)).toEqual([])
    expect(poolRows(undefined)).toEqual([])
  })

  it('finds the binding resource of a pool, and reports an unsized pool as unsized', () => {
    const sized = pool({
      binding_resource: 'memory_gib',
      resources_view: [
        { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', machines: 10, per_machine: 64, reserve: 64, overcommit_ratio: 4, guaranteed_floor: 0, floor_free: 0, burstable_envelope: 1024, burstable_room: 768, raw: 640, usable: 576, sellable: 1344, guaranteed: 320, burstable: 256, burstable_physical: 64, spot: 0, spot_physical: 0, sold_nominal: 576, remaining: 768, guaranteed_ceiling: 256, physical_used: 384, physical_free: 192, stranded: true, spot_room: 768, spot_reclaim: 0, sized: true, utilisation_pct: 42.9, status: 'ok', overcommitted: false, over: 0, series: [], history_days: 0, soft_wall_days: null, soft_wall_date: null, hard_wall_days: null, hard_wall_date: null, order_by_days: null, order_by_date: null, order_by_wall: '', late: false },
        { resource: 'memory_gib', label: 'Memory', unit: 'GiB', machines: 10, per_machine: 512, reserve: 512, overcommit_ratio: 1, guaranteed_floor: 0, floor_free: 0, burstable_envelope: 2048, burstable_room: 0, raw: 5120, usable: 4608, sellable: 4608, guaranteed: 2560, burstable: 2048, burstable_physical: 2048, spot: 0, spot_physical: 0, sold_nominal: 4608, remaining: 0, guaranteed_ceiling: 2048, physical_used: 4608, physical_free: 0, stranded: false, spot_room: 0, spot_reclaim: 0, sized: true, utilisation_pct: 100, status: 'critical', overcommitted: false, over: 0, series: [], history_days: 0, soft_wall_days: null, soft_wall_date: null, hard_wall_days: null, hard_wall_date: null, order_by_days: null, order_by_date: null, order_by_wall: '', late: false },
      ],
    })
    expect(bindingOf(sized)?.label).toBe('Memory')
    // The binding resource is full while the OTHER one still has 192 free:
    // that free hardware is stranded, and the flag says so.
    expect(bindingOf(sized)?.remaining).toBe(0)
    expect(sized.resources_view[0].stranded).toBe(true)
    expect(isSized(sized)).toBe(true)
    expect(isSized(pool({}))).toBe(false)
    expect(bindingOf(pool({}))).toBeUndefined()
  })
})

describe('the sparkline', () => {
  it('draws a path over the points, and nothing at all for a series too short to have a shape', () => {
    expect(sparkPath([])).toBe('')
    expect(sparkPath([5])).toBe('')
    const p = sparkPath([0, 10], 100, 20)
    expect(p.startsWith('M0.0,')).toBe(true)
    expect(p).toContain('L100.0,')
  })
})

describe('classes, families and the class-aware mix', () => {
  it('orders a pool’s classes canonically and never invents one', () => {
    expect(orderedClasses(['spot', 'Guaranteed'])).toEqual(['guaranteed', 'spot'])
    expect(orderedClasses(['reserved'])).toEqual([])
    expect(orderedClasses(null)).toEqual([])
    // Burstable is opted into: a new pool does not enforce it.
    expect(DEFAULT_POOL_CLASSES).toEqual(['guaranteed', 'spot'])
  })

  it('tells a family from a SKU', () => {
    expect(isFamily('ecs.m7n.*')).toBe(true)
    expect(isFamily('ecs.m7n.xlarge.8')).toBe(false)
  })

  it('round-trips a mix with a class on a line, and without one', () => {
    const q = basketQuery([
      { sku: 'ecs.m7n.2xlarge.8', units: '2', class: 'burstable' },
      { sku: 'evs.ssd.gb', units: 100 },
      { sku: '', units: 1 },
      { sku: 'eip', units: 0 },
    ])
    expect(q).toBe('ecs.m7n.2xlarge.8:2:burstable,evs.ssd.gb:100')
    expect(parseBasketQuery(q)).toEqual([
      { sku: 'ecs.m7n.2xlarge.8', units: '2', class: 'burstable' },
      { sku: 'evs.ssd.gb', units: '100', class: '' },
    ])
  })

  it('says what a floor does — the founder’s 60 / 40 case', () => {
    // 100 usable at 2.5:1 with 60 kept for guaranteed: burstable is capped at
    // 40 physical, which is 100 nominal, whatever order the sales arrive in.
    expect(floorEffect('1', '100', '0', '2.5', '60')).toEqual({ usable: 100, floor: 60, envelope: 100 })
    // No floor: burstable can take every physical unit.
    expect(floorEffect('1', '100', '0', '2.5', '')).toEqual({ usable: 100, floor: 0, envelope: 250 })
    // A floor above usable is clamped, and an unsized row says nothing.
    expect(floorEffect('1', '100', '10', '4', '500')).toEqual({ usable: 90, floor: 90, envelope: 0 })
    expect(floorEffect('', '', '', '', '')).toBeNull()
  })
})

describe('the pool fingerprint that the follow-up reads of an open pool are keyed on', () => {
  const base = {
    id: 'p', zone_id: 'z', name: 'm7n-a', machines: 1, classes: ['guaranteed', 'spot'], lead_time_days: 0, source: 'manual', note: '', updated_by: '', updated_at: '2026-09-20T10:00:00Z',
    resources: [], status: 'ok', binding_resource: 'vcpu', utilisation_pct: 10, placements: [], zone_unknown: false,
    basket: { items: [], units: null, reason: '', binding_resource: '', resources: [], unshaped_skus: [] },
    order_by_days: null, order_by_date: null, order_by_resource: '', order_by_wall: '', late: false,
    resources_view: [{ resource: 'vcpu', usable: 256, overcommit_ratio: 1, guaranteed_floor: 0, guaranteed: 80, burstable: 0, spot: 16, spot_reclaim: 0 }],
  } as unknown as CapacityPoolView

  it('is stable for the same pool, so an unrelated re-render refetches nothing', () => {
    expect(poolFingerprint(base)).toBe(poolFingerprint({ ...base }))
  })

  it('changes when the pool is resized, when a resource changes class, and when a placement is added — the three things the id alone never saw', () => {
    const resized = { ...base, updated_at: '2026-09-20T10:05:00Z', resources_view: [{ ...base.resources_view[0], usable: 66, spot_reclaim: 7 }] } as CapacityPoolView
    const reclassed = { ...base, resources_view: [{ ...base.resources_view[0], guaranteed: 72, spot: 24 }] } as CapacityPoolView
    const placed = { ...base, placements: [{ sku: 'ecs.m7n.2xlarge.8', family: false, class: 'spot', shape: {}, shape_source: 'seed', units: 0, resources: 0, matched_skus: [] }] } as CapacityPoolView
    const all = [base, resized, reclassed, placed].map(poolFingerprint)
    expect(new Set(all).size).toBe(4)
  })
})
