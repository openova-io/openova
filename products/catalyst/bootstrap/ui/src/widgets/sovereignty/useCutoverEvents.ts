/**
 * useCutoverEvents — React hook driving the sovereignty cutover surface
 * in the admin Sovereign Console (issues #790 / #793).
 *
 * Two sources of truth, same reducer (mirror of useDeploymentEvents.ts):
 *
 *   1. GET /api/v1/sovereign/cutover/status — returns the current snapshot
 *      from the `self-sovereign-cutover-status` ConfigMap on the cluster.
 *      Always called on mount so a page that lands AFTER the cutover
 *      finished still renders the full 8-step trail (and the green
 *      "Sovereignty achieved" banner).
 *   2. SSE  /api/v1/sovereign/cutover/events — live event channel.
 *      Emits `cutover-step` frames for per-step transitions and a
 *      terminal `cutover-status` frame on completion.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode): URLs go through `${API_BASE}` from
 *      shared/config/urls.ts; the path is the only inline string here.
 *   #2 (never compromise): the runtime parser at parseCutoverStatus()
 *      rejects unknown wire shapes — a malformed frame is dropped, not
 *      treated as success.
 *
 * Why a dedicated hook (vs reusing useDeploymentEvents)
 * ────────────────────────────────────────────────────
 * The cutover SSE channel speaks a different vocabulary (`cutover-step`,
 * `cutover-status`) than the deployment channel (`done`, `handover-ready`).
 * Multiplexing them would force every frame parser to discriminate on
 * event-type at the dispatch layer, and the reconnect/backoff logic
 * would need to know about both. Cleaner to keep them separated and
 * factor the shared shape (replay-then-stream + reconnect) into both.
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'
import {
  applyCutoverStepUpdate,
  buildInitialCutoverStatus,
  parseCutoverStatus,
  type CutoverStatus,
  type CutoverStepStatusRecord,
  isCutoverStepID,
} from '@/shared/types/cutover'

export type CutoverStreamStatus =
  | 'connecting'
  | 'streaming'
  | 'completed'
  | 'failed'
  | 'unreachable'

export interface UseCutoverEventsOptions {
  /**
   * Test seam — disables the EventSource attach. The GET /status fetch
   * still runs (jsdom can fetch via mocked global). Mirrors the same
   * flag useDeploymentEvents exposes.
   */
  disableStream?: boolean
}

export interface UseCutoverEventsResult {
  status: CutoverStatus
  streamStatus: CutoverStreamStatus
  streamError: string | null
  /** Triggered by POST /sovereign/cutover/start. */
  startCutover: () => Promise<void>
  /** Increment to re-open the SSE stream + re-fetch /status. */
  retry: () => void
  /** True while a POST /start is in flight (button disables on this). */
  starting: boolean
  /** Last error from POST /start; cleared on a fresh attempt. */
  startError: string | null
}

/* ────────────────────────────────────────────────────────────────
 * Endpoint helpers — single inline of the wire path; never a literal
 * `/api/...` (per the no-hardcoded-api guardrail).
 * ────────────────────────────────────────────────────────────── */

const CUTOVER_PATH = '/v1/sovereign/cutover'

const cutoverStatusURL = () => `${API_BASE}${CUTOVER_PATH}/status`
const cutoverEventsURL = () => `${API_BASE}${CUTOVER_PATH}/events`
const cutoverStartURL = () => `${API_BASE}${CUTOVER_PATH}/start`

/* ────────────────────────────────────────────────────────────────
 * Hook
 * ────────────────────────────────────────────────────────────── */

