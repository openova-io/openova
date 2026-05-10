/**
 * resources.api.ts — typed REST client for the Resources family
 * (qa-loop iter-12 Fix #50). The Resources surface mirrors the wired
 * networking/ pattern from iter-11 Fix #48 — every page subscribes to
 * a real catalyst-api endpoint via `authedFetch` + TanStack Query, no
 * "(pending live data)" placeholders.
 *
 * Endpoint contract:
 *
 *   GET  /api/v1/sovereigns/{id}/k8s/{kind}            — paginated list
 *        ?namespace=<ns>&labelSelector=...&limit=...
 *   GET  /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name} — single object
 *   POST /api/v1/sovereigns/{id}/k8s/apply              — multi-doc YAML
 *   GET  /api/v1/sovereigns/{id}/k8s/search?q=<substr>  — cross-kind search
 *   GET  /api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}
 *        — WebSocket (built via logsWebSocketURL)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every URL is
 * derived from `API_BASE`. No literal `/api/...` path or hostname
 * lives in the consumers.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

/* ── Wire types ──────────────────────────────────────────────────── */

export interface K8sListResponse {
  kind: string
  cluster: string
  items: K8sObject[]
  continue?: string
  ageSeconds?: number
}

export interface K8sSearchHit {
  kind: string
  name: string
  namespace?: string
}

export interface K8sSearchResponse {
  items: K8sSearchHit[]
  total: number
  query?: string
}

export interface K8sMultiApplyResultEntry {
  kind: string
  namespace?: string
  name: string
  created: boolean
  resourceVersion?: string
  flux?: boolean
  giteaPRUrl?: string
  error?: string
}

export interface K8sMultiApplyResponse {
  /** Per-doc result rows, in the same order as the YAML document
   *  separators in the request body. Mirrors `items` on the wire. */
  items: K8sMultiApplyResultEntry[]
  /** Count of `items` whose `created=true`. */
  created: number
  /** Count of `items` whose `created=false` (i.e. updates). */
  updated: number
}

/* ── URL helpers ─────────────────────────────────────────────────── */

function listURL(deploymentId: string, kind: string, opts: ListOpts = {}): string {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  if (opts.labelSelector) params.set('labelSelector', opts.labelSelector)
  if (opts.limit) params.set('limit', String(opts.limit))
  if (opts.continueToken) params.set('continue', opts.continueToken)
  const qs = params.toString()
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/${encodeURIComponent(
    kind,
  )}${qs ? `?${qs}` : ''}`
}

function searchURL(deploymentId: string, q: string, kinds?: string): string {
  const params = new URLSearchParams()
  params.set('q', q)
  if (kinds) params.set('kinds', kinds)
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/search?${params.toString()}`
}

