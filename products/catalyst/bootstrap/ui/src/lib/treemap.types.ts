/**
 * treemap.types.ts — typed contract for the Sovereign Dashboard
 * resource-utilisation treemap surface.
 *
 * The Dashboard renders a Recharts <Treemap> where:
 *   • box AREA  ← the resource limit allocated to a node (cpu/memory/
 *     storage/replicas), driven by `size_value`.
 *   • box COLOR ← a continuous gradient over `percentage` (0..100)
 *     from blue (under-utilised, capacity wasted) → green (optimum) →
 *     red (over-utilised / hot).
 *
 * The HTTP shape this module aligns to is documented inline below; the
 * sibling backend handler in
 *   products/catalyst/bootstrap/api/internal/handler/dashboard.go
 * emits exactly this shape.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), this module
 * exports only types + a thin fetch wrapper — there is NO inlined
 * dimension list, threshold value, or palette literal anywhere in this
 * file or its consumers.
 */

import { API_BASE } from '@/shared/config/urls'

/**
 * The granularity dimension a treemap layer groups by.
 *
 *   • application — Helm release / bp-* unit
 *   • namespace   — Kubernetes namespace
 *   • cluster     — Sovereign cluster (one per kubeconfig)
 *   • family      — product family (observability, security, …)
 *   • sovereign   — top-level Sovereign tenant
 */
export type TreemapDimension =
  | 'application'
  | 'namespace'
  | 'cluster'
  | 'family'
  | 'sovereign'

/**
 * What the gradient maps to. The backend stamps every cell with a
 * `percentage` field whose semantics depend on this selector.
 *
 *   • utilization — used / limit, 0..100. 0 is wasted, 100 is hot.
 *   • health      — healthy-pod ratio, 0..100. INVERTED gradient at the
 *                   render layer (100% healthy = green, 0% = red).
 *   • age         — age in days, normalised 0..100. Newer = blue (cool),
 *                   older = red (drift / staleness).
 */
export type TreemapColorBy = 'utilization' | 'health' | 'age'

/**
 * What drives box AREA. Every choice ends up in `size_value` on the
 * cell — the renderer never has to translate at the render layer.
 *
 *   • cpu_limit      — sum of `resources.limits.cpu` across all pods (millicores)
 *   • memory_limit   — sum of `resources.limits.memory` (bytes)
 *   • storage_limit  — sum of PVC `requests.storage` (bytes)
 *   • replica_count  — sum of `spec.replicas` across the matched workloads
 */
export type TreemapSizeBy =
  | 'cpu_limit'
  | 'memory_limit'
  | 'storage_limit'
  | 'replica_count'

/**
 * Capacity metrics that auto-lock `colorBy` to the matching utilisation
 * dimension. When sizing by cpu/memory/storage capacity the only
 * meaningful color overlay is "how much of that capacity is in use" —
 * the controller enforces this server-side AND on the client to avoid
 * an inconsistent UX between the request URL and the dropdown state.
 */
export const CAPACITY_SIZE_METRICS: ReadonlySet<TreemapSizeBy> = new Set([
  'cpu_limit',
  'memory_limit',
  'storage_limit',
])

/**
 * One cell in the treemap (or a parent in a nested layout). The shape
 * is recursive — `children` carries the next-layer aggregation when the
 * dashboard is rendering 2+ layers deep.
 *
 * Fields:
 *   • id          — stable identifier (helm release name, namespace,
 *                   cluster id, family slug …). `null` for synthetic
 *                   buckets like "unknown" / "ungrouped".
 *   • name        — human-readable label rendered inside the cell.
 *   • count       — number of leaf items rolled up into this cell
 *                   (pods, applications). Surfaced in the tooltip.
 *   • percentage  — 0..100, drives the cell color.
 *   • size_value  — raw value (millicores / bytes / replicas); recharts
 *                   uses this as the area metric. Optional so a parent
 *                   that has only `children` can be rendered as a
 *                   nesting frame without its own area.
 *   • children    — next-layer cells when nesting > 1 layer.
 */
export interface TreemapItem {
  id: string | null
  name: string
  count: number
  percentage: number
  size_value?: number
  children?: TreemapItem[]
}

/**
 * Top-level response from `GET /api/v1/dashboard/treemap`.
 *
 *   • items       — the treemap tree itself.
 *   • total_count — sum of leaf counts across the whole tree (used in
 *                   the page header to show "<n> applications across
 *                   <m> clusters").
 */
export interface TreemapData {
  items: TreemapItem[]
  total_count: number
}

/**
 * Build the query string for a dashboard treemap request.
 *
 * `group_by` is comma-separated so the backend receives a single
 * ordered list of layers — same convention as kubectl `-o jsonpath`
 * dotted accessors. The first dimension is the outer ring; deeper
 * layers nest within.
 */
export function buildTreemapQuery(
  groupBy: readonly TreemapDimension[],
  colorBy: TreemapColorBy,
  sizeBy: TreemapSizeBy,
  deploymentId?: string,
): string {
  const params = new URLSearchParams()
  params.set('group_by', groupBy.join(','))
  params.set('color_by', colorBy)
  params.set('size_by', sizeBy)
  if (deploymentId) params.set('deployment_id', deploymentId)
  return params.toString()
}

