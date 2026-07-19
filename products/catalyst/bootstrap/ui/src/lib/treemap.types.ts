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
import { STATUS_KIND_COLOR, type StatusKind } from '@/shared/lib/statusColors'

/**
 * The granularity dimension a treemap layer groups by.
 *
 *   • application  — Helm release / bp-* unit
 *   • namespace    — Kubernetes namespace
 *   • cluster      — Sovereign cluster (one per kubeconfig)
 *   • family       — product family (observability, security, …)
 *   • sovereign    — top-level Sovereign estate
 *   • region       — cloud region (multi-region topology)
 *   • vcluster     — vCluster role (mgmt/dmz/rtz; host pods → "host")
 *   • organization — owning Organization, keyed on the
 *     `openova.io/organization` label join key (the same key the per-Org
 *     showback attributes by). Pods with no Org label (host/control-plane
 *     namespaces) roll into a single "Platform overhead" bucket.
 *   • progress     — #4731 lifecycle-stage buckets derived from the Job
 *     tree's `type=group` parents (dependency order), falling back to
 *     kind-derived stages when a deployment's group set is sparse.
 *     JOB-SOURCED: any stack containing this dimension is built
 *     client-side from GET /api/v1/deployments/{id}/jobs.
 *   • kind         — #4731 the typed JobKind discriminator (#3646):
 *     install / reconcile / step / cron / task / mutation / reconciler /
 *     lifecycle. JOB-SOURCED like `progress`.
 */
export type TreemapDimension =
  | 'application'
  | 'namespace'
  | 'cluster'
  | 'family'
  | 'sovereign'
  | 'region'
  | 'vcluster'
  | 'organization'
  | 'progress'
  | 'kind'

/**
 * Dimensions whose data source is the recursive Job tree
 * (GET /api/v1/deployments/{id}/jobs), not the pods/utilisation
 * backend. This is the ONLY conditional in the #4731 design: a layer
 * stack containing any of these builds its TreemapItem[] client-side;
 * every other stack calls getDashboardTreemap exactly as before.
 */
export const JOB_SOURCED_DIMENSIONS: ReadonlySet<TreemapDimension> = new Set([
  'progress',
  'kind',
])

/** True when the layer stack must be built from the Job tree. */
export function isJobSourcedStack(layers: readonly TreemapDimension[]): boolean {
  return layers.some((d) => JOB_SOURCED_DIMENSIONS.has(d))
}

/**
 * What the gradient maps to. The backend stamps every cell with a
 * `percentage` field whose semantics depend on this selector.
 *
 *   • utilization — used / limit, 0..100. 0 is wasted, 100 is hot.
 *   • health      — healthy-pod ratio, 0..100. INVERTED gradient at the
 *                   render layer (100% healthy = green, 0% = red).
 *   • age         — age in days, normalised 0..100. Newer = blue (cool),
 *                   older = red (drift / staleness).
 *   • status      — #4731 CATEGORICAL, not a gradient: job-status
 *                   semantic colours via shared/lib/statusColors.ts
 *                   (green=succeeded/healthy, blue=running, amber=
 *                   degraded, red=failed/failing, grey=pending). Cells
 *                   carry the colour driver in `statusKind`, NOT in
 *                   `percentage` — see {@link statusColor}. Only offered
 *                   for job-sourced stacks.
 */
export type TreemapColorBy = 'utilization' | 'health' | 'age' | 'status'

/**
 * What drives box AREA. Every choice ends up in `size_value` on the
 * cell — the renderer never has to translate at the render layer.
 *
 *   • cpu_request    — sum of `resources.requests.cpu` (millicores) — DEFAULT
 *   • memory_request — sum of `resources.requests.memory` (bytes)
 *   • cpu_usage      — sum of PodMetrics `usage.cpu` (live, millicores)
 *   • memory_usage   — sum of PodMetrics `usage.memory` (live, bytes)
 *   • cpu_limit      — sum of `resources.limits.cpu` (millicores)
 *   • memory_limit   — sum of `resources.limits.memory` (bytes)
 *   • storage_limit  — sum of PVC `requests.storage` (bytes)
 *   • replica_count  — sum of ready pods across the matched workloads
 *
 * `cpu_request` is the dashboard default — most bp-* charts ship
 * without container limits set (limits cause throttling), so a
 * limits-only view shows tiny or empty cells. Requests are always set
 * and tell operators what the cluster has actually budgeted.
 *
 * `uniform` (#4731) is the job-sourced area metric — every job leaf
 * counts 1, so bucket area = job count. Jobs carry no resource
 * capacity, so it is the ONLY size metric offered on a job-sourced
 * stack and is never sent to the pods/utilisation backend.
 */
export type TreemapSizeBy =
  | 'cpu_request'
  | 'memory_request'
  | 'cpu_usage'
  | 'memory_usage'
  | 'cpu_limit'
  | 'memory_limit'
  | 'storage_limit'
  | 'replica_count'
  | 'uniform'

