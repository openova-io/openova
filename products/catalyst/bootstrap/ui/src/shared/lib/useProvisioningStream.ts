/**
 * useProvisioningStream — connects to the catalyst-api SSE endpoint and
 * surfaces real-time provisioning state to the wizard.
 *
 * Wire format (from products/catalyst/bootstrap/api/internal/handler/deployments.go):
 *
 *   POST /api/v1/deployments  → { id, status, streamURL: "/api/v1/deployments/<id>/logs" }
 *   GET  <streamURL>          → SSE stream emitting one of:
 *
 *     data: {"time":"...", "phase":"<id>", "level":"info|warn|error", "message":"..."}\n\n
 *
 *     event: done
 *     data: { ...full Deployment.State() snapshot... }\n\n
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 ("waterfall is the contract") +
 * #2 ("never compromise from quality"), this hook does NOT mock. It opens
 * a real EventSource against the live backend stream, parses every event
 * exactly as the backend serializes it, and exposes the full event log
 * + per-phase status derived from the real stream.
 */

import { useEffect, useState } from 'react'
import {
  ALL_PHASES,
  type BootstrapPhase,
  type PhaseStatus,
  findPhase,
} from '@/shared/constants/bootstrap-phases'

/** Event level — matches catalyst-api's provisioner.Event.Level field. */
export type EventLevel = 'info' | 'warn' | 'error'

/** Single SSE event from the backend, exactly as serialized. */
export interface ProvisioningEvent {
  /** RFC3339 UTC timestamp the backend emitted at. */
  time: string
  /** Phase id — see shared/constants/bootstrap-phases.ts ALL_PHASES. */
  phase: string
  /** Severity. `error` flips that phase's status to `failed`. */
  level: EventLevel
  /** Free-form log line from the underlying tofu/Flux source. */
  message: string
}

/** Snapshot the backend emits in the `done` event — Deployment.State(). */
export interface DeploymentSnapshot {
  id: string
  status: 'provisioning' | 'ready' | 'failed' | string
  startedAt: string
  finishedAt: string | null
  sovereignFQDN: string
  region: string
  error?: string
  result?: {
    sovereignFQDN: string
    controlPlaneIP: string
    loadBalancerIP: string
    consoleURL: string
    gitopsRepoURL: string
  }
}

/** Per-phase derived state — keyed by phase id. */
export interface PhaseState {
  phase: BootstrapPhase
  status: PhaseStatus
  /** Most recent event for this phase (for status-line preview). */
  lastEvent: ProvisioningEvent | null
  /** Number of events received for this phase. */
  eventCount: number
  /** First event timestamp (used to compute duration). */
  startedAt: string | null
  /** Last event timestamp (used to compute duration). */
  endedAt: string | null
}

export type ConnectionStatus =
  | 'connecting'
  | 'streaming'
  | 'completed'
  | 'failed'
  | 'disconnected'

export interface ProvisioningStreamState {
  /** Full chronological event log. */
  events: ProvisioningEvent[]
  /** Per-phase state map keyed by phase.id. */
  phases: Record<string, PhaseState>
  /** Active phase id — last phase to emit a non-error event. */
  activePhase: string | null
  /** First phase that hit an error, if any. */
  failedPhase: string | null
  /** Final snapshot from the `done` event, when the stream completed. */
  snapshot: DeploymentSnapshot | null
  /** SSE connection state. */
  connection: ConnectionStatus
  /** Top-level error message for the whole stream, if any. */
  streamError: string | null
}

/**
 * Initial per-phase state: every phase starts pending with no events.
 */
function emptyPhaseMap(): Record<string, PhaseState> {
  const out: Record<string, PhaseState> = {}
  for (const p of ALL_PHASES) {
    out[p.id] = {
      phase: p,
      status: 'pending',
      lastEvent: null,
      eventCount: 0,
      startedAt: null,
      endedAt: null,
    }
  }
  return out
}

/**
 * Apply a single event to the phase state map and return the new map.
 *
 * Rules:
 * - First event for a phase flips it from `pending` to `running` and
 *   stamps startedAt.
 * - level=error flips that phase to `failed` immediately and freezes
 *   endedAt.
 * - When a new phase starts, any previous `running` phase (with an earlier
 *   place in ALL_PHASES) flips to `done` — backend doesn't emit per-phase
 *   completion events, so we infer it from "phase boundary crossed".
 */
