/**
 * openflow-adapter-sse — React hook that consumes the openova-flow-server's
 * SSE stream and surfaces the live FlowInstance + FlowNode[] + Relationship[]
 * triple to the canvas (`@openova/flow-canvas`).
 *
 * Endpoint: `${API_BASE}/v1/flows/{deploymentId}/stream` — proxied by
 * catalyst-api (see products/catalyst/bootstrap/api/internal/handler/
 * openova_flow_proxy.go) to the in-cluster openova-flow-server. The
 * server itself merges per-region adapter-flux emissions into a single
 * FlowMessage stream keyed by `flowId = deploymentId`, so a multi-region
 * provision (fsn1 + hel1) lands as ONE merged graph here.
 *
 * Wire envelopes follow products/openova-flow/CONTRACT.md verbatim:
 *
 *   • `snapshot`     — full state; replaces local cache (idempotent on
 *                       reconnect because the buffer is replayed).
 *   • `upsert-flow`  — flow-level metadata changed.
 *   • `upsert-nodes` — merge by `id` keyed within `flowId`.
 *   • `upsert-rels`  — merge by `(fromId, toId, type)` natural key.
 *   • `delete-nodes` — remove ids; relationships referencing the removed
 *                       nodes are pruned by the consumer.
 *   • `delete-rels`  — remove specific edges by natural key.
 *
 * The hook owns its EventSource lifecycle:
 *
 *   • Opens on mount; closes on unmount or deploymentId change.
 *   • Reconnects with capped exponential backoff on transport errors.
 *     The server's durable buffer replays from the beginning on
 *     reconnect; the consumer is idempotent (upsert + delete are
 *     commutative on the local maps) so replay is safe.
 *   • Falls back to a one-shot GET /v1/flows/{id}/snapshot on first
 *     mount, so deep-links to a completed flow render immediately
 *     instead of waiting for the first SSE frame.
 *
 * Canonical SSE seam in this codebase: `useDeploymentEvents.ts` (line
 * 526+, `openStream` / `onerror` reconnect, capped retry). This adapter
 * mirrors that pattern.
 *
 * Architecture canon — docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall): target-state SSE consumer; no MVP polling fallback.
 *   #2 (no compromise): reducer is identical on snapshot-replay and
 *                       live-tail paths; one update path.
 *   #4 (never hardcode): URL composed from `API_BASE`, deploymentId
 *                        URL-encoded, every backoff/cap an env-tunable
 *                        constant.
 */