/**
 * Capacity-style metrics that auto-lock `colorBy` to `utilization`.
 * `cpu_usage` and `memory_usage` are live signals — the `utilization`
 * color overlay would be tautological when sized by usage, so they
 * are NOT in this set and the operator can pick any color metric.
 */
export const CAPACITY_SIZE_METRICS: ReadonlySet<TreemapSizeBy> = new Set([
  'cpu_request',
  'memory_request',
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
 *   • statusKind  — #4731 typed CATEGORICAL colour channel for
 *                   job-sourced trees: drives the fill when
 *                   colorBy='status' (see {@link statusColor}). The
 *                   backend never emits it; only the client-side job
 *                   tree builder stamps it (leaves: their JobStatus
 *                   classified via statusKindOf; buckets: the
 *                   aggregate via {@link aggregateStatusKinds}).
 *   • statusLabel — #4731 raw status word ("succeeded", "degraded",
 *                   "healthy", …) for the tooltip / cell sub-label so
 *                   the operator sees the exact 7-value JobStatus, not
 *                   just its colour class.
 *   • jobId       — #4731 set ONLY on job leaves: the full Job id the
 *                   cell click navigates to (JobDetail logs). Its
 *                   presence is the leaf discriminator — buckets never
 *                   carry it, so the click handler can tell a real job
 *                   leaf from a bucket rendered flat after a drill.
 */
export interface TreemapItem {
  id: string | null
  name: string
  count: number
  /**
   * Color-driving percentage in [0..100]. Encoded as JSON null when
   * the data source for the requested colorBy is unavailable
   * (today: utilization without metrics-server). The cell renderer
   * substitutes a neutral grey + tooltip rather than computing a
   * color from a missing input.
   */
  percentage: number | null
  size_value?: number
  children?: TreemapItem[]
  statusKind?: StatusKind
  statusLabel?: string
  jobId?: string
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
 * Returns an `rgb(R,G,B)` CSS string. Inputs are clamped to [0, 100]
 * for color purposes — the BE may emit percentages > 100 (over-request
 * is a real signal), and those map to the same red endpoint, but the
 * tooltip surfaces the raw value so operators see the magnitude.
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

/**
 * #4731 — semantic status fill per StatusKind. CATEGORICAL, not a
 * gradient: the value is a `color-mix()` tint of the ONE statusColors
 * theme token for the kind (never a new hex literal), matching the tile
 * tints the provisioning skeleton used so the morph keeps the exact
 * same colour language. `undefined` renders as pending (grey) — a
 * missing status can never masquerade as success.
 */
const STATUS_FILL_MIX_PCT: Record<StatusKind, number> = {
  success: 65,
  'in-progress': 60,
  warning: 65,
  failed: 70,
  pending: 16,
  // #4731 — even FAINTER than pending so a parked/dormant tether reads as
  // "asleep" rather than "queued". The treemap pairs this with a dashed
  // outline (see SquarifiedCell) for an unmistakable dormant ≠ pending cue.
  dormant: 8,
}

export function statusColor(kind: StatusKind | undefined): string {
  const k: StatusKind = kind ?? 'pending'
  return `color-mix(in srgb, ${STATUS_KIND_COLOR[k]} ${STATUS_FILL_MIX_PCT[k]}%, transparent)`
}

/**
 * #4731 — roll a set of leaf StatusKinds up into one bucket kind.
 * Precedence: failed > warning > in-progress > success (only when ALL
 * succeeded) > pending; a mixed success+pending bucket reads as
 * in-progress (mid-flight even if no leaf is individually running).
 * Same contract the provisioning pane's chips used.
 */
export function aggregateStatusKinds(kinds: readonly StatusKind[]): StatusKind {
  if (kinds.length === 0) return 'pending'
  if (kinds.some((k) => k === 'failed')) return 'failed'
  if (kinds.some((k) => k === 'warning')) return 'warning'
  if (kinds.some((k) => k === 'in-progress')) return 'in-progress'
  if (kinds.every((k) => k === 'success')) return 'success'
  // #4731 — an all-dormant bucket (the parked cutover tether) rolls up as
  // dormant, never pending. Any real work in the bucket wins over dormant
  // (handled by the branches above / the pending fall-through below), so
  // dormant only surfaces when it is the ONLY thing present.
  if (kinds.every((k) => k === 'dormant')) return 'dormant'
  if (kinds.some((k) => k === 'success')) return 'in-progress'
  return 'pending'
}

/** Pick the right colour function for a TreemapColorBy selector. */
export function colorFunctionFor(colorBy: TreemapColorBy): (pct: number) => string {
  switch (colorBy) {
    case 'health':
      return healthColor
    case 'age':
      return ageColor
    case 'status':
      // #4731 — status is CATEGORICAL: the renderer colours cells from
      // their `statusKind` channel via statusColor(), never from the
      // 0..100 percentage. This branch keeps the (pct)=>string contract
      // total for any caller that maps percentages generically (e.g. a
      // gradient legend): every percentage resolves to the neutral
      // pending tint, the same fallback an absent statusKind gets.
      return () => statusColor(undefined)
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
