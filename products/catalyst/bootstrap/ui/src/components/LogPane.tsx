/**
 * LogPane — floating right-edge exec-log pane (issue #351).
 *
 * Replaces the legacy FloatingLogPane. Behavioural contract:
 *
 *   • Width: ~30vw (was 25vw); honours `--log-pane-width` so the
 *     consumer can override per-page if needed. Min width 360px.
 *   • Position: fixed; right: 0; top: 56 (PortalShell header h-14);
 *     bottom: 0. z-index 60 — sits above the canvas, no modal
 *     backdrop (canvas remains pannable while the pane is open).
 *   • Slide-in: 200ms cubic-bezier from off-screen right.
 *   • Full-screen toggle (button + Esc): expands to 100vw / 100vh,
 *     hiding the canvas behind. A second Esc / button click restores
 *     the docked width.
 *   • Embeds {@link LogSearch} above {@link ExecutionLogs} — the pane
 *     owns the LogFilter + match-index state and threads them through.
 *   • Closes on:
 *       1. X button click
 *       2. Escape key (when not in full-screen — first Esc exits
 *          full-screen; second Esc closes the pane)
 *       3. (Caller's responsibility) click on canvas empty area
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in one component:
 *       slide-in, search, full-screen, escape close.
 *   #4 (never hardcode) — width / colours / spacing all read theme
 *       tokens; only the slide-in keyframe owns motion-specific values.
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { ExecutionLogs, type LogLine, LOG_VIEWER_BG, formatLogTimestamp } from './ExecutionLogs'
import { LogSearch, lineMatches, type LogFilter } from './LogSearch'

interface LogPaneProps {
  /**
   * Stable execution id used to fetch logs from the catalyst-api.
   * When falsy / empty AND `fallbackLines` is also empty, the pane
   * renders the "no execution recorded yet" empty state instead of
   * mounting the polling viewer.
   */
  executionId: string | null | undefined
  /**
   * Bug #481 — inline fallback log lines for derived jobs that have no
   * Bridge-allocated Execution row. Phase-0 jobs (`infrastructure:*`,
   * `cluster-bootstrap`) live entirely in the SSE event reducer, so
   * `useJobDetail` returns 404 and `executionId` is null, but the
   * operator still expects to see the captured event log when they
   * click the job. When this prop is non-empty AND `executionId` is
   * null, the pane renders these lines through the same search /
   * filter pipeline as the polling viewer.
   */
  fallbackLines?: readonly LogLine[]
  /** Display title — typically the host job's display name or jobName. */
  jobTitle: string
  /** Status text rendered as a small chip in the header strip. */
  statusLabel?: string
  /** Status colour class (matches StatusBadge tones). */
  statusTone?: 'pending' | 'running' | 'succeeded' | 'failed'
  /** Closes the pane (called from X click, Escape key when docked). */
  onClose: () => void
}

const STATUS_TONE: Record<NonNullable<LogPaneProps['statusTone']>, { bg: string; fg: string; border: string }> = {
  pending:   { bg: 'rgba(148,163,184,0.10)', fg: 'var(--color-text-dim)', border: 'rgba(148,163,184,0.30)' },
  running:   { bg: 'rgba(56,189,248,0.10)',  fg: '#38BDF8',                border: 'rgba(56,189,248,0.40)' },
  succeeded: { bg: 'rgba(74,222,128,0.10)',  fg: '#4ADE80',                border: 'rgba(74,222,128,0.40)' },
  failed:    { bg: 'rgba(248,113,113,0.10)', fg: '#F87171',                border: 'rgba(248,113,113,0.40)' },
}

const EMPTY_FILTER: LogFilter = { query: '', regex: false, levels: new Set() }

