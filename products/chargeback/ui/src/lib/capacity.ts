import type {
  CapacityBasket,
  CapacityBasketItem,
  CapacityClassDef,
  CapacityOverview,
  CapacityPlacementView,
  CapacityPoolInput,
  CapacityPoolResource,
  CapacityPoolStatus,
  CapacityPoolView,
  CapacityResourceKind,
  CapacityResourceView,
  CapacityThresholds,
  CapacityZoneView,
} from '../api/types'
import { toNumber } from './num'

/**
 * Capacity readers (DESIGN.md §11). The server derives every figure; what
 * lives here is display arithmetic the page renders with — the threshold
 * colouring, the words for a wall and an order-by date, the basket form
 * parsing, and the pool editor's validation — pinned by vitest against the
 * same figures the Go tests derive.
 *
 * ONE RULE RUNS THROUGH ALL OF IT: a figure read from a field that is
 * structurally empty must never render as good news. A pool with no machines
 * reads `unset`, never `ok`; a basket with no sized resource says so in words
 * instead of showing a number; an unplaced SKU is named, never summed away.
 */

/** The thresholds the server ships; used until the document arrives. */
export const DEFAULT_THRESHOLDS: CapacityThresholds = { warn_pct: 70, critical_pct: 85 }

/** The three classes, in the order the console shows them. */
/** The order the three classes are always listed in. */
export const CLASS_ORDER: ReadonlyArray<string> = ['guaranteed', 'burstable', 'spot']

/** What a new pool enforces until the operator says otherwise: burstable is opted into. */
export const DEFAULT_POOL_CLASSES: ReadonlyArray<string> = ['guaranteed', 'spot']

/** A pool's classes in canonical order, whatever order they arrived in. */
export function orderedClasses(classes: ReadonlyArray<string> | null | undefined): string[] {
  const set = new Set((classes ?? []).map((c) => c.toLowerCase()))
  return CLASS_ORDER.filter((c) => set.has(c))
}

/** Whether a placement SKU is a family pattern ("ecs.m7n.*"). */
export function isFamily(sku: string): boolean {
  return sku.trim().endsWith('.*')
}

export const CLASSES: ReadonlyArray<CapacityClassDef> = [
  { class: 'guaranteed', label: 'Guaranteed', note: 'physically backed at 1:1; admitted only if it fits usable capacity' },
  { class: 'burstable', label: 'Burstable', note: 'sold against the oversubscribed envelope; throttled at the soft wall' },
  { class: 'spot', label: 'Spot', note: 'no reservation; reclaimed when the room it runs in shrinks, and never refuses another class' },
]

/** The resource kinds this product meters, for a page rendered before the document arrives. */
const FALLBACK_KINDS: Record<string, CapacityResourceKind> = {
  vcpu: { resource: 'vcpu', label: 'vCPU', unit: 'vCPU', position: 10 },
  memory_gib: { resource: 'memory_gib', label: 'Memory', unit: 'GiB', position: 20 },
  block_ssd_gib: { resource: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB', position: 30 },
  block_hdd_gib: { resource: 'block_hdd_gib', label: 'Block HDD', unit: 'GiB', position: 40 },
  object_gib: { resource: 'object_gib', label: 'Object storage', unit: 'GiB', position: 50 },
  eip_addresses: { resource: 'eip_addresses', label: 'Elastic IPs', unit: 'addresses', position: 60 },
  bandwidth_mbps: { resource: 'bandwidth_mbps', label: 'Bandwidth', unit: 'Mbps', position: 70 },
}

/**
 * The kind for a resource key: the server's catalogue first, the built-in
 * fallback next, and otherwise the key itself. A key nobody has named is
 * still a resource — there is no list to be on.
 */
export function kindOf(key: string, kinds?: CapacityResourceKind[] | null): CapacityResourceKind {
  return kinds?.find((k) => k.resource === key) ?? FALLBACK_KINDS[key] ?? { resource: key, label: key, unit: '', position: 1000 }
}

/** The label a class shows under. */
export function classLabel(key: string, classes?: CapacityClassDef[] | null): string {
  return (classes ?? CLASSES).find((c) => c.class === key)?.label ?? key
}

/**
 * utilisationStatus classifies a percentage of SELLABLE: unset when nothing
 * is sized (pct null), warn at >= warn_pct, critical at >= critical_pct — the
 * same rule as capacity.Status on the server.
 */
export function utilisationStatus(pct: number | null | undefined, thresholds: CapacityThresholds = DEFAULT_THRESHOLDS): CapacityPoolStatus {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return 'unset'
  if (pct >= thresholds.critical_pct) return 'critical'
  if (pct >= thresholds.warn_pct) return 'warn'
  return 'ok'
}

/** The heat class for a status. */
export function heatClass(status: CapacityPoolStatus | string): string {
  switch (status) {
    case 'ok':
    case 'warn':
    case 'critical':
      return `heat ${status}`
    default:
      return 'heat unset'
  }
}

/** Amounts are exact decimals; render up to 3 decimals, trailing zeros trimmed. */
export function formatAmount(v: number | string | null | undefined, digits = 3): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = toNumber(v)
  if (!Number.isFinite(n)) return '—'
  return n.toLocaleString('en-US', { maximumFractionDigits: digits })
}

