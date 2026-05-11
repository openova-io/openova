/**
 * flow-bridge — catalyst-ui adapter layer between the openova-flow SSE
 * stream and the @openova/flow-canvas component.
 *
 * # Why this file is the ONLY catalyst-ui-shaped translation layer
 *
 * The OpenovaFlow Foundation refactor split visualisation into the
 * standalone @openova/flow-{core,canvas} packages. Those packages know
 * NOTHING about catalyst-api, deploymentId, region descriptors, or
 * catalyst's CSS-variable palette. This bridge holds the catalyst-
 * specific glue:
 *
 *   • `CATALYST_STATUS_PALETTE` — maps the open-vocabulary status strings
 *     the openova-flow-emitter emits (`pending` / `running` / `succeeded`
 *     / `failed`) onto catalyst's existing `--bubble-*` CSS tokens. Theme
 *     flips (data-theme="light"/"dark") continue to drive the canvas.
 *
 *   • `flowStateToArrays` — converts the {nodes, relationships} Maps
 *     surfaced by `useFlowStream` into the readonly arrays the FlowCanvas
 *     props expect. Map → Array is a single materialisation per render
 *     and is `O(N)`; the Maps live in the hook so envelope merges stay
 *     `O(1)`.
 *
 *   • `regionDescriptorsFromFlow` — derives the FlowCanvas `regions`
 *     prop from the live FlowNode set. Until the wizard store carries
 *     authoritative per-deployment regions on a chroot Sovereign, the
 *     adapter-flux's per-region emissions ARE the source of truth (each
 *     region's adapter tags its FlowNodes with `region: '<location-code>'`,
 *     so a multi-region provision lands two unique region values in the
 *     stream). Falls back to a synthetic single-region descriptor when
 *     no nodes have a region tag.
 *
 * # NOT a Job-shape bridge
 *
 * The earlier `flow-bridge.ts` (PR #1389, reverted by #1394) translated
 * legacy `Job[]` from `/api/v1/deployments/{id}/logs` into FlowNode +
 * Relationship. That bridge is GONE — the new openova-flow-server +
 * per-region adapter-flux DaemonSets emit FlowMessage envelopes
 * directly, so catalyst-ui consumes the contract without going through
 * Catalyst's legacy Job model. (Multi-region needed this anyway: the
 * legacy `/logs` stream is single-cluster only, while
 * `/v1/flows/{id}/stream` merges every cluster's per-HR FlowMessage
 * stream into one.)
 *
 * Architecture canon — docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall): full target-state path — SSE → contract → canvas,
 *      no Job-shape intermediary.
 *   #2 (no compromise): the palette MUST cover every status string the
 *      adapter emits; missing keys would render bubbles as the canvas's
 *      DEFAULT_PALETTE which uses bare hex (no theme flip).
 *   #4 (never hardcode): regions come from the live FlowNode stream,
 *      never from a hardcoded list. The fallback is a generic descriptor
 *      that surfaces "the canvas is rendering but no per-region tags
 *      were seen yet" without fabricating a region id.
 */

import type {
  FlowNode,
  Relationship,
  RegionDescriptor,
  StatusTone,
} from '@openova/flow-core'
import type { FlowStreamState } from './openflow-adapter-sse'

/* ────────────────────────────────────────────────────────────────────
 * Catalyst status palette
 *
 * Maps the open-vocabulary status strings the openova-flow adapter
 * emits onto the catalyst-ui CSS tokens defined in globals.css. The
 * `arrow` colour is concrete hex because SVG marker defs need a
 * resolvable colour at definition time (not a CSS var).
 * ──────────────────────────────────────────────────────────────────── */

export const CATALYST_STATUS_PALETTE: Record<string, StatusTone> = {
  pending: {
    fill: 'var(--bubble-fill-pending)',
    ring: 'var(--bubble-ring-pending)',
    glyph: 'var(--bubble-glyph-pending)',
    glow: 'var(--bubble-glow-pending)',
    edge: 'var(--bubble-edge-pending)',
    arrow: '#94A3B8',
    label: 'Pending',
  },
  running: {
    fill: 'var(--bubble-fill-running)',
    ring: 'var(--bubble-ring-running)',
    glyph: 'var(--bubble-glyph-running)',
    glow: 'var(--bubble-glow-running)',
    edge: 'var(--bubble-edge-running)',
    arrow: '#38BDF8',
    label: 'Running',
  },
  succeeded: {
    fill: 'var(--bubble-fill-succeeded)',
    ring: 'var(--bubble-ring-succeeded)',
    glyph: 'var(--bubble-glyph-succeeded)',
    glow: 'var(--bubble-glow-succeeded)',
    edge: 'var(--bubble-edge-succeeded)',
    arrow: '#16A34A',
    label: 'Succeeded',
  },
  failed: {
    fill: 'var(--bubble-fill-failed)',
    ring: 'var(--bubble-ring-failed)',
    glyph: 'var(--bubble-glyph-failed)',
    glow: 'var(--bubble-glow-failed)',
    edge: 'var(--bubble-edge-failed)',
    arrow: '#B91C1C',
    label: 'Failed',
  },
}

