/**
 * useDeploymentEvents — React hook that drives the Sovereign Admin shell
 * (AdminPage + ApplicationPage) from the catalyst-api event channel.
 *
 * Two sources of truth, same reducer (`eventReducer.ts`):
 *
 *   1. GET /api/v1/deployments/{id}/events — returns the buffered slice
 *      of every event the deployment has ever emitted, plus a snapshot
 *      of the deployment's terminal state (status: ready / failed /
 *      provisioning). Always called first on mount so deep-links to a
 *      completed deployment render the full history without waiting
 *      for SSE.
 *   2. SSE /api/v1/deployments/{id}/logs — live event channel. Skips the
 *      first N events (where N is the count we already replayed) so the
 *      reducer is never double-applied.
 *
 * The hook returns:
 *   • state         — the current ReducerState (per-Application + phase
 *                     banner status maps + per-target event log).
 *   • snapshot      — the terminal deployment-state object (`result`,
 *                     `sovereignFQDN`, …). Null until SSE/SET resolves.
 *   • streamStatus  — connecting / streaming / completed / failed /
 *                     unreachable. Drives the top-bar pill.
 *   • streamError   — server-emitted failure message, if any.
 *   • startedAt /   — anchor timestamps for the elapsed clock.
 *     finishedAt
 *   • retry         — increment to re-open the stream.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URLs are
 * built from `API_BASE` (which itself derives from Vite's `BASE_URL`),
 * never inlined. This is the same source-of-truth the legacy
 * ProvisionPage used; switching the basepath in vite.config flows
 * through automatically.
 *
 * Per #2 (never compromise), the reducer is the SAME on the GET-replay
 * path and the SSE-live path. There is no "MVP" branch where one path
 * does less than the other.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'
import {
  buildInitialState,
  reduceEvents,
  markAllReady,
  markFailedTerminal,
  type DeploymentEvent,
  type ReducerState,
} from './eventReducer'

export type StreamStatus = 'connecting' | 'streaming' | 'completed' | 'failed' | 'unreachable'

/**
 * In-flight backend statuses (issue #782). The deployment record carries
 * one of these values while provisioning is still progressing. The SSE
 * stream closing while the canonical record is in any of these states
 * means the transport dropped, NOT that the deployment failed: the
 * wizard MUST keep showing a "still running" UI and reconnect rather
 * than render a "Provisioning failed" banner with the stale phase id.
 *
 * Mirrors api/internal/handler/deployments.go isInFlightStatus().
 */
const IN_FLIGHT_STATUSES: ReadonlySet<string> = new Set([
  'pending',
  'provisioning',
  'tofu-applying',
  'flux-bootstrapping',
  'phase1-watching',
])

/**
 * #3611 — per-region HelmRelease health, mirroring the Go `RegionHealth`
 * struct in api/internal/provisioner/region_health.go. One entry per
 * region observed in a multi-region deployment.
 */
export interface RegionHealth {
  /** Region key — declared primary region (e.g. "me-east-215-a") or the
   * cloud-init ?region= key a secondary control-plane PUT under. */
  region: string
  /** Exactly one entry is the primary (drives the deployment "ready"). */
  primary: boolean
  /** bp-* HelmReleases observed in the terminal "installed" state. */
  hrReady: number
  /** bp-* HelmReleases observed in this region. */
  hrTotal: number
  /** Secondary-only: significantly short of the primary's Ready count. */
  degraded: boolean
}

