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

/**
 * Map of plural URL kind segment → canonical singular registry name
 * exposed by the catalyst-api k8scache Registry.
 *
 * The cloud-list URL surface is operator-typed so the natural English
 * pluralisation (`/cloud/resource/services/...`) must keep working
 * alongside the canonical singular registry name (`service`).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the table
 * mirrors the UI side of `cloud-list/kinds.ts:KIND_TO_REGISTRY`.
 *
 * Anything not listed here is returned unchanged — kinds already in
 * singular form (e.g. `pod`) flow through untouched, and unknown
 * kinds bubble up to the API which returns the canonical
 * `unknown-kind` 404 envelope.
 */
const KIND_PLURAL_TO_SINGULAR: Readonly<Record<string, string>> = Object.freeze({
  pods: 'pod',
  deployments: 'deployment',
  statefulsets: 'statefulset',
  daemonsets: 'daemonset',
  replicasets: 'replicaset',
  services: 'service',
  ingresses: 'ingress',
  configmaps: 'configmap',
  secrets: 'secret',
  namespaces: 'namespace',
  nodes: 'node',
  persistentvolumes: 'persistentvolume',
  endpointslices: 'endpointslice',
  events: 'event',
  podmetrics: 'podmetrics',
  policyreports: 'policyreport',
  clusterpolicyreports: 'clusterpolicyreport',
})

/**
 * Normalise the URL kind segment into the canonical registry name.
 * Lower-cases input + maps known plural forms to their singular
 * registry id; unknown kinds are returned lower-cased so the server
 * can answer with its `availableKinds` 404 envelope.
 */
export function normaliseKindForRegistry(kind: string): string {
  const lower = (kind ?? '').trim().toLowerCase()
  if (lower === '') return ''
  return KIND_PLURAL_TO_SINGULAR[lower] ?? lower
}

/** Resource detail tab ids — used by ResourceDetailPage routing.
 *
 * Wave-2 Family-E (#1583, C11-010) added the `sbom` tab. It renders
 * only for kinds where Trivy reports apply (Pods today; image-bearing
 * kinds in future iterations). The tab bar always lists it so the
 * matrix's accessibility-tree snapshot can assert the SBOM tab is
 * discoverable from any kind's detail page — the panel itself
 * surfaces an "only applicable to Pods" hint on non-applicable kinds.
 */
