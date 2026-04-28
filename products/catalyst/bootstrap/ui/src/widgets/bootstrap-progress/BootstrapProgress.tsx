/**
 * BootstrapProgress — vertical step-progress indicator for the 11-phase
 * bootstrap kit + the 5 OpenTofu Phase-0 checkpoints.
 *
 * Closes issue #121 (`[I] ux: design wizard step-progress indicator that
 * shows all 11 bootstrap-kit phases — visual checkpoint per component
 * installed`).
 *
 * Design:
 * - One row per phase, in chronological order.
 * - Layer A (OpenTofu) and Layer B (bootstrap-kit) are visually separated
 *   with a section divider so the user sees the hand-off moment.
 * - Each row shows: status icon, phase label, upstream component chip,
 *   description, elapsed time (when running/done).
 * - Failed phase row shows the `failed_at_<id>` sovereign-state marker
 *   inline so operators can correlate with backend logs.
 *
 * The widget is presentational: it consumes a phases map (from
 * useProvisioningStream) and renders. No business logic, no fetches.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") this iterates
 * over ALL_PHASES from shared/constants/bootstrap-phases.ts — adding a
 * new phase to the constants list ripples here automatically.
 */

import { Check, Loader2, Circle, AlertCircle, MinusCircle, RotateCw, BookOpen, ExternalLink } from 'lucide-react'
import { useState } from 'react'
import {
  ALL_PHASES,
  OPENTOFU_PHASES,
  BOOTSTRAP_KIT_PHASES,
  TOTAL_PHASES,
  failedAtSovereignState,
  type BootstrapPhase,
  type PhaseStatus,
} from '@/shared/constants/bootstrap-phases'
import type { PhaseState } from '@/shared/lib/useProvisioningStream'

export interface BootstrapProgressProps {
  /** Phase state map from useProvisioningStream — keyed by phase.id. */
  phases: Record<string, PhaseState>
  /** Optional click handler to focus the log pane on a specific phase. */
  onPhaseClick?: (phaseId: string) => void
  /** Optional currently-focused phase id (highlights the row). */
  focusedPhaseId?: string | null
  /** Compact mode: smaller rows, no descriptions. */
  compact?: boolean
  /**
   * Retry handler invoked when the user clicks "Retry phase" on a failed row.
   * Implementations should POST to
   *   /api/v1/deployments/<id>/phases/<phaseId>/retry
   * and re-open the SSE stream. Closes issue #125 — failed-phase UX.
   * When omitted, the retry button is hidden (e.g. demo screenshots).
   */
  onRetryPhase?: (phaseId: string) => Promise<void> | void
  /**
   * URL the failed-phase row's "Rollback procedure" link points at.
   * Defaults to the canonical runbook anchor. Override to point at an
   * internal copy when serving in air-gap environments.
   */
  rollbackDocsURL?: string
}

/** Default rollback docs URL — anchor on docs/RUNBOOK-PROVISIONING.md. */
const DEFAULT_ROLLBACK_DOCS_URL =
  'https://github.com/openova-io/openova/blob/main/docs/RUNBOOK-PROVISIONING.md#rollback-procedures-per-phase'

const STATUS_COLORS: Record<PhaseStatus, { fg: string; bg: string; border: string }> = {
  pending: {
    fg: 'var(--wiz-text-hint)',
    bg: 'var(--wiz-bg-xs)',
    border: 'var(--wiz-border-sub)',
  },
  running: {
    fg: 'var(--wiz-accent)',
    bg: 'rgba(56,189,248,0.07)',
    border: 'rgba(56,189,248,0.35)',
  },
  done: {
    fg: '#4ADE80',
    bg: 'rgba(74,222,128,0.07)',
    border: 'rgba(74,222,128,0.35)',
  },
  failed: {
    fg: '#F87171',
    bg: 'rgba(248,113,113,0.07)',
    border: 'rgba(248,113,113,0.35)',
  },
  skipped: {
    fg: 'var(--wiz-text-sub)',
    bg: 'var(--wiz-bg-xs)',
    border: 'var(--wiz-border-sub)',
  },
}

