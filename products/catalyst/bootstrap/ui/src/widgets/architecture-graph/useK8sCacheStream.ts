/**
 * useK8sCacheStream — subscribe a Sovereign's k8scache.Factory via SSE
 * and keep a normalized in-memory snapshot of every watched K8s
 * object the architecture-graph adapter consumes.
 *
 * Endpoint contract (issue #321 + companion #975):
 *
 *   GET /api/v1/sovereigns/{deploymentId}/k8s/stream?
 *         kinds=node,pod,deployment,statefulset,daemonset,
 *               replicaset,service,ingress,namespace,
 *               persistentvolumeclaim,persistentvolume,
 *               configmap,endpointslice,server.hcloud,volume.hcloud
 *         &initialState=1
 *
 * Wire frame: `data: {cluster,kind,type,object,at}\n\n` per the SSE
 * spec; the encoder strips Secret/ConfigMap data fields server-side
 * before the frame leaves the catalyst-api process.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — initialState=1 plus delta stream lands together.
 *   #3 (event-driven) — no polling. The browser holds one EventSource
 *      per page; the kernel-level keepalive carries the connection
 *      across NAT timeouts (the catalyst-api emits a `: ping` comment
 *      every 15s).
 *   #4 (never hardcode) — the kinds list is exported from this file
 *      so tests + adapter assertions can pin it in one place.
 */

import { useEffect, useRef, useState } from 'react'

import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

/**
 * Kinds the architecture graph subscribes to. The list lives at the
 * widget seam so the page-level adapter can index every object the
 * graph needs by `kind` without re-reading the SSE wire.
 *
 * Order is intentional: cluster-scoped first, then namespaced, then
 * Crossplane projections — readable at a glance during debugging.
 */
export const GRAPH_K8S_KINDS = [
  // Cluster-scoped
  'namespace',
  'node',
  'persistentvolume',
  // Namespaced workloads
  'pod',
  'deployment',
  'statefulset',
  'daemonset',
  'replicaset',
  'service',
  'ingress',
  'persistentvolumeclaim',
  'configmap',
  'endpointslice',
  // Crossplane bridge — the cloud-side counterpart to PV via volume.hcloud
  'server.hcloud',
  'volume.hcloud',
] as const

export type K8sKind = (typeof GRAPH_K8S_KINDS)[number]

/**
 * Wire shape of one event delivered by the catalyst-api SSE encoder.
 * Object is the unstructured K8s payload (with .data scrubbed for
 * Secret/ConfigMap on the server side).
 */
export interface K8sStreamEvent {
  cluster: string
  kind: K8sKind
  type: 'ADDED' | 'MODIFIED' | 'DELETED'
  object: K8sObject
  at: string
}

export interface K8sObjectMetadata {
  name?: string
  namespace?: string
  uid?: string
  resourceVersion?: string
  creationTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  ownerReferences?: Array<{
    apiVersion?: string
    kind?: string
    name?: string
    uid?: string
    controller?: boolean
    blockOwnerDeletion?: boolean
  }>
}

export interface K8sObject {
  apiVersion?: string
  kind?: string
  metadata?: K8sObjectMetadata
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  // EndpointSlice carries `endpoints` + `ports` at the top level.
  endpoints?: unknown
  ports?: unknown
  [key: string]: unknown
}

/**
 * Snapshot is the in-memory mirror of every object the graph layer
 * cares about — keyed by composite id `{kind}:{namespace}/{name}` so a
 * Pod in two namespaces doesn't collide.
 *
 * Cluster-scoped kinds (Namespace, Node, PV, server.hcloud,
 * volume.hcloud) use `{kind}:{name}` (empty namespace).
 */
export type K8sSnapshot = Map<string, K8sObject>

export interface K8sStreamState {
  /** Live snapshot — every observed object minus DELETED. */
  snapshot: K8sSnapshot
  /** EventSource readyState mirror. */
  status: 'connecting' | 'open' | 'closed' | 'error'
  /** Monotonically increasing — bumps on every applied delta so
   *  consumers can debounce without re-walking the snapshot. */
  revision: number
}