/** "80.0 %" · "—" when nothing is sized. */
export function formatUtilisation(pct: number | null | undefined): string {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return '—'
  return `${pct.toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 })} %`
}

/** "4:1" · "1.5:1" · "1:1" — how a ratio reads. */
export function formatRatio(v: number | string | null | undefined): string {
  const n = toNumber(v)
  if (!Number.isFinite(n) || n <= 0) return '1:1'
  return `${n.toLocaleString('en-US', { maximumFractionDigits: 2 })}:1`
}

/**
 * formatDays says how far off a wall is: "—" when there is none, "today",
 * "12 days", "3.5 months" (>= 60 days), "2.1 years" (>= 730). A NEGATIVE
 * count is a date that has already passed and reads as such — that is the
 * whole point of the order-by date, and rendering it as "—" or as 0 would
 * hide the one case that matters.
 */
export function formatDays(days: number | null | undefined): string {
  if (days === null || days === undefined || !Number.isFinite(days)) return '—'
  const past = days < 0
  const d = Math.abs(days)
  let out: string
  if (d < 0.5) return 'today'
  else if (d < 1) out = '1 day'
  else if (d >= 730) out = `${(d / 365).toFixed(1)} years`
  else if (d >= 60) out = `${(d / 30.4375).toFixed(1)} months`
  else {
    const r = Math.round(d)
    out = `${r} day${r === 1 ? '' : 's'}`
  }
  return past ? `${out} ago` : out
}

/** What a pool's per-machine vector reads as: "64 vCPU · 512 GiB per machine". */
export function vectorSummary(resources: CapacityPoolResource[] | null | undefined, kinds?: CapacityResourceKind[] | null): string {
  if (!resources?.length) return ''
  return resources.map((r) => `${formatAmount(r.per_machine)} ${r.unit || kindOf(r.resource, kinds).unit}`).join(' · ')
}

/** "8 vCPU · 64 GiB" — a shape in words, in the catalogue's order. */
export function shapeSummary(shape: Record<string, number | string> | null | undefined, kinds?: CapacityResourceKind[] | null): string {
  if (!shape) return ''
  return sortedResourceKeys(Object.keys(shape), kinds)
    .filter((k) => toNumber(shape[k]) > 0)
    .map((k) => `${formatAmount(shape[k])} ${kindOf(k, kinds).unit || k}`)
    .join(' · ')
}

/** Resource keys in the catalogue's display order, then by key. */
export function sortedResourceKeys(keys: string[], kinds?: CapacityResourceKind[] | null): string[] {
  return [...keys].sort((a, b) => {
    const pa = kindOf(a, kinds).position
    const pb = kindOf(b, kinds).position
    return pa !== pb ? pa - pb : a.localeCompare(b)
  })
}

