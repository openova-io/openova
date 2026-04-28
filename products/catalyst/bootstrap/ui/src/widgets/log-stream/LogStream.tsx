/**
 * LogStream — `tail -f` equivalent log viewer rendering the live SSE
 * event stream from the catalyst-api during provisioning.
 *
 * Closes issue #122 (`[I] ux: design SSE event log streaming pane during
 * StepProvisioning — tail -f equivalent in browser`).
 *
 * Features:
 *  - Auto-scroll to newest line, with a "scroll lock" toggle when the
 *    user scrolls up to inspect history (matches modern terminal UX).
 *  - Per-phase filter — clicking a phase row in the bootstrap-progress
 *    widget passes its id here, and the log scopes to that phase.
 *  - Per-level filter — info/warn/error toggles (sticky chips).
 *  - Live free-text grep (case-insensitive, runs over the visible window).
 *  - Copy-all-visible button (always shows the currently filtered view).
 *  - Connection-state pill — `connecting | streaming | completed | failed`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 ("waterfall is the contract") +
 * #2 ("never compromise from quality"), this widget is presentational and
 * consumes the real ProvisioningEvent stream — it does NOT mock data.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { Copy, Lock, Unlock, Search, X } from 'lucide-react'
import { findPhase } from '@/shared/constants/bootstrap-phases'
import type {
  ProvisioningEvent,
  ConnectionStatus,
  EventLevel,
} from '@/shared/lib/useProvisioningStream'

export interface LogStreamProps {
  /** Live event stream from useProvisioningStream. */
  events: ProvisioningEvent[]
  /** Connection state — drives the status pill. */
  connection: ConnectionStatus
  /** Stream-level error message, if any (rendered as a sticky banner). */
  streamError?: string | null
  /** When set, scope the log to events from this phase id. */
  focusedPhaseId?: string | null
  /** Clear the focused phase filter (clicking the X chip). */
  onClearFocus?: () => void
  /** Optional: show the per-level filter chip row (default: true). */
  showLevelFilters?: boolean
  /** Compact mode — denser font, no description column. */
  compact?: boolean
}

const LEVEL_COLORS: Record<EventLevel, { fg: string; bg: string }> = {
  info: { fg: '#94A3B8', bg: 'transparent' },
  warn: { fg: '#FBBF24', bg: 'rgba(251,191,36,0.05)' },
  error: { fg: '#F87171', bg: 'rgba(248,113,113,0.07)' },
}

const CONNECTION_COLORS: Record<ConnectionStatus, { fg: string; bg: string; label: string }> = {
  disconnected: { fg: 'var(--wiz-text-sub)', bg: 'var(--wiz-bg-xs)',         label: 'Idle' },
  connecting:   { fg: '#FBBF24',              bg: 'rgba(251,191,36,0.08)',    label: 'Connecting…' },
  streaming:    { fg: '#4ADE80',              bg: 'rgba(74,222,128,0.08)',    label: 'Streaming' },
  completed:    { fg: 'var(--wiz-accent)',     bg: 'rgba(56,189,248,0.08)',    label: 'Completed' },
  failed:       { fg: '#F87171',              bg: 'rgba(248,113,113,0.08)',   label: 'Failed' },
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso.slice(11, 19)
  // Local-time HH:MM:SS — concise, predictable, terminal-like.
  return d.toTimeString().slice(0, 8)
}