/* ────────────────────────────────────────────────────────────────────
 * Map → Array materialisers
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Surface the FlowStreamState's Maps as the readonly arrays the
 * FlowCanvas component takes. Order matches insertion order — which on
 * the server side matches adapter-flux emission order, so render
 * stability holds across reconnects (the durable buffer replays
 * envelopes in the original sequence).
 */
export function flowStateToArrays(state: FlowStreamState): {
  nodes: FlowNode[]
  relationships: Relationship[]
} {
  return {
    nodes: [...state.nodes.values()],
    relationships: [...state.relationships.values()],
  }
}

/* ────────────────────────────────────────────────────────────────────
 * Region descriptor derivation
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Build `RegionDescriptor[]` for the FlowCanvas `regions` prop.
 *
 *   • Walks the live FlowNode stream once, collects unique `region`
 *     tags (per-region adapter-flux DaemonSets tag every emitted node
 *     with its location code — `fsn1`, `hel1`, etc.).
 *   • Sorts deterministically so the canvas's swimlane layout is
 *     stable across renders.
 *   • Falls back to a single synthetic descriptor when no region tags
 *     were seen yet (single-cluster provision, or stream is still
 *     warming up). The fallback id is empty-string so the canvas's
 *     layout doesn't bucket anything into a wrong region.
 *
 * `wizardRegions` (from `useWizardStore().regions`) is an optional
 * authority — when present on the mother console it supplies the
 * human-readable labels (location + display name). The fallback uses
 * the bare location code as the label.
 */
export function regionDescriptorsFromFlow(
  nodes: ReadonlyMap<string, FlowNode>,
  wizardRegions?: ReadonlyArray<{ id: string; code: string; location: string; name: string }>,
): RegionDescriptor[] {
  const seen = new Set<string>()
  for (const n of nodes.values()) {
    const r = n.region
    if (typeof r === 'string' && r.length > 0) seen.add(r)
  }
  const ids = [...seen].sort()
  if (ids.length === 0 && wizardRegions && wizardRegions.length > 0) {
    return wizardRegions.map((r) => ({
      id: r.id,
      label: `${r.code.toUpperCase()} · ${r.location}`,
      meta: r.name,
    }))
  }
  if (ids.length === 0) {
    return [{ id: '', label: 'Primary Region', meta: 'Single-region cluster' }]
  }
  return ids.map((id) => {
    const fromWizard = wizardRegions?.find((r) => r.id === id || r.code === id)
    if (fromWizard) {
      return {
        id,
        label: `${fromWizard.code.toUpperCase()} · ${fromWizard.location}`,
        meta: fromWizard.name,
      }
    }
    return { id, label: id.toUpperCase(), meta: undefined }
  })
}

/* ────────────────────────────────────────────────────────────────────
 * Status rollup — for the StatusStrip and provisioning banner
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Roll the live FlowNode stream up into a single `ProvisioningStatus`
 * value. Mirrors the legacy FlowPage rollup logic but operates on the
 * new contract: leaves are nodes whose `meta.kind !== 'group'` and
 * which are not parents of any `contains` relationship. The canvas
 * itself computes group-ness from contains-edges, so we replicate that
 * here for the strip.
 */
export type FlowProvisioningStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'

export function rollupFlowStatus(args: {
  nodes: ReadonlyMap<string, FlowNode>
  relationships: ReadonlyMap<string, Relationship>
}): {
  status: FlowProvisioningStatus
  finished: number
  total: number
  earliestStartedMs: number | null
} {
  const { nodes, relationships } = args
  const groupIds = new Set<string>()
  for (const r of relationships.values()) {
    if (r.type === 'contains') groupIds.add(r.toId)
  }
  let finished = 0
  let total = 0
  let earliestStartedMs: number | null = null
  const buckets = new Set<string>()
  for (const n of nodes.values()) {
    if (groupIds.has(n.id)) continue
    total += 1
    buckets.add(n.status)
    if (n.status === 'succeeded' || n.status === 'failed') finished += 1
    if (typeof n.startedAt === 'number' && Number.isFinite(n.startedAt)) {
      if (earliestStartedMs === null || n.startedAt < earliestStartedMs) {
        earliestStartedMs = n.startedAt
      }
    }
  }
  if (total === 0) return { status: 'pending', finished, total, earliestStartedMs }
  if (buckets.has('failed')) {
    const allTerminal = Array.from(buckets).every(
      (s) => s === 'succeeded' || s === 'failed',
    )
    return {
      status: allTerminal ? 'failed' : 'running',
      finished,
      total,
      earliestStartedMs,
    }
  }
  if (buckets.has('running') || buckets.has('pending')) {
    return { status: 'running', finished, total, earliestStartedMs }
  }
  return { status: 'succeeded', finished, total, earliestStartedMs }
}
