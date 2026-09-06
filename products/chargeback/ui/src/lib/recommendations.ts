import type { Recommendation } from '../api/types'
import { resourceHref, resourcesHref } from './links'
import type { Lens } from './scope'

/**
 * Recommendations page helpers (#6867, DESIGN.md §3.7). Seven rule types
 * come off the server; the page groups by type, ranks by severity then
 * saving, and points each row at the place where it gets fixed.
 */

export type Severity = 'high' | 'medium' | 'low'
export const SEVERITIES: readonly Severity[] = ['high', 'medium', 'low']
const SEVERITY_RANK: Record<string, number> = { high: 0, medium: 1, low: 2 }

export interface TypeMeta {
  label: string
  /** One line: what the rule looks at and why it costs money. */
  rule: string
  /** What the action link does. */
  action: string
}

/** Order here is the display order for a type that has no saving to rank by. */
export const TYPE_META: Readonly<Record<string, TypeMeta>> = {
  'stopped-instance-billed': {
    label: 'Stopped instances still billed',
    rule: 'The price book bills stopped hours as compute — a stopped server costs the same as a running one until it is deleted or its billing mode changes.',
    action: 'Open resource',
  },
  'unattached-volume': {
    label: 'Unattached volumes',
    rule: 'Block storage not attached to any server keeps its full GiB-hour rate while holding nothing a workload reads.',
    action: 'Open resource',
  },
  'unbound-eip': {
    label: 'Unbound elastic IPs',
    rule: 'An elastic IP bound to nothing is billed every hour it stays reserved.',
    action: 'Open resource',
  },
  'low-cpu-utilisation': {
    label: 'Low CPU utilisation',
    rule: 'Seven-day mean CPU under 10 % — the saving is one flavor step down at the price-book rate.',
    action: 'Open resource',
  },
  'unpriced-sku': {
    label: 'Unpriced SKUs',
    rule: 'Usage is collected for a SKU the price book carries no rate for, so its cost shows as 0 and never reaches a statement.',
    action: 'Add the rate',
  },
  'stale-source': {
    label: 'Stale cost sources',
    rule: 'A verified source has not collected recently, so every number for that customer is behind.',
    action: 'Check the source',
  },
  'no-price-book': {
    label: 'Customers without a price book',
    rule: 'Usage is collected but nothing rates it — cost, statements and budgets all read 0 for this customer.',
    action: 'Assign a price book',
  },
}

const TYPE_ORDER = Object.keys(TYPE_META)

export function typeMeta(type: string): TypeMeta {
  return TYPE_META[type] ?? { label: type.replace(/-/g, ' ').replace(/^\w/, (c) => c.toUpperCase()), rule: '', action: 'Open' }
}

/** Savings are monthly at rate × 730 h (DESIGN.md §3.7). */
export const HOURS_PER_MONTH = 730

export function severityRank(s: string): number {
  return SEVERITY_RANK[s] ?? SEVERITIES.length
}

/** High before medium before low; within a severity the biggest saving first; ties by title. */
export function sortRecommendations(rows: Recommendation[]): Recommendation[] {
  return [...rows].sort((a, b) => severityRank(a.severity) - severityRank(b.severity) || b.monthly_saving - a.monthly_saving || a.title.localeCompare(b.title))
}

export interface RecommendationGroup {
  type: string
  meta: TypeMeta
  rows: Recommendation[]
  saving: number
}

/** Groups by type; groups ordered by their total saving, then by the rule order for savings-less types. */
export function groupRecommendations(rows: Recommendation[]): RecommendationGroup[] {
  const by = new Map<string, Recommendation[]>()
  for (const r of rows) {
    const list = by.get(r.type) ?? []
    list.push(r)
    by.set(r.type, list)
  }
  const groups: RecommendationGroup[] = []
  for (const [type, list] of by) groups.push({ type, meta: typeMeta(type), rows: sortRecommendations(list), saving: list.reduce((n, r) => n + (Number.isFinite(r.monthly_saving) ? r.monthly_saving : 0), 0) })
  const order = (t: string) => {
    const i = TYPE_ORDER.indexOf(t)
    return i < 0 ? TYPE_ORDER.length : i
  }
  return groups.sort((a, b) => b.saving - a.saving || order(a.type) - order(b.type) || a.type.localeCompare(b.type))
}

export function severityCounts(rows: Recommendation[]): Record<Severity, number> {
  const c: Record<Severity, number> = { high: 0, medium: 0, low: 0 }
  for (const r of rows) if (r.severity in c) c[r.severity as Severity]++
  return c
}

export function totalSaving(rows: Recommendation[]): number {
  return rows.reduce((n, r) => n + (Number.isFinite(r.monthly_saving) ? r.monthly_saving : 0), 0)
}

export interface RecommendationFilter {
  severities: Severity[]
  types: string[]
}

/** Client-side chips: an empty list means "every"; both lists must match. */
export function filterRecommendations(rows: Recommendation[], f: RecommendationFilter): Recommendation[] {
  return rows.filter((r) => (f.severities.length === 0 || f.severities.includes(r.severity as Severity)) && (f.types.length === 0 || f.types.includes(r.type)))
}

export function filterFromParams(p: URLSearchParams): RecommendationFilter {
  const sev = (p.get('severity') ?? '').split(',').filter((s): s is Severity => (SEVERITIES as readonly string[]).includes(s))
  const types = (p.get('type') ?? '').split(',').filter(Boolean)
  return { severities: sev, types }
}

export function paramsFromFilter(f: RecommendationFilter): URLSearchParams {
  const p = new URLSearchParams()
  if (f.severities.length) p.set('severity', f.severities.join(','))
  if (f.types.length) p.set('type', f.types.join(','))
  return p
}

export interface Action {
  to: string
  label: string
}

/**
 * Where a recommendation gets acted on. Resource-shaped ones open the
 * resource; configuration-shaped ones open the operator's configuration
 * page — a customer cannot edit a price book, so those carry no link on the
 * customer lens (the row's detail already says whom to ask).
 */
export function actionFor(r: Recommendation, lens: Lens): Action | null {
  const meta = typeMeta(r.type)
  const src = typeof r.evidence?.source_id === 'string' ? r.evidence.source_id : ''
  if (r.resource_id && src) return { to: resourceHref(lens, src, r.resource_id), label: meta.action }
  // Without the source id the detail route cannot be built; the list found by id is one click away.
  if (r.resource_id) return { to: resourcesHref(lens, `q=${encodeURIComponent(r.resource_id)}`), label: 'Find resource' }
  switch (r.type) {
    case 'unpriced-sku':
      return lens.operator ? { to: '/pricebooks', label: meta.action } : null
    case 'stale-source':
      return lens.operator ? { to: `/customers/${r.customer_id}?tab=sources`, label: meta.action } : { to: '/my/sources', label: meta.action }
    case 'no-price-book':
      return lens.operator ? { to: `/customers/${r.customer_id}?tab=settings`, label: meta.action } : null
  }
  return null
}