/** The zones of the overview flattened with their region, in the server's order. */
export function zoneRows(ov: CapacityOverview | null | undefined, region?: string | 'all'): Array<{ region: string; regionName: string; zone: CapacityZoneView }> {
  const out: Array<{ region: string; regionName: string; zone: CapacityZoneView }> = []
  for (const r of ov?.regions ?? []) {
    if (region && region !== 'all' && r.code !== region) continue
    for (const z of r.zones) out.push({ region: r.code, regionName: r.name, zone: z })
  }
  return out
}

/** Every pool of the overview with its zone and region, in the server's order. */
export function poolRows(ov: CapacityOverview | null | undefined, region?: string | 'all'): Array<{ region: string; zone: CapacityZoneView; pool: CapacityPoolView }> {
  const out: Array<{ region: string; zone: CapacityZoneView; pool: CapacityPoolView }> = []
  for (const row of zoneRows(ov, region)) {
    for (const pool of row.zone.pools) out.push({ region: row.region, zone: row.zone, pool })
  }
  return out
}

/** The resource of a pool the binding resource names, if any. */
export function bindingOf(pool: CapacityPoolView): CapacityResourceView | undefined {
  return pool.resources_view?.find((r) => r.resource === pool.binding_resource)
}

/** Whether a pool carries any capacity at all (machines and a per-machine vector). */
export function isSized(pool: CapacityPoolView | null | undefined): boolean {
  return Boolean(pool?.resources_view?.some((r) => r.sized))
}

/**
 * The words a basket answers in. A basket that could not be measured returns
 * its REASON, never a number — the defect this module shipped once was a
 * figure read from a structurally empty field rendering as good news.
 */
export function basketAnswer(basket: CapacityBasket | null | undefined, kinds?: CapacityResourceKind[] | null): { units: number | null; text: string; binding: string } {
  if (!basket) return { units: null, text: 'not measured', binding: '' }
  if (basket.units === null || basket.units === undefined) {
    return { units: null, text: basket.reason || 'not measured', binding: '' }
  }
  const binding = basket.binding_resource ? kindOf(basket.binding_resource, kinds).label : ''
  return { units: basket.units, text: `${basket.units.toLocaleString('en-US')} more`, binding }
}

/** "1 × ecs.m7n.2xlarge.8 (guaranteed) · 0.8 × …" — a mix in words. */
export function basketSummary(items: CapacityBasketItem[] | null | undefined): string {
  if (!items?.length) return ''
  return items.map((i) => `${formatAmount(i.units)} × ${i.sku}`).join(' · ')
}

/**
 * The basket form text a mix round-trips through:
 * "sku:units[:class],sku:units[:class]". A line without a class takes the
 * pool's default for that SKU.
 */
export function basketQuery(items: Array<{ sku: string; units: number | string; class?: string }>): string {
  return items
    .filter((i) => i.sku.trim() !== '' && toNumber(i.units) > 0)
    .map((i) => `${i.sku.trim()}:${String(i.units).trim()}${i.class ? `:${i.class}` : ''}`)
    .join(',')
}

/** parseBasketQuery is the reverse, matching store.ParseBasket. */
export function parseBasketQuery(s: string): Array<{ sku: string; units: string; class: string }> {
  const out: Array<{ sku: string; units: string; class: string }> = []
  for (const part of s.split(',')) {
    const fields = part.trim().split(':')
    const sku = (fields[0] ?? '').trim()
    if (!sku) continue
    out.push({ sku, units: (fields[1] ?? '').trim() || '1', class: (fields[2] ?? '').trim().toLowerCase() })
  }
  return out
}

export interface PoolFormResource {
  resource: string
  per_machine: string
  reserve: string
  overcommit_ratio: string
  guaranteed_floor: string
}

export interface PoolForm {
  name: string
  machines: string
  /** The classes this pool enforces. */
  classes: string[]
  lead_time_days: string
  note: string
  resources: PoolFormResource[]
}

