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
    const es = new EventSource(url)
    let seen = 0

    es.onopen = () => {
      setStreamStatus('streaming')
      setStartedAt((prev) => prev ?? Date.now())
    }
    es.onmessage = (msg) => {
      try {
        const ev = JSON.parse(msg.data) as DeploymentEvent
        seen += 1
        if (seen <= historyCountRef.current) return
        setState((prev) => reduceEvents(prev, [ev]))
      } catch {
        /* malformed event — drop, the next event will recover */
      }
    }
    const onDone = (msg: MessageEvent) => {
      try {
        const snap = JSON.parse(msg.data) as DeploymentSnapshot
        setSnapshot(snap)
        setFinishedAt(Date.now())
        if (snap?.status === 'ready') {
          // Same grounding rule as the GET-replay path above.
          const componentStates =
            snap.componentStates ?? snap.result?.componentStates ?? null
          setState((prev) => markAllReady(prev, componentStates))
          setStreamStatus('completed')
        } else {
          // Issue #519 — same Phase-0 banner convergence as the GET-
          // replay path above. The SSE `done` event is the live-stream
          // mirror; failing to converge here would cause the same
          // "Phase-0 stuck Running" UX on a tab that stayed open from
          // the start.
          if (snap?.status === 'failed') {
            const componentStates =
              snap.componentStates ?? snap.result?.componentStates ?? null
            const phase1Outcome =
              snap.phase1Outcome ?? snap.result?.phase1Outcome ?? ''
            setState((prev) => markFailedTerminal(prev, phase1Outcome, componentStates))
          }
          setStreamStatus('failed')
          setStreamError(snap?.error ?? `Deployment ended with status=${snap?.status ?? 'unknown'}`)
        }
      } catch (err) {
        setStreamStatus('failed')
        setStreamError(`Failed to parse final snapshot: ${String(err)}`)
      }
      es.close()
    }
    es.addEventListener('done', onDone as EventListener)

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
    es.addEventListener('handover-ready', onHandoverReady as EventListener)

    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) {
        setStreamStatus((prev) => {
          if (prev === 'completed') return prev
          return prev === 'connecting' ? 'unreachable' : 'failed'
        })
        setStreamError((prev) => prev ?? 'SSE connection closed before completion')
      }
    }
    return () => {
      es.removeEventListener('done', onDone as EventListener)
      es.removeEventListener('handover-ready', onHandoverReady as EventListener)
      es.close()
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