export interface DeploymentSnapshot {
  id?: string
  status?: 'pending' | 'provisioning' | 'ready' | 'failed' | string
  startedAt?: string
  finishedAt?: string | null
  sovereignFQDN?: string
  region?: string
  error?: string
  numEvents?: number
  /**
   * C8-001 (2026-05-17 t143) — Sovereign-provisioning request fields
   * lifted to the snapshot so the chroot's `/sovereign/settings` page
   * works without a populated wizard store (chroot localStorage is
   * fresh post-handover, so reading Capacity / Pool subdomain / BYO
   * domain from `useWizardStore()` rendered four em-dashes). The
   * catalyst-api's `Deployment.State()` surfaces these from the
   * persisted RedactedRequest projection; the SettingsPage reads
   * snapshot-first with the wizard store as fallback.
   */
  controlPlaneSize?: string
  regionControlPlaneSizes?: string[]
  sovereignPoolDomain?: string
  sovereignSubdomain?: string
  sovereignDomainMode?: string
  /** Present only when domainMode === 'byo'. */
  sovereignByoDomain?: string
  orgName?: string
  orgEmail?: string
  /**
   * Phase-1 helmwatch ground-truth — populated by the catalyst-api when
   * its HelmRelease informer terminated. Lifted to the top level by
   * deployments.go so the UI can read it without unwrapping `result`.
   * Keys are bare slugs ("cilium", "catalyst-platform"), values are
   * helmwatch states ("pending"/"installing"/"installed"/"degraded"/
   * "failed"). Empty/missing means the watch was skipped or could not
   * start; the eventReducer's `markAllReady` flips `phase1WatchSkipped`
   * in that case.
   */
  componentStates?: Record<string, string>
  /** UTC timestamp the helmwatch loop terminated (RFC3339). */
  phase1FinishedAt?: string
  /**
   * Helmwatch's terminal classification — empty, "ready", "failed",
   * "timeout", "flux-not-reconciling", "kubeconfig-missing",
   * "watcher-start-failed". The presence of any non-empty value implies
   * `runPhase1Watch` was reached, which in turn implies Phase 0
   * (Hetzner provision + cloud-init kubeconfig PUT) succeeded. Issue
   * #519 — this is the durable signal the wizard uses to converge the
   * Phase-0 banner when streaming events lost the `tofu-output` line in
   * a producer-buffer overflow on the high-throughput tofu-apply burst.
   */
  phase1Outcome?: string
  /**
   * Issues #764 + #768 — fully-qualified handover redirect URL stamped
   * by catalyst-api when the Phase-1 watch terminates with
   * OutcomeReady. Shape:
   *
   *   https://console.<sovereignFqdn>/auth/handover?token=<jwt>
   *
   * The token is RS256-signed (5-minute TTL); the Sovereign-side
   * /auth/handover handler validates it, mints a session, and 302s to
   * /console/dashboard. Empty until catalyst-api auto-fires the
   * mint; non-empty value triggers the wizard's "Open your Sovereign
   * console →" button + the 5-second auto-redirect timer.
   */
  handoverURL?: string
  /**
   * Issues #764 + #768 — RFC 3339 UTC timestamp the catalyst-api
   * auto-fired the handover JWT mint. Used by the wizard's
   * notification effect to render the "Sovereign is ready —
   * redirecting…" toast exactly once per deployment, even on a
   * poll-after-SSE-reconnect.
   */
  handoverFiredAt?: string
  /**
   * #3611 — per-region HelmRelease health census. For a multi-region
   * deployment this is the only honest read of how each region actually
   * converged: `componentStates` above collapses every region together,
   * and `status: "ready"` is driven solely by the PRIMARY region's
   * Phase-1 watch. A degraded secondary (hw145: region-a 60/63 HRs,
   * region-b 48/63) would otherwise hide behind a bare green "ready" —
   * this array surfaces "region-b 48/63, degraded" instead. Primary
   * first, secondaries sorted by region key. Absent for single-region
   * provs. Lifted to the top level by deployments.go (mirror of the Go
   * `RegionHealth` struct in api/internal/provisioner/region_health.go).
   */
  regions?: RegionHealth[]
  /**
   * #3611 — surface-only roll-up of `regions`: true when any secondary
   * region is significantly behind the primary's Ready-HR count. NOT a
   * gate (catalyst-api keeps flipping `status: "ready"` off the primary
   * watcher alone so a slow secondary never hangs the prov); it exists so
   * the console can badge a "ready" deployment as "secondary degraded".
   * Absent/false for single-region and fully-converged provs.
   */
  secondaryDegraded?: boolean
  result?: {
    sovereignFQDN: string
    controlPlaneIP: string
    loadBalancerIP: string
    consoleURL: string
    gitopsRepoURL: string
    /** Same map as the top-level `componentStates`; either may be present. */
    componentStates?: Record<string, string>
    /** Same as top-level `phase1FinishedAt`. */
    phase1FinishedAt?: string
    /** Same as top-level `phase1Outcome`. */
    phase1Outcome?: string
    /** Same as top-level `handoverURL`. */
    handoverURL?: string
    /** Same as top-level `handoverFiredAt`. */
    handoverFiredAt?: string
  }
}