export function LogStream({
  events,
  connection,
  streamError,
  focusedPhaseId,
  onClearFocus,
  showLevelFilters = true,
  compact = false,
}: LogStreamProps) {
  const [autoScroll, setAutoScroll] = useState(true)
  const [grep, setGrep] = useState('')
  const [activeLevels, setActiveLevels] = useState<Set<EventLevel>>(
    () => new Set<EventLevel>(['info', 'warn', 'error']),
  )
  const scrollerRef = useRef<HTMLDivElement>(null)

  // Filter pipeline — phase, level, grep.
  const visible = useMemo(() => {
    const needle = grep.trim().toLowerCase()
    return events.filter((ev) => {
      if (focusedPhaseId && ev.phase !== focusedPhaseId) return false
      if (!activeLevels.has(ev.level)) return false
      if (needle && !ev.message.toLowerCase().includes(needle) && !ev.phase.toLowerCase().includes(needle)) return false
      return true
    })
  }, [events, focusedPhaseId, activeLevels, grep])

  // Auto-scroll on new events when not locked.
  useEffect(() => {
    if (!autoScroll) return
    const el = scrollerRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
  }, [visible.length, autoScroll])

  // Detect manual scroll-up → auto-disable autoScroll.
  function handleScroll() {
    const el = scrollerRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 20
    if (autoScroll && !atBottom) setAutoScroll(false)
    if (!autoScroll && atBottom) setAutoScroll(true)
  }

  function toggleLevel(level: EventLevel) {
    setActiveLevels((prev) => {
      const next = new Set(prev)
      if (next.has(level)) next.delete(level)
      else next.add(level)
      return next
    })
  }

  async function copyAll() {
    const text = visible
      .map((ev) => `${formatTime(ev.time)}  [${ev.phase}] ${ev.level.toUpperCase()}  ${ev.message}`)
      .join('\n')
    await navigator.clipboard.writeText(text)
  }

  const conn = CONNECTION_COLORS[connection]
  const focusedPhase = focusedPhaseId ? findPhase(focusedPhaseId) : null

  // Per-level event counts for the filter chips.
  const levelCounts = useMemo(() => {
    const c: Record<EventLevel, number> = { info: 0, warn: 0, error: 0 }
    for (const ev of events) {
      if (focusedPhaseId && ev.phase !== focusedPhaseId) continue
      c[ev.level] = (c[ev.level] ?? 0) + 1
    }
    return c
  }, [events, focusedPhaseId])

  return (
    <section aria-label="Provisioning live log" className="ls">
      {/* Header: connection pill, scope chip, controls */}
      <header className="ls-header">
        <div className="ls-pill" style={{ color: conn.fg, background: conn.bg, borderColor: conn.fg }}>
          <span className="ls-pill-dot" style={{ background: conn.fg }} />
          {conn.label}
        </div>

        {focusedPhase && (
          <button
            type="button"
            onClick={onClearFocus}
            className="ls-scope-chip"
            title="Clear phase filter"
          >
            <span className="ls-scope-label">phase</span>
            <code>{focusedPhase.id}</code>
            <X size={11} />
          </button>
        )}

        <div className="ls-grep">
          <Search size={11} className="ls-grep-icon" />
          <input
            type="text"
            value={grep}
            onChange={(e) => setGrep(e.target.value)}
            placeholder="grep…"
            spellCheck={false}
            aria-label="Filter log lines"
            className="ls-grep-input"
          />
        </div>

        <div className="ls-actions">
          <button
            type="button"
            onClick={() => setAutoScroll((v) => !v)}
            title={autoScroll ? 'Pause auto-scroll' : 'Resume auto-scroll'}
            className="ls-icon-btn"
          >
            {autoScroll ? <Unlock size={11} /> : <Lock size={11} />}
            <span className="ls-icon-btn-label">{autoScroll ? 'follow' : 'paused'}</span>
          </button>
          <button
            type="button"
            onClick={copyAll}
            disabled={visible.length === 0}
            title="Copy visible log lines"
            className="ls-icon-btn"
          >
            <Copy size={11} />
            <span className="ls-icon-btn-label">copy</span>
          </button>
        </div>
      </header>

      {/* Per-level filters */}
      {showLevelFilters && (
        <div className="ls-filters">
          {(['info', 'warn', 'error'] as EventLevel[]).map((level) => {
            const active = activeLevels.has(level)
            const c = LEVEL_COLORS[level]
            const count = levelCounts[level]
            return (
              <button
                key={level}
                type="button"
                onClick={() => toggleLevel(level)}
                className="ls-level-chip"
                style={{
                  color: active ? c.fg : 'var(--wiz-text-sub)',
                  background: active ? c.bg : 'transparent',
                  borderColor: active ? c.fg : 'var(--wiz-border-sub)',
                  opacity: active ? 1 : 0.5,
                }}
              >
                <span style={{ textTransform: 'uppercase', fontWeight: 700 }}>{level}</span>
                <span style={{ opacity: 0.7 }}>{count}</span>
              </button>
            )
          })}
          <div style={{ flex: 1 }} />
          <span className="ls-counter">
            {visible.length} / {events.length} lines
          </span>
        </div>
      )}

      {streamError && (
        <div className="ls-error">{streamError}</div>
      )}

      {/* The actual scroller */}
      <div
        ref={scrollerRef}
        className={`ls-scroller ${compact ? 'ls-scroller-compact' : ''}`}
        onScroll={handleScroll}
        role="log"
        aria-live="polite"
        aria-relevant="additions"
      >
        {visible.length === 0 && (
          <div className="ls-empty">
            {events.length === 0
              ? connection === 'streaming'
                ? 'Waiting for first event…'
                : connection === 'connecting'
                  ? 'Connecting to deployment stream…'
                  : 'No events yet.'
              : 'No lines match the current filters.'}
          </div>
        )}
        {visible.map((ev, i) => (
          <LogLine key={`${ev.time}-${i}`} ev={ev} />
        ))}
      </div>

      <style>{`
        .ls {
          display: flex; flex-direction: column;
          background: var(--wiz-bg-xs);
          border: 1px solid var(--wiz-border-sub);
          border-radius: 10px;
          overflow: hidden;
          height: 100%;
          min-height: 0;
        }
        .ls-header {
          display: flex; align-items: center; gap: 8px;
          padding: 8px 10px;
          border-bottom: 1px solid var(--wiz-border-sub);
          background: var(--wiz-bg-sub);
          flex-wrap: wrap;
        }
        .ls-pill {
          display: inline-flex; align-items: center; gap: 5px;
          padding: 2px 9px; border-radius: 12px;
          font-size: 10px; font-weight: 700;
          letter-spacing: 0.04em;
          border: 1px solid;
        }
        .ls-pill-dot {
          width: 6px; height: 6px; border-radius: 50%;
          animation: ls-pulse 1.6s ease-in-out infinite;
        }
        @keyframes ls-pulse {
          0%, 100% { opacity: 1; }
          50%      { opacity: 0.35; }
        }
        .ls-scope-chip {
          display: inline-flex; align-items: center; gap: 4px;
          padding: 2px 7px; border-radius: 5px;
          background: rgba(56,189,248,0.08);
          border: 1px solid rgba(56,189,248,0.25);
          color: var(--wiz-accent);
          font-size: 10px;
          cursor: pointer;
          font-family: 'Inter', sans-serif;
        }
        .ls-scope-chip code {
          font-family: 'JetBrains Mono', monospace;
          font-size: 10px;
        }
        .ls-scope-label {
          opacity: 0.7;
          text-transform: uppercase;
          font-weight: 700;
          letter-spacing: 0.05em;
          font-size: 9px;
        }
        .ls-grep {
          flex: 1; min-width: 100px;
          display: inline-flex; align-items: center; gap: 5px;
          padding: 0 8px; height: 24px;
          border: 1px solid var(--wiz-border-sub);
          background: var(--wiz-bg-input);
          border-radius: 6px;
        }
        .ls-grep-icon { color: var(--wiz-text-hint); flex-shrink: 0; }
        .ls-grep-input {
          flex: 1; min-width: 0; height: 100%;
          background: transparent; border: none; outline: none;
          color: var(--wiz-text-hi); font-size: 11px;
          font-family: 'JetBrains Mono', monospace;
        }
        .ls-grep-input::placeholder { color: var(--wiz-text-hint); }
        .ls-actions { display: inline-flex; gap: 5px; }
        .ls-icon-btn {
          display: inline-flex; align-items: center; gap: 4px;
          padding: 3px 7px; height: 24px; border-radius: 6px;
          background: var(--wiz-bg-input);
          border: 1px solid var(--wiz-border-sub);
          color: var(--wiz-text-md);
          font-size: 9px; font-weight: 700;
          letter-spacing: 0.05em; text-transform: uppercase;
          cursor: pointer; transition: background 0.15s;
          font-family: 'Inter', sans-serif;
        }
        .ls-icon-btn:hover:not(:disabled) { background: var(--wiz-bg-sub); }
        .ls-icon-btn:disabled { opacity: 0.4; cursor: default; }
        .ls-icon-btn-label { font-size: 9px; }
        .ls-filters {
          display: flex; align-items: center; gap: 6px;
          padding: 6px 10px;
          border-bottom: 1px solid var(--wiz-border-sub);
          flex-wrap: wrap;
        }
        .ls-level-chip {
          display: inline-flex; align-items: center; gap: 5px;
          padding: 2px 7px; border-radius: 5px;
          font-size: 9px;
          border: 1px solid;
          cursor: pointer; transition: all 0.15s;
          font-family: 'Inter', sans-serif;
        }
        .ls-counter {
          font-size: 10px; color: var(--wiz-text-sub);
          font-family: 'JetBrains Mono', monospace;
        }
        .ls-error {
          padding: 6px 10px;
          background: rgba(248,113,113,0.07);
          border-bottom: 1px solid rgba(248,113,113,0.25);
          color: #F87171;
          font-size: 11px;
          font-family: 'JetBrains Mono', monospace;
        }
        .ls-scroller {
          flex: 1; min-height: 0; overflow-y: auto;
          padding: 8px 10px;
          font-family: 'JetBrains Mono', monospace;
          font-size: 11px;
          line-height: 1.6;
          background: rgba(2,6,15,0.45);
        }
        .ls-scroller-compact { font-size: 10px; line-height: 1.5; }
        .ls-empty {
          color: var(--wiz-text-hint);
          font-style: italic;
          font-family: 'Inter', sans-serif;
          font-size: 11px;
          padding: 16px 4px;
          text-align: center;
        }
      `}</style>
    </section>
  )
}

