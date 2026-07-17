/**
 * types.ts — wire types for the force-directed Architecture graph
 * widget (P2 of issue openova-io/openova#309).
 *
 * The graph widget is data-shape-agnostic: a higher-level adapter turns
 * the hierarchical infrastructure tree into these neutral
 * GraphNode / GraphEdge shapes, and the GraphCanvas only knows about
 * those.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target shape) — these types are the FINAL contract
 *      between the page-level orchestrator and the canvas.
 *   #4 (never hardcode) — node radius, color and edge colour are all
 *      derived from the type field.
 */

/**
 * Architecture node types — drawn from the hierarchical infrastructure
 * tree. Region / Cluster / vCluster come straight from the spec; the
 * other shapes (LoadBalancer, NodePool, WorkerNode, Network, PVC,
 * Bucket, Volume, Service, Ingress) surface the leaves so the operator
 * can see the whole picture in one canvas.
 *
 * Per #348 item 2: the underlying graph data layer holds every node
 * type AND every relation regardless of which chips are active. The
 * adapter emits a node for every type listed below; chip add/remove is
 * pure visibility filtering on top of the full cache.
 */
export type ArchNodeType =
  | 'Cloud'
  | 'Region'
  | 'Cluster'
  | 'vCluster'
  | 'NodePool'
  | 'WorkerNode'
  | 'LoadBalancer'
  | 'Network'
  | 'PVC'
  | 'Bucket'
  | 'Volume'
  | 'Service'
  | 'Ingress'
  // K8s-side projection (issue #975) — surfaced from the per-Sovereign
  // k8scache.Factory's Indexer via /api/v1/sovereigns/{id}/k8s/stream.
  // Every K8s node carries a `Pod:<ns>/<name>` style composite id; the
  // bridge with the cloud-side `WorkerNode` is a name-or-IP match
  // collapsed at adapter merge time.
  | 'Namespace'
  | 'Pod'
  | 'Deployment'
  | 'StatefulSet'
  | 'DaemonSet'
  | 'ReplicaSet'
  | 'ConfigMap'
  | 'Secret'
  // Reconciler-side projection (#3958 unified Cloud-graph). The
  // declarative reconcilers from the /reconciliation endpoint merge into
  // the SAME canvas as typed nodes. Flux (HelmRelease / Kustomization)
  // carry real spec.dependsOn edges; the rest are edgeless declarative
  // reconcilers (cert-manager / CNPG / External-Secrets / the Catalyst
  // control-plane CRs). The category (shape), family (border colour) and
  // status (fill) for every one of these is derived in the three maps
  // below — never hardcoded at a call site.
  | 'HelmRelease'
  | 'Kustomization'
  | 'Certificate'
  | 'ExternalSecret'
  | 'Application'
  | 'Environment'
  | 'Organization'
  | 'Continuum'
  | 'UserAccess'
  | 'Gateway'
  | 'HTTPRoute'
  | 'NetworkPolicy'
  | 'CiliumNetworkPolicy'
  | 'Database'

/**
 * Canonical ordered list of every type the data layer + chip strip
 * knows about. Single source of truth — referenced by adapter,
 * page-level orchestrator, legend, type-bar, tests. No hardcoded
 * lists anywhere else.
 */
export const ALL_NODE_TYPES: ArchNodeType[] = [
  'Cloud',
  'Region',
  'Cluster',
  'vCluster',
  'NodePool',
  'WorkerNode',
  'LoadBalancer',
  'Network',
  'PVC',
  'Bucket',
  'Volume',
  'Service',
  'Ingress',
  // K8s-side
  'Namespace',
  'Pod',
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'ReplicaSet',
  'ConfigMap',
  'Secret',
  // Reconciler-side (#3958)
  'HelmRelease',
  'Kustomization',
  'Certificate',
  'ExternalSecret',
  'Application',
  'Environment',
  'Organization',
  'Continuum',
  'UserAccess',
  'Gateway',
  'HTTPRoute',
  'NetworkPolicy',
  'CiliumNetworkPolicy',
  'Database',
]

