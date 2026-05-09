/**
 * resource.api.ts — single REST surface for the EPIC-4 Slice R (#1099)
 * resource browser drill-down. Sibling to compliance.api.ts +
 * fleet.api.ts (no Zustand; TanStack Query later for in-component
 * state).
 *
 * All endpoints sit under
 *   /api/v1/sovereigns/{deploymentId}/k8s/{kind}/{ns}/{name}[...]
 * with `_` substituted for the namespace segment on cluster-scoped
 * resources (chi can't route empty segments).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL is
 * derived from API_BASE — not a single literal hostname or `/api/...`
 * path lives in this file's UI consumers.
 */
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

/** Resource kinds the action endpoints accept for `scale`. */
export const SCALABLE_KINDS = new Set(['deployment', 'statefulset'])
/** Resource kinds the action endpoints accept for `restart`. */
export const RESTARTABLE_KINDS = new Set(['deployment', 'statefulset', 'daemonset'])

/** Resource detail tab ids — used by ResourceDetailPage routing. */
export const RESOURCE_DETAIL_TABS = [
  'overview',
  'yaml',
  'logs',
  'exec',
  'events',
  'metrics',
  'tree',
] as const
export type ResourceDetailTab = (typeof RESOURCE_DETAIL_TABS)[number]
export const DEFAULT_RESOURCE_DETAIL_TAB: ResourceDetailTab = 'overview'

export function isValidResourceDetailTab(value: unknown): value is ResourceDetailTab {
  return typeof value === 'string' && (RESOURCE_DETAIL_TABS as readonly string[]).includes(value)
}

export function parseTabFromPath(value: string | undefined): ResourceDetailTab {
  return isValidResourceDetailTab(value) ? value : DEFAULT_RESOURCE_DETAIL_TAB
}

/**
 * Compose the URL-safe namespace segment. Cluster-scoped resources have
 * no namespace; chi can't route empty path segments, so we substitute
 * `_` and the server canonicalises back to "".
 */
export function nsSegment(ns: string | undefined): string {
  const trimmed = (ns ?? '').trim()
  return trimmed === '' ? '_' : encodeURIComponent(trimmed)
}

/** Build the canonical detail-page route href for a (kind, ns, name). */
export function resourceDetailHref(
  basePath: string,
  kind: string,
  ns: string | undefined,
  name: string,
  tab: ResourceDetailTab = DEFAULT_RESOURCE_DETAIL_TAB,
): string {
  const trimmed = basePath.replace(/\/$/, '')
  return `${trimmed}/resource/${encodeURIComponent(kind)}/${nsSegment(ns)}/${encodeURIComponent(name)}/${tab}`
}

function resourceURL(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  suffix = '',
): string {
  const tail = suffix === '' ? '' : `/${suffix.replace(/^\//, '')}`
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/${encodeURIComponent(
    kind,
  )}/${nsSegment(ns)}/${encodeURIComponent(name)}${tail}`
}

function metricsURL(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  window: string,
): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(
    deploymentId,
  )}/k8s/metrics/${encodeURIComponent(kind)}/${nsSegment(ns)}/${encodeURIComponent(
    name,
  )}?window=${encodeURIComponent(window)}`
}

/** Parse a JSON response, throwing with the body text on non-2xx. */
async function parseJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let bodyText = ''
    try {
      bodyText = await res.text()
    } catch {
      /* noop */
    }
    throw new Error(`HTTP ${res.status}: ${bodyText.slice(0, 200)}`)
  }
  return (await res.json()) as T
}

// ─── Live resource fetch ────────────────────────────────────────────

export async function getResource(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<K8sObject> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name), { signal })
  return parseJSON<K8sObject>(res)
}

// ─── Resource tree ──────────────────────────────────────────────────

export interface ResourceTreeNode {
  kind: string
  apiGroup?: string
  ns?: string
  name: string
  uid?: string
  phase?: string
  ready: boolean
  owners?: ResourceTreeNode[]
  children?: ResourceTreeNode[]
}

export async function getResourceTree(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<ResourceTreeNode> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'tree'), { signal })
  return parseJSON<ResourceTreeNode>(res)
}

// ─── YAML editor (R3) ───────────────────────────────────────────────

export interface YAMLApplyResponse {
  name: string
  namespace?: string
  dryRun: boolean
  resourceVersion: string
}

export async function dryRunYAML(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  yaml: string,
): Promise<YAMLApplyResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'dry-run'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ yaml }),
  })
  return parseJSON<YAMLApplyResponse>(res)
}

export async function applyYAML(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  yaml: string,
): Promise<YAMLApplyResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'apply'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ yaml }),
  })
  return parseJSON<YAMLApplyResponse>(res)
}

// ─── Per-row actions (R6) ───────────────────────────────────────────

export interface ScaleResponse {
  name: string
  namespace?: string
  replicas: number
}

export async function scaleResource(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  replicas: number,
): Promise<ScaleResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'scale'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ replicas }),
  })
  return parseJSON<ScaleResponse>(res)
}

export interface RestartResponse {
  name: string
  namespace?: string
  restartedAt: string
}

export async function restartResource(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
): Promise<RestartResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'restart'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  return parseJSON<RestartResponse>(res)
}

export interface DeleteResponse {
  name: string
  namespace?: string
  message: string
}

export async function deleteResource(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
): Promise<DeleteResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name), {
    method: 'DELETE',
  })
  return parseJSON<DeleteResponse>(res)
}

// ─── Metrics (R5) ───────────────────────────────────────────────────

export interface MetricsSample {
  cpuMilli?: number
  memBytes?: number
  podCount?: number
  [key: string]: number | undefined
}

export interface MetricsResponse {
  kind: string
  namespace?: string
  name: string
  current: MetricsSample
  series: MetricsSample[]
  source: 'metrics.k8s.io' | 'unavailable' | string
}

export async function getResourceMetrics(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  window = '1h',
  signal?: AbortSignal,
): Promise<MetricsResponse> {
  const res = await authedFetch(metricsURL(deploymentId, kind, ns, name, window), { signal })
  return parseJSON<MetricsResponse>(res)
}

// ─── Helpers shared with widgets ────────────────────────────────────

/** Detect the Flux-managed annotation per slice R3 branching. */
export function isFluxManaged(obj: K8sObject | null | undefined): boolean {
  if (!obj?.metadata) return false
  const meta = obj.metadata as { labels?: Record<string, string>; annotations?: Record<string, string> }
  const lbl = meta.labels?.['app.kubernetes.io/managed-by'] ?? meta.labels?.['managed-by']
  if (lbl && lbl.toLowerCase() === 'flux') return true
  const ann = meta.annotations?.['catalyst.openova.io/managed-by']
  if (ann && ann.toLowerCase() === 'flux') return true
  return false
}

/** Detect "manual" management (operator owns the resource directly). */
export function isManuallyManaged(obj: K8sObject | null | undefined): boolean {
  if (!obj?.metadata) return false
  const meta = obj.metadata as { labels?: Record<string, string>; annotations?: Record<string, string> }
  const ann = meta.annotations?.['catalyst.openova.io/managed-by']
  if (ann && ann.toLowerCase() === 'manual') return true
  // Default: when no managed-by annotation exists, treat as manual.
  return !isFluxManaged(obj)
}