export interface UseK8sCacheStreamOptions {
  /** When false, the hook does not open an EventSource. Tests pass
   *  this. */
  enabled?: boolean
  /** Override the kinds set; defaults to GRAPH_K8S_KINDS. */
  kinds?: readonly string[]
}

/**
 * Compose the composite snapshot key for an event. Empty namespace
 * for cluster-scoped kinds so PV / Node / server.hcloud all key on
 * `{kind}:{name}` cleanly.
 */
export function objectKey(kind: string, obj: K8sObject): string {
  const ns = obj.metadata?.namespace ?? ''
  const name = obj.metadata?.name ?? ''
  if (ns) {
    return `${kind}:${ns}/${name}`
  }
  return `${kind}:${name}`
}

export function useK8sCacheStream(
  deploymentId: string,
  opts: UseK8sCacheStreamOptions = {},
): K8sStreamState {
  const { enabled = true, kinds = GRAPH_K8S_KINDS } = opts

  const [state, setState] = useState<K8sStreamState>(() => ({
    snapshot: new Map(),
    status: 'connecting',
    revision: 0,
  }))

  // The snapshot Map is mutated in-place under a ref so high-frequency
  // delta bursts don't trigger one React render per event. We commit
  // a new state slice on every applied delta — React's auto-batching
  // collapses bursts within a tick.
  const snapshotRef = useRef<K8sSnapshot>(new Map())

  useEffect(() => {
    // jsdom (and any non-browser host) does not provide EventSource;
    // bail with an empty snapshot so consuming components render
    // their no-K8s-data state without a runtime crash.
    if (!enabled || !deploymentId || typeof EventSource === 'undefined') {
      return
    }
    snapshotRef.current = new Map()
    setState({ snapshot: snapshotRef.current, status: 'connecting', revision: 0 })

    // EventSource cannot carry custom headers (no Authorization: Bearer
    // path), so on Sovereign mode the chroot SPA must attach the OIDC
    // access token as a query parameter. The catalyst-api ReadSessionToken
    // accepts `?access_token=<jwt>` as a fallback channel and validates
    // it through the same JWKS path as the header. Mother (Catalyst-Zero)
    // sessions have no OIDC tokens in sessionStorage, so the param is
    // omitted and the existing catalyst_session cookie carries auth.
    const params = new URLSearchParams()
    params.set('kinds', kinds.join(','))
    params.set('initialState', '1')
    const tokens = loadTokens()
    if (tokens?.accessToken) {
      params.set('access_token', tokens.accessToken)
    }
    const url = `${API_BASE}/v1/sovereigns/${encodeURIComponent(
      deploymentId,
    )}/k8s/stream?${params.toString()}`
    const es = new EventSource(url, { withCredentials: true })

    es.onopen = () => {
      setState((prev) => ({ ...prev, status: 'open' }))
    }
    es.onerror = () => {
      // EventSource auto-reconnects; surface the state so the page
      // can render a "reconnecting" indicator without tearing down.
      setState((prev) => ({ ...prev, status: 'error' }))
    }
    es.onmessage = (ev) => {
      let payload: K8sStreamEvent
      try {
        payload = JSON.parse(ev.data) as K8sStreamEvent
      } catch {
        return
      }
      const key = objectKey(payload.kind, payload.object)
      if (payload.type === 'DELETED') {
        snapshotRef.current.delete(key)
      } else {
        snapshotRef.current.set(key, payload.object)
      }
      setState((prev) => ({
        snapshot: snapshotRef.current,
        status: 'open',
        revision: prev.revision + 1,
      }))
    }

    return () => {
      es.close()
      setState((prev) => ({ ...prev, status: 'closed' }))
    }
  }, [deploymentId, enabled, kinds])

  return state
}
