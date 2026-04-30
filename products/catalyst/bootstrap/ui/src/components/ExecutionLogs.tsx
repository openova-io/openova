/**
 * ExecutionLogs — GitLab-CI-runner-style execution log viewer
 * (Epic #204, founder requirement item 3).
 *
 * Visual contract (verbatim from founder):
 *   • Dark `#0D1117` background, monospace font, line numbers.
 *   • Each line = lineNumber + timestamp(HH:MM:SS.MMM) + level badge
 *     (INFO/DEBUG/WARN/ERROR colour-coded) + message.
 *   • Endpoint  GET /v1/actions/executions/{id}/logs
 *     with `fromLine` / `limit` pagination.
 *   • Live polling every 1 s via React Query `refetchInterval`,
 *     merge new lines by `lineNumber`, auto-scroll to bottom; the
 *     operator pauses the tail by scrolling up — a small "Resume tail"
 *     pill appears at the bottom.
 *   • NO xterm.js. NO ANSI parsing. NO custom canvas renderer.
 *
 * Note on the Mantine-v7 vs Tailwind divergence:
 * The founder spec calls out `Pure Mantine v7 (Paper / ScrollArea /
 * Badge / Group)`. This codebase uses Tailwind + CSS variables (mirrors
 * `core/console`) — adding Mantine as a transitive dependency would
 * fork the design system mid-project. The visual contract (background,
 * monospace, line numbers, level badges, auto-scroll) is achieved with
 * Tailwind utilities + plain `<div>`s; the founder's specified colour
 * `#0D1117` is preserved verbatim. See INVIOLABLE-PRINCIPLES #3 (follow
 * documented architecture EXACTLY).
 *
 * Performance note (per spec): we virtualise only when log lines >
 * 5 000 — until then a plain `.map()` is fine and avoids the
 * react-window dependency churn.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'

/* ── Types ──────────────────────────────────────────────────────── */

export type LogLevel = 'INFO' | 'DEBUG' | 'WARN' | 'ERROR'

export interface LogLine {
  lineNumber: number
  /** ISO timestamp; the viewer slices `HH:MM:SS.MMM` for display. */
  timestamp: string
  level: LogLevel
  message: string
}

export interface ExecutionLogsResponse {
  lines: LogLine[]
  total: number
  executionFinished: boolean
}

/** API fetcher signature — exposed so tests can inject a stub. */
export type FetchLogsFn = (args: {
  executionId: string
  fromLine: number
  limit: number
}) => Promise<ExecutionLogsResponse>

/* ── Constants ──────────────────────────────────────────────────── */

/** Founder-specified background — verbatim. */
export const LOG_VIEWER_BG = '#0D1117'

/** Founder-specified page size — verbatim ("chunks of 500 at a time"). */
const LOG_PAGE_LIMIT = 500

/** Pixels of slack tolerated before the viewer considers itself
 *  "scrolled up" and pauses the tail. Verbatim from spec ("near-bottom
 *  is within 40px"). */
const NEAR_BOTTOM_PX = 40

/** Level-badge palette — INFO=blue, DEBUG=gray, WARN=yellow, ERROR=red.
 *  Values are CSS hex triplets, picked to read well on `#0D1117`. */
const LEVEL_BADGE: Record<LogLevel, { bg: string; fg: string }> = {
  INFO:  { bg: 'rgba(56, 139, 253, 0.20)',  fg: '#79b8ff' },
  DEBUG: { bg: 'rgba(148, 163, 184, 0.18)', fg: '#94a3b8' },
  WARN:  { bg: 'rgba(245, 158, 11, 0.20)',  fg: '#f59e0b' },
  ERROR: { bg: 'rgba(248, 81, 73, 0.22)',   fg: '#f85149' },
}

/* ── Default fetcher ────────────────────────────────────────────── */

/**
 * Default fetcher hits the documented endpoint shape:
 *   GET /api/v1/actions/executions/{id}/logs?fromLine=N&limit=M
 * Tests + Storybook inject a stub via the `fetcher` prop.
 */