/**
 * Default-off node types — high-cardinality kinds whose chips start
 * unchecked. Operators can enable them at any time. Today: Pod and
 * ConfigMap, which can balloon past 200+ nodes on a healthy Sovereign
 * and crowd the canvas before any signal emerges.
 */
export const DEFAULT_INACTIVE_TYPES: ReadonlySet<ArchNodeType> = new Set([
  'Pod',
  'ConfigMap',
  // #3958 — ReplicaSet and Secret are equally high-cardinality (every
  // Deployment owns ≥1 ReplicaSet; every chart ships a fistful of
  // Secrets). Start them off so the unified canvas doesn't open with
  // 400+ leaf bubbles before the operator opts in.
  'ReplicaSet',
  'Secret',
])

/**
 * Edge relationship types. Containment is just one of these — the
 * founder spec verbatim: "forget about the containment, just show it
 * as another type of relation."
 *
 * Per #348 item 4: each relation has an ArchiMate-derived rendering
 * (composition, aggregation, assignment, triggering, flow, used-by,
 * realization, association). See EDGE_MARKER_END / EDGE_DASHED below.
 */
export type ArchEdgeType =
  | 'contains'
  | 'member-of'
  | 'runs-on'
  | 'routes-to'
  | 'triggers'
  | 'flows-to'
  | 'attached-to'
  | 'depends-on'
  | 'used-by'
  | 'realizes'
  | 'peers-with'
  | 'associates'

export const ALL_EDGE_TYPES: ArchEdgeType[] = [
  'contains',
  'member-of',
  'runs-on',
  'routes-to',
  'triggers',
  'flows-to',
  'attached-to',
  'depends-on',
  'used-by',
  'realizes',
  'peers-with',
  'associates',
]

/**
 * Auto-100% density threshold (#348 item 1). Any node type with
 * total < SMALL_TYPE_THRESHOLD is unaffected by the global density
 * slider; always rendered fully unless its chip is explicitly
 * disabled. Per-type density popover for small types is hidden — only
 * the visibility toggle remains.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), this constant
 * is the single source of truth referenced by both the page-level
 * orchestrator and the test suite.
 */
export const SMALL_TYPE_THRESHOLD = 20

/**
 * Node status — the FILL channel of the unified Cloud-graph (#3958).
 * Five buckets, NOT one-per-vocabulary-word: the cloud/k8s side speaks
 * healthy/degraded/failed/unknown; the reconciler side speaks
 * Reconciled/Reconciling/Drifted/Degraded/Suspended. Both collapse onto
 * the same five FILL colours via STATUS_FILL below.
 *
 *   green  = Reconciled / Healthy        → 'healthy'
 *   blue   = Reconciling / Progressing   → 'reconciling'
 *   yellow = Drifted / Warning           → 'drifted'
 *   red    = Degraded / Failed           → 'failed'
 *   grey   = Suspended / Unknown         → 'unknown'
 */
export type ArchStatus =
  | 'healthy'
  | 'reconciling'
  | 'drifted'
  | 'degraded'
  | 'failed'
  | 'suspended'
  | 'unknown'

/**
 * FILL (inside colour) = status. The single source of truth for the
 * first of the three independent visual channels (#3958). `degraded`
 * folds onto the same red as `failed` (the design's red bucket is
 * "Degraded/Failed"); `suspended` folds onto the same grey as `unknown`.
 */
export const STATUS_FILL: Record<ArchStatus, string> = {
  healthy: '#16a34a', // green  — Reconciled / Healthy
  reconciling: '#2563eb', // blue   — Reconciling / Progressing
  drifted: '#d4a017', // yellow — Drifted / Warning
  degraded: '#dc2626', // red    — Degraded
  failed: '#dc2626', // red    — Failed
  suspended: '#6b7280', // grey   — Suspended
  unknown: '#6b7280', // grey   — Unknown
}

/**
 * SHAPE = coarse category. The second independent visual channel.
 * Six categories, each rendered as a distinct polygon by GraphCanvas
 * (NO icons). The mapping below is the single source of truth.
 */