export interface ParsedPool {
  body: CapacityPoolInput | null
  error: string
}

const NUMBER = /^\d+(\.\d+)?$/

/**
 * parsePoolForm turns the pool editor into the POST/PUT body, mirroring the
 * server's own refusals so the operator sees them before a round trip: a
 * pool needs a name and at least one resource (a machine with nothing in it
 * is not capacity), amounts are non-negative, and a ratio is positive.
 */
export function parsePoolForm(form: PoolForm): ParsedPool {
  const name = form.name.trim()
  if (!name) return { body: null, error: 'name the pool — what this set of machines is called, e.g. m7n-a' }
  const machines = form.machines.trim().replace(/,/g, '') || '0'
  if (!NUMBER.test(machines)) return { body: null, error: 'machines must be a non-negative number' }
  const lead = form.lead_time_days.trim() || '0'
  if (!/^\d+$/.test(lead)) return { body: null, error: 'the lead time is a whole number of days' }

  const classes = orderedClasses(form.classes)
  if (classes.length === 0) return { body: null, error: 'a pool enforces at least one class: tick guaranteed, burstable or spot' }
  // Overcommit and the floor are BURSTABLE's two numbers. Without burstable
  // the editor does not show them, and what is sent is 1 and 0 whatever the
  // form still holds from before the box was unticked.
  const burstable = classes.includes('burstable')

  const seen = new Set<string>()
  const resources: CapacityPoolInput['resources'] = []
  for (const r of form.resources) {
    const key = r.resource.trim().toLowerCase()
    if (!key) continue
    if (seen.has(key)) return { body: null, error: `${key} is listed twice` }
    seen.add(key)
    const per = r.per_machine.trim().replace(/,/g, '') || '0'
    const reserve = r.reserve.trim().replace(/,/g, '') || '0'
    const ratio = burstable ? r.overcommit_ratio.trim().replace(/,/g, '') || '1' : '1'
    const floor = burstable ? r.guaranteed_floor.trim().replace(/,/g, '') || '0' : '0'
    if (!NUMBER.test(per)) return { body: null, error: `${key}: the amount per machine must be a non-negative number` }
    if (!NUMBER.test(reserve)) return { body: null, error: `${key}: the reserve must be a non-negative number` }
    if (!NUMBER.test(ratio) || Number(ratio) === 0) return { body: null, error: `${key}: the overcommit ratio must be a positive number (1 is no oversubscription)` }
    if (!NUMBER.test(floor)) return { body: null, error: `${key}: the guaranteed floor must be a non-negative number` }
    const usable = Math.max(0, Number(machines) * Number(per) - Number(reserve))
    if (Number(floor) > usable) return { body: null, error: `${key}: the guaranteed floor is ${floor} but only ${formatAmount(usable)} is usable (machines × per machine, less the reserve)` }
    resources.push({ resource: key, per_machine: per, reserve, overcommit_ratio: ratio, guaranteed_floor: floor })
  }
  if (resources.length === 0) return { body: null, error: 'a pool holds at least one resource: a machine with nothing in it is not capacity' }
  return { body: { name, machines, classes, lead_time_days: Number(lead), note: form.note.trim(), resources }, error: '' }
}

/** The reserve one machine's worth of a resource comes to — the N+1 button. */
export function reserveForMachines(perMachine: string, machines: number): string {
  const per = toNumber(perMachine.trim().replace(/,/g, ''))
  if (!Number.isFinite(per) || per <= 0 || machines <= 0) return '0'
  return String(per * machines)
}

export interface ParsedShape {
  resources: Record<string, string>
  error: string
}

/**
 * parseShapeForm turns the shape editor's per-resource inputs into the PUT
 * body: blanks and zeros are dropped (they remove the resource), anything
 * else must be a non-negative number. An empty result is legal — it removes
 * the shape — and the caller decides whether to warn.
 */