/**
 * Payload of the `handover-ready` typed SSE event emitted by
 * catalyst-api/internal/handler.fireHandover (issues #764 + #768).
 *
 * Wire shape:
 *
 *   event: handover-ready
 *   data: {"handoverURL": "...", "expiresAt": "..."}
 *
 * The wizard's useDeploymentEvents hook listens via
 * EventSource.addEventListener('handover-ready', …) so the typed
 * channel is independent of the default-message reducer. Receiving
 * this event is the live-stream signal to render the "Open your
 * Sovereign console →" button + start the 5s auto-redirect timer.
 *
 * The same data is durable on the deployment record at
 * /deployments/{id} (top-level handoverURL + handoverFiredAt) so a
 * page that lands AFTER the event was emitted still picks up the
 * redirect via the GET-replay path.
 */
export interface HandoverReadyEvent {
  /** Same shape as DeploymentSnapshot.handoverURL. */
  handoverURL: string
  /** RFC 3339 UTC expiry of the JWT (mint-time + 5 minutes). */
  expiresAt: string
}

export interface UseDeploymentEventsOptions {
  /** Stable deployment id from the URL parameter. */
  deploymentId: string
  /** Application ids the page expects to render — bootstrap-kit + selected. */
  applicationIds: readonly string[]
  /**
   * Test seam — disables the EventSource attach. The GET /events fetch
   * still runs (jsdom can fetch via mocked global). Mirrors the same
   * flag the legacy ProvisionPage exposed.
   */
  disableStream?: boolean
}

export interface UseDeploymentEventsResult {
  state: ReducerState
  snapshot: DeploymentSnapshot | null
  streamStatus: StreamStatus
  streamError: string | null
  startedAt: number | null
  finishedAt: number | null
  retry: () => void
  /**
   * Issues #764 + #768 — surfaces the handover-ready signal to the
   * provision page. Non-null when EITHER the live SSE stream
   * delivered a typed `handover-ready` event OR the GET /deployments/
   * {id} poll observed a non-empty `handoverURL` on the record.
   * Carries the canonical URL the provision page renders as the
   * "Open your Sovereign console →" button + auto-redirect target.
   * Null until catalyst-api has auto-fired the mint.
   */
  handoverReady: HandoverReadyEvent | null
}