export type NodeCategory =
  | 'compute' // ● Circle    — Compute / Workload
  | 'control' // ■ Square    — Control / Reconciler
  | 'network' // ▲ Triangle  — Networking
  | 'config' // ◆ Diamond   — Config / Identity / Secret
  | 'data' // ⬢ Hexagon   — Data / Storage
  | 'scope' // ⬠ Pentagon  — Scope / Container

export const ALL_CATEGORIES: NodeCategory[] = [
  'compute',
  'control',
  'network',
  'config',
  'data',
  'scope',
]

/** Human-readable category labels for the legend. */
export const CATEGORY_LABEL: Record<NodeCategory, string> = {
  compute: 'Compute / Workload',
  control: 'Control / Reconciler',
  network: 'Networking',
  config: 'Config / Identity / Secret',
  data: 'Data / Storage',
  scope: 'Scope / Container',
}

/**
 * Per-type → category (shape). Every ArchNodeType MUST appear here, or
 * the canvas falls back to a circle. Exhaustive Record keeps the compiler
 * honest: a new ArchNodeType added above forces a row here.
 */
export const NODE_CATEGORY: Record<ArchNodeType, NodeCategory> = {
  // ● Compute / Workload
  Pod: 'compute',
  Deployment: 'compute',
  StatefulSet: 'compute',
  DaemonSet: 'compute',
  ReplicaSet: 'compute',
  WorkerNode: 'compute',
  NodePool: 'compute',
  // ■ Control / Reconciler
  HelmRelease: 'control',
  Kustomization: 'control',
  Application: 'control',
  Environment: 'control',
  Organization: 'control',
  Continuum: 'control',
  // ▲ Networking
  Service: 'network',
  Ingress: 'network',
  LoadBalancer: 'network',
  Gateway: 'network',
  HTTPRoute: 'network',
  Network: 'network',
  NetworkPolicy: 'network',
  CiliumNetworkPolicy: 'network',
  // ◆ Config / Identity / Secret
  ConfigMap: 'config',
  Secret: 'config',
  ExternalSecret: 'config',
  Certificate: 'config',
  UserAccess: 'config',
  // ⬢ Data / Storage
  Database: 'data',
  PVC: 'data',
  Volume: 'data',
  Bucket: 'data',
  // ⬠ Scope / Container
  Cloud: 'scope',
  Region: 'scope',
  Cluster: 'scope',
  vCluster: 'scope',
  Namespace: 'scope',
}

/**
 * BORDER colour = family. The third independent visual channel. Family
 * is derived from the controlling operator / API group. Every
 * ArchNodeType maps to exactly one family (the family it most-belongs
 * to as a resource); reconciler nodes can ALSO override per-node from
 * the live apiVersion (see familyForApiGroup).
 */
export type NodeFamily =
  | 'flux' // helm.toolkit / kustomize.toolkit  — teal
  | 'crossplane' // *.crossplane.io                   — purple
  | 'certManager' // cert-manager.io                   — amber
  | 'cnpg' // postgresql.cnpg.io                — indigo
  | 'externalSecrets' // external-secrets.io               — pink
  | 'cilium' // cilium / network                  — cyan
  | 'coreK8s' // apps / core / batch               — slate
  | 'catalyst' // *.openova.io                      — brand-green
  | 'cloud' // hcloud / huawei                   — orange

export const ALL_FAMILIES: NodeFamily[] = [
  'flux',
  'crossplane',
  'certManager',
  'cnpg',
  'externalSecrets',
  'cilium',
  'coreK8s',
  'catalyst',
  'cloud',
]

export const FAMILY_LABEL: Record<NodeFamily, string> = {
  flux: 'Flux',
  crossplane: 'Crossplane',
  certManager: 'cert-manager',
  cnpg: 'CNPG',
  externalSecrets: 'External-Secrets',
  cilium: 'Cilium / net',
  coreK8s: 'core-k8s',
  catalyst: 'Catalyst CRs',
  cloud: 'Cloud provider',
}