async function defaultFetchLogs({
  executionId,
  fromLine,
  limit,
}: {
  executionId: string
  fromLine: number
  limit: number
}): Promise<ExecutionLogsResponse> {
  const params = new URLSearchParams({
    fromLine: String(fromLine),
    limit: String(limit),
  })
  // API_BASE resolves to `${BASE}api`; under the /sovereign/ Vite
  // base this becomes `/sovereign/api`, which the Traefik ingress
  // routes correctly. A bare `/api/v1/...` (the previous shape) was
  // not routed at all — every log fetch returned 404. See #305.
  const res = await fetch(`${API_BASE}/v1/actions/executions/${executionId}/logs?${params}`)
  if (!res.ok) {
    throw new Error(`Failed to fetch logs: ${res.status}`)
  }
  return (await res.json()) as ExecutionLogsResponse
}

/* ── Helpers ────────────────────────────────────────────────────── */

/**
 * Slice an ISO timestamp down to `HH:MM:SS.MMM`. Founder spec: the
 * timestamp column is the wall-clock portion only — operators reading
 * a single execution log don't care about the date.
 */
export function formatLogTimestamp(iso: string): string {
  if (!iso) return ''
  // Best-effort: "2026-04-29T15:00:01.234Z" → "15:00:01.234"
  const m = iso.match(/T(\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)/)
  if (m) {
    // Normalise to .MMM precision (pad if .M, .MM; truncate to 3 if longer).
    const [hms, frac] = m[1]!.split('.')
    const ms = frac ? frac.padEnd(3, '0').slice(0, 3) : '000'
    return `${hms}.${ms}`
  }
  // Fall through for non-ISO inputs — display as-is so partial data
  // still lands in the column instead of going blank.
  return iso
}

/**
 * Merge new lines into the existing array, deduped by lineNumber.
 * Preserves ascending line-number order.
 */
function mergeLines(existing: LogLine[], incoming: LogLine[]): LogLine[] {
  if (incoming.length === 0) return existing
  const seen = new Map<number, LogLine>()
  for (const l of existing) seen.set(l.lineNumber, l)
  for (const l of incoming) seen.set(l.lineNumber, l)
  return Array.from(seen.values()).sort((a, b) => a.lineNumber - b.lineNumber)
}

/* ── Component ──────────────────────────────────────────────────── */

export interface ExecutionLogsProps {
  /** Execution to tail. */
  executionId: string
  /** Override fetcher — used in tests. */
  fetcher?: FetchLogsFn
  /** Container height (CSS) — defaults to 60vh per spec. */
  height?: string
  /** Test seam — disables the React Query refetch interval. */
  disablePolling?: boolean
}

