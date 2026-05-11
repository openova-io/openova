/**
 * @openova/flow-core — type contract for any backend that wants to
 * drive the OpenovaFlow canvas. The contract is plugin-shaped: a
 * `FlowAdapter` produces `FlowMessage` events; the canvas renders.
 *
 * NO OpenOva-specific values, NO Catalyst-specific shapes — every
 * domain concept (statuses, families, regions, actions) is a
 * descriptor the adapter supplies.
 *
 * Design canon — locked 2026-05-11 ("OpenovaFlow Foundation"):
 *   • A flow is a first-class entity (FlowInstance) with a unique
 *     runtime id and optional templateId (definitionId), parent flow,
 *     and triggered-by chain. This is the "DAG vs DAG-run" distinction
 *     that Argo/Temporal/Flux all need.
 *   • Nodes belong to exactly one flow (FlowNode.flowId).
 *   • Edges (Relationships) are typed using PMI temporal dependency
 *     names — `finish-to-start`, `start-to-start`, `finish-to-finish`,
 *     `start-to-finish`, plus `triggers` (event-based, no temporal
 *     constraint) and `contains` (structural: toId contains fromId).
 *   • `contains` replaces the legacy `parentId` field on the node —
 *     hierarchy is now expressed in the edge graph, not on the node.
 *
 * Per the architecture canon every layer (palette, family, region,
 * status) is supplied by the adapter. No hardcoded enums leak into
 * core; canvas reads what core typed.
 */

import type { ReactNode } from 'react'

/* ────────────────────────────────────────────────────────────────────
 * Flow + Node
 * ──────────────────────────────────────────────────────────────────── */

/**
 * A flow runtime instance.
 *
 *   • `id` — unique within the runtime universe (DAG-run id, workflow
 *           execution id, deploymentId, etc.).
 *   • `definitionId` — optional template id (DAG id, workflow type id).
 *           Two FlowInstances with the same definitionId are two runs
 *           of the same template.
 *   • `parentFlowId` — when this flow was spawned as a child of
 *           another flow (Temporal child workflow, Argo workflow
 *           template-of-templates, nested provisioning step).
 *   • `triggeredBy` — flows whose terminal state triggered this one.
 *           Each entry pairs the triggering flow id with the gating
 *           condition (`success` / `failure` / `always`).
 *   • `status` — open string. The adapter's `statusPalette` keys this
 *           into a visual tone; the canvas never inspects the value.
 *   • `startedAt`/`endedAt` — unix ms. `endedAt` is undefined while
 *           the flow is still running.
 *   • `meta` — adapter-defined opaque payload. Surfaced via
 *           `renderDetail` if the adapter implements it.
 */
export interface FlowInstance {
  id: string
  definitionId?: string
  parentFlowId?: string
  triggeredBy?: Array<{
    flowId: string
    when: 'success' | 'failure' | 'always'
  }>
  status: string
  startedAt: number
  endedAt?: number
  meta?: Record<string, unknown>
}

/**
 * A node inside a flow.
 *
 *   • `id` — unique within the parent `flowId`. Two flows MAY contain
 *           nodes with the same id; consumers always carry the
 *           `(flowId, id)` pair to disambiguate.
 *   • `flowId` — the flow this node belongs to. A node belongs to
 *           exactly one flow; cross-flow relationships are expressed
 *           through `Relationship` with `fromFlowId`/`toFlowId`.
 *   • `label` — display label (no markdown, no HTML).
 *   • `status` — open string, palette-keyed by the adapter.
 *   • `family` — grouping axis: drives the colour band/ring.
 *           Adapter-defined; canvas uses adapter's `families`
 *           descriptor list to map id → colour + display label.
 *   • `region` — grouping axis: drives swimlane / band assignment.
 *           Adapter-defined; canvas uses adapter's `regions`
 *           descriptor list.
 *   • `startedAt`/`endedAt` — unix ms; both optional (a queued node
 *           has neither, a running one has only startedAt).
 *   • `meta` — adapter-defined opaque payload (job appId, pod name,
 *           execution id, etc.) used by `renderDetail`/`actions`.
 */
