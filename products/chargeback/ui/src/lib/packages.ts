import type { EntitlementState, Feature, FeatureGroup, FloorItem, Overage, PackageCell, PackageFeature, PackageInfo, PackagesDoc } from '../api/types'

/**
 * Packages and the entitlement matrix (DESIGN.md §22), as the console and
 * the public calculator read them. Pure: the grouped matrix the Packages
 * tab draws, the step-up check, the add-ons a Source may take, the
 * comparison table and the add-on lines an estimate sends are all derived
 * here and unit-tested, never eyeballed. No money arithmetic beyond adding
 * the add-on prices a prospect ticked — every published figure is the
 * server's.
 */

export const STATES: ReadonlyArray<{ value: EntitlementState; label: string; help: string }> = [
  { value: 'included', label: 'Included', help: 'The package carries it; a boolean feature shows on the invoice at 0.000.' },
  { value: 'optional', label: 'Optional', help: 'A paid add-on: taken per Organization, billed at its add-on SKU like the plan.' },
  { value: 'not_offered', label: 'Not offered', help: 'Not available on this package.' },
]

export const OVERAGES: ReadonlyArray<{ value: Overage; label: string; help: string }> = [
  { value: 'metered', label: 'Metered', help: 'Above the included quantity, the excess is billed at the feature’s SKU.' },
  { value: 'hard_cap', label: 'Hard cap', help: 'Nothing above it: the platform refuses; the run reports the excess and bills 0.' },
  { value: 'unlimited', label: 'Unlimited', help: 'No allowance and nothing billed.' },
]

/** The groups when the document carries none (an older server): the fixed order of DESIGN.md §22.1. */
export const DEFAULT_GROUPS: FeatureGroup[] = [
  { key: 'capacity', name: 'Capacity' },
  { key: 'features', name: 'Features' },
  { key: 'access', name: 'Access' },
  { key: 'ops', name: 'Managed operations' },
  { key: 'scope', name: 'Scope' },
  { key: 'resilience', name: 'Resilience' },
  { key: 'service', name: 'Service level' },
]

export const KINDS: ReadonlyArray<{ value: Feature['kind']; label: string; help: string }> = [
  { value: 'boolean', label: 'Boolean', help: 'The package carries it or not; optional is a paid add-on.' },
  { value: 'quantity', label: 'Quantity', help: 'Comes with a quantity the package includes and an overage policy above it.' },
  { value: 'level', label: 'Level', help: 'An ordered list of labels; each package is at one, and the next may be purchasable.' },
  { value: 'access', label: 'Access', help: 'A platform door: included or not offered, never priced.' },
]

export function stateLabel(state: string | null | undefined): string {
  if (state === 'teaser') return 'Not offered'
  return STATES.find((s) => s.value === state)?.label ?? 'Not offered'
}

export function overageLabel(overage: string | null | undefined): string {
  return OVERAGES.find((o) => o.value === overage)?.label ?? 'Metered'
}

/** The badge tone of a state: included reads green, optional blue, not offered plain. */
export function stateTone(state: string | null | undefined): 'ok' | 'info' | undefined {
  if (state === 'included') return 'ok'
  if (state === 'optional') return 'info'
  return undefined
}

/** `plan.xl` → `XL`; a plan the document names is shown by its name. */
export function planName(doc: PackagesDoc | null | undefined, planSku: string): string {
  const p = doc?.packages.find((x) => x.sku === planSku)
  if (p) return p.name
  return planSku.replace(/^plan\./, '').toUpperCase()
}

/** The cell of a feature on a package; a feature with no row reads not offered. */
export function cellOf(feature: Pick<PackageFeature, 'cells'> | null | undefined, planSku: string): PackageCell {
  return feature?.cells[planSku] ?? { state: 'not_offered' }
}

/** "included from XL" — the hint beside an add-on, or '' when no package includes it. */
export function includedFromText(doc: PackagesDoc | null | undefined, cell: PackageCell): string {
  if (!cell.included_from) return ''
  return `included from ${planName(doc, cell.included_from)}`
}

/** "from XL" — what a teaser cell says instead of a dash. */
export function teaserText(doc: PackagesDoc | null | undefined, cell: PackageCell): string {
  if (cell.state !== 'teaser' || !cell.included_from) return ''
  return `from ${planName(doc, cell.included_from)}`
}

