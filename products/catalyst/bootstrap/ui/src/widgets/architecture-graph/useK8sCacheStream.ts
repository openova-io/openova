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
  // Gateway-API + NetworkPolicy (#3987 UAT row 200) — the graph draws
  // Gateway / HTTPRoute / NetworkPolicy nodes (Networking lens), so the
  // widget's self-sufficient fallback stream must watch them too. The
  // CloudPage's shared subscription already covers them via
  // CLOUD_PAGE_K8S_KINDS; without these the standalone graph rendered
  // the Networking chips as 0/0 against a cluster full of routes.
  'gateway',
  'httproute',
  'networkpolicy',
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
  /**
   * #3939/#3931 (#3642 sibling): for a per-tier vCluster (mgmt/dmz/rtz)
   * host-synced object, the catalyst-api flatten step surfaces the
   * DE-MANGLED in-vCluster identity at the top level so the SPA can
   * render the real name the operator knows
   * (`gitea-75d9f486fb-g8hsr`) instead of the loft syncer's mangled
   * host name (`gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster`).
   *
   * IMPORTANT: `metadata.{name,namespace}` deliberately stay the HOST
   * coordinates — the Logs (`/k8s/logs/{ns}/{pod}/...`) and
   * resource-tree (`/k8s/{kind}/{ns}/{name}/tree`) drill-downs resolve
   * against the HOST apiserver (the mothership holds only the host
   * kubeconfig). Prefer `displayName`/`vclusterNamespace` for DISPLAY,
   * `metadata` for the drill-down hrefs.
   */
  displayName?: string
  vclusterNamespace?: string
  /**
   * #5571: the k8scache cluster id (region) this object was observed
   * in, stamped from the SSE event's `cluster` field as the delta is
   * applied. The wire `object` never carries it — region identity
   * lives on the ENVELOPE, so it has to be copied down onto the object
   * or it is lost the moment the event is folded into the snapshot.
   *
   * Consumers that display estate-wide sets (the Cloud list pages)
   * MUST surface this, otherwise one region's set is indistinguishable
   * from the whole Sovereign's.
   */
  clusterId?: string
  [key: string]: unknown
}

/**
 * Snapshot is the in-memory mirror of every object the graph layer
 * cares about — keyed by composite id
 * `{kind}:{namespace}/{name}@{cluster}` so a Pod in two namespaces
 * doesn't collide, AND the SAME `{ns}/{name}` in two REGIONS doesn't
 * collide either (#5571).
 *
 * Cluster-scoped kinds (Namespace, Node, PV, server.hcloud,
 * volume.hcloud) use `{kind}:{name}@{cluster}` (empty namespace).
 *
 * 🛑 The `@{cluster}` suffix is load-bearing (#5571). A 2-region
 * Sovereign runs the SAME chart in both regions, so a large share of
 * objects share `{ns}/{name}` across regions — measured on hw292:
 * 21 of 64 NetworkPolicies and 37 of 99 CiliumNetworkPolicies. Keying
 * without the cluster silently drops the loser of each collision and
 * makes a DELETE in one region erase the row for the other, which the
 * Cloud security-posture pages read as "policy absent". The suffix
 * form (not a prefix) is deliberate: every existing key scan is
 * `key.startsWith(`${kind}:`)` / `key.split(':', 1)[0]`, which keeps
 * working unchanged.
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
 * Compose the composite snapshot key from raw coordinates. Empty
 * namespace for cluster-scoped kinds so PV / Node / server.hcloud all
 * key on `{kind}:{name}` cleanly.
 *
 * `cluster` is REQUIRED (#5571) — it is appended as a `@{cluster}`
 * SUFFIX so `key.startsWith(`${kind}:`)` and `key.split(':', 1)[0]`
 * scans elsewhere in the SPA keep working verbatim. It is required
 * rather than defaulted so a new call site cannot silently
 * re-introduce the cross-region collapse by forgetting it; pass `''`
 * only where there is genuinely no region context.
 */
export function snapshotKey(
  kind: string,
  ns: string,
  name: string,
  cluster: string,
): string {
  const base = ns ? `${kind}:${ns}/${name}` : `${kind}:${name}`
  return cluster ? `${base}@${cluster}` : base
}

/**
 * Compose the composite snapshot key for an event's object. See
 * `snapshotKey` — `cluster` comes from the SSE event ENVELOPE
 * (`payload.cluster`), never from the object itself.
 */
export function objectKey(
  kind: string,
  obj: K8sObject,
  cluster: string,
): string {
  return snapshotKey(
    kind,
    obj.metadata?.namespace ?? '',
    obj.metadata?.name ?? '',
    cluster,
  )
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
      // Guard against non-K8s-event frames the catalyst-api SSE encoder
      // multiplexes onto the same channel. The first frame on connect is
      // `{type:"ready", cluster, kinds, at}` (issued by the server's
      // immediate-snapshot path so the UI can flip from "connecting" to
      // "open" before any kube event arrives — Fix #6 / PR #1189). It
      // carries no `object` payload; calling objectKey(.., undefined)
      // would throw "Cannot read properties of undefined (reading
      // 'metadata')" and tear down the entire /cloud route. Skip any
      // frame whose type is not one of the three K8s delta types OR
      // whose object/metadata is missing — the snapshot only mutates on
      // real ADDED/MODIFIED/DELETED frames with a typed object.
      if (
        payload.type !== 'ADDED' &&
        payload.type !== 'MODIFIED' &&
        payload.type !== 'DELETED'
      ) {
        // Surface server-driven readiness so the page flips to "open"
        // even if the EventSource onopen event hasn't fired yet on this
        // browser. No snapshot mutation, no revision bump.
        setState((prev) => ({ ...prev, status: 'open' }))
        return
      }
      if (!payload.object || !payload.object.metadata) {
        return
      }
      // #5571: region identity lives on the event ENVELOPE, not on the
      // object. Key by it so the two regions of a 2-region Sovereign
      // cannot overwrite each other, and stamp it DOWN onto the stored
      // object so consumers (Cloud list pages, owner-ref resolution)
      // can still tell the regions apart after the fold.
      const cluster = payload.cluster ?? ''
      const key = objectKey(payload.kind, payload.object, cluster)
      if (payload.type === 'DELETED') {
        snapshotRef.current.delete(key)
      } else {
        snapshotRef.current.set(
          key,
          cluster ? { ...payload.object, clusterId: cluster } : payload.object,
        )
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