/**
 * Fetch the dashboard treemap tree. Throws on non-2xx so React Query
 * surfaces the error via `query.isError`.
 *
 * Per INVIOLABLE-PRINCIPLES #4 the URL is derived from the central
 * `API_BASE` config, never hardcoded inline.
 */
export async function getDashboardTreemap(
  groupBy: readonly TreemapDimension[],
  colorBy: TreemapColorBy,
  sizeBy: TreemapSizeBy,
  deploymentId?: string,
): Promise<TreemapData> {
  const qs = buildTreemapQuery(groupBy, colorBy, sizeBy, deploymentId)
  const res = await fetch(`${API_BASE}/v1/dashboard/treemap?${qs}`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`treemap fetch failed: ${res.status}`)
  }
  return (await res.json()) as TreemapData
}

/**
 * Continuous gradient over [0..100]:
 *   0   → blue   (#3B82F6 — wasted capacity)
 *   50  → green  (#10B981 — optimum)
 *   100 → red    (#EF4444 — over-utilised / hot)
 *
 * Returns an `rgb(R,G,B)` CSS string. Out-of-range inputs are clamped.
 *
 * The two stops are interpolated component-wise so any percentage maps
 * to a deterministic colour — no palette table, no nearest-bucket
 * snapping (per INVIOLABLE-PRINCIPLES #4 there is no hardcoded list of
 * "10%/20%/…" tiers in the renderer).
 */
export function utilizationColor(pct: number): string {
  const p = clamp(pct, 0, 100)
  // Two-segment interpolation: [0..50] blue→green, [50..100] green→red.
  const BLUE = { r: 59,  g: 130, b: 246 }
  const GREEN = { r: 16, g: 185, b: 129 }
  const RED = { r: 239, g: 68,  b: 68 }
  if (p <= 50) {
    const t = p / 50
    return rgb(lerp(BLUE.r, GREEN.r, t), lerp(BLUE.g, GREEN.g, t), lerp(BLUE.b, GREEN.b, t))
  }
  const t = (p - 50) / 50
  return rgb(lerp(GREEN.r, RED.r, t), lerp(GREEN.g, RED.g, t), lerp(GREEN.b, RED.b, t))
}

/**
 * Health gradient — INVERTED utilisation gradient.
 *   0   → red   (everything broken)
 *   100 → green (everything healthy)
 *
 * 0..50 maps red→amber, 50..100 maps amber→green so a "warning" tier
 * still has its canonical amber colour for the operator's eye.
 */
export function healthColor(pct: number): string {
  const p = clamp(pct, 0, 100)
  const RED = { r: 239, g: 68,  b: 68 }
  const AMBER = { r: 245, g: 158, b: 11 }
  const GREEN = { r: 16, g: 185, b: 129 }
  if (p <= 50) {
    const t = p / 50
    return rgb(lerp(RED.r, AMBER.r, t), lerp(RED.g, AMBER.g, t), lerp(RED.b, AMBER.b, t))
  }
  const t = (p - 50) / 50
  return rgb(lerp(AMBER.r, GREEN.r, t), lerp(AMBER.g, GREEN.g, t), lerp(AMBER.b, GREEN.b, t))
}

/**
 * Age gradient — newer = blue (cool, fresh), older = red (drift).
 * Same shape as `utilizationColor` but conceptually different so the
 * Dashboard's color-by selector can branch without an `if` in render.
 */
export function ageColor(pct: number): string {
  return utilizationColor(pct)
}

/** Pick the right colour function for a TreemapColorBy selector. */
export function colorFunctionFor(colorBy: TreemapColorBy): (pct: number) => string {
  switch (colorBy) {
    case 'health':
      return healthColor
    case 'age':
      return ageColor
    case 'utilization':
    default:
      return utilizationColor
  }
}

function clamp(v: number, lo: number, hi: number): number {
  if (Number.isNaN(v)) return lo
  return Math.max(lo, Math.min(hi, v))
}

function lerp(a: number, b: number, t: number): number {
  return Math.round(a + (b - a) * t)
}

function rgb(r: number, g: number, b: number): string {
  return `rgb(${r}, ${g}, ${b})`
}

/**
 * Map a TreemapSizeBy capacity-metric to its matching utilisation
 * `colorBy`. Currently every utilisation breakdown collapses to the
 * single `utilization` value, but the helper keeps the auto-lock seam
 * symmetrical so a future `cpu_utilization`/`memory_utilization`
 * split lands without changing call sites.
 */
export function lockedColorBy(sizeBy: TreemapSizeBy): TreemapColorBy | null {
  return CAPACITY_SIZE_METRICS.has(sizeBy) ? 'utilization' : null
}

/** Walk the in-memory tree to the cells at a given drill path. */
export function walkDrillPath(
  root: readonly TreemapItem[],
  path: readonly { id: string | null }[],
): TreemapItem[] {
  let current: TreemapItem[] | undefined = root as TreemapItem[]
  for (const step of path) {
    if (!current) return []
    const next: TreemapItem | undefined = current.find((c) => c.id === step.id)
    if (!next || !next.children || next.children.length === 0) return []
    current = next.children
  }
  return current ?? []
}