export function LogPane({
  executionId,
  fallbackLines,
  jobTitle,
  statusLabel,
  statusTone = 'pending',
  onClose,
}: LogPaneProps) {
  const [filter, setFilter] = useState<LogFilter>(EMPTY_FILTER)
  const [matchCount, setMatchCount] = useState<number>(0)
  const [matchIndex, setMatchIndex] = useState<number>(0)
  const [fullScreen, setFullScreen] = useState<boolean>(false)

  // Reset matchIndex whenever the count drops below it (e.g. operator
  // tightens the query). Bumping it from 0 → 1 the moment results
  // arrive avoids the n/N controls staying stuck disabled.
  useEffect(() => {
    if (matchCount === 0) {
      if (matchIndex !== 0) setMatchIndex(0)
      return
    }
    if (matchIndex === 0) setMatchIndex(1)
    else if (matchIndex > matchCount) setMatchIndex(matchCount)
  }, [matchCount, matchIndex])

  const goPrev = useCallback(() => {
    setMatchIndex((i) => {
      if (matchCount === 0) return 0
      return i <= 1 ? matchCount : i - 1
    })
  }, [matchCount])

  const goNext = useCallback(() => {
    setMatchIndex((i) => {
      if (matchCount === 0) return 0
      return i >= matchCount ? 1 : i + 1
    })
  }, [matchCount])

  const toggleFullScreen = useCallback(() => {
    setFullScreen((v) => !v)
  }, [])

  // Escape: first hop exits full-screen; second hop closes the pane.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      // Don't intercept Esc when an input is the focus target unless
      // it's our search input (which handles its own keyboard).
      e.stopPropagation()
      if (fullScreen) {
        setFullScreen(false)
      } else {
        onClose()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [fullScreen, onClose])

  const tone = STATUS_TONE[statusTone]

  return (
    <aside
      role="complementary"
      aria-label={`Logs for ${jobTitle}`}
      data-testid="log-pane"
      data-fullscreen={fullScreen ? 'true' : 'false'}
      className={`log-pane${fullScreen ? ' is-fullscreen' : ''}`}
    >
      <style>{LOG_PANE_CSS}</style>
      <header className="log-pane-header" data-testid="log-pane-header">
        <span
          className="log-pane-status"
          style={{ background: tone.bg, color: tone.fg, borderColor: tone.border }}
          data-testid="log-pane-status"
        >
          {statusLabel ?? statusTone}
        </span>
        <span className="log-pane-title" data-testid="log-pane-title" title={jobTitle}>
          {jobTitle}
        </span>
        <button
          type="button"
          className="log-pane-icon-btn"
          aria-label={fullScreen ? 'Exit full-screen' : 'Enter full-screen'}
          aria-pressed={fullScreen}
          data-testid="log-pane-fullscreen"
          onClick={toggleFullScreen}
          title={fullScreen ? 'Exit full-screen (Esc)' : 'Full-screen'}
        >
          {fullScreen ? (
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
              <path
                d="M5 1 L5 5 L1 5 M9 1 L9 5 L13 5 M5 13 L5 9 L1 9 M9 13 L9 9 L13 9"
                stroke="currentColor"
                strokeWidth="1.4"
                fill="none"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          ) : (
            <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
              <path
                d="M1 5 L1 1 L5 1 M9 1 L13 1 L13 5 M13 9 L13 13 L9 13 M5 13 L1 13 L1 9"
                stroke="currentColor"
                strokeWidth="1.4"
                fill="none"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          )}
        </button>
        <button
          type="button"
          className="log-pane-icon-btn"
          aria-label="Close log pane"
          data-testid="log-pane-close"
          onClick={onClose}
          title="Close (Esc)"
        >
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
            <path d="M2 2 L12 12 M12 2 L2 12" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        </button>
      </header>

      {executionId ? (
        <>
          <LogSearch
            matchCount={matchCount}
            matchIndex={matchIndex}
            onPrev={goPrev}
            onNext={goNext}
            onFilterChange={setFilter}
          />
          <div className="log-pane-body" data-testid="log-pane-body">
            <ExecutionLogs
              executionId={executionId}
              filter={filter}
              matchIndex={matchIndex}
              onMatchCountChange={setMatchCount}
              height="100%"
            />
          </div>
        </>
      ) : fallbackLines && fallbackLines.length > 0 ? (
        <>
          <LogSearch
            matchCount={matchCount}
            matchIndex={matchIndex}
            onPrev={goPrev}
            onNext={goNext}
            onFilterChange={setFilter}
          />
          <div className="log-pane-body" data-testid="log-pane-body">
            <FallbackLogList
              lines={fallbackLines}
              filter={filter}
              matchIndex={matchIndex}
              onMatchCountChange={setMatchCount}
            />
          </div>
        </>
      ) : (
        <div className="log-pane-empty" data-testid="log-pane-empty">
          No execution recorded yet.
        </div>
      )}
    </aside>
  )
}

/* ── FallbackLogList — render synthetic LogLines without polling ─── */
/**
 * Bug #481 — used when the JobDetail page's selected job is a derived
 * job (Phase-0 tofu, cluster-bootstrap) that has no Bridge-allocated
 * Execution row. The DerivedJob.steps array IS the log content for
 * those jobs; this component renders it through the same dark-theme,
 * line-numbered presentation as ExecutionLogs without the polling /
 * pagination machinery.
 *
 * Visual contract is intentionally identical to ExecutionLogs:
 *   • Background `#0D1117`, monospace, line numbers on the left.
 *   • Search filter applied via LogSearch (case / regex / level).
 *   • Match-index scroll-into-view via `data-match-position`.
 *
 * Pure presentation, no data fetching. Inputs are static; the parent
 * (JobDetail) re-renders this on every reducer update so the lines
 * stream in live as the SSE replay catches up.
 */
interface FallbackLogListProps {
  lines: readonly LogLine[]
  filter: LogFilter
  matchIndex: number
  onMatchCountChange: (n: number) => void
}