export interface FlowNode {
  id: string
  flowId: string
  label: string
  status: string
  family?: string
  region?: string
  startedAt?: number
  endedAt?: number
  meta?: Record<string, unknown>
}

/* ────────────────────────────────────────────────────────────────────
 * Relationships
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Edge type vocabulary. PMI temporal dependency taxonomy + two
 * non-temporal kinds: `triggers` (event-driven, no temporal lag) and
 * `contains` (structural / hierarchical).
 *
 *   • `contains` — `toId` (parent) contains `fromId` (child). This is
 *                  the replacement for the legacy `Node.parentId`
 *                  field. It is structural, not temporal; the layout
 *                  treats it as the grouping axis.
 *   • `finish-to-start` (FS) — `toId` starts after `fromId` finishes.
 *                              The PMI default ordering. Counted for
 *                              depth.
 *   • `start-to-start` (SS) — `toId` starts when (or after) `fromId`
 *                             starts. Counted for depth.
 *   • `finish-to-finish` (FF) — `toId` finishes when (or after)
 *                                `fromId` finishes. Counted for depth.
 *   • `start-to-finish` (SF) — `toId` finishes after `fromId` starts.
 *                              Rare; counted for depth.
 *   • `triggers` — `fromId` emits an event that triggers `toId`. No
 *                  temporal constraint beyond the event itself.
 *                  Counted for depth.
 */
export type RelationshipType =
  | 'contains'
  | 'finish-to-start'
  | 'start-to-start'
  | 'finish-to-finish'
  | 'start-to-finish'
  | 'triggers'

/**
 * Convenience predicate: returns true for blocking-DAG relationship
 * types (FS/SS/FF/SF/triggers), false for `contains` (structural).
 *
 * Used by the layout to filter the edge set into "hierarchy" vs
 * "blocking-DAG" graphs without an inline string switch in three
 * places.
 */
export function isBlockingRelationship(t: RelationshipType): boolean {
  return t !== 'contains'
}

/**
 * A directed relationship between two nodes.
 *
 *   • `fromId`/`toId` — node ids.
 *   • `fromFlowId`/`toFlowId` — flow ids; both omitted when the edge
 *           lives entirely inside the parent flow context. The
 *           `toFlowId` is required (and `fromFlowId` if it differs)
 *           when the edge crosses flow boundaries.
 *   • `type` — see {@link RelationshipType}.
 *   • `condition` — gates the edge: `on-success` (default for
 *           FS/SS/FF/SF), `on-failure` (renders as a fail-path edge,
 *           NOT counted for depth), `always` (independent of source
 *           state).
 *   • `lag` — seconds to wait after the trigger point before the
 *           successor starts. `0` is the default.
 */
export interface Relationship {
  fromId: string
  toId: string
  fromFlowId?: string
  toFlowId?: string
  type: RelationshipType
  condition?: 'on-success' | 'on-failure' | 'always'
  lag?: number
}

/* ────────────────────────────────────────────────────────────────────
 * Adapter descriptors (palette, families, regions, actions)
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Status → visual tone. Tone values are CSS-color strings (hex, rgb,
 * hsl, or `var(--token)`). Canvas does NOT add `var(--…)` wrapping;
 * if the adapter wants theme-token resolution it MUST supply tokens
 * itself (e.g. `'var(--bubble-fill-succeeded)'`).
 *
 * The tone vocabulary is fixed at 6 facets so a status descriptor is
 * symmetric across adapters; an adapter that only cares about fill
 * can supply the same value for the other 5.
 */
export interface StatusTone {
  /** Inner circle fill colour. */
  fill: string
  /** Inner ring stroke colour (drawn on the bubble itself). */
  ring: string
  /** Status glyph / icon text colour. */
  glyph: string
  /** Soft halo / glow underlay colour (no-glow = `'transparent'`). */
  glow: string
  /** Outgoing-edge stroke colour. */
  edge: string
  /** Outgoing-edge arrow-head colour (concrete hex — SVG marker
   *  defs need a resolvable colour at definition time, not CSS vars). */
  arrow: string
  /** Human-readable label, used in tooltips / a11y text. */
  label: string
}