/** The label of a level cell, or '' when the feature is not a level or the cell carries none. */
export function levelLabel(feature: Pick<PackageFeature, 'kind' | 'levels'>, cell: PackageCell): string {
  if (feature.kind !== 'level' || cell.level === undefined || cell.level === null) return ''
  return feature.levels?.[cell.level] ?? `level ${cell.level}`
}

/** The label of the level the purchasable next level raises to, or ''. */
export function nextLevelLabel(feature: Pick<PackageFeature, 'kind' | 'levels'>, cell: PackageCell): string {
  if (feature.kind !== 'level' || !cell.next_level_addon || cell.level === undefined || cell.level === null) return ''
  return feature.levels?.[cell.level + 1] ?? `level ${cell.level + 1}`
}

export interface OptionalFeature {
  key: string
  name: string
  blurb: string
  addon_sku: string
  /** Price per month as the server states it, '' when the add-on is not priced. */
  price_month: string
  /** The hint, '' when no package includes it. */
  included_from: string
  /** On a level feature: the label of the level the add-on raises to. */
  next_level?: string
}

/**
 * The features a package offers as a paid add-on, in matrix order: a
 * boolean feature marked optional, and the purchasable next level of a
 * level feature.
 */
export function optionalFeatures(doc: PackagesDoc | null | undefined, planSku: string): OptionalFeature[] {
  if (!doc) return []
  const out: OptionalFeature[] = []
  for (const f of doc.features) {
    const c = cellOf(f, planSku)
    if (f.kind === 'level') {
      if (!c.next_level_addon?.addon_sku) continue
      out.push({ key: f.key, name: f.name, blurb: f.blurb ?? '', addon_sku: c.next_level_addon.addon_sku, price_month: c.next_level_addon.price_month ?? '', included_from: includedFromText(doc, c), next_level: nextLevelLabel(f, c) })
      continue
    }
    if (c.state !== 'optional' || !c.addon_sku) continue
    out.push({ key: f.key, name: f.name, blurb: f.blurb ?? '', addon_sku: c.addon_sku, price_month: c.price_month ?? '', included_from: includedFromText(doc, c) })
  }
  return out
}

/** The features a package includes, in matrix order (every kind alike). */
export function includedFeatures(doc: PackagesDoc | null | undefined, planSku: string): PackageFeature[] {
  if (!doc) return []
  return doc.features.filter((f) => cellOf(f, planSku).state === 'included')
}

/** The plan SKU of a plan slug: `m` → `plan.m`. */
export function planSku(slug: string): string {
  return slug.startsWith('plan.') ? slug : `plan.${slug}`
}

/** The words for an `includes` key: vcpu → vCPU, memory_gb → Memory (GB), bandwidth_mbps → Bandwidth (Mbps). */
export function includesLabel(key: string, doc?: PackagesDoc | null): string {
  if (key === 'vcpu') return 'vCPU'
  if (key === 'memory_gb') return 'Memory (GB)'
  const feature = doc?.features.find((f) => f.kind === 'quantity' && key.startsWith(`${f.key}_`))
  if (feature) return `${feature.name}${feature.unit ? ` (${feature.unit})` : ''}`
  const at = key.lastIndexOf('_')
  if (at > 0) {
    const name = key.slice(0, at).replace(/_/g, ' ')
    const unit = key.slice(at + 1)
    return `${name.charAt(0).toUpperCase()}${name.slice(1)} (${unit.length <= 4 ? unit.toUpperCase() : unit})`
  }
  return key.charAt(0).toUpperCase() + key.slice(1)
}

/** The `includes` rows of the comparison table: every key any package states, vCPU and memory first. */
export function includesRows(doc: PackagesDoc | null | undefined): Array<{ key: string; label: string }> {
  if (!doc) return []
  const keys: string[] = []
  for (const p of doc.packages) for (const k of Object.keys(p.includes ?? {})) if (!keys.includes(k)) keys.push(k)
  const rank = (k: string) => (k === 'vcpu' ? 0 : k === 'memory_gb' ? 1 : 2)
  keys.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
  return keys.map((key) => ({ key, label: includesLabel(key, doc) }))
}

/** What a package includes under one key, or '—'. */
export function includesValue(pkg: PackageInfo, key: string): string {
  const v = pkg.includes?.[key]
  if (v === undefined || v === null || v === '') return '—'
  return String(v)
}

