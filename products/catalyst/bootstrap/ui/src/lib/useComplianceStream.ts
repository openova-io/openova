/**
 * useComplianceStream — React hook driving compliance dashboards from
 * the catalyst-api `GET /api/v1/sovereigns/{id}/compliance/stream`
 * Server-Sent-Events channel (slice S, #1096).
 *
 * Wire path, per ADR-0001 §5:
 *
 *   PolicyReport (Kyverno) + custom evaluators
 *      ▼
 *   catalyst-api ComplianceHandler (per-resource score recompute)
 *      ▼
 *   GET /api/v1/sovereigns/{id}/compliance/stream  (SSE)
 *      ▼  one JSON frame per score change
 *   browser EventSource → this hook → Map<scope:id, Score>
 *
 * Layered on `useK8sStream.ts` (the canonical seam) — same exponential-
 * backoff reconnect, same access_token query-param auth, same
 * test-seam pattern. Diverges only in:
 *   - URL: /compliance/stream (not /k8s/stream)
 *   - Wire frame: complianceEvent = { type, cluster, score, at } where
 *     `score` is the Score struct rather than a K8s object
 *   - Cache key: "<score.scope>:<score.id>" (cluster-id-prefixed via
 *     the resourceState path) so the consumer can look up the
 *     sovereign rollup directly.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — uses EventSource, no setInterval polling.
 *   #4 (never hardcode) — every URL derives from `streamURL()` in
 *      compliance.api.ts which composes API_BASE.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { loadTokens } from '@/shared/lib/oidc'
import { streamURL, type Score, type ScoreScope } from '@/pages/admin/compliance/compliance.api'

/* ── Wire types ──────────────────────────────────────────────────── */

export type ComplianceEventType = 'score' | 'scorecard'

/**
 * ComplianceEvent — wire frame on `/compliance/stream`. Mirrors
 * `complianceEvent` in `internal/handler/compliance.go`.
 */
export interface ComplianceEvent {
  type: ComplianceEventType
  cluster: string
  score: Score
  at: string // RFC3339
}

/* ── Hook options + result ──────────────────────────────────────── */

export interface UseComplianceStreamOptions {
  sovereignId: string
  /** Test seam — disables EventSource entirely. */
  disableStream?: boolean
}

export interface UseComplianceStreamResult {
  /** Flat array of every Score currently cached, deduped by scope:id. */
  scores: Score[]
  /** Same data, grouped by scope. */
  byScope: Record<ScoreScope, Score[]>
  /** Look up a score by `${scope}:${id}` cache key. */
  getScore: (scope: ScoreScope, id: string) => Score | undefined
  isLoading: boolean
  isError: boolean
  lastEventAt: Date | null
  reconnect: () => void
}

/* ── Internal state ──────────────────────────────────────────────── */

const INITIAL_BACKOFF_MS = 500
const MAX_BACKOFF_MS = 30_000

function cacheKey(scope: ScoreScope, id: string): string {
  return `${scope}:${id}`
}

function emptyByScope(): Record<ScoreScope, Score[]> {
  return {
    resource: [],
    application: [],
    environment: [],
    organization: [],
    sovereign: [],
  }
}

/* ── The hook ────────────────────────────────────────────────────── */

export function useComplianceStream(
  opts: UseComplianceStreamOptions,
): UseComplianceStreamResult {
  const { sovereignId, disableStream = false } = opts

  const [tick, setTick] = useState(0)
  const [scores, setScores] = useState<Score[]>([])
  const [byScope, setByScope] = useState<Record<ScoreScope, Score[]>>(emptyByScope)
  const [isLoading, setIsLoading] = useState(true)
  const [isError, setIsError] = useState(false)
  const [lastEventAt, setLastEventAt] = useState<Date | null>(null)

  const cacheRef = useRef<Map<string, Score>>(new Map())

  // Memoised cache lookup helper. Recomputed only when the underlying
  // `scores` reference changes — same pattern as useK8sStream.
  const getScore = useMemo(() => {
    return (scope: ScoreScope, id: string) => cacheRef.current.get(cacheKey(scope, id))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scores])

  useEffect(() => {
    if (disableStream || !sovereignId) {
      setIsLoading(false)
      return
    }

    setIsLoading(true)
    setIsError(false)
    cacheRef.current = new Map()

    let cancelled = false
    let es: EventSource | null = null
    let backoff = INITIAL_BACKOFF_MS
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null

    const recompute = () => {
      const next: Score[] = []
      const grouped = emptyByScope()
      for (const score of cacheRef.current.values()) {
        next.push(score)
        const list = grouped[score.scope] ?? (grouped[score.scope] = [] as Score[])
        list.push(score)
      }
      setScores(next)
      setByScope(grouped)
    }

    const connect = () => {
      if (cancelled) return
      const tokens = loadTokens()
      const url = streamURL(sovereignId, tokens?.accessToken)
      es = new EventSource(url)

      es.onopen = () => {
        if (cancelled) return
        backoff = INITIAL_BACKOFF_MS
        setIsLoading(false)
        setIsError(false)
      }

      es.onmessage = (msg) => {
        if (cancelled) return
        try {
          const ev = JSON.parse(msg.data) as ComplianceEvent
          if (!ev || !ev.score || !ev.score.scope || !ev.score.id) return
          const key = cacheKey(ev.score.scope, ev.score.id)
          cacheRef.current.set(key, ev.score)
          setLastEventAt(new Date())
          recompute()
        } catch {
          /* malformed event — drop, the next event will recover */
        }
      }

      es.onerror = () => {
        if (cancelled) return
        setIsError(true)
        try {
          es?.close()
        } catch {
          /* ignore */
        }
        es = null
        const wait = backoff
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
        reconnectTimer = setTimeout(connect, wait)
      }
    }

    connect()
    return () => {
      cancelled = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      try {
        es?.close()
      } catch {
        /* ignore */
      }
    }
  }, [sovereignId, disableStream, tick])

  const reconnect = () => setTick((n) => n + 1)

  return {
    scores,
    byScope,
    getScore,
    isLoading,
    isError,
    lastEventAt,
    reconnect,
  }
}