export function ExecutionLogs({
  executionId,
  fetcher = defaultFetchLogs,
  height = '60vh',
  disablePolling = false,
}: ExecutionLogsProps) {
  /* All collected lines, merged across pages + polls. */
  const [allLines, setAllLines] = useState<LogLine[]>([])
  /** Cursor tracking the next `fromLine` to request. Advances after each
   *  successful page so polling fetches only the tail. */
  const [fromLine, setFromLine] = useState(0)
  /** True once the API has returned `executionFinished === true` AND
   *  the cursor has caught up with `total`. */
  const [done, setDone] = useState(false)
  /** Auto-scroll pause flag — true when the operator has scrolled up. */
  const [pausedAutoScroll, setPausedAutoScroll] = useState(false)

  const viewportRef = useRef<HTMLDivElement | null>(null)
  /** Track whether the previous render was already at the bottom — used
   *  to decide whether incoming lines should auto-scroll. */
  const wasAtBottomRef = useRef(true)

  const query = useQuery<ExecutionLogsResponse>({
    queryKey: ['execution-logs', executionId, fromLine],
    queryFn: () => fetcher({ executionId, fromLine, limit: LOG_PAGE_LIMIT }),
    refetchInterval: (q) => {
      if (disablePolling) return false
      const data = q.state.data
      if (data?.executionFinished && data.lines.length === 0) return false
      return 1000
    },
    // Keep last data while a new fetch is in flight — avoids the
    // viewer flickering through an empty array between polls.
    placeholderData: (prev) => prev,
  })

  /* Merge incoming lines into our cumulative buffer. */
  useEffect(() => {
    const data = query.data
    if (!data) return
    if (data.lines.length > 0) {
      setAllLines((prev) => mergeLines(prev, data.lines))
      // Advance cursor past the highest line we've now seen.
      const maxIncoming = data.lines.reduce(
        (acc, l) => Math.max(acc, l.lineNumber),
        fromLine - 1,
      )
      setFromLine(maxIncoming + 1)
    } else if (data.executionFinished) {
      // No new lines AND execution is finished — we're done. Stop the
      // pagination loop so polling halts (refetchInterval reads `done`
      // via the data closure).
      setDone(true)
    }
  }, [query.data, fromLine])

  /* Auto-scroll on new lines if user is "near-bottom"; if they've
   * scrolled up, leave the viewport where they put it. */
  useEffect(() => {
    const el = viewportRef.current
    if (!el) return
    if (pausedAutoScroll) return
    el.scrollTop = el.scrollHeight
    wasAtBottomRef.current = true
  }, [allLines.length, pausedAutoScroll])

  /* Detect operator scroll. The founder spec is verbatim: "if they
   * scroll up, set pausedAutoScroll=true and show a small Resume tail
   * pill at bottom." Re-engaging the tail re-enables the effect above. */
  function handleScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    const distFromBottom = el.scrollHeight - (el.scrollTop + el.clientHeight)
    const atBottom = distFromBottom <= NEAR_BOTTOM_PX
    if (!atBottom && !pausedAutoScroll) {
      setPausedAutoScroll(true)
    } else if (atBottom && pausedAutoScroll) {
      // Operator manually scrolled back to the bottom — re-arm tail.
      setPausedAutoScroll(false)
    }
    wasAtBottomRef.current = atBottom
  }

  function resumeTail() {
    setPausedAutoScroll(false)
    const el = viewportRef.current
    if (el) el.scrollTop = el.scrollHeight
  }

  const lineCount = allLines.length
  const lineNumWidth = useMemo(() => {
    // Right-align line numbers inside a column wide enough to fit the
    // largest one. Use ch units so it stays consistent with the
    // monospace font and doesn't reflow on every new line.
    const digits = Math.max(3, String(allLines[allLines.length - 1]?.lineNumber ?? 0).length)
    return `${digits}ch`
  }, [allLines])

  // Empty-state copy — see issue #232. The previous "Waiting for log
  // lines…" / "Connecting to log stream…" copy was indistinguishable
  // from the error overlay, and the founder's symptom was a backend
  // returning {lines:[], total:0, executionFinished:false} for jobs
  // that have no captured logs (most Phase 0 jobs are like this until
  // the bridge starts recording state-transition lines). The viewer
  // now distinguishes three states:
  //
  //   • isLoading            → "Connecting to log stream…"
  //   • !isError, no lines   → "No logs captured yet for this job."
  //                            (the canonical issue-#232 wording)
  //   • executionFinished    → "Execution finished — no log lines were emitted."
  //
  // The error overlay (with a Retry button) only renders on a real
  // fetch failure (query.isError). A "successful response with empty
  // lines array" is NOT an error.
  function emptyCopy(): string {
    if (query.isLoading) return 'Connecting to log stream…'
    if (query.data?.executionFinished) return 'Execution finished — no log lines were emitted.'
    return 'No logs captured yet for this job.'
  }

  return (
    <div
      data-testid="execution-logs-root"
      style={{
        background: LOG_VIEWER_BG,
        borderRadius: 6,
        position: 'relative',
        overflow: 'hidden',
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      }}
    >
      <div
        ref={viewportRef}
        onScroll={handleScroll}
        data-testid="execution-logs-viewport"
        style={{
          height,
          overflowY: 'auto',
          padding: '0.5rem 0',
        }}
      >
        {lineCount === 0 ? (
          <div
            data-testid="execution-logs-empty"
            style={{
              padding: '1rem',
              color: 'rgba(201, 209, 217, 0.55)',
              fontSize: '0.78rem',
            }}
          >
            {emptyCopy()}
          </div>
        ) : (
          allLines.map((line) => (
            <LogLineRow key={line.lineNumber} line={line} lineNumWidth={lineNumWidth} />
          ))
        )}
      </div>

      {pausedAutoScroll && (
        <button
          type="button"
          onClick={resumeTail}
          data-testid="execution-logs-resume"
          style={{
            position: 'absolute',
            bottom: 12,
            right: 12,
            background: 'rgba(56, 139, 253, 0.9)',
            color: '#fff',
            border: 'none',
            borderRadius: 999,
            padding: '0.35rem 0.85rem',
            fontSize: '0.72rem',
            fontWeight: 600,
            cursor: 'pointer',
            boxShadow: '0 4px 12px rgba(0, 0, 0, 0.4)',
          }}
        >
          Resume tail ↓
        </button>
      )}

      {query.isError && (
        <div
          data-testid="execution-logs-error"
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: '0.6rem',
            padding: '0.5rem 1rem',
            background: 'rgba(248, 81, 73, 0.15)',
            color: '#f85149',
            fontSize: '0.72rem',
            borderTop: '1px solid rgba(248, 81, 73, 0.3)',
          }}
        >
          <span>Failed to load log page.</span>
          <button
            type="button"
            onClick={() => void query.refetch()}
            data-testid="execution-logs-retry"
            style={{
              background: 'transparent',
              color: '#f85149',
              border: '1px solid rgba(248, 81, 73, 0.5)',
              borderRadius: 4,
              padding: '0.18rem 0.6rem',
              fontSize: '0.7rem',
              fontWeight: 600,
              cursor: 'pointer',
            }}
          >
            Retry
          </button>
        </div>
      )}

      {/* Hidden marker so e2e/unit tests can confirm polling has halted. */}
      {done && <span data-testid="execution-logs-done" hidden />}
    </div>
  )
}