/** "4 vCPU · 8 GB · 50 GB disk" — the headline of a package's shape, '' when it has none. */
export function shapeHeadline(pkg: Pick<PackageInfo, 'shape' | 'includes'>): string {
  const sh = pkg.shape ?? {}
  const vcpu = sh.vcpu ?? pkg.includes?.vcpu
  const mem = sh.memory_gb ?? pkg.includes?.memory_gb
  const parts: string[] = []
  if (vcpu !== undefined && vcpu !== null && vcpu !== '') parts.push(`${vcpu} vCPU`)
  if (mem !== undefined && mem !== null && mem !== '') parts.push(`${mem} GB`)
  if (sh.disk_gb !== undefined && sh.disk_gb !== null && sh.disk_gb !== '') parts.push(`${sh.disk_gb} GB disk`)
  return parts.join(' · ')
}

/** "1 vCPU · 4 GB guaranteed" — the floors under the headline, '' when none is set. */
export function shapeGuaranteed(pkg: Pick<PackageInfo, 'shape'>): string {
  const sh = pkg.shape ?? {}
  const parts: string[] = []
  if (sh.vcpu_guaranteed !== undefined && sh.vcpu_guaranteed !== null && sh.vcpu_guaranteed !== '') parts.push(`${sh.vcpu_guaranteed} vCPU`)
  if (sh.memory_gb_guaranteed !== undefined && sh.memory_gb_guaranteed !== null && sh.memory_gb_guaranteed !== '') parts.push(`${sh.memory_gb_guaranteed} GB`)
  return parts.length ? `${parts.join(' · ')} guaranteed` : ''
}

/** The step-up check in words: "gap 7.000 · bundled add-ons 10.000 ✓", or the red "✗ raise add-on prices". */
export function stepUpText(pkg: Pick<PackageInfo, 'step_up'>, currency: string): { text: string; ok: boolean } | null {
  const su = pkg.step_up
  if (!su) return null
  const gap = `gap ${su.gap_month} · bundled add-ons ${su.bundled_addons_sum_month}${currency ? ` ${currency}` : ''}`
  if (su.rule_holds) return { text: `${gap} ✓`, ok: true }
  return { text: `${gap} ✗ raise add-on prices`, ok: false }
}

/**
 * The step-up hint of the comparison table: when the add-ons a prospect
 * ticked under a package are all included on the next package and their
 * prices add up to at least the gap, "<next> includes all of this for <gap>
 * more". Null otherwise.
 */
export function stepUpHint(doc: PackagesDoc | null | undefined, planSku: string, picked: string[]): { next_sku: string; next_name: string; gap_month: string; sum: string } | null {
  const pkg = doc?.packages.find((p) => p.sku === planSku)
  const su = pkg?.step_up
  if (!su || !picked.length) return null
  if (!picked.every((k) => su.bundled_addon_keys.includes(k))) return null
  const offered = optionalFeatures(doc, planSku)
  let sum = 0
  for (const k of picked) {
    const o = offered.find((x) => x.key === k)
    if (!o || !o.price_month) return null
    sum += Number(o.price_month)
  }
  if (sum + 1e-9 < Number(su.gap_month)) return null
  return { next_sku: su.next_sku, next_name: su.next_name, gap_month: su.gap_month, sum: sum.toFixed(su.gap_month.split('.')[1]?.length ?? 3) }
}

export interface MatrixRow {
  feature: Feature
  /** Keyed by plan SKU; every package has a cell (not offered by default). */
  cells: Record<string, PackageCell>
  /** False for a feature with no cell in this book yet. */
  inBook: boolean
}

/**
 * The console's matrix: EVERY feature (GET /features) except the floor,
 * with the cells the book's document states and not_offered elsewhere, so an
 * operator can fill a feature in even before the book carries a cell for it.
 */
export function matrixRows(doc: PackagesDoc | null | undefined, features: Feature[]): MatrixRow[] {
  const byKey = new Map((doc?.features ?? []).map((f) => [f.key, f]))
  const plans = doc?.packages ?? []
  return features
    .filter((f) => f.group !== 'floor')
    .map((feature) => {
      const published = byKey.get(feature.key)
      const cells: Record<string, PackageCell> = {}
      for (const p of plans) cells[p.sku] = cellOf(published, p.sku)
      return { feature, cells, inBook: Boolean(published) }
    })
}

export interface MatrixGroup {
  group: FeatureGroup
  rows: MatrixRow[]
}

