import type { EntitlementState, Feature, PackageCell, PackageFeature, PackageInfo, PackagesDoc } from '../api/types'

/**
 * Packages and the entitlement matrix (DESIGN.md §22), as the console and
 * the public calculator read them. Pure: the matrix the Packages tab draws,
 * the add-ons a Source may take, the comparison table and the add-on lines
 * an estimate sends are all derived here and unit-tested, never eyeballed.
 * No money arithmetic — every figure comes from the server's document.
 */

export const STATES: ReadonlyArray<{ value: EntitlementState; label: string; help: string }> = [
  { value: 'included', label: 'Included', help: 'The package carries it; the invoice shows it at 0.000.' },
  { value: 'optional', label: 'Optional', help: 'A paid add-on: taken per Organization, billed at its add-on SKU like the plan.' },
  { value: 'not_offered', label: 'Not offered', help: 'Not available on this package.' },
]

export function stateLabel(state: string | null | undefined): string {
  return STATES.find((s) => s.value === state)?.label ?? 'Not offered'
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

export interface OptionalFeature {
  key: string
  name: string
  blurb: string
  addon_sku: string
  /** Price per month as the server states it, '' when the add-on is not priced. */
  price_month: string
  /** The hint, '' when no package includes it. */
  included_from: string
}

/** The features a package offers as a paid add-on, in matrix order. */
export function optionalFeatures(doc: PackagesDoc | null | undefined, planSku: string): OptionalFeature[] {
  if (!doc) return []
  const out: OptionalFeature[] = []
  for (const f of doc.features) {
    const c = cellOf(f, planSku)
    if (c.state !== 'optional' || !c.addon_sku) continue
    out.push({ key: f.key, name: f.name, blurb: f.blurb ?? '', addon_sku: c.addon_sku, price_month: c.price_month ?? '', included_from: includedFromText(doc, c) })
  }
  return out
}

/** The features a package includes, in matrix order (boolean and quantity alike). */
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

export interface MatrixRow {
  feature: Feature
  /** Keyed by plan SKU; every package has a cell (not offered by default). */
  cells: Record<string, PackageCell>
  /** False for a feature with no cell in this book yet. */
  inBook: boolean
}

/**
 * The console's matrix: EVERY feature (GET /features), with the cells the
 * book's document states and not_offered elsewhere, so an operator can fill
 * a feature in even before the book carries a cell for it.
 */
export function matrixRows(doc: PackagesDoc | null | undefined, features: Feature[]): MatrixRow[] {
  const byKey = new Map((doc?.features ?? []).map((f) => [f.key, f]))
  const plans = doc?.packages ?? []
  return features.map((feature) => {
    const published = byKey.get(feature.key)
    const cells: Record<string, PackageCell> = {}
    for (const p of plans) cells[p.sku] = cellOf(published, p.sku)
    return { feature, cells, inBook: Boolean(published) }
  })
}

/** The state chip's text: "Included", "+ 1.500 OMR / month", "Not offered"; a quantity feature shows the quantity. */
export function cellText(feature: Pick<Feature, 'kind' | 'unit'>, cell: PackageCell, currency: string): string {
  if (cell.state === 'included') {
    if (feature.kind === 'quantity' && cell.quantity !== undefined && cell.quantity !== null && cell.quantity !== '') return `${cell.quantity} ${feature.unit ?? ''}`.trim()
    return 'Included'
  }
  if (cell.state === 'optional') return cell.price_month ? `+ ${cell.price_month} ${currency} / month` : 'Optional · unpriced'
  return 'Not offered'
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
    out.push({ sku: o.addon_sku, key: o.key, label: `${o.name} add-on`, price_month: o.price_month })
  }
  return out
}
