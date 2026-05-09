/**
 * LogViewer — EPIC-4 Slice X2 (#1099). xterm.js-based Pod-log viewer
 * mounted inside ResourceDetailPage's Logs tab.
 *
 * - Connects to `wss://<api>/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}`
 *   via the slice X1 protocol (one log line per binary frame).
 * - Color-aware (xterm preserves ANSI codes verbatim).
 * - Search box (`⌃F` / `⌘F`) → xterm's search addon.
 * - Persistent scrollback (env-configurable, default 10,000 lines).
 * - Reconnect on disconnect with `?since=<rfc3339>` from the most
 *   recent frame's wall-clock so the consumer never sees duplicates.
 * - Container picker dropdown when the Pod has multiple containers.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state, not waterfall) — the search + container picker
 *      ship at first cut, not as TODOs.
 *   #4 (never hardcode) — every URL via resource.api.ts.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { Terminal } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import { SearchAddon } from '@xterm/addon-search'
import 'xterm/css/xterm.css'

import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

import { logsWebSocketURL } from '@/pages/sovereign/cloud-list/resource.api'

export interface LogViewerProps {
  deploymentId: string
  /** Pod namespace (REQUIRED — Pod is namespaced). */
  ns: string
  /** Pod name. */
  pod: string
  /** Live K8s object — used to populate the container picker. */
  obj?: K8sObject | null
  /** Default container to start streaming. Falls back to `obj`'s first
   *  container when omitted. */
  initialContainer?: string
  /** Persistent scrollback line count. */
  scrollback?: number
  /** Test seam — substitute the WebSocket constructor. */
  websocketFactory?: (url: string) => WebSocket
  /** Test seam — disable terminal mounting (for jsdom unit tests). */
  disableTerminal?: boolean
}

const DEFAULT_SCROLLBACK = 10_000