function multiApplyURL(deploymentId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/apply`
}

/* ── Helpers ─────────────────────────────────────────────────────── */

async function parseJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let bodyText = ''
    try {
      bodyText = await res.text()
    } catch {
      /* noop */
    }
    throw new Error(`HTTP ${res.status}: ${bodyText.slice(0, 240)}`)
  }
  return (await res.json()) as T
}

/* ── Public API ──────────────────────────────────────────────────── */

export interface ListOpts {
  namespace?: string
  labelSelector?: string
  limit?: number
  continueToken?: string
  signal?: AbortSignal
}

export async function listK8s(
  deploymentId: string,
  kind: string,
  opts: ListOpts = {},
): Promise<K8sListResponse> {
  const res = await authedFetch(listURL(deploymentId, kind, opts), { signal: opts.signal })
  return parseJSON<K8sListResponse>(res)
}

export async function searchK8s(
  deploymentId: string,
  q: string,
  kinds?: string,
  signal?: AbortSignal,
): Promise<K8sSearchResponse> {
  const res = await authedFetch(searchURL(deploymentId, q, kinds), { signal })
  return parseJSON<K8sSearchResponse>(res)
}

export interface MultiApplyRequest {
  yaml: string
  commitMessage?: string
}

export async function multiApplyYAML(
  deploymentId: string,
  body: MultiApplyRequest,
): Promise<K8sMultiApplyResponse> {
  const res = await authedFetch(multiApplyURL(deploymentId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return parseJSON<K8sMultiApplyResponse>(res)
}

/* ── Kind catalogue ──────────────────────────────────────────────── */

/**
 * Plural URL kind segments accepted by the Resources router and the
 * canonical singular registry name they map to on the catalyst-api
 * side. Mirrors the singular set from `cloud-list/resource.api.ts`'s
 * KIND_PLURAL_TO_SINGULAR — kept in sync intentionally so /resources/X
 * and /cloud/resource/X resolve the same kind.
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
  persistentvolumeclaims: 'persistentvolumeclaim',
  endpointslices: 'endpointslice',
  events: 'event',
})

const KIND_SINGULAR_TO_PLURAL: Readonly<Record<string, string>> = Object.freeze(
  Object.fromEntries(
    Object.entries(KIND_PLURAL_TO_SINGULAR).map(([plural, singular]) => [singular, plural]),
  ),
)

export function pluralKind(input: string): string {
  const lower = (input ?? '').trim().toLowerCase()
  if (!lower) return 'pods'
  if (lower in KIND_PLURAL_TO_SINGULAR) return lower
  return KIND_SINGULAR_TO_PLURAL[lower] ?? lower
}

export function singularKind(input: string): string {
  const lower = (input ?? '').trim().toLowerCase()
  if (!lower) return ''
  return KIND_PLURAL_TO_SINGULAR[lower] ?? lower
}

export interface KindEntry {
  /** Plural URL segment (`pods`). */
  id: string
  /** Display label (`Pods`). */
  label: string
  /** Singular registry name (`pod`). */
  registry: string
  /** True when the kind is namespaced. */
  namespaced: boolean
}

/**
 * Canonical kind tab order on the Resources index page. Order matters:
 * matrix TC-198 asserts on tokens "Pods", "Deployments", "Services",
 * "ConfigMaps" appearing in this order when /resources is rendered.
 */
export const RESOURCE_KINDS: readonly KindEntry[] = Object.freeze([
  { id: 'pods', label: 'Pods', registry: 'pod', namespaced: true },
  { id: 'deployments', label: 'Deployments', registry: 'deployment', namespaced: true },
  { id: 'statefulsets', label: 'StatefulSets', registry: 'statefulset', namespaced: true },
  { id: 'daemonsets', label: 'DaemonSets', registry: 'daemonset', namespaced: true },
  { id: 'replicasets', label: 'ReplicaSets', registry: 'replicaset', namespaced: true },
  { id: 'services', label: 'Services', registry: 'service', namespaced: true },
  { id: 'ingresses', label: 'Ingresses', registry: 'ingress', namespaced: true },
  { id: 'configmaps', label: 'ConfigMaps', registry: 'configmap', namespaced: true },
  { id: 'secrets', label: 'Secrets', registry: 'secret', namespaced: true },
  { id: 'namespaces', label: 'Namespaces', registry: 'namespace', namespaced: false },
  { id: 'nodes', label: 'Nodes', registry: 'node', namespaced: false },
  { id: 'persistentvolumes', label: 'PersistentVolumes', registry: 'persistentvolume', namespaced: false },
  { id: 'endpointslices', label: 'EndpointSlices', registry: 'endpointslice', namespaced: true },
])

export function findKind(idOrRegistry: string): KindEntry | undefined {
  const lower = (idOrRegistry ?? '').toLowerCase()
  return RESOURCE_KINDS.find((k) => k.id === lower || k.registry === lower)
}

/* ── Region helpers ──────────────────────────────────────────────── */

/**
 * Pull the canonical region label out of a Node or Pod object. Nodes
 * carry the well-known label `topology.kubernetes.io/region` (cloud
 * agnostic) and Hetzner-specific `failure-domain.beta.kubernetes.io/region`
 * — we accept either. Pods inherit the label via spec.nodeSelector or
 * the scheduler annotates `catalyst.openova.io/region` on the Pod via
 * the qa-fixtures admission shim.
 */
export function regionOf(obj: K8sObject | null | undefined): string {
  if (!obj?.metadata) return ''
  const labels = obj.metadata.labels ?? {}
  const annotations = obj.metadata.annotations ?? {}
  return (
    labels['topology.kubernetes.io/region'] ??
    labels['failure-domain.beta.kubernetes.io/region'] ??
    annotations['catalyst.openova.io/region'] ??
    ''
  )
}
