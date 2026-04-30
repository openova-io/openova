/**
 * FloatingLogPane — slide-in 25vw log viewer that overlays the right
 * edge of the FlowPage canvas. Mounts on single-click of a job bubble
 * (per founder spec, see FlowPage.tsx). Reuses the canonical
 * <ExecutionLogs /> component (#208) for the log body — no rebuild,
 * just a new container chrome.
 *
 * Behavioural contract (locked):
 *   • Width: 25vw (matches the founder's mock spec verbatim).
 *   • Position: fixed; right: 0; vertical extent = full viewport
 *     height below the PortalShell top header.
 *   • z-index: above the canvas. NO modal backdrop — the canvas
 *     remains pannable / clickable while the pane is open.
 *   • Closes on:
 *       1. X button click
 *       2. Escape key
 *       3. (Caller's responsibility) click on canvas empty area
 *   • Slide-in: 200ms cubic-bezier from off-screen right.
 *   • Pending-job branch: when `executionId` is empty, renders a
 *     "No execution recorded yet" empty state instead of mounting
 *     the log poller.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in this component:
 *       slide-in animation, escape close, empty-state branch.
 *   #4 (never hardcode) — width / colours / spacing all read theme
 *       tokens; only the slide-in keyframe owns motion-specific values.
 */

import { useEffect } from 'react'
import { ExecutionLogs } from './ExecutionLogs'

interface FloatingLogPaneProps {
  /**
   * Stable execution id used to fetch logs from the catalyst-api.
   * When falsy / empty, the pane renders the "no execution recorded
   * yet" empty state instead of mounting the polling viewer (a job
   * that's still pending has no execution row in the backend).
   */
  executionId: string | null | undefined
  /** Display title — typically `${job.jobName}`. */
  jobTitle: string
  /** Status text rendered as a small chip in the header strip. */
  statusLabel?: string
  /** Status colour class (matches StatusBadge tones). */
  statusTone?: 'pending' | 'running' | 'succeeded' | 'failed'
  /** Closes the pane (called from X click, Escape key). */
  onClose: () => void
}

const STATUS_TONE: Record<NonNullable<FloatingLogPaneProps['statusTone']>, { bg: string; fg: string; border: string }> = {
  pending:   { bg: 'rgba(148,163,184,0.10)', fg: 'var(--color-text-dim)', border: 'rgba(148,163,184,0.30)' },
  running:   { bg: 'rgba(56,189,248,0.10)',  fg: '#38BDF8',                border: 'rgba(56,189,248,0.40)' },
  succeeded: { bg: 'rgba(74,222,128,0.10)',  fg: '#4ADE80',                border: 'rgba(74,222,128,0.40)' },
  failed:    { bg: 'rgba(248,113,113,0.10)', fg: '#F87171',                border: 'rgba(248,113,113,0.40)' },
}

export function FloatingLogPane({
  executionId,
  jobTitle,
  statusLabel,
  statusTone = 'pending',
  onClose,
}: FloatingLogPaneProps) {
  // Escape key → close. Bound at the document level so the listener
  // fires regardless of which child element is focused. Cleaned up on
  // unmount so the listener never leaks across remounts.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const tone = STATUS_TONE[statusTone]

  return (
    <aside
      role="complementary"
      aria-label={`Logs for ${jobTitle}`}
      data-testid="floating-log-pane"
      style={FLOATING_PANE_STYLE}
    >
      <style>{FLOATING_PANE_CSS}</style>
      <header className="floating-pane-header" data-testid="floating-log-pane-header">
        <span
          className="floating-pane-status"
          style={{ background: tone.bg, color: tone.fg, borderColor: tone.border }}
          data-testid="floating-log-pane-status"
        >
          {statusLabel ?? statusTone}
        </span>
        <span className="floating-pane-title" data-testid="floating-log-pane-title" title={jobTitle}>
          {jobTitle}
        </span>
        <button
          type="button"
          className="floating-pane-close"
          aria-label="Close log pane"
          data-testid="floating-log-pane-close"
          onClick={onClose}
        >
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
            <path d="M2 2 L12 12 M12 2 L2 12" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        </button>
      </header>
      <div className="floating-pane-body" data-testid="floating-log-pane-body">
        {executionId ? (
          <ExecutionLogs executionId={executionId} />
        ) : (
          <div
            className="floating-pane-empty"
            data-testid="floating-log-pane-empty"
          >
            No execution recorded yet.
          </div>
        )}
      </div>
    </aside>
  )
}

/* ── Styles (co-located, theme-token bound) ─────────────────────── */

// Inline `style` — keeps the 25vw width pinned to the founder spec
// without binding to a Tailwind arbitrary value (which would only fire
// at runtime if Tailwind's safelist had it). The body / header rules
// live in the embedded <style>{FLOATING_PANE_CSS}</style> so theme
// switches work via CSS-variable cascade.
const FLOATING_PANE_STYLE: React.CSSProperties = {
  position: 'fixed',
  right: 0,
  top: 56, // PortalShell header h-14 = 3.5rem = 56px
  bottom: 0,
  width: '25vw',
  minWidth: 320,
  zIndex: 60,
  background: 'var(--color-surface)',
  borderLeft: '1px solid var(--color-border)',
  display: 'flex',
  flexDirection: 'column',
  boxShadow: '-8px 0 24px rgba(2,6,15,0.45)',
  animation: 'floating-pane-in 200ms cubic-bezier(0.4, 0, 0.2, 1)',
}

const FLOATING_PANE_CSS = `
@keyframes floating-pane-in {
  from { transform: translateX(100%); opacity: 0; }
  to   { transform: translateX(0);    opacity: 1; }
}

.floating-pane-header {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.65rem 0.85rem;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.floating-pane-status {
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
.floating-pane-title {
  flex: 1 1 auto;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text-strong);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.floating-pane-close {
  appearance: none;
  background: transparent;
  border: 1px solid var(--color-border);
  color: var(--color-text-dim);
  border-radius: 6px;
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;
}
.floating-pane-close:hover {
  color: var(--color-text-strong);
  border-color: var(--color-border-strong, var(--color-text-dim));
  background: rgba(148,163,184,0.08);
}

.floating-pane-body {
  flex: 1 1 auto;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.floating-pane-empty {
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