/** The matrix rows grouped under their group heading, in the document's group order; a group with no row is left out. */
export function groupedRows(doc: PackagesDoc | null | undefined, features: Feature[]): MatrixGroup[] {
  const rows = matrixRows(doc, features)
  const groups = doc?.groups?.length ? doc.groups : DEFAULT_GROUPS
  const out: MatrixGroup[] = []
  for (const g of groups) {
    const mine = rows.filter((r) => (r.feature.group || 'features') === g.key)
    if (mine.length) out.push({ group: g, rows: mine })
  }
  const known = new Set(groups.map((g) => g.key))
  const rest = rows.filter((r) => !known.has(r.feature.group || 'features'))
  if (rest.length) out.push({ group: { key: 'other', name: 'Other' }, rows: rest })
  return out
}

/** The floor items the console edits: every feature in the floor group. */
export function floorRows(features: Feature[]): Feature[] {
  return features.filter((f) => f.group === 'floor')
}

/** The published features grouped for the comparison table, in the document's group order. */
export function groupedFeatures(doc: PackagesDoc | null | undefined): Array<{ group: FeatureGroup; features: PackageFeature[] }> {
  if (!doc) return []
  const groups = doc.groups?.length ? doc.groups : DEFAULT_GROUPS
  const out: Array<{ group: FeatureGroup; features: PackageFeature[] }> = []
  for (const g of groups) {
    const mine = doc.features.filter((f) => (f.group || 'features') === g.key)
    if (mine.length) out.push({ group: g, features: mine })
  }
  const known = new Set(groups.map((g) => g.key))
  const rest = doc.features.filter((f) => !known.has(f.group || 'features'))
  if (rest.length) out.push({ group: { key: 'other', name: 'Other' }, features: rest })
  return out
}

/** The floor as published, [] when the document carries none. */
export function floorItems(doc: PackagesDoc | null | undefined): FloorItem[] {
  return doc?.floor ?? []
}

/** The state chip's text: "Included", "+ 1.500 OMR / month", "50 Mbps", a level's label, "from XL", "Not offered". */
export function cellText(feature: Pick<Feature, 'kind' | 'unit' | 'levels'>, cell: PackageCell, currency: string): string {
  if (cell.state === 'teaser') return teaserText(null, cell) || 'Not offered'
  if (feature.kind === 'level') {
    const label = levelLabel(feature, cell)
    if (label) return label
    return cell.state === 'not_offered' ? 'Not offered' : 'Included'
  }
  if (cell.state === 'included') {
    if (feature.kind === 'quantity') {
      if (cell.overage === 'unlimited' && (cell.quantity === undefined || cell.quantity === null || cell.quantity === '')) return 'Unlimited'
      if (cell.quantity !== undefined && cell.quantity !== null && cell.quantity !== '') return `${cell.quantity} ${feature.unit ?? ''}`.trim()
    }
    return 'Included'
  }
  if (cell.state === 'optional' && cell.grow_only) return 'In grow mode'
  if (cell.state === 'optional') return cell.price_month ? `+ ${cell.price_month} ${currency} / month` : 'Optional · unpriced'
  return 'Not offered'
}

/** The line under the chip: the overage of a quantity cell, the purchasable next level of a level cell, '' otherwise. */
export function cellSubText(feature: Pick<Feature, 'kind' | 'unit' | 'levels'>, cell: PackageCell, currency: string): string {
  if (feature.kind === 'quantity' && cell.state === 'included' && cell.overage) return overageLabel(cell.overage).toLowerCase()
  // DESIGN.md §22.11 — a grow-only cell: no price, billed as usage.
  if (cell.grow_only) {
    const next = feature.kind === 'level' && cell.level !== undefined && cell.level !== null ? (feature.levels?.[cell.level + 1] ?? '') : ''
    return next ? `${next} in grow mode, billed as usage` : 'billed as usage'
  }
  if (feature.kind === 'level' && cell.next_level_addon) {
    const next = nextLevelLabel(feature, cell)
    const price = cell.next_level_addon.price_month ? `+ ${cell.next_level_addon.price_month} ${currency} / month` : 'unpriced'
    return next ? `${next} ${price}` : price
  }
  return ''
}

export interface AddonLine {
  sku: string
  key: string
  label: string
  price_month: string
}

/** The add-on lines a package choice with the given add-ons becomes, in matrix order; unknown or non-optional keys are dropped. */
export function addonLines(doc: PackagesDoc | null | undefined, slug: string, addons: string[]): AddonLine[] {
  const out: AddonLine[] = []
  const sku = planSku(slug)
  for (const o of optionalFeatures(doc, sku)) {
    if (!addons.includes(o.key)) continue
    out.push({ sku: o.addon_sku, key: o.key, label: o.next_level ? `${o.name} — ${o.next_level}` : `${o.name} add-on`, price_month: o.price_month })
  }
  return out
}