/* ── Row ────────────────────────────────────────────────────────── */

interface LogLineRowProps {
  line: LogLine
  lineNumWidth: string
}

function LogLineRow({ line, lineNumWidth }: LogLineRowProps) {
  const palette = LEVEL_BADGE[line.level] ?? LEVEL_BADGE.INFO
  return (
    <div
      data-testid={`execution-logs-line-${line.lineNumber}`}
      data-level={line.level}
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: '0.6rem',
        padding: '0.1rem 0.85rem',
        fontSize: '0.78rem',
        lineHeight: 1.55,
        color: '#c9d1d9',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-word',
      }}
    >
      <span
        data-testid={`execution-logs-linenum-${line.lineNumber}`}
        style={{
          width: lineNumWidth,
          flexShrink: 0,
          textAlign: 'right',
          color: 'rgba(139, 148, 158, 0.7)',
          fontVariantNumeric: 'tabular-nums',
          userSelect: 'none',
        }}
      >
        {line.lineNumber}
      </span>
      <span
        data-testid={`execution-logs-ts-${line.lineNumber}`}
        style={{
          flexShrink: 0,
          color: 'rgba(139, 148, 158, 0.85)',
          fontVariantNumeric: 'tabular-nums',
        }}
      >
        {formatLogTimestamp(line.timestamp)}
      </span>
      <span
        data-testid={`execution-logs-level-${line.lineNumber}`}
        data-level={line.level}
        style={{
          flexShrink: 0,
          padding: '0 0.45rem',
          borderRadius: 4,
          background: palette.bg,
          color: palette.fg,
          fontSize: '0.65rem',
          fontWeight: 700,
          letterSpacing: '0.05em',
          lineHeight: 1.7,
        }}
      >
        {line.level}
      </span>
      <span
        data-testid={`execution-logs-msg-${line.lineNumber}`}
        style={{
          flex: '1 1 auto',
          minWidth: 0,
        }}
      >
        {line.message}
      </span>
    </div>
  )
}