import { useEffect, useRef, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'
import type {
  FlowInstance,
  FlowMessage,
  FlowNode,
  Relationship,
  RelationshipType,
} from '@openova/flow-core'

/* ────────────────────────────────────────────────────────────────────
 * Public state shape
 * ──────────────────────────────────────────────────────────────────── */

export type FlowStreamStatus =
  | 'connecting'
  | 'streaming'
  | 'completed'
  | 'failed'
  | 'unreachable'

export interface FlowStreamState {
  /**
   * Current flow envelope, or `null` until the first snapshot lands.
   * Carries definitionId / startedAt / endedAt / status — the canvas
   * uses it for the FlowInstance prop on FlowCanvas.
   */
  flow: FlowInstance | null
  /**
   * Nodes keyed by id. Map order is insertion order (matches the
   * adapter's emission order), which makes the canvas's deterministic
   * Y-rank rendering stable.
   */
  nodes: Map<string, FlowNode>
  /**
   * Relationships keyed by `${fromId}::${toId}::${type}` — the natural
   * key from the contract. Same Map ordering rationale as nodes.
   */
  relationships: Map<string, Relationship>
  /** Current SSE lifecycle state. */
  streamStatus: FlowStreamStatus
  /** Latest server-emitted error, if any. */
  streamError: string | null
}

const EMPTY_STATE: FlowStreamState = {
  flow: null,
  nodes: new Map(),
  relationships: new Map(),
  streamStatus: 'connecting',
  streamError: null,
}

/** Natural key for a relationship — matches the CONTRACT.md merge key. */
function relKey(r: { fromId: string; toId: string; type: RelationshipType }): string {
  return `${r.fromId}::${r.toId}::${r.type}`
}

/* ────────────────────────────────────────────────────────────────────
 * Pure reducer — applies one FlowMessage to a state snapshot.
 *
 * Exported for unit testing in isolation; the hook below wraps it with
 * the React lifecycle + SSE transport.
 * ──────────────────────────────────────────────────────────────────── */

export function reduceFlowMessage(
  state: FlowStreamState,
  msg: FlowMessage,
): FlowStreamState {
  switch (msg.type) {
    case 'snapshot': {
      // `nodes` / `relationships` / `ids` / `pairs` are all OPTIONAL on
      // the wire — the Go server omits empty slices (omitempty) so a
      // legitimate empty-state snapshot arrives as `{"type":"snapshot",
      // "nodes":[]}` with NO `relationships` key at all. Without a
      // nullish coalesce here the reducer blows up with
      // `t.relationships is not iterable` the moment the first frame
      // lands. (Crash observed 2026-05-11 on a snapshot with 2 nodes
      // and 0 relationships — bundle index-CEnQMVBy.js@2285:51356.)
      const nodes = new Map<string, FlowNode>()
      for (const n of (msg.nodes ?? [])) nodes.set(n.id, n)
      const relationships = new Map<string, Relationship>()
      for (const r of (msg.relationships ?? [])) relationships.set(relKey(r), r)
      return {
        flow: msg.flow,
        nodes,
        relationships,
        streamStatus: state.streamStatus,
        streamError: state.streamError,
      }
    }
    case 'upsert-flow': {
      return { ...state, flow: msg.flow }
    }
    case 'upsert-nodes': {
      const nodes = new Map(state.nodes)
      for (const n of (msg.nodes ?? [])) nodes.set(n.id, n)
      return { ...state, nodes }
    }
    case 'upsert-rels': {
      const relationships = new Map(state.relationships)
      for (const r of (msg.relationships ?? [])) relationships.set(relKey(r), r)
      return { ...state, relationships }
    }
    case 'delete-nodes': {
      const toDrop = new Set(msg.ids ?? [])
      const nodes = new Map(state.nodes)
      for (const id of toDrop) nodes.delete(id)
      // Prune relationships pointing to/from the removed nodes — per
      // the contract, the consumer is responsible (the server only
      // emits node-id removals; it does NOT cascade rel deletes).
      const relationships = new Map(state.relationships)
      for (const [k, r] of relationships) {
        if (toDrop.has(r.fromId) || toDrop.has(r.toId)) {
          relationships.delete(k)
        }
      }
      return { ...state, nodes, relationships }
    }
    case 'delete-rels': {
      const relationships = new Map(state.relationships)
      for (const pair of (msg.pairs ?? [])) relationships.delete(relKey(pair))
      return { ...state, relationships }
    }
  }
}

/* ────────────────────────────────────────────────────────────────────
 * useFlowStream — primary hook used by FlowPage.
 *
 * Returns a stable {flow, nodes, relationships, streamStatus,
 * streamError} object that re-renders whenever the SSE adapter pushes
 * a new envelope.
 * ──────────────────────────────────────────────────────────────────── */

export interface UseFlowStreamArgs {
  deploymentId: string
  /**
   * Test seam — when true, skips the EventSource + initial fetch
   * entirely. Used by FlowPage.test.tsx so the renderer stays
   * deterministic without a live transport.
   */
  disableStream?: boolean
}

const MAX_RECONNECT_ATTEMPTS = 5
const BASE_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 8000

export function useFlowStream(args: UseFlowStreamArgs): FlowStreamState {
  const { deploymentId, disableStream = false } = args
  const [state, setState] = useState<FlowStreamState>(() => EMPTY_STATE)
  const reconnectAttemptsRef = useRef<number>(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (disableStream || !deploymentId) {
      // Reset to empty so a remount with a different deploymentId
      // doesn't show the previous flow's bubbles.
      setState(EMPTY_STATE)
      return
    }

    setState(EMPTY_STATE)
    reconnectAttemptsRef.current = 0

    const encodedId = encodeURIComponent(deploymentId)
    const snapshotUrl = `${API_BASE}/v1/flows/${encodedId}/snapshot`
    const streamUrl = `${API_BASE}/v1/flows/${encodedId}/stream`

    let cancelled = false
    let es: EventSource | null = null

    const onMessage = (msg: MessageEvent) => {
      try {
        const env = JSON.parse(msg.data) as FlowMessage
        setState((prev) => reduceFlowMessage(prev, env))
      } catch {
        /* malformed event — drop, the next event will recover. The
         * adapter explicitly emits well-formed FlowMessage envelopes so
         * we surface no error here; a parse failure on a one-off frame
         * is non-fatal. */
      }
    }

    const openStream = (): EventSource => {
      const next = new EventSource(streamUrl)
      next.onopen = () => {
        if (cancelled) return
        reconnectAttemptsRef.current = 0
        setState((prev) => ({ ...prev, streamStatus: 'streaming' }))
      }
      // The server emits NAMED SSE events (`event: snapshot`, `event:
      // upsert-nodes`, etc.) per the OpenovaFlow contract. EventSource's
      // `onmessage` handler ONLY fires for the default-named "message"
      // event, so it never sees these frames. We register explicit
      // addEventListener handlers for every named event the contract
      // defines. The `onmessage` assignment below is retained as a safety
      // net for any future un-named frame the server might emit.
      next.addEventListener('snapshot', onMessage as EventListener)
      next.addEventListener('upsert-flow', onMessage as EventListener)
      next.addEventListener('upsert-nodes', onMessage as EventListener)
      next.addEventListener('upsert-rels', onMessage as EventListener)
      next.addEventListener('delete-nodes', onMessage as EventListener)
      next.addEventListener('delete-rels', onMessage as EventListener)
      // Heartbeats carry no payload — listen but don't reduce.
      next.addEventListener('heartbeat', () => {})
      next.onmessage = onMessage
      next.onerror = () => {
        if (cancelled) return
        if (next.readyState !== EventSource.CLOSED) return
        // Reconnect with capped exponential backoff. The server replays
        // its durable buffer on reconnect; the reducer is idempotent so
        // re-applying already-seen envelopes is safe.
        const attempt = (reconnectAttemptsRef.current += 1)
        if (attempt > MAX_RECONNECT_ATTEMPTS) {
          setState((prev) => ({
            ...prev,
            streamStatus: 'failed',
            streamError:
              prev.streamError ??
              'SSE connection dropped repeatedly — could not maintain a stream to the openova-flow-server',
          }))
          return
        }
        const backoff = Math.min(
          BASE_BACKOFF_MS * 2 ** (attempt - 1),
          MAX_BACKOFF_MS,
        )
        if (reconnectTimerRef.current !== null) {
          clearTimeout(reconnectTimerRef.current)
        }
        reconnectTimerRef.current = setTimeout(() => {
          if (cancelled) return
          es = openStream()
        }, backoff)
      }
      return next
    }

    // Initial GET /snapshot for instant first paint on deep-links to a
    // completed flow. Fire and forget — if the request fails we just
    // wait for the SSE replay. Per CONTRACT.md the snapshot envelope is
    // identical in shape to the equivalent SSE frame, so the same
    // reducer path applies.
    void fetch(snapshotUrl, { headers: { Accept: 'application/json' } })
      .then(async (resp) => {
        if (!resp.ok) return
        if (cancelled) return
        const env = (await resp.json()) as FlowMessage
        setState((prev) => reduceFlowMessage(prev, env))
      })
      .catch(() => {
        /* unreachable on first paint is OK — SSE will fill in shortly */
      })

    es = openStream()

    return () => {
      cancelled = true
      if (reconnectTimerRef.current !== null) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
      es?.close()
      es = null
    }
  }, [deploymentId, disableStream])

  return state
}
