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

import { useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'
import {
  buildInitialState,
  reduceEvents,
  markAllReady,
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
  result?: {
    sovereignFQDN: string
    controlPlaneIP: string
    loadBalancerIP: string
    consoleURL: string
    gitopsRepoURL: string
  }
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
          if (body.state.status === 'ready') {
            setState((prev) => markAllReady(prev))
            setStreamStatus('completed')
          } else if (body.state.status === 'failed') {
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
          setState((prev) => markAllReady(prev))
          setStreamStatus('completed')
        } else {
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
      es.close()
    }
  }, [deploymentId, retryNonce, disableStream])

  return {
    state,
    snapshot,
    streamStatus,
    streamError,
    startedAt,
    finishedAt,
    retry: () => setRetryNonce((n) => n + 1),
  }
}
