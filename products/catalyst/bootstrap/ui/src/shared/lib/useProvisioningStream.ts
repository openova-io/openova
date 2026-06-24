/**
 * useProvisioningStream — connects to the catalyst-api SSE endpoint and
 * surfaces real-time provisioning state to the wizard.
 *
 * Wire format (from products/catalyst/bootstrap/api/internal/handler/deployments.go):
 *
 *   POST /api/v1/deployments  → { id, status, streamURL: "/api/v1/deployments/<id>/logs" }
 *   GET  <streamURL>          → SSE stream emitting one of:
 *
 *     // Phase-0 (OpenTofu) — `phase` is the BootstrapPhase.id directly:
 *     data: {"time":"...", "phase":"tofu-apply", "level":"info|warn|error", "message":"..."}\n\n
 *
 *     // Phase-1 (bootstrap-kit) — `phase` is the constant "component"; the
 *     // real id is in `component` and the lifecycle in `state`:
 *     data: {"time":"...", "phase":"component", "component":"cilium",
 *            "state":"pending|installing|installed|degraded|failed",
 *            "level":"info|warn|error", "message":"... N/Y HRs ready ..."}\n\n
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
import { apiUrl } from '@/shared/config/urls'
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
  /**
   * Phase id. For Phase-0 (OpenTofu) events this is a `BootstrapPhase.id`
   * from shared/constants/bootstrap-phases.ts (`tofu-init`, `tofu-apply`,
   * `flux-bootstrap`, …). For Phase-1 (bootstrap-kit) events the backend
   * sends the constant `"component"` (or `"component-log"`) here and carries
   * the real per-component id in the `component` field below — see
   * helmwatch.PhaseComponent + provisioner.Event in the catalyst-api.
   */
  phase: string
  /** Severity. `error` flips that phase's status to `failed`. */
  level: EventLevel
  /** Free-form log line from the underlying tofu/Flux source. */
  message: string
  /**
   * Normalised component id for Phase-1 `phase:"component"` events
   * (`"bp-cilium"` → `"cilium"` via helmwatch.ComponentIDFromHelmRelease).
   * Empty for Phase-0 OpenTofu events. This — NOT `phase` — is what maps to
   * a bootstrap-kit `BootstrapPhase.id`.
   */
  component?: string
  /**
   * Per-component lifecycle for `phase:"component"` events, one of
   * `pending|installing|installed|degraded|failed`. Empty for Phase-0 events
   * and for `phase:"component-log"` events (which carry an ordinary log
   * line, not a state transition). Drives the bootstrap-kit row's status.
   */
  state?: string
}

/**
 * The backend's Phase-1 events arrive with phase="component" and the real
 * id in the `component` field. A handful of component ids don't match their
 * BootstrapPhase.id 1:1 — chiefly the umbrella, whose HR is
 * `bp-catalyst-platform` → ComponentIDFromHelmRelease strips only the
 * `bp-` prefix → `catalyst-platform`, while the phase id retains the
 * `bp-catalyst-platform` form. Map those aliases here; everything else
 * (cilium, cert-manager, flux, crossplane, sealed-secrets, spire,
 * jetstream, openbao, keycloak, gitea) is identity.
 */
const COMPONENT_ID_TO_PHASE_ID: Record<string, string> = {
  'catalyst-platform': 'bp-catalyst-platform',
}

/**
 * Resolve the bootstrap-kit phase id an event belongs to. For Phase-1
 * `phase:"component"` / `phase:"component-log"` events this reads the
 * `component` field (with the alias map applied); for Phase-0 events it
 * returns the `phase` field unchanged.
 */
export function resolvePhaseId(ev: ProvisioningEvent): string {
  if ((ev.phase === 'component' || ev.phase === 'component-log') && ev.component) {
    const cid = ev.component
    return COMPONENT_ID_TO_PHASE_ID[cid] ?? cid
  }
  return ev.phase
}

/**
 * Translate the backend's per-component lifecycle (`ev.state`) into the
 * widget's PhaseStatus. `installed` → done, `installing`/`pending` →
 * running, `failed`/`degraded` → failed. A missing/unknown state (or a
 * `component-log` line, which carries no state) returns null so the caller
 * falls back to the generic level-based status derivation.
 */