/** Family descriptor — colour-coded grouping axis used as the ring. */
export interface FamilyDescriptor {
  id: string
  label: string
  /** CSS colour (hex, rgb, hsl, or `var(--…)`). */
  color: string
}

/** Region descriptor — used to label/swimlane multi-region flows. */
export interface RegionDescriptor {
  id: string
  label: string
  meta?: string
}

/**
 * A custom action the canvas can surface in a per-node context menu /
 * floating tray. Adapters use this for "open in Argo UI", "stream
 * logs", "retry job", etc.
 */
export interface NodeAction {
  id: string
  label: string
  /** Optional icon (any React node — Tabler/Lucide/raw SVG). */
  icon?: ReactNode
  /** Predicate — return false to hide the action for a given node. */
  enabled?: (nodeId: string) => boolean
  /** Invoked when the operator chooses the action. */
  invoke: (nodeId: string) => void | Promise<void>
}

/* ────────────────────────────────────────────────────────────────────
 * Wire protocol — FlowMessage
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Wire messages emitted by an adapter / server. The canvas + host
 * consume the discriminated union and update local state accordingly.
 *
 *   • `snapshot` — full state for a flow. Emitted on subscribe; later
 *                  upserts mutate the local cache.
 *   • `upsert-flow` — flow-level metadata changed (status, endedAt).
 *   • `upsert-nodes` — one or more nodes updated; merge by `id` keyed
 *                      within `flowId`.
 *   • `upsert-rels` — one or more relationships added/updated; merge
 *                     by `(fromId, toId, type)`.
 *   • `delete-nodes` — remove nodes by id (relationships to/from the
 *                      removed nodes are automatically pruned by the
 *                      consumer).
 *   • `delete-rels` — remove specific edges by their natural key.
 *
 * `snapshot` carries the full triple (flow + nodes + relationships)
 * so a re-subscribing client doesn't need a separate fetch path.
 */
export type FlowMessage =
  | { type: 'snapshot'; flow: FlowInstance; nodes: FlowNode[]; relationships: Relationship[] }
  | { type: 'upsert-flow'; flow: FlowInstance }
  | { type: 'upsert-nodes'; nodes: FlowNode[] }
  | { type: 'upsert-rels'; relationships: Relationship[] }
  | { type: 'delete-nodes'; ids: string[] }
  | {
      type: 'delete-rels'
      pairs: Array<{ fromId: string; toId: string; type: RelationshipType }>
    }

/* ────────────────────────────────────────────────────────────────────
 * FlowAdapter — the plugin interface
 * ──────────────────────────────────────────────────────────────────── */

/**
 * The plugin shape any backend implements to drive the canvas.
 *
 *   • `schemaVersion` — current contract version (always 1 for the
 *                       foundation release). Adapter authors bump this
 *                       when emitting messages that violate v1.
 *   • `subscribe` — open a live subscription for a flow. Sink is
 *                   called with one or more FlowMessage values; the
 *                   first should be a `snapshot`. Returns an
 *                   unsubscribe function.
 *   • `fetchSnapshot` — optional one-shot fetch for the initial
 *                       render before any live event arrives. Hosts
 *                       use this for SSR or pre-rendered shells.
 *   • `statusPalette` — status (open string) → StatusTone. Must cover
 *                       every status value the adapter emits.
 *   • `families`/`regions` — descriptors for the grouping axes the
 *                            adapter uses. May be empty.
 *   • `renderDetail` — optional slot for a per-node detail pane (log
 *                      tail, execution panel, kubectl describe).
 *   • `actions` — optional custom action list for the per-node tray.
 */
export interface FlowAdapter {
  schemaVersion: 1
  subscribe(flowId: string, sink: (msg: FlowMessage) => void): () => void
  fetchSnapshot?(flowId: string): Promise<{
    flow: FlowInstance
    nodes: FlowNode[]
    relationships: Relationship[]
  }>
  statusPalette: Record<string, StatusTone>
  families?: FamilyDescriptor[]
  regions?: RegionDescriptor[]
  renderDetail?: (nodeId: string) => ReactNode
  actions?: NodeAction[]
}