export function useCutoverEvents(
  opts: UseCutoverEventsOptions = {},
): UseCutoverEventsResult {
  const { disableStream = false } = opts

  const [status, setStatus] = useState<CutoverStatus>(() =>
    buildInitialCutoverStatus(),
  )
  const [streamStatus, setStreamStatus] =
    useState<CutoverStreamStatus>('connecting')
  const [streamError, setStreamError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)
  const [startError, setStartError] = useState<string | null>(null)
  const [retryNonce, setRetryNonce] = useState(0)

  // Cancel-flag ref so the unmount cleanup can race-stop the in-flight
  // GET fetch without setting state on an unmounted tree.
  const cancelledRef = useRef(false)

  // ── Replay snapshot — GET /status on mount + on retry ──────────
  useEffect(() => {
    cancelledRef.current = false
    let cancelled = false
    const url = cutoverStatusURL()
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(async (resp) => {
        if (cancelled || cancelledRef.current) return
        if (!resp.ok) {
          // 404 means the catalyst-api endpoint isn't deployed yet on
          // this Sovereign — treat as `tethered` + `unreachable` so the
          // UI still renders the "Achieve True Sovereignty" CTA, just
          // greyed out, with a tooltip explaining the API isn't live.
          if (resp.status === 404) {
            setStreamStatus('unreachable')
            setStreamError(
              'cutover API not yet available on this Sovereign — try again after the bp-self-sovereign-cutover chart is installed',
            )
            return
          }
          setStreamStatus('failed')
          setStreamError(`cutover status returned HTTP ${resp.status}`)
          return
        }
        const body = (await resp.json()) as unknown
        if (cancelled || cancelledRef.current) return
        try {
          const next = parseCutoverStatus(body)
          setStatus(next)
          if (next.state === 'sovereign') {
            setStreamStatus('completed')
          }
        } catch (err) {
          // Malformed frame — keep the seeded `tethered` placeholder
          // and flip the stream pill to `failed` so the operator sees
          // something is off.
          setStreamStatus('failed')
          setStreamError(
            err instanceof Error
              ? err.message
              : 'malformed cutover status response',
          )
        }
      })
      .catch(() => {
        if (cancelled || cancelledRef.current) return
        setStreamStatus('unreachable')
        setStreamError(
          (prev) =>
            prev ?? 'could not reach the catalyst-api to fetch cutover status',
        )
      })
    return () => {
      cancelled = true
    }
  }, [retryNonce])

  // ── SSE live stream — runs in parallel; reducer is idempotent ─
  useEffect(() => {
    if (disableStream) return
    setStreamError(null)
    let cancelled = false
    let es: EventSource | null = null
    let reconnectAttempts = 0
    const MAX_RECONNECT_ATTEMPTS = 5
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null

    const onCutoverStep = (msg: MessageEvent) => {
      if (cancelled) return
      try {
        const payload = JSON.parse(msg.data) as unknown
        if (payload === null || typeof payload !== 'object') return
        const rec = payload as Record<string, unknown>
        if (!isCutoverStepID(rec.step)) return
        const stepStatus = rec.status
        if (
          stepStatus !== 'pending' &&
          stepStatus !== 'running' &&
          stepStatus !== 'done' &&
          stepStatus !== 'failed'
        )
          return
        const update: CutoverStepStatusRecord = {
          step: rec.step,
          status: stepStatus,
          startedAt:
            typeof rec.startedAt === 'string' ? rec.startedAt : undefined,
          finishedAt:
            typeof rec.finishedAt === 'string' ? rec.finishedAt : undefined,
          message: typeof rec.message === 'string' ? rec.message : undefined,
        }
        setStatus((prev) => applyCutoverStepUpdate(prev, update))
      } catch {
        /* malformed event — drop, the next event recovers */
      }
    }

    const onCutoverStatus = (msg: MessageEvent) => {
      if (cancelled) return
      try {
        const payload = JSON.parse(msg.data) as unknown
        const next = parseCutoverStatus(payload)
        setStatus(next)
        if (next.state === 'sovereign') {
          setStreamStatus('completed')
        }
      } catch {
        /* malformed terminal frame — leave the per-step state intact */
      }
    }

    const handleStreamClose = () => {
      if (cancelled) return
      // SSE close is a TRANSPORT signal, not a state outcome.
      // If the snapshot is already terminal, stay completed; otherwise
      // back off and reconnect.
      setStatus((prev) => {
        if (prev.state === 'sovereign') return prev
        return prev
      })
      if (reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
        setStreamStatus('failed')
        setStreamError(
          (prev) =>
            prev ??
            'cutover SSE dropped repeatedly — could not maintain a stream',
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
    }

    const openStream = (): EventSource => {
      const next = new EventSource(cutoverEventsURL())
      next.onopen = () => {
        if (cancelled) return
        setStreamStatus((prev) =>
          prev === 'completed' ? prev : 'streaming',
        )
        reconnectAttempts = 0
      }
      next.addEventListener('cutover-step', onCutoverStep as EventListener)
      next.addEventListener(
        'cutover-status',
        onCutoverStatus as EventListener,
      )
      next.onerror = () => {
        if (next.readyState === EventSource.CLOSED) {
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
        es.removeEventListener('cutover-step', onCutoverStep as EventListener)
        es.removeEventListener(
          'cutover-status',
          onCutoverStatus as EventListener,
        )
        es.close()
      }
    }
  }, [retryNonce, disableStream])

  // ── POST /start trigger ───────────────────────────────────────
  const startCutover = useCallback(async () => {
    setStarting(true)
    setStartError(null)
    try {
      const resp = await fetch(cutoverStartURL(), {
        method: 'POST',
        headers: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({}),
      })
      if (!resp.ok) {
        const text = await resp.text().catch(() => '')
        throw new Error(
          `cutover start returned HTTP ${resp.status}${text ? `: ${text}` : ''}`,
        )
      }
      // 200 — the catalyst-api may include an initial status payload;
      // try to parse, but a body-less 200 is also fine. The SSE stream
      // will deliver per-step updates either way.
      try {
        const body = (await resp.json()) as unknown
        const next = parseCutoverStatus(body)
        setStatus(next)
      } catch {
        /* no body / malformed — SSE will fill in */
      }
      setStreamStatus((prev) => (prev === 'completed' ? prev : 'streaming'))
    } catch (err) {
      setStartError(err instanceof Error ? err.message : String(err))
      throw err
    } finally {
      setStarting(false)
    }
  }, [])

  const retry = useCallback(() => setRetryNonce((n) => n + 1), [])

  return {
    status,
    streamStatus,
    streamError,
    startCutover,
    retry,
    starting,
    startError,
  }
}