function statusFromComponentState(state: string | undefined): PhaseStatus | null {
  switch (state) {
    case 'installed':
      return 'done'
    case 'installing':
    case 'pending':
      return 'running'
    case 'failed':
    case 'degraded':
      return 'failed'
    default:
      return null
  }
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
 * Exported for unit tests that drive applyEvent directly.
 */
export function emptyPhaseMap(): Record<string, PhaseState> {
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
export function applyEvent(
  phases: Record<string, PhaseState>,
  ev: ProvisioningEvent,
): Record<string, PhaseState> {
  // Phase-1 events carry the real id in `component`; Phase-0 events use
  // `phase` directly. resolvePhaseId hides that asymmetry (#3914 — without
  // it every bootstrap-kit component collapsed into the tofu-apply fallback
  // and the 11-row Phase-1 timeline — the actual ~30-min void — never lit
  // up).
  const resolvedId = resolvePhaseId(ev)
  const known = findPhase(resolvedId)
  // Unknown phase id (e.g. a generic "tofu" stdout line from streamLines, or
  // a component-log line whose component is somehow off-map) — record it on
  // whichever phase is currently running so the user still sees the line.
  // Fall through to the active running phase, else tofu-apply.
  const targetId = known
    ? resolvedId
    : (Object.keys(phases).find((id) => phases[id]!.status === 'running') ??
       'tofu-apply')

  const next = { ...phases }
  const target = next[targetId]
  if (!target) return phases

  const wasPending = target.status === 'pending'

  // If a NEW phase starts running, mark all earlier-in-order phases that
  // are still `running` as `done` — phase boundary inference. Flux installs
  // several bootstrap-kit HRs in parallel, but the backend still emits an
  // explicit `installed` state per component, so this only fills the gap for
  // phases the stream never closes explicitly (chiefly the Phase-0→Phase-1
  // hand-off).
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

  // Status precedence:
  //   1. An explicit per-component lifecycle (`ev.state`, Phase-1) is the
  //      authoritative signal — `installed`→done, `installing`/`pending`→
  //      running, `failed`/`degraded`→failed. It overrides everything,
  //      including a prior terminal status, because the backend genuinely
  //      re-flips an HR (failed → installing → installed on a chart re-pin).
  //   2. Otherwise (no state — e.g. a `component-log` line, or a generic
  //      stdout line) a terminal status is sticky: a trailing log tail must
  //      NOT revive a phase the stream already marked done/failed.
  //   3. Otherwise level=error → failed.
  //   4. Default → running (a log line on an in-flight phase).
  const fromState = statusFromComponentState(ev.state)
  const newStatus: PhaseStatus =
    fromState !== null ? fromState
    : target.status === 'done' || target.status === 'failed' ? target.status // sticky
    : ev.level === 'error' ? 'failed'
    : 'running'

  // A done/failed component freezes endedAt; running/pending leaves it open
  // so the live duration label keeps ticking.
  const terminal = newStatus === 'done' || newStatus === 'failed'

  next[targetId] = {
    ...target,
    status: newStatus,
    lastEvent: ev,
    eventCount: target.eventCount + 1,
    startedAt: target.startedAt ?? ev.time,
    endedAt: terminal ? ev.time : target.endedAt,
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
    // Normalize streamURL through apiUrl. The catalyst-api emits a
    // tier-naive `/api/v1/deployments/<id>/logs` (see
    // api/internal/handler/deployments.go), but when the UI is mounted
    // under `/sovereign/`, the browser must send `/sovereign/api/...`
    // to hit Traefik's strip-sovereign middleware. apiUrl re-roots the
    // path under the active BASE_URL while leaving cross-origin
    // (http/https) URLs untouched. See issue #494.
    const es = new EventSource(apiUrl(streamURL))

    es.onopen = () => setConnection('streaming')

    es.onmessage = (msg) => {
      try {
        const data = JSON.parse(msg.data) as ProvisioningEvent
        setEvents((prev) => [...prev, data])
        setPhases((prev) => applyEvent(prev, data))
        // activePhase / failedPhase must carry the RESOLVED phase id (the
        // bootstrap-kit component id for Phase-1 events), not the raw
        // `"component"` constant — BootstrapProgress highlights the row keyed
        // by this value (#3914).
        const phaseId = resolvePhaseId(data)
        if (data.level === 'error' || data.state === 'failed' || data.state === 'degraded') {
          setFailedPhase((prev) => prev ?? phaseId)
        } else {
          setActivePhase(phaseId)
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
