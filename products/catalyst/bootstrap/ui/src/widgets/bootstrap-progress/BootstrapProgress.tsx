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

import { Check, Loader2, Circle, AlertCircle, MinusCircle } from 'lucide-react'
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
}

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
}: {
  phase: BootstrapPhase
  state: PhaseState
  onClick?: () => void
  focused: boolean
  compact: boolean
}) {
  const colors = STATUS_COLORS[state.status]
  const dur = durationLabel(state)
  const clickable = !!onClick

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!clickable}
      aria-current={focused ? 'step' : undefined}
      aria-label={`${phase.label} — ${state.status}${dur ? ` (${dur})` : ''}`}
      className="bp-row"
      style={{
        background: focused ? colors.bg : 'transparent',
        border: `1px solid ${focused ? colors.border : 'transparent'}`,
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
        {state.status === 'failed' && (
          <div className="bp-failed-marker">
            sovereign state · <code>{failedAtSovereignState(phase.id)}</code>
          </div>
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
        .bp-row:disabled { cursor: default; }
        .bp-row:not(:disabled):hover { background: var(--wiz-bg-sub); }
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
        .bp-tail {
          font-size: 10px; color: var(--wiz-text-lo);
          font-family: 'JetBrains Mono', monospace;
          white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
          margin-top: 2px;
        }
      `}</style>
    </button>
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
            style={{ width: `${overallPct}%`, background: allDone ? '#4ADE80' : 'var(--wiz-accent)' }}
          />
        </div>
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
      `}</style>
    </nav>
  )
}