function applyEvent(
  phases: Record<string, PhaseState>,
  ev: ProvisioningEvent,
): Record<string, PhaseState> {
  const known = findPhase(ev.phase)
  // Unknown phase id (e.g. "tofu" generic stdout from streamLines) — record
  // it on whichever opentofu/* phase is currently running, so the user still
  // sees the line. Fall through to the active running phase.
  const targetId = known
    ? ev.phase
    : (Object.keys(phases).find((id) => phases[id]!.status === 'running') ??
       'tofu-apply')

  const next = { ...phases }
  const target = next[targetId]
  if (!target) return phases

  const wasPending = target.status === 'pending'

  // If a NEW phase starts running, mark all earlier-in-order phases that
  // are still `running` as `done` — phase boundary inference.
  if (wasPending) {
    const targetIdx = ALL_PHASES.findIndex((p) => p.id === targetId)
    for (let i = 0; i < targetIdx; i++) {
      const earlierId = ALL_PHASES[i]!.id
      const earlier = next[earlierId]
      if (earlier && earlier.status === 'running') {
        next[earlierId] = { ...earlier, status: 'done', endedAt: ev.time }
      }
    }
  }

  const newStatus: PhaseStatus =
    ev.level === 'error' ? 'failed'
    : target.status === 'failed' ? 'failed'   // sticky once failed
    : 'running'

  next[targetId] = {
    ...target,
    status: newStatus,
    lastEvent: ev,
    eventCount: target.eventCount + 1,
    startedAt: target.startedAt ?? ev.time,
    endedAt: newStatus === 'failed' ? ev.time : target.endedAt,
  }
  return next
}

/**
 * Hook entrypoint. Pass `null` for streamURL while the wizard is still
 * gathering the deployment id; the hook will sit idle until a real URL
 * arrives.
 */
export function useProvisioningStream(streamURL: string | null): ProvisioningStreamState {
  const [events, setEvents] = useState<ProvisioningEvent[]>([])
  const [phases, setPhases] = useState<Record<string, PhaseState>>(emptyPhaseMap)
  const [activePhase, setActivePhase] = useState<string | null>(null)
  const [failedPhase, setFailedPhase] = useState<string | null>(null)
  const [snapshot, setSnapshot] = useState<DeploymentSnapshot | null>(null)
  const [connection, setConnection] = useState<ConnectionStatus>('disconnected')
  const [streamError, setStreamError] = useState<string | null>(null)

  useEffect(() => {
    // No URL yet — wizard is still gathering the deployment id. Defer
    // the state reset to a microtask so React's effect rules don't flag
    // the synchronous setState. The initial state is already 'disconnected',
    // so this only matters when streamURL transitions back to null.
    if (!streamURL) {
      queueMicrotask(() => setConnection('disconnected'))
      return
    }

    queueMicrotask(() => setConnection('connecting'))
    const es = new EventSource(streamURL)

    es.onopen = () => setConnection('streaming')

    es.onmessage = (msg) => {
      try {
        const data = JSON.parse(msg.data) as ProvisioningEvent
        setEvents((prev) => [...prev, data])
        setPhases((prev) => applyEvent(prev, data))
        if (data.level === 'error') {
          setFailedPhase((prev) => prev ?? data.phase)
        } else {
          setActivePhase(data.phase)
        }
      } catch (err) {
        // Malformed JSON — log and continue. We never silently drop events
        // (per Inviolable-Principle #8 disclose every divergence): surface
        // the parse error as a synthetic warning event so the user sees it.
        const synthetic: ProvisioningEvent = {
          time: new Date().toISOString(),
          phase: 'stream',
          level: 'warn',
          message: `[wizard] dropped malformed event: ${String(err)}`,
        }
        setEvents((prev) => [...prev, synthetic])
      }
    }

    // Backend emits the done event with `event: done` — bind explicitly.
    es.addEventListener('done', (msg: MessageEvent) => {
      try {
        const snap = JSON.parse(msg.data) as DeploymentSnapshot
        setSnapshot(snap)
        // Mark every still-running phase as done (the snapshot tells us
        // provisioning succeeded end-to-end if status==='ready').
        if (snap.status === 'ready') {
          setPhases((prev) => {
            const next = { ...prev }
            for (const id of Object.keys(next)) {
              const ph = next[id]!
              if (ph.status === 'running') {
                next[id] = { ...ph, status: 'done', endedAt: snap.finishedAt ?? ph.endedAt }
              }
            }
            return next
          })
          setConnection('completed')
        } else {
          setConnection('failed')
          setStreamError(snap.error ?? `Deployment ended with status=${snap.status}`)
        }
      } catch (err) {
        setStreamError(`Failed to parse final snapshot: ${String(err)}`)
        setConnection('failed')
      }
      es.close()
    })

    es.onerror = () => {
      // EventSource auto-reconnects unless we close. If we already saw a
      // done event we've already closed; otherwise the network dropped.
      // Surface as a non-terminal warning — leave the connection state at
      // `streaming` so the UI doesn't flash to failed on a transient blip.
      // If onmessage doesn't resume in 30s, the user can hit retry from
      // the failure UI.
      if (es.readyState === EventSource.CLOSED) {
        setConnection((prev) => (prev === 'completed' ? 'completed' : 'failed'))
        setStreamError((prev) => prev ?? 'SSE connection closed unexpectedly')
      }
    }

    return () => {
      es.close()
    }
  }, [streamURL])

  return {
    events,
    phases,
    activePhase,
    failedPhase,
    snapshot,
    connection,
    streamError,
  }
}
