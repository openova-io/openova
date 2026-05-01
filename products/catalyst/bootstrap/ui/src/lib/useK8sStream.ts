/**
 * useK8sStream — React hook that drives any UI surface from the
 * catalyst-api K8s data-plane (issue #321).
 *
 * Wire path, per ADR-0001 §5:
 *
 *   kube-apiserver
 *      ▲
 *      │ watch stream — long-running HTTP/2
 *      ▼
 *   catalyst-api in-process Indexer (SharedInformerFactory)
 *      │
 *      ▼
 *   GET /api/v1/sovereigns/{id}/k8s/stream?kinds=...
 *      │
 *      ▼  (Server-Sent Events, multiplexed by kind)
 *   browser EventSource → this hook → Map<key, K8sObject>
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — the hook lands at full feature: typed events,
 *      reconnect with exponential backoff, last-event timestamp,
 *      manual refresh trigger, generic over the K8s shape.
 *   #3 (event-driven) — uses EventSource, no setInterval polls.
 *   #4 (never hardcode) — every URL derives from API_BASE.
 *
 * Returned shape:
 *   - items[]      — flat array of every object currently in the
 *                    cache, deduped by `${kind}:${ns}/${name}`.
 *   - byKind       — same data, grouped by kind for List pages.
 *   - isLoading    — true until the SSE connection opens.
 *   - isError      — true when reconnect attempts are mid-backoff.
 *   - lastEventAt  — Date of the last received event; null if none.
 *   - reconnect()  — force-disconnect; useful for "Refresh" buttons.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'

/* ── Wire types ──────────────────────────────────────────────────── */

export type K8sEventType = 'ADDED' | 'MODIFIED' | 'DELETED'

/**
 * Generic K8s object shape. Stored as `Record<string, unknown>` in
 * the cache (matching the unstructured backend wire shape) with
 * convenience accessors via the `getMeta` helper.
 */
export type K8sObject = Record<string, unknown>

export interface K8sEvent<T extends K8sObject = K8sObject> {
  cluster: string
  kind: string
  type: K8sEventType
  object: T
  at: string // RFC3339
}

export interface K8sObjectMeta {
  name: string
  namespace: string
  uid: string
  resourceVersion: string
  labels: Record<string, string>
  annotations: Record<string, string>
  creationTimestamp: string
}

/**
 * Pull metadata.* fields out of an unstructured K8s object. Kept as
 * a free function (not a method on a class) so consumers can map
 * over the items array without instantiating wrappers.
 */
export function getMeta(obj: K8sObject): K8sObjectMeta {
  const meta = (obj['metadata'] ?? {}) as Record<string, unknown>
  const labels = (meta['labels'] ?? {}) as Record<string, string>
  const annotations = (meta['annotations'] ?? {}) as Record<string, string>
  return {
    name: typeof meta['name'] === 'string' ? (meta['name'] as string) : '',
    namespace: typeof meta['namespace'] === 'string' ? (meta['namespace'] as string) : '',
    uid: typeof meta['uid'] === 'string' ? (meta['uid'] as string) : '',
    resourceVersion: typeof meta['resourceVersion'] === 'string' ? (meta['resourceVersion'] as string) : '',
    labels,
    annotations,
    creationTimestamp:
      typeof meta['creationTimestamp'] === 'string' ? (meta['creationTimestamp'] as string) : '',
  }
}

/* ── Hook options ────────────────────────────────────────────────── */

export interface UseK8sStreamOptions {
  /** Sovereign id (the URL segment in `/sovereigns/{id}/k8s/stream`). */
  sovereignId: string
  /**
   * Kinds to watch. Empty array means "all registered kinds" — the
   * server's `?kinds=` parameter is omitted in that case.
   */
  kinds: readonly string[]
  /**
   * If true, asks the server to emit a synthetic ADDED for every
   * cached object on connect (`?initialState=1`). Useful when this
   * hook is the SOLE source of seed data; pass false when a separate
   * REST list call is the cold-start path.
   *
   * Defaults to true so consumers that pass `kinds: ['pod', 'deployment']`
   * see populated data without an additional fetch.
   */
  initialState?: boolean
  /**
   * Test seam — disables EventSource entirely. The hook returns its
   * initial empty state.
   */
  disableStream?: boolean
}

export interface UseK8sStreamResult<T extends K8sObject = K8sObject> {
  /** Flat array of every cached object — useful for the Architecture graph. */
  items: T[]
  /** Same data grouped by kind — convenient for List pages. */
  byKind: Record<string, T[]>
  isLoading: boolean
  isError: boolean
  lastEventAt: Date | null
  reconnect: () => void
}

/* ── Internal state ──────────────────────────────────────────────── */

const INITIAL_BACKOFF_MS = 500
const MAX_BACKOFF_MS = 30_000

function cacheKey(kind: string, obj: K8sObject): string {
  const m = getMeta(obj)
  return `${kind}:${m.namespace}/${m.name}`
}

function buildStreamURL(sovereignId: string, kinds: readonly string[], initialState: boolean): string {
  const safeId = encodeURIComponent(sovereignId)
  const params = new URLSearchParams()
  if (kinds.length > 0) {
    params.set('kinds', kinds.join(','))
  }
  if (initialState) {
    params.set('initialState', '1')
  }
  const qs = params.toString()
  return `${API_BASE}/v1/sovereigns/${safeId}/k8s/stream${qs ? '?' + qs : ''}`
}

/* ── The hook ────────────────────────────────────────────────────── */

export function useK8sStream<T extends K8sObject = K8sObject>(
  opts: UseK8sStreamOptions,
): UseK8sStreamResult<T> {
  const { sovereignId, kinds, initialState = true, disableStream = false } = opts

  // Stable identity for `kinds` so the EventSource doesn't reopen on
  // every render — sort + join produces a deterministic dep key.
  const kindsKey = useMemo(() => [...kinds].sort().join(','), [kinds])

  const [tick, setTick] = useState(0)
  const [items, setItems] = useState<T[]>([])
  const [byKind, setByKind] = useState<Record<string, T[]>>({})
  const [isLoading, setIsLoading] = useState(true)
  const [isError, setIsError] = useState(false)
  const [lastEventAt, setLastEventAt] = useState<Date | null>(null)

  // Mutable cache (Map) lives in a ref so reducer-style updates don't
  // recreate it on every render.
  const cacheRef = useRef<Map<string, { kind: string; obj: T }>>(new Map())

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
      const next: T[] = []
      const grouped: Record<string, T[]> = {}
      for (const { kind, obj } of cacheRef.current.values()) {
        next.push(obj)
        const list = grouped[kind] ?? (grouped[kind] = [])
        list.push(obj)
      }
      setItems(next)
      setByKind(grouped)
    }

    const connect = () => {
      if (cancelled) return
      const url = buildStreamURL(sovereignId, kinds, initialState)
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
          const ev = JSON.parse(msg.data) as K8sEvent<T>
          if (!ev || !ev.kind || !ev.object) return
          const key = cacheKey(ev.kind, ev.object)
          if (ev.type === 'DELETED') {
            cacheRef.current.delete(key)
          } else {
            cacheRef.current.set(key, { kind: ev.kind, obj: ev.object })
          }
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
        // Exponential backoff with cap.
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sovereignId, kindsKey, initialState, disableStream, tick])

  const reconnect = () => setTick((n) => n + 1)

  return {
    items,
    byKind,
    isLoading,
    isError,
    lastEventAt,
    reconnect,
  }
}