export const FAMILY_BORDER: Record<NodeFamily, string> = {
  flux: '#0d9488', // teal
  crossplane: '#7c3aed', // purple
  certManager: '#d97706', // amber
  cnpg: '#4338ca', // indigo
  externalSecrets: '#db2777', // pink
  cilium: '#0891b2', // cyan
  coreK8s: '#64748b', // slate
  catalyst: '#15803d', // brand-green
  cloud: '#ea580c', // orange
}

/**
 * Per-type → family (border colour). Reconciler nodes carry their real
 * apiVersion at runtime, but a static default per type keeps the cloud/
 * k8s side correct and gives every node a family even when the wire
 * payload omits the group.
 */
export const NODE_FAMILY: Record<ArchNodeType, NodeFamily> = {
  // Flux
  HelmRelease: 'flux',
  Kustomization: 'flux',
  // cert-manager
  Certificate: 'certManager',
  // CNPG
  Database: 'cnpg',
  // External-Secrets
  ExternalSecret: 'externalSecrets',
  // Cilium / networking
  Network: 'cilium',
  NetworkPolicy: 'cilium',
  CiliumNetworkPolicy: 'cilium',
  Gateway: 'cilium',
  HTTPRoute: 'cilium',
  // Catalyst control-plane CRs (*.openova.io)
  Application: 'catalyst',
  Environment: 'catalyst',
  Organization: 'catalyst',
  Continuum: 'catalyst',
  UserAccess: 'catalyst',
  vCluster: 'catalyst',
  // Cloud provider (hcloud / huawei)
  Cloud: 'cloud',
  Region: 'cloud',
  Cluster: 'cloud',
  NodePool: 'cloud',
  WorkerNode: 'cloud',
  LoadBalancer: 'cloud',
  Bucket: 'cloud',
  Volume: 'cloud',
  // core-k8s (apps / core / batch)
  Pod: 'coreK8s',
  Deployment: 'coreK8s',
  StatefulSet: 'coreK8s',
  DaemonSet: 'coreK8s',
  ReplicaSet: 'coreK8s',
  ConfigMap: 'coreK8s',
  Secret: 'coreK8s',
  Service: 'coreK8s',
  Ingress: 'coreK8s',
  Namespace: 'coreK8s',
  PVC: 'coreK8s',
}

/**
 * familyForApiGroup — derive the BORDER family from a live apiVersion /
 * API group string (e.g. "helm.toolkit.fluxcd.io/v2", "cert-manager.io",
 * "postgresql.cnpg.io"). Returns undefined when the group is unknown so
 * the caller can fall back to NODE_FAMILY[type]. This lets a reconciler
 * node colour its border from the actual controlling operator rather
 * than a per-type guess.
 */
export function familyForApiGroup(group: string | undefined): NodeFamily | undefined {
  if (!group) return undefined
  const g = group.toLowerCase()
  if (g.includes('helm.toolkit') || g.includes('kustomize.toolkit') || g.includes('source.toolkit') || g.includes('fluxcd.io')) {
    return 'flux'
  }
  if (g.includes('crossplane.io')) return 'crossplane'
  if (g.includes('cert-manager.io')) return 'certManager'
  if (g.includes('postgresql.cnpg.io') || g.includes('cnpg.io')) return 'cnpg'
  if (g.includes('external-secrets.io')) return 'externalSecrets'
  if (g.includes('cilium.io')) return 'cilium'
  if (g.includes('openova.io')) return 'catalyst'
  if (g.includes('hcloud') || g.includes('huawei') || g.includes('hetzner')) return 'cloud'
  if (g === 'apps' || g === 'batch' || g === 'v1' || g === 'core' || g.includes('networking.k8s.io') || g.includes('gateway.networking.k8s.io')) {
    return 'coreK8s'
  }
  return undefined
}