export function parseShapeForm(values: Record<string, string>): ParsedShape {
  const out: Record<string, string> = {}
  for (const [res, raw] of Object.entries(values)) {
    const key = res.trim().toLowerCase()
    if (!key) continue
    const s = raw.trim().replace(/,/g, '')
    if (s === '') continue
    if (!NUMBER.test(s)) return { resources: {}, error: `${key}: enter a non-negative number` }
    if (Number(s) === 0) continue
    out[key] = s
  }
  return { resources: out, error: '' }
}

/** "2026-09-09 09:00Z" for the as-of hour; "" when null. */
export function asOfLabel(asOf: string | null | undefined): string {
  if (!asOf) return ''
  const d = new Date(asOf)
  if (Number.isNaN(d.getTime())) return asOf
  return d.toISOString().replace('T', ' ').slice(0, 16) + 'Z'
}

/** The points of one class's series scaled into a sparkline path, or "" when flat/empty. */
export function sparkPath(values: number[], width = 120, height = 28): string {
  if (values.length < 2) return ''
  const max = Math.max(...values, 0)
  const min = Math.min(...values, 0)
  const span = max - min || 1
  const step = width / (values.length - 1)
  return values
    .map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${(height - ((v - min) / span) * height).toFixed(1)}`)
    .join(' ')
}

/** One placement with the pool and zone it belongs to — a row of the Placements tab. */
export interface PlacementRow {
  key: string
  region: string
  zone: string
  pool: CapacityPoolView
  placement: CapacityPlacementView
}

/** Every placement of every pool, flat, in region / zone / pool / SKU / class order. */
export function placementRows(ov: CapacityOverview | null | undefined, region: string | 'all' = 'all'): PlacementRow[] {
  const out: PlacementRow[] = []
  for (const { region: rc, zone, pool } of poolRows(ov, region)) {
    for (const placement of pool.placements ?? []) {
      out.push({ key: `${pool.id}|${placement.sku}|${placement.class}`, region: rc, zone: zone.code, pool, placement })
    }
  }
  return out
}

/**
 * What a guaranteed floor of F does to a resource, in the words the editor
 * shows under the input: how much burstable can ever be sold, and how much
 * stays guaranteed's whatever burstable does.
 */
export function floorEffect(machines: string, perMachine: string, reserve: string, ratio: string, floor: string): { usable: number; envelope: number; floor: number } | null {
  const n = (v: string) => Number(v.trim().replace(/,/g, '') || '0')
  const usable = Math.max(0, n(machines) * n(perMachine) - n(reserve))
  const r = n(ratio) || 1
  const f = Math.min(n(floor), usable)
  if (!Number.isFinite(usable) || !Number.isFinite(r) || !Number.isFinite(f) || usable <= 0) return null
  return { usable, floor: f, envelope: (usable - f) * r }
}

/**
 * A pool's FINGERPRINT: everything a follow-up read about the pool depends
 * on — when it was last edited, what is placed on it, and what each class
 * holds of each resource.
 *
 * The drill-in's follow-up reads (what is running, a named mix's headroom) are
 * fetched once the row is opened and used to be keyed on the pool's id alone.
 * The id does not change when the pool is resized or a resource changes
 * class, so both kept showing the answer to the PREVIOUS pool: a resize that
 * left spot over its room showed no reclaim at all until the page was
 * reloaded. Found by the live walk, which did not reload between the edit and
 * the read; the local walk had, and that masked it. They are keyed on this
 * now.
 */
export function poolFingerprint(pool: CapacityPoolView): string {
  const figures = (pool.resources_view ?? []).map((r) => [r.resource, r.usable, r.overcommit_ratio, r.guaranteed_floor, r.guaranteed, r.burstable, r.spot, r.spot_reclaim].join(':'))
  const placed = (pool.placements ?? []).map((p) => `${p.sku}@${p.class}`)
  return [pool.id, pool.updated_at, (pool.classes ?? []).join(','), placed.join(','), figures.join(',')].join('|')
}