function FallbackLogList({
  lines,
  filter,
  matchIndex,
  onMatchCountChange,
}: FallbackLogListProps) {
  const filterActive =
    filter.query.trim().length > 0 || filter.levels.size > 0

  const displayed = useMemo(() => {
    if (!filterActive) return lines
    const out: LogLine[] = []
    for (const ll of lines) {
      if (filter.levels.size > 0 && !filter.levels.has(ll.level)) continue
      if (!lineMatches(ll.message, filter)) continue
      out.push(ll)
    }
    return out
  }, [lines, filter, filterActive])

  useEffect(() => {
    onMatchCountChange(filterActive ? displayed.length : 0)
  }, [displayed, filterActive, onMatchCountChange])

  const lineNumWidth = useMemo(() => {
    const last = displayed[displayed.length - 1]
    const digits = Math.max(3, String(last?.lineNumber ?? 0).length)
    return `${digits}ch`
  }, [displayed])

  return (
    <div
      data-testid="fallback-log-list"
      style={{
        background: LOG_VIEWER_BG,
        borderRadius: 6,
        position: 'relative',
        overflow: 'auto',
        height: '100%',
        fontFamily:
          'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      }}
    >
      {displayed.length === 0 ? (
        <div
          data-testid="fallback-log-empty"
          style={{
            padding: '1rem',
            color: 'rgba(201, 209, 217, 0.55)',
            fontSize: '0.78rem',
          }}
        >
          {filterActive
            ? 'No log lines match the active search.'
            : 'No logs captured yet for this job.'}
        </div>
      ) : (
        displayed.map((line, idx) => {
          const matchPosition = filterActive ? idx + 1 : 0
          const isFocusedMatch = filterActive && matchIndex === matchPosition
          return (
            <div
              key={line.lineNumber}
              data-testid={`fallback-log-line-${line.lineNumber}`}
              data-level={line.level}
              data-match-position={matchPosition || undefined}
              data-focused-match={isFocusedMatch ? 'true' : undefined}
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
                background: isFocusedMatch ? 'rgba(56, 139, 253, 0.18)' : 'transparent',
              }}
            >
              <span
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
                style={{
                  flexShrink: 0,
                  color: 'rgba(139, 148, 158, 0.85)',
                  fontVariantNumeric: 'tabular-nums',
                }}
              >
                {formatLogTimestamp(line.timestamp)}
              </span>
              <span
                style={{
                  flex: '1 1 auto',
                  minWidth: 0,
                }}
              >
                {line.message}
              </span>
            </div>
          )
        })
      )}
    </div>
  )
}

const LOG_PANE_CSS = `
.log-pane {
  position: fixed;
  right: 0;
  top: 56px;
  bottom: 0;
  width: var(--log-pane-width, 30vw);
  min-width: 360px;
  z-index: 60;
  background: var(--color-surface);
  border-left: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  box-shadow: -8px 0 24px rgba(2, 6, 15, 0.45);
  animation: log-pane-in 200ms cubic-bezier(0.4, 0, 0.2, 1);
  transition: width 220ms cubic-bezier(0.4, 0, 0.2, 1),
              top 220ms cubic-bezier(0.4, 0, 0.2, 1),
              left 220ms cubic-bezier(0.4, 0, 0.2, 1);
}
.log-pane.is-fullscreen {
  width: 100vw;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 80;
  border-left: 0;
  box-shadow: 0 0 0 9999px rgba(2, 6, 15, 0.85);
}
@keyframes log-pane-in {
  from { transform: translateX(100%); opacity: 0; }
  to   { transform: translateX(0);    opacity: 1; }
}
.log-pane-header {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  padding: 0.55rem 0.75rem;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.log-pane-status {
  display: inline-flex;
  align-items: center;
  padding: 0.12rem 0.55rem;
  border-radius: 999px;
  font-size: 0.62rem;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  border: 1px solid;
  white-space: nowrap;
}
.log-pane-title {
  flex: 1 1 auto;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text-strong);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.log-pane-icon-btn {
  appearance: none;
  background: transparent;
  border: 1px solid var(--color-border);
  color: var(--color-text-dim);
  border-radius: 6px;
  width: 26px;
  height: 26px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;
  flex-shrink: 0;
}
.log-pane-icon-btn:hover {
  color: var(--color-text-strong);
  border-color: var(--color-text-dim);
  background: rgba(148, 163, 184, 0.08);
}
.log-pane-icon-btn[aria-pressed='true'] {
  color: var(--color-accent, #38BDF8);
  border-color: var(--color-accent, #38BDF8);
  background: rgba(56, 189, 248, 0.10);
}
.log-pane-body {
  flex: 1 1 auto;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.log-pane-body > * {
  flex: 1 1 auto;
  min-height: 0;
}
.log-pane-empty {
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  justify-content: center;
  color: var(--color-text-dim);
  font-size: 0.85rem;
  padding: 2rem 1rem;
  text-align: center;
}
`