/** A node on the graph canvas — composite id, type-tagged, with status. */
export interface GraphNode {
  /** Composite id: `${type}:${elementId}`. Stable across renders. */
  id: string
  type: ArchNodeType
  label: string
  /** Optional one-line subtext (e.g. SKU, IP). */
  sublabel?: string
  status?: ArchStatus
  /** Free-form per-type metadata shown in the detail panel. */
  metadata?: Record<string, string>
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  type: ArchEdgeType
}

/* ── Canvas runtime shapes ───────────────────────────────────────── */

/**
 * Internal D3-force-augmented node (mutable x/y/fx/fy). The canvas
 * keeps these in a Map keyed by id so positions persist across
 * prop-driven re-renders. Exported because the page tests inspect
 * the same shape.
 */
export interface LiveNode extends GraphNode {
  x: number
  y: number
  vx: number
  vy: number
  /** Pinned x — set by drag-to-pin. null = unpinned (D3 will move it). */
  fx: number | null
  fy: number | null
  /** Cached degree (incoming + outgoing edges). */
  degree: number
}

/**
 * Internal edge — D3-force mutates `source` / `target` from string ids
 * to LiveNode references after the first simulation tick. Use the
 * `edgeNodeId()` helper everywhere you read these fields. We omit
 * source/target from the GraphEdge structural extension because their
 * runtime shape is `string | LiveNode` (string only on the very first
 * tick; node ref afterward).
 */
export interface LiveEdge extends Omit<GraphEdge, 'source' | 'target'> {
  source: string | LiveNode
  target: string | LiveNode
}

/**
 * D3-force-link mutates link.source / link.target from their initial
 * string id values to node-object references after the first tick. Any
 * code that reads either field MUST go through this helper to support
 * both shapes.
 *
 * Critical: the canonical bug pattern (and the reason this helper exists
 * as a single export) is `link.source === id` — that comparison is true
 * pre-tick and false post-tick. Always read via `edgeNodeId(link.source)`.
 */
export function edgeNodeId(v: string | { id: string }): string {
  return typeof v === 'object' ? v.id : v
}

/* ── Visual mapping ──────────────────────────────────────────────── */

/**
 * Per-type color palette. Each type is visually distinct on the
 * canvas. Per INVIOLABLE-PRINCIPLES #4 (never hardcode visible-only
 * tokens): these map onto the project's CSS variables where one
 * exists, and fall back to literal hex for the type-distinctive
 * accents the palette lacks.
 */
export const NODE_FILL: Record<ArchNodeType, string> = {
  Cloud: '#7048e8', // violet — provider tenant anchor
  Region: '#1c7ed6', // blue
  Cluster: '#0ca678', // teal — control-plane group
  vCluster: '#37b24d', // green — isolation scope
  NodePool: '#f59f00', // amber
  WorkerNode: '#fab005', // yellow
  LoadBalancer: '#e8590c', // orange
  Network: '#868e96', // grey
  PVC: '#9775fa', // light violet — persistent volume claim
  Bucket: '#5c7cfa', // indigo — object storage
  Volume: '#22b8cf', // cyan — block storage
  Service: '#20c997', // mint — k8s service
  Ingress: '#e64980', // pink — k8s ingress
  // K8s-side projection
  Namespace: '#495057', // dark grey — logical grouping
  Pod: '#74c0fc', // light blue — leaf workload
  Deployment: '#4dabf7', // sky blue — workload owner
  StatefulSet: '#6741d9', // deep violet — stateful workload owner
  DaemonSet: '#9c36b5', // magenta — per-node workload owner
  ReplicaSet: '#3b5bdb', // indigo-blue — replicaset owner
  ConfigMap: '#adb5bd', // light grey — config payload
  Secret: '#f76707', // burnt orange — secret payload
  // Reconciler-side projection (#3958). The chip dot uses this; the
  // canvas itself colours border-by-family + fill-by-status.
  HelmRelease: '#0d9488', // teal — Flux
  Kustomization: '#0ca678', // green-teal — Flux
  Certificate: '#d97706', // amber — cert-manager
  ExternalSecret: '#db2777', // pink — external-secrets
  Application: '#15803d', // brand-green — Catalyst CR
  Environment: '#2f9e44', // green — Catalyst CR
  Organization: '#37b24d', // light green — Catalyst CR
  Continuum: '#66a80f', // olive — Catalyst CR
  UserAccess: '#5c940d', // dark olive — Catalyst CR
  Gateway: '#0891b2', // cyan — networking
  HTTPRoute: '#1098ad', // teal-cyan — networking
  NetworkPolicy: '#15aabf', // bright cyan — networking
  CiliumNetworkPolicy: '#0c8599', // deep cyan — Cilium L3-L7 micro-seg (#5129)
  Database: '#4338ca', // indigo — CNPG
}