function LogLine({ ev }: { ev: ProvisioningEvent }) {
  const c = LEVEL_COLORS[ev.level]
  const phase = findPhase(ev.phase)
  return (
    <div className="ls-line" style={{ background: c.bg }}>
      <span className="ls-line-time">{formatTime(ev.time)}</span>
      <span
        className="ls-line-phase"
        title={phase ? phase.label : ev.phase}
      >
        {ev.phase}
      </span>
      <span className="ls-line-level" style={{ color: c.fg }}>
        {ev.level.toUpperCase().padEnd(5)}
      </span>
      <span className="ls-line-msg" style={{ color: c.fg }}>
        {ev.message}
      </span>

      <style>{`
        .ls-line {
          display: grid;
          grid-template-columns: 64px minmax(0, 14ch) 5ch 1fr;
          gap: 8px;
          padding: 1px 4px;
          border-radius: 3px;
        }
        .ls-line-time { color: var(--wiz-text-hint); font-size: 10px; }
        .ls-line-phase {
          color: var(--wiz-accent);
          white-space: nowrap;
          overflow: hidden;
          text-overflow: ellipsis;
          font-weight: 600;
        }
        .ls-line-level {
          font-weight: 700;
          letter-spacing: 0.04em;
          font-size: 10px;
        }
        .ls-line-msg {
          word-break: break-word;
          white-space: pre-wrap;
        }
      `}</style>
    </div>
  )
}