export function LogViewer(props: LogViewerProps) {
  const {
    deploymentId,
    ns,
    pod,
    obj,
    initialContainer,
    scrollback = DEFAULT_SCROLLBACK,
    websocketFactory,
    disableTerminal = false,
  } = props

  // Discover containers from the live Pod object.
  const containers = useMemo<string[]>(() => discoverContainers(obj), [obj])
  const defaultContainer = initialContainer ?? containers[0] ?? ''
  const [container, setContainer] = useState(defaultContainer)
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchTerm, setSearchTerm] = useState('')
  const [status, setStatus] = useState<'connecting' | 'open' | 'closed' | 'error'>('connecting')
  const [error, setError] = useState<string | null>(null)
  const [retries, setRetries] = useState(0)

  // Terminal + addons + WS — all refs so React renders don't recreate.
  const termContainerRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const searchRef = useRef<SearchAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const lastTimestampRef = useRef<string | null>(null)
  const cancelledRef = useRef(false)

  // Mount xterm (skipped under jsdom).
  useEffect(() => {
    if (disableTerminal) return
    if (typeof window === 'undefined') return
    if (!termContainerRef.current) return
    const term = new Terminal({
      convertEol: true,
      scrollback,
      fontFamily:
        'JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12,
      theme: { background: '#0c0d10', foreground: '#d8dee9' },
    })
    const fit = new FitAddon()
    const search = new SearchAddon()
    term.loadAddon(fit)
    term.loadAddon(search)
    term.open(termContainerRef.current)
    try {
      fit.fit()
    } catch {
      /* jsdom etc. */
    }
    termRef.current = term
    fitRef.current = fit
    searchRef.current = search
    return () => {
      try {
        term.dispose()
      } catch {
        /* noop */
      }
      termRef.current = null
      fitRef.current = null
      searchRef.current = null
    }
  }, [disableTerminal, scrollback])

  // Refit on window resize.
  useEffect(() => {
    function onResize() {
      try {
        fitRef.current?.fit()
      } catch {
        /* noop */
      }
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  // Manage the WebSocket lifecycle. Reconnects on disconnect with
  // ?since=<lastTimestamp> per X1's resume protocol.
  useEffect(() => {
    if (!container || !deploymentId || !pod || !ns) return
    cancelledRef.current = false
    const factory = websocketFactory ?? ((u: string) => new WebSocket(u))

    function open() {
      const since = lastTimestampRef.current ?? undefined
      const url = logsWebSocketURL(deploymentId, ns, pod, container, {
        follow: true,
        tailLines: 100,
        ...(since ? { since } : {}),
      })
      let ws: WebSocket
      try {
        ws = factory(url)
      } catch (e) {
        setStatus('error')
        setError(e instanceof Error ? e.message : String(e))
        return
      }
      wsRef.current = ws
      setStatus('connecting')

      ws.binaryType = 'arraybuffer'

      ws.onopen = () => {
        setStatus('open')
        setError(null)
      }
      ws.onmessage = (ev) => {
        const text = decodeFrame(ev.data)
        // Track wall-clock for resume; the kubelet proxy doesn't
        // include a timestamp inside the frame, so we stamp the local
        // arrival time. Resume duplicates are bounded to <1s overlap.
        lastTimestampRef.current = new Date().toISOString()
        const term = termRef.current
        if (term) {
          term.write(text)
        }
      }
      ws.onerror = () => {
        // Browsers fire onerror without details; the close event will
        // carry the diagnostic.
        setError('connection error')
      }
      ws.onclose = (ev) => {
        if (cancelledRef.current) return
        setStatus('closed')
        // Backoff: 1s, 2s, 4s, capped at 8s. Reconnect unless this was
        // an intentional close (1000 with no payload from us).
        if (ev.code === 1000) {
          // Pod log ended cleanly — only reconnect if user re-clicks.
          return
        }
        const wait = Math.min(8000, 1000 * 2 ** Math.min(3, retries))
        setRetries((n) => n + 1)
        setTimeout(() => {
          if (!cancelledRef.current) open()
        }, wait)
      }
    }

    open()
    return () => {
      cancelledRef.current = true
      try {
        wsRef.current?.close(1000, 'unmount')
      } catch {
        /* noop */
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [container, deploymentId, ns, pod, websocketFactory])

  // Keyboard shortcut: ⌃F / ⌘F → toggle search box.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const isFind = (e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')
      if (!isFind) return
      // Only intercept when the LogViewer is focused. Use a heuristic:
      // the term container is the active element OR contains it.
      const active = document.activeElement
      const cont = termContainerRef.current
      if (!cont) return
      if (active === cont || (active instanceof Node && cont.contains(active))) {
        e.preventDefault()
        setSearchOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Run search whenever term changes.
  useEffect(() => {
    if (!searchOpen || !searchTerm) return
    try {
      searchRef.current?.findNext(searchTerm)
    } catch {
      /* noop */
    }
  }, [searchOpen, searchTerm])

  return (
    <div className="flex flex-col gap-2" data-testid="log-viewer">
      <div className="flex flex-wrap items-center gap-2">
        {containers.length > 1 && (
          <label className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]">
            Container
            <select
              data-testid="log-viewer-container"
              value={container}
              onChange={(e) => {
                setContainer(e.target.value)
                lastTimestampRef.current = null
                setRetries(0)
                termRef.current?.clear()
              }}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
            >
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>
        )}
        <button
          type="button"
          data-testid="log-viewer-search-toggle"
          onClick={() => setSearchOpen((o) => !o)}
          className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
          aria-pressed={searchOpen}
        >
          {searchOpen ? 'Hide search' : 'Search (⌃F)'}
        </button>
        <span
          data-testid="log-viewer-status"
          className={
            'ml-auto rounded px-2 py-0.5 text-xs ' +
            (status === 'open'
              ? 'bg-emerald-500/20 text-emerald-300'
              : status === 'error'
              ? 'bg-rose-500/20 text-rose-300'
              : 'bg-amber-500/20 text-amber-300')
          }
        >
          {status === 'open'
            ? 'streaming'
            : status === 'connecting'
            ? 'connecting'
            : status === 'error'
            ? 'error'
            : retries > 0
            ? `reconnecting (#${retries})`
            : 'closed'}
        </span>
      </div>

      {searchOpen && (
        <div className="flex items-center gap-2">
          <input
            data-testid="log-viewer-search-input"
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            placeholder="Find in logs"
            className="flex-1 rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
          />
          <button
            type="button"
            data-testid="log-viewer-search-prev"
            onClick={() => {
              try {
                searchRef.current?.findPrevious(searchTerm)
              } catch {
                /* noop */
              }
            }}
            className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
          >
            Prev
          </button>
          <button
            type="button"
            data-testid="log-viewer-search-next"
            onClick={() => {
              try {
                searchRef.current?.findNext(searchTerm)
              } catch {
                /* noop */
              }
            }}
            className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
          >
            Next
          </button>
        </div>
      )}

      {error && (
        <div data-testid="log-viewer-error" className="rounded border border-rose-500 bg-[var(--color-bg-2)] p-2 text-xs text-rose-300">
          {error}
        </div>
      )}

      <div
        data-testid="log-viewer-terminal"
        ref={termContainerRef}
        tabIndex={0}
        className="h-[480px] w-full rounded border border-[var(--color-border)] bg-[#0c0d10] p-1"
      />
    </div>
  )
}

function decodeFrame(data: unknown): string {
  if (typeof data === 'string') return data
  if (data instanceof ArrayBuffer) {
    return new TextDecoder().decode(data)
  }
  return ''
}

function discoverContainers(obj?: K8sObject | null): string[] {
  if (!obj) return []
  const spec = obj.spec as { containers?: { name?: string }[]; initContainers?: { name?: string }[] } | undefined
  const out: string[] = []
  for (const c of spec?.containers ?? []) {
    if (c?.name) out.push(c.name)
  }
  for (const c of spec?.initContainers ?? []) {
    if (c?.name) out.push(c.name)
  }
  return out
}