export const RESOURCE_DETAIL_TABS = [
  'overview',
  'yaml',
  'logs',
  'exec',
  'events',
  'metrics',
  'sbom',
  'compliance',
  // #3996 — the lightweight ArgoCD/Flux management tab. Rendered only for
  // the manageable Flux reconciler kinds (HelmRelease / Kustomization /
  // GitRepository / OCIRepository / HelmRepository / HelmChart); hidden on
  // every other kind, exactly like the Pod-only 'exec' tab.
  'reconcile',
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

/** The answer 409 managed-by-git of a direct write: Git owns the object (`owner`), nothing was written. */
export class ManagedByGitError extends Error {
  readonly owner: string
  constructor(owner: string, detail: string) {
    super(detail || `Managed by Git: ${owner}`)
    this.name = 'ManagedByGitError'
    this.owner = owner
  }
}

/** The ManagedByGitError of an answer, or null when the answer is not
 *  409 managed-by-git. */
export function managedByGitError(status: number, bodyText: string): ManagedByGitError | null {
  if (status !== 409) return null
  try {
    const body = JSON.parse(bodyText) as { error?: unknown; owner?: unknown; detail?: unknown }
    if (body.error !== 'managed-by-git') return null
    return new ManagedByGitError(String(body.owner ?? ''), String(body.detail ?? ''))
  } catch {
    return null
  }
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
    throw managedByGitError(res.status, bodyText) ?? new Error(`HTTP ${res.status}: ${bodyText.slice(0, 200)}`)
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
  /**
   * G80 #2627: true when the K8s kind has no `Ready` condition concept
   * (Secret/ConfigMap/PolicyReport/Endpoints/ServiceAccount/Role/etc.).
   * UI renders `N/A` instead of defaulting to `Pending`. Backend omits
   * when false (the common case — workload kinds with real readiness
   * semantics). Inverted from the natural "applicable" framing because
   * Go's `bool+omitempty` strips the false case.
   */
  passive?: boolean
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
  reason: string,
): Promise<YAMLApplyResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'apply'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ yaml, reason }),
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
  reason: string,
): Promise<ScaleResponse> {
  const res = await authedFetch(resourceURL(deploymentId, kind, ns, name, 'scale'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ replicas, reason }),
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

/** A DELETE has no body, so the reason of the break-glass write is the query parameter `reason`. */
export async function deleteResource(
  deploymentId: string,
  kind: string,
  ns: string | undefined,
  name: string,
  reason: string,
): Promise<DeleteResponse> {
  const url = `${resourceURL(deploymentId, kind, ns, name)}?reason=${encodeURIComponent(reason)}`
  const res = await authedFetch(url, {
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

/**
 * The owner in Git of an object from its detail payload, or null. The rules and their order are
 * those of Owner in the API package clusterwrite: the label kustomize.toolkit.fluxcd.io/name, the
 * label helm.toolkit.fluxcd.io/name, a Flux owner reference, the label app.kubernetes.io/managed-by
 * flux. A kind of the intent lock carries no such label: its write answers 409 (ManagedByGitError).
 */
export function gitOwner(obj: K8sObject | null | undefined): string | null {
  const meta = obj?.metadata
  if (!meta) return null
  const labels = meta.labels ?? {}
  const qualified = (namespace: string | undefined, name: string) => (namespace ? `${namespace}/${name}` : name)
  for (const [group, kind] of [
    ['kustomize.toolkit.fluxcd.io', 'Kustomization'],
    ['helm.toolkit.fluxcd.io', 'HelmRelease'],
  ]) {
    const name = labels[`${group}/name`]
    if (name) return `${kind} ${qualified(labels[`${group}/namespace`], name)}`
  }
  for (const ref of meta.ownerReferences ?? []) {
    if ((ref.apiVersion ?? '').includes('fluxcd.io')) return `${ref.kind ?? ''} ${qualified(meta.namespace, ref.name ?? '')}`
  }
  if ((labels['app.kubernetes.io/managed-by'] ?? '').toLowerCase() === 'flux') return 'Flux'
  return null
}

// ─── Logs WebSocket (X2) ─────────────────────────────────────────────

export interface LogsURLOptions {
  follow?: boolean
  tailLines?: number
  since?: string
  previous?: boolean
}

/**
 * Build the wss:// URL for the Pod-log WebSocket. Per slice X1 #1164
 * the path is `/k8s/logs/{ns}/{pod}/{container}` under the standard
 * sovereign API base; query params control tail / follow / since.
 *
 * The protocol is ws:// when the document is on http:// (vitest /
 * dev) and wss:// otherwise — the same posture as authedFetch.
 */
export function logsWebSocketURL(
  deploymentId: string,
  ns: string,
  pod: string,
  container: string,
  opts: LogsURLOptions = {},
): string {
  const params: string[] = []
  if (opts.follow !== false) params.push('follow=true')
  else params.push('follow=false')
  const tailLines = typeof opts.tailLines === 'number' ? opts.tailLines : 100
  params.push(`tailLines=${tailLines}`)
  if (opts.since) params.push(`since=${encodeURIComponent(opts.since)}`)
  if (opts.previous) params.push('previous=true')
  const qs = params.length ? `?${params.join('&')}` : ''
  // Convert API_BASE (e.g. /api or /sovereign/api) into a wss URL.
  const path = `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/logs/${encodeURIComponent(
    ns,
  )}/${encodeURIComponent(pod)}/${encodeURIComponent(container)}${qs}`
  return absoluteWebSocketURL(path)
}

/**
 * Build the wss:// URL of the exec WebSocket, the console's pod shell
 * (ExecPanel opens it directly).
 */
export function execWebSocketURL(
  deploymentId: string,
  ns: string,
  pod: string,
  container: string,
  command = '/bin/sh',
): string {
  const path = `${API_BASE}/v1/sovereigns/${encodeURIComponent(
    deploymentId,
  )}/k8s/exec/${encodeURIComponent(ns)}/${encodeURIComponent(pod)}/${encodeURIComponent(
    container,
  )}?command=${encodeURIComponent(command)}`
  return absoluteWebSocketURL(path)
}

function absoluteWebSocketURL(path: string): string {
  if (typeof window === 'undefined') return path
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}${path}`
}