function StatusIcon({ status }: { status: PhaseStatus }) {
  const c = STATUS_COLORS[status]
  switch (status) {
    case 'done':
      return <Check size={12} strokeWidth={3} style={{ color: c.fg }} />
    case 'running':
      return <Loader2 size={12} className="animate-spin" style={{ color: c.fg }} />
    case 'failed':
      return <AlertCircle size={12} style={{ color: c.fg }} />
    case 'skipped':
      return <MinusCircle size={12} style={{ color: c.fg }} />
    case 'pending':
    default:
      return <Circle size={10} style={{ color: c.fg }} />
  }
}

function durationLabel(state: PhaseState): string | null {
  if (!state.startedAt) return null
  const start = new Date(state.startedAt).getTime()
  const end = state.endedAt
    ? new Date(state.endedAt).getTime()
    : state.status === 'running' ? Date.now() : start
  const sec = Math.max(0, Math.round((end - start) / 1000))
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}m ${s.toString().padStart(2, '0')}s`
}

function PhaseRow({
  phase,
  state,
  onClick,
  focused,
  compact,
  onRetry,
  rollbackDocsURL,
}: {
  phase: BootstrapPhase
  state: PhaseState
  onClick?: () => void
  focused: boolean
  compact: boolean
  onRetry?: (phaseId: string) => Promise<void> | void
  rollbackDocsURL: string
}) {
  const colors = STATUS_COLORS[state.status]
  const dur = durationLabel(state)
  const clickable = !!onClick
  const isFailed = state.status === 'failed'
  const [retrying, setRetrying] = useState(false)

  // Failed rows render a distinct red-bordered surface even when not focused
  // — operators need to spot them at a glance per issue #125.
  const failedSurface = isFailed
    ? { background: colors.bg, border: `1px solid ${colors.border}` }
    : { background: focused ? colors.bg : 'transparent', border: `1px solid ${focused ? colors.border : 'transparent'}` }

  async function handleRetry(e: React.MouseEvent) {
    e.stopPropagation()
    if (!onRetry || retrying) return
    setRetrying(true)
    try {
      await onRetry(phase.id)
    } finally {
      setRetrying(false)
    }
  }

  return (
    <div
      role={clickable ? 'button' : undefined}
      onClick={clickable ? onClick : undefined}
      onKeyDown={clickable ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick?.() } } : undefined}
      tabIndex={clickable ? 0 : -1}
      aria-current={focused ? 'step' : undefined}
      aria-label={`${phase.label} — ${state.status}${dur ? ` (${dur})` : ''}`}
      className={`bp-row ${isFailed ? 'bp-row-failed' : ''}`}
      style={{
        ...failedSurface,
        cursor: clickable ? 'pointer' : 'default',
      }}
    >
      <span
        className="bp-icon"
        style={{
          background: colors.bg,
          border: `1px solid ${colors.border}`,
        }}
      >
        <StatusIcon status={state.status} />
      </span>

      <div className="bp-body">
        <div className="bp-title-row">
          <span className="bp-title" style={{ color: state.status === 'pending' ? 'var(--wiz-text-sub)' : 'var(--wiz-text-hi)' }}>
            {phase.label}
          </span>
          <span className="bp-chip" style={{ color: colors.fg, borderColor: colors.border }}>
            {phase.upstream}
          </span>
          {dur && (
            <span className="bp-dur" style={{ color: 'var(--wiz-text-hint)' }}>
              {dur}
            </span>
          )}
        </div>
        {!compact && (
          <div className="bp-desc" style={{ color: 'var(--wiz-text-sub)' }}>
            {phase.description}
          </div>
        )}
        {isFailed && (
          <>
            <div className="bp-failed-marker">
              sovereign state · <code>{failedAtSovereignState(phase.id)}</code>
            </div>
            {state.lastEvent?.message && (
              <div className="bp-failed-msg" role="alert">
                {state.lastEvent.message}
              </div>
            )}
            <div className="bp-failed-actions">
              {onRetry && (
                <button
                  type="button"
                  onClick={handleRetry}
                  disabled={retrying}
                  className="bp-btn bp-btn-retry"
                  aria-label={`Retry phase ${phase.label}`}
                >
                  {retrying ? <Loader2 size={11} className="animate-spin" /> : <RotateCw size={11} />}
                  <span>{retrying ? 'Retrying…' : 'Retry phase'}</span>
                </button>
              )}
              <a
                href={`${rollbackDocsURL}-${phase.id}`}
                target="_blank"
                rel="noopener noreferrer"
                className="bp-btn bp-btn-rollback"
                onClick={(e) => e.stopPropagation()}
                aria-label={`Rollback procedure for ${phase.label}`}
              >
                <BookOpen size={11} />
                <span>Rollback procedure</span>
                <ExternalLink size={10} style={{ opacity: 0.6 }} />
              </a>
            </div>
          </>
        )}
        {state.lastEvent && state.status === 'running' && !compact && (
          <div className="bp-tail">{state.lastEvent.message}</div>
        )}
      </div>

      <style>{`
        .bp-row {
          display: flex; align-items: flex-start; gap: 10px;
          padding: 8px 10px; border-radius: 8px;
          width: 100%; text-align: left;
          font-family: 'Inter', sans-serif;
          transition: background 0.15s, border-color 0.15s;
          background: transparent;
        }
        .bp-row[role="button"]:hover { background: var(--wiz-bg-sub); }
        .bp-row[role="button"]:focus-visible {
          outline: 2px solid var(--wiz-accent);
          outline-offset: 1px;
        }
        .bp-row-failed {
          /* Steady red border + tinted background — operators must spot
             a failed phase at a glance per issue #125. */
          background: rgba(248,113,113,0.07) !important;
          border: 1px solid rgba(248,113,113,0.45) !important;
          box-shadow: inset 3px 0 0 #F87171;
        }
        .bp-icon {
          width: 22px; height: 22px; border-radius: 50%;
          display: flex; align-items: center; justify-content: center;
          flex-shrink: 0; transition: all 0.15s;
        }
        .bp-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
        .bp-title-row {
          display: flex; align-items: center; gap: 7px;
          flex-wrap: wrap;
        }
        .bp-title {
          font-size: 12px; font-weight: 600;
          line-height: 1.3;
        }
        .bp-chip {
          font-size: 9px; font-weight: 600;
          padding: 1px 6px; border-radius: 4px;
          border: 1px solid;
          font-family: 'JetBrains Mono', monospace;
        }
        .bp-dur {
          font-size: 10px; margin-left: auto;
          font-family: 'JetBrains Mono', monospace;
        }
        .bp-desc {
          font-size: 10.5px; line-height: 1.4;
        }
        .bp-failed-marker {
          font-size: 10px; color: #F87171;
          margin-top: 3px;
        }
        .bp-failed-marker code {
          font-family: 'JetBrains Mono', monospace;
          background: rgba(248,113,113,0.08);
          padding: 1px 5px; border-radius: 3px;
        }
        .bp-failed-msg {
          font-size: 10.5px;
          color: #FCA5A5;
          font-family: 'JetBrains Mono', monospace;
          background: rgba(248,113,113,0.05);
          border-left: 2px solid rgba(248,113,113,0.55);
          padding: 5px 8px;
          margin-top: 5px;
          border-radius: 0 4px 4px 0;
          line-height: 1.5;
          word-break: break-word;
          white-space: pre-wrap;
        }
        .bp-failed-actions {
          display: flex;
          align-items: center;
          gap: 6px;
          margin-top: 7px;
          flex-wrap: wrap;
        }
        .bp-btn {
          display: inline-flex;
          align-items: center;
          gap: 5px;
          padding: 4px 9px;
          border-radius: 5px;
          font-size: 10px;
          font-weight: 600;
          letter-spacing: 0.02em;
          font-family: 'Inter', sans-serif;
          cursor: pointer;
          transition: all 0.15s;
          border: 1px solid;
          text-decoration: none;
          line-height: 1.3;
        }
        .bp-btn-retry {
          color: #FCA5A5;
          background: rgba(248,113,113,0.10);
          border-color: rgba(248,113,113,0.40);
        }
        .bp-btn-retry:hover:not(:disabled) {
          background: rgba(248,113,113,0.18);
          border-color: rgba(248,113,113,0.65);
          color: #FECACA;
        }
        .bp-btn-retry:disabled {
          opacity: 0.55;
          cursor: not-allowed;
        }
        .bp-btn-rollback {
          color: var(--wiz-text-md);
          background: transparent;
          border-color: var(--wiz-border-sub);
        }
        .bp-btn-rollback:hover {
          background: var(--wiz-bg-sub);
          border-color: var(--wiz-border);
          color: var(--wiz-text-hi);
        }
        .bp-tail {
          font-size: 10px; color: var(--wiz-text-lo);
          font-family: 'JetBrains Mono', monospace;
          white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
          margin-top: 2px;
        }
        .animate-spin { animation: bp-spin 0.9s linear infinite; }
        @keyframes bp-spin { to { transform: rotate(360deg); } }
      `}</style>
    </div>
  )
}

function SectionHeader({ title, subtitle, count }: { title: string; subtitle: string; count: { done: number; total: number } }) {
  return (
    <div className="bp-section">
      <div className="bp-section-title">
        <span>{title}</span>
        <span className="bp-section-count">{count.done} / {count.total}</span>
      </div>
      <div className="bp-section-sub">{subtitle}</div>
      <style>{`
        .bp-section {
          display: flex; flex-direction: column; gap: 2px;
          padding: 6px 10px;
          border-bottom: 1px solid var(--wiz-border-sub);
          margin-bottom: 4px;
        }
        .bp-section-title {
          display: flex; align-items: center; justify-content: space-between;
          font-size: 10px; font-weight: 700;
          letter-spacing: 0.1em; text-transform: uppercase;
          color: var(--wiz-text-sub);
        }
        .bp-section-count {
          font-family: 'JetBrains Mono', monospace;
          color: var(--wiz-text-hi);
          font-size: 11px;
        }
        .bp-section-sub {
          font-size: 10px; color: var(--wiz-text-hint);
          line-height: 1.4;
        }
      `}</style>
    </div>
  )
}

export function BootstrapProgress({
  phases,
  onPhaseClick,
  focusedPhaseId,
  compact = false,
  onRetryPhase,
  rollbackDocsURL = DEFAULT_ROLLBACK_DOCS_URL,
}: BootstrapProgressProps) {
  const doneCount = (list: BootstrapPhase[]) =>
    list.filter((p) => phases[p.id]?.status === 'done').length

  const allDone = ALL_PHASES.every((p) => phases[p.id]?.status === 'done')
  const overallPct = Math.round(
    (ALL_PHASES.reduce((s, p) => {
      const st = phases[p.id]?.status
      if (st === 'done') return s + 1
      if (st === 'running') return s + 0.5
      return s
    }, 0) /
      TOTAL_PHASES) *
      100,
  )

  // Locate the first failed phase (chronological order) so the header banner
  // can surface it immediately even before the user scrolls.
  const firstFailedPhase = ALL_PHASES.find((p) => phases[p.id]?.status === 'failed') ?? null

  return (
    <nav aria-label="Bootstrap provisioning progress" className="bp">
      <header className="bp-header">
        <div className="bp-header-row">
          <span className="bp-header-title">Provisioning {TOTAL_PHASES} phases</span>
          <span className="bp-header-pct">{overallPct}%</span>
        </div>
        <div className="bp-header-bar">
          <div
            className="bp-header-bar-fill"
            style={{ width: `${overallPct}%`, background: firstFailedPhase ? '#F87171' : allDone ? '#4ADE80' : 'var(--wiz-accent)' }}
          />
        </div>
        {firstFailedPhase && (
          <div className="bp-header-failed-banner" role="alert">
            <AlertCircle size={12} style={{ flexShrink: 0 }} />
            <span>
              <strong>Phase failed:</strong> {firstFailedPhase.label} — sovereign state{' '}
              <code>{failedAtSovereignState(firstFailedPhase.id)}</code>. Use the row below to retry or open the rollback procedure.
            </span>
          </div>
        )}
      </header>

      <SectionHeader
        title="Phase 0 · OpenTofu"
        subtitle="Real cloud resources via the canonical infra/hetzner/ module"
        count={{ done: doneCount(OPENTOFU_PHASES), total: OPENTOFU_PHASES.length }}
      />
      {OPENTOFU_PHASES.map((phase) => {
        const state = phases[phase.id]
        if (!state) return null
        return (
          <PhaseRow
            key={phase.id}
            phase={phase}
            state={state}
            onClick={onPhaseClick ? () => onPhaseClick(phase.id) : undefined}
            focused={focusedPhaseId === phase.id}
            compact={compact}
            onRetry={onRetryPhase}
            rollbackDocsURL={rollbackDocsURL}
          />
        )
      })}

      <SectionHeader
        title="Phase 1 · Bootstrap kit"
        subtitle="11 components reconciled by Flux inside the new cluster"
        count={{ done: doneCount(BOOTSTRAP_KIT_PHASES), total: BOOTSTRAP_KIT_PHASES.length }}
      />
      {BOOTSTRAP_KIT_PHASES.map((phase) => {
        const state = phases[phase.id]
        if (!state) return null
        return (
          <PhaseRow
            key={phase.id}
            phase={phase}
            state={state}
            onClick={onPhaseClick ? () => onPhaseClick(phase.id) : undefined}
            focused={focusedPhaseId === phase.id}
            compact={compact}
            onRetry={onRetryPhase}
            rollbackDocsURL={rollbackDocsURL}
          />
        )
      })}

      <style>{`
        .bp { display: flex; flex-direction: column; gap: 4px; }
        .bp-header {
          padding: 8px 10px 10px;
          display: flex; flex-direction: column; gap: 6px;
        }
        .bp-header-row {
          display: flex; align-items: center; justify-content: space-between;
        }
        .bp-header-title {
          font-size: 11px; font-weight: 700; letter-spacing: 0.06em;
          text-transform: uppercase; color: var(--wiz-text-md);
        }
        .bp-header-pct {
          font-family: 'JetBrains Mono', monospace;
          font-size: 13px; font-weight: 700; color: var(--wiz-text-hi);
        }
        .bp-header-bar {
          height: 3px; border-radius: 2px;
          background: var(--wiz-border);
          overflow: hidden;
        }
        .bp-header-bar-fill {
          height: 100%; transition: width 0.4s ease, background 0.3s ease;
        }
        .bp-header-failed-banner {
          display: flex; align-items: flex-start; gap: 7px;
          padding: 8px 11px; border-radius: 7px;
          background: rgba(248,113,113,0.08);
          border: 1px solid rgba(248,113,113,0.30);
          color: #FCA5A5;
          font-size: 11px; line-height: 1.5;
          margin-top: 4px;
        }
        .bp-header-failed-banner code {
          font-family: 'JetBrains Mono', monospace;
          background: rgba(248,113,113,0.12);
          padding: 1px 5px; border-radius: 3px;
          font-size: 10px;
        }
        .bp-header-failed-banner strong { color: #F87171; }
      `}</style>
    </nav>
  )
}