export const EDGE_STROKE: Record<ArchEdgeType, string> = {
  contains: '#4c6ef5', // solid blue
  'member-of': '#7950f2', // solid violet — aggregation
  'runs-on': '#15aabf', // solid cyan — assignment
  'routes-to': '#fa5252', // solid red — triggering
  triggers: '#fa5252', // solid red — triggering
  'flows-to': '#fd7e14', // dashed orange — flow
  'attached-to': '#868e96', // dashed grey
  'depends-on': '#fd7e14', // dashed orange — used-by
  'used-by': '#fd7e14', // dashed orange — used-by
  realizes: '#15aabf', // dashed cyan — realization
  'peers-with': '#7950f2', // dashed violet — association
  associates: '#868e96', // solid grey — association (plain)
}

/**
 * Edges that render dashed instead of solid (#348 item 4 — ArchiMate
 * notation). Used-by, flow, realization and association-with-direction
 * are rendered dashed; composition / aggregation / assignment /
 * triggering are solid.
 */
export const EDGE_DASHED: Record<ArchEdgeType, boolean> = {
  contains: false,
  'member-of': false,
  'runs-on': false,
  'routes-to': false,
  triggers: false,
  'flows-to': true,
  'attached-to': true,
  'depends-on': true,
  'used-by': true,
  realizes: true,
  'peers-with': false,
  associates: false,
}

/**
 * ArchiMate marker kinds. The renderer emits one SVG <marker> per
 * value here; the per-edge mapping below picks which sits at each end.
 *
 * Mapping rules per #348 item 4:
 *   • contains          → composition: filled diamond at PARENT (source) end
 *   • member-of         → aggregation: hollow diamond at PARENT (target) end
 *   • runs-on           → assignment: filled circles at BOTH ends
 *   • routes-to/triggers→ triggering: filled triangle at TARGET
 *   • flows-to          → flow: filled triangle at TARGET, dashed line
 *   • depends-on/used-by→ used-by: open triangle at TARGET, dashed line
 *   • realizes          → realization: hollow triangle at TARGET, dashed
 *   • peers-with/associates → association: plain (no markers)
 *   • attached-to       → association-attached: small open circle at TARGET, dashed
 */
export type EdgeMarker =
  | 'composition'
  | 'aggregation'
  | 'assignment-dot'
  | 'triggering'
  | 'used-by'
  | 'realization'
  | 'attached'
  | null

export const EDGE_MARKER_START: Record<ArchEdgeType, EdgeMarker> = {
  contains: 'composition', // filled diamond at parent (source) end
  'member-of': null,
  'runs-on': 'assignment-dot',
  'routes-to': null,
  triggers: null,
  'flows-to': null,
  'attached-to': null,
  'depends-on': null,
  'used-by': null,
  realizes: null,
  'peers-with': null,
  associates: null,
}

export const EDGE_MARKER_END: Record<ArchEdgeType, EdgeMarker> = {
  contains: null,
  'member-of': 'aggregation', // hollow diamond at parent (target) end
  'runs-on': 'assignment-dot',
  'routes-to': 'triggering',
  triggers: 'triggering',
  'flows-to': 'triggering',
  'attached-to': 'attached',
  'depends-on': 'used-by',
  'used-by': 'used-by',
  realizes: 'realization',
  'peers-with': null,
  associates: null,
}