export function useDeploymentEvents(
  opts: UseDeploymentEventsOptions,
): UseDeploymentEventsResult {
  const { deploymentId, applicationIds, disableStream = false } = opts

  // Stable identity for applicationIds — sort + join so a fresh array
  // with the same membership doesn't re-seed state on every render.
  const appsKey = useMemo(() => [...applicationIds].sort().join('|'), [applicationIds])

  const [state, setState] = useState<ReducerState>(() => buildInitialState(applicationIds))
  const [snapshot, setSnapshot] = useState<DeploymentSnapshot | null>(null)
  const [streamStatus, setStreamStatus] = useState<StreamStatus>('connecting')
  const [streamError, setStreamError] = useState<string | null>(null)
  const [startedAt, setStartedAt] = useState<number | null>(null)
  const [finishedAt, setFinishedAt] = useState<number | null>(null)
  const [retryNonce, setRetryNonce] = useState(0)
  // Issues #764 + #768 — handover-ready signal. Either the live SSE
  // stream's typed `handover-ready` event sets this, or the GET-replay
  // path observes `snapshot.handoverURL` non-empty and fills it. The
  // provision page reads this to render the redirect button + start the
  // 5s auto-redirect timer.
  const [handoverReady, setHandoverReady] = useState<HandoverReadyEvent | null>(null)

  // Re-seed reducer when the application set changes (operator returned
  // to the wizard and adjusted before clicking retry).
  useEffect(() => {
    setState(buildInitialState(applicationIds))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appsKey])

  // History replay — fetch the buffered slice BEFORE opening the SSE
  // stream. For a deployment that already finished, this is the only
  // way to render the full history (the SSE replay on connect serves
  // the same slice but a plain GET is easier to test and gives us a
  // stateless `done` flag we can render banner states from before the
  // EventSource even opens). The SSE stream below de-duplicates by
  // index — only events whose count exceeds the history length are
  // applied live.
  const historyCountRef = useRef(0)

  useEffect(() => {
    if (!deploymentId) return
    let cancelled = false
    const url = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/events`
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(async (resp) => {
        if (cancelled) return
        if (!resp.ok) return
        const body = (await resp.json()) as {
          events?: DeploymentEvent[]
          state?: DeploymentSnapshot
          done?: boolean
        }
        if (cancelled) return
        const events = Array.isArray(body.events) ? body.events : []
        historyCountRef.current = events.length
        if (events.length > 0) {
          const first = events[0]?.time
          const firstMs = first ? Date.parse(first) : NaN
          if (!Number.isNaN(firstMs)) {
            setStartedAt((prev) => prev ?? firstMs)
          }
          setState((prev) => reduceEvents(prev, events))
        }
        if (body.done && body.state) {
          setSnapshot(body.state)
          setFinishedAt((prev) => prev ?? Date.now())
          // Issues #764 + #768 — recover handover-ready from the
          // durable record on a page that lands AFTER the live SSE
          // event was emitted. Either the top-level lifted fields or
          // the result-nested copy populates `handoverURL`; the
          // setter is idempotent (no second toast / second redirect
          // timer) because it sets state once.
          const handoverURL =
            body.state.handoverURL ?? body.state.result?.handoverURL ?? ''
          if (handoverURL) {
            setHandoverReady((prev) =>
              prev ?? {
                handoverURL,
                // GET-replay path doesn't carry the JWT expiry — pass
                // empty so the consumer's "expired?" check defaults to
                // false. The token's actual expiry is on the JWT
                // payload itself (5 minutes from mint); the
                // Sovereign-side handler validates it on redirect.
                expiresAt: '',
              },
            )
          }
          if (body.state.status === 'ready') {
            // GROUNDING — pass the helmwatch componentStates map (if
            // any) into markAllReady so each card seeds from the
            // durable map; cards NOT named in the map remain pending
            // and the AdminPage banner explains why. We never let
            // deployment.status==="ready" alone flip cards to
            // installed.
            const componentStates =
              body.state.componentStates ?? body.state.result?.componentStates ?? null
            setState((prev) => markAllReady(prev, componentStates))
            setStreamStatus('completed')
          } else if (body.state.status === 'failed') {
            // Issue #519 — `failed` does NOT mean Phase 0 failed. Most
            // commonly it means Phase 0 succeeded and Phase 1 timed out
            // / saw zero HelmReleases / could not start (post-PR #495).
            // Converge the Phase-0 banner if helmwatch recorded any
            // outcome — that proves Phase 0 finished. Without this the
            // wizard pins Hetzner-infra at "running" forever because
            // the `tofu-output` event was lost in producer-channel
            // overflow on the high-throughput tofu-apply burst.
            const componentStates =
              body.state.componentStates ?? body.state.result?.componentStates ?? null
            const phase1Outcome =
              body.state.phase1Outcome ?? body.state.result?.phase1Outcome ?? ''
            setState((prev) => markFailedTerminal(prev, phase1Outcome, componentStates))
            setStreamStatus('failed')
            setStreamError(body.state.error ?? null)
          }
        }
      })
      .catch(() => {
        // Network failure on the history endpoint — fall through to
        // SSE; same handling as the legacy ProvisionPage.
      })
    return () => {
      cancelled = true
    }
  }, [deploymentId, retryNonce])

  // SSE live stream — opens after history replay seeds the reducer.
  useEffect(() => {
    if (disableStream) return
    if (!deploymentId) return
    setStreamStatus('connecting')
    setStreamError(null)
    setSnapshot(null)
    setFinishedAt(null)
    setHandoverReady(null)
    const url = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/logs`
    let cancelled = false
    let seen = 0
    let es: EventSource | null = null
    // Issue #782 — guards reconnection backoff. When the SSE drops while
    // the canonical deployment status is still in-flight (e.g. the
    // reverse proxy idle-timed out the long-lived stream), we re-open
    // the EventSource against the same URL. The backend's
    // replay-on-connect serves the buffered history again; the `seen`
    // counter resets so the reducer skips already-applied events via
    // the same `seen <= historyCountRef.current` rule. We hard-cap
    // reconnect attempts so a permanently-broken transport eventually
    // surfaces as `failed` rather than spinning forever.
    let reconnectAttempts = 0
    const MAX_RECONNECT_ATTEMPTS = 5
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null

    const applySnapshotAndComplete = (snap: DeploymentSnapshot) => {
      setSnapshot(snap)
      setFinishedAt(Date.now())
      const handoverURL = snap.handoverURL ?? snap.result?.handoverURL ?? ''
      if (handoverURL) {
        setHandoverReady((prev) => prev ?? { handoverURL, expiresAt: '' })
      }
      if (snap.status === 'ready') {
        const componentStates =
          snap.componentStates ?? snap.result?.componentStates ?? null
        setState((prev) => markAllReady(prev, componentStates))
        setStreamStatus('completed')
      } else if (snap.status === 'failed') {
        const componentStates =
          snap.componentStates ?? snap.result?.componentStates ?? null
        const phase1Outcome =
          snap.phase1Outcome ?? snap.result?.phase1Outcome ?? ''
        setState((prev) => markFailedTerminal(prev, phase1Outcome, componentStates))
        setStreamStatus('failed')
        // Issue #782 — surface the REAL error from the canonical record.
        // Never synthesise "Deployment ended with status=<phase>" because
        // a phase id (e.g. "phase1-watching") is a transient state, not a
        // final outcome. If the backend somehow reports a non-terminal
        // status here we leave streamError null rather than fabricate a
        // misleading copy.
        setStreamError(snap.error ?? null)
      } else {
        // Backend reported a non-terminal status on what should be a
        // terminal `done` event. This shouldn't happen, but defensive:
        // treat it as still in-flight rather than failure. The
        // reconnect/poll loop above will re-evaluate.
        setStreamStatus('streaming')
      }
    }

    const onDone = (msg: MessageEvent) => {
      try {
        const snap = JSON.parse(msg.data) as DeploymentSnapshot
        applySnapshotAndComplete(snap)
      } catch (err) {
        setStreamStatus('failed')
        setStreamError(`Failed to parse final snapshot: ${String(err)}`)
      }
      es?.close()
    }

    // Issues #764 + #768 — typed `handover-ready` SSE event. Carries
    // {handoverURL, expiresAt}; the provision page renders the
    // "Open your Sovereign console →" button + 5s auto-redirect timer
    // off this signal. The default-message reducer never sees this
    // event because the typed channel is dispatched separately.
    const onHandoverReady = (msg: MessageEvent) => {
      try {
        const payload = JSON.parse(msg.data) as HandoverReadyEvent
        if (payload && typeof payload.handoverURL === 'string' && payload.handoverURL !== '') {
          // First-write-wins — a duplicate event from an SSE reconnect
          // (same payload because durable buffer replays it) is a
          // no-op. The provision page's redirect timer is keyed off
          // the same identity, so a re-set with the same URL would
          // also be a no-op there, but defending here keeps the
          // contract clean.
          setHandoverReady((prev) => prev ?? payload)
        }
      } catch {
        /* malformed payload — drop, the GET-replay fallback recovers */
      }
    }

    /**
     * Issue #782 — re-fetch /deployments/{id} once and switch on the
     * canonical status. Called when the SSE stream closes WITHOUT a
     * terminal `event: done`. The transport drop alone is NOT a failure
     * signal; only `status === 'failed'` from this canonical poll is.
     *
     * Returns true if a terminal state was reached (ready/failed/
     * unreachable), false if the deployment is still in flight (caller
     * will reconnect SSE with backoff).
     */
    const pollCanonicalState = async (): Promise<boolean> => {
      const pollUrl = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}`
      try {
        const resp = await fetch(pollUrl, { headers: { Accept: 'application/json' } })
        if (cancelled) return true
        if (!resp.ok) {
          // 404/5xx — treat as unreachable so the operator can retry.
          setStreamStatus('unreachable')
          setStreamError(
            (prev) => prev ?? `Deployment status poll returned HTTP ${resp.status}`,
          )
          return true
        }
        const snap = (await resp.json()) as DeploymentSnapshot
        if (cancelled) return true
        const status = snap?.status ?? ''
        if (status === 'ready' || status === 'failed') {
          applySnapshotAndComplete(snap)
          return true
        }
        if (IN_FLIGHT_STATUSES.has(status)) {
          // Still in flight — surface the snapshot so handover-URL
          // (which catalyst-api may have stamped before the stream
          // dropped) is recovered immediately, and keep the UI in
          // `streaming` so the wizard's spinner stays up. The caller
          // re-opens the EventSource via the reconnect branch.
          setSnapshot(snap)
          const handoverURL = snap.handoverURL ?? snap.result?.handoverURL ?? ''
          if (handoverURL) {
            // The handover URL is itself a terminal signal: catalyst-api
            // only stamps it after the Phase-1 watch reaches
            // OutcomeReady. Promote to completed so the AppsPage banner
            // renders even if the top-level status hasn't flipped yet.
            setHandoverReady((prev) => prev ?? { handoverURL, expiresAt: '' })
            const componentStates =
              snap.componentStates ?? snap.result?.componentStates ?? null
            setState((prev) => markAllReady(prev, componentStates))
            setStreamStatus('completed')
            setFinishedAt(Date.now())
            return true
          }
          // Spinner UI — the reducer state already reflects whatever
          // events the GET-replay seeded; nothing more to do here.
          setStreamStatus('streaming')
          return false
        }
        // Unknown status — defensive: don't synthesize a failure copy.
        return false
      } catch {
        if (cancelled) return true
        setStreamStatus('unreachable')
        setStreamError(
          (prev) => prev ?? 'Could not reach the catalyst-api to confirm deployment status',
        )
        return true
      }
    }

    const handleStreamClose = () => {
      if (cancelled) return
      // Issue #782 — SSE close is a TRANSPORT signal, not a deployment
      // outcome. Re-fetch the canonical /deployments/{id} record and
      // switch on the real status: `ready` → success banner, `failed`
      // → real-error banner, in-flight → spinner + reconnect SSE.
      void pollCanonicalState().then((terminal) => {
        if (cancelled) return
        if (terminal) return
        // Still in flight — reconnect the SSE stream after a small
        // exponential backoff. The reducer is idempotent over replayed
        // events thanks to the `seen <= historyCountRef.current` guard,
        // so the buffered history can be safely served again.
        if (reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
          setStreamStatus('failed')
          setStreamError(
            (prev) =>
              prev ??
              'SSE connection dropped repeatedly — could not maintain a stream to the catalyst-api',
          )
          return
        }
        reconnectAttempts += 1
        const backoffMs = Math.min(1000 * 2 ** (reconnectAttempts - 1), 8000)
        if (reconnectTimer !== null) clearTimeout(reconnectTimer)
        reconnectTimer = setTimeout(() => {
          if (cancelled) return
          es = openStream()
        }, backoffMs)
      })
    }

    const openStream = (): EventSource => {
      const next = new EventSource(url)
      seen = 0
      next.onopen = () => {
        setStreamStatus('streaming')
        setStartedAt((prev) => prev ?? Date.now())
      }
      next.onmessage = (msg) => {
        try {
          const ev = JSON.parse(msg.data) as DeploymentEvent
          seen += 1
          if (seen <= historyCountRef.current) return
          setState((prev) => reduceEvents(prev, [ev]))
        } catch {
          /* malformed event — drop, the next event will recover */
        }
      }
      next.addEventListener('done', onDone as EventListener)
      next.addEventListener('handover-ready', onHandoverReady as EventListener)
      next.onerror = () => {
        if (next.readyState === EventSource.CLOSED) {
          // Issue #782 — SSE close is a TRANSPORT signal, not a
          // deployment outcome. Re-fetch the canonical /deployments/{id}
          // record and switch on the real status. We NEVER flip to
          // `failed` purely on a stream close.
          handleStreamClose()
        }
      }
      return next
    }

    es = openStream()

    return () => {
      cancelled = true
      if (reconnectTimer !== null) clearTimeout(reconnectTimer)
      if (es) {
        es.removeEventListener('done', onDone as EventListener)
        es.removeEventListener('handover-ready', onHandoverReady as EventListener)
        es.close()
      }
    }
  }, [deploymentId, retryNonce, disableStream])

  // Stable callback — referential identity matters because callers
  // (e.g. AppsPage's notification effect) include `retry` in their
  // useEffect dependency arrays. A new function every render would
  // re-fire the effect on every state change and cause the toast push
  // → re-render → toast push loop that triggered the test hang.
  const retry = useCallback(() => setRetryNonce((n) => n + 1), [])

  return {
    state,
    snapshot,
    streamStatus,
    streamError,
    startedAt,
    finishedAt,
    retry,
    handoverReady,
  }
}
