/**
 * StatusStrip — top contextual strip rendered below the PortalShell
 * header on /flow* and /jobs* routes only. Mirrors the canonical
 * provision-mockup.html topbar geometry (breadcrumb + provisioning
 * pill + progress bar + optional Jobs↔Batches mode toggle).
 *
 * Layout (left → right):
 *   [Sovereign / fqdn]  [● Provisioning · pulse]  [████░░ N/M · elapsed]
 *   [Jobs ↔ Batches toggle (only on /flow)]
 *
 * Per founder spec, the captions toggle (`Aa`) and log-panel toggle
 * (`⊞`) from the mock are explicitly DROPPED — the log is contextual
 * (FloatingLogPane on bubble click) and captions don't add enough
 * value to keep around.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — status, progress, elapsed are all props.
 *   #4 — every colour is a theme token; the running pulse animation
 *        keeps in lockstep with provision-mockup.html @keyframes pulse.
 */

import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'

export type ProvisioningStatus = 'pending' | 'running' | 'succeeded' | 'failed'

interface StatusStripProps {
  /** Stable deployment id — embedded in the breadcrumb back-link target. */
  deploymentId: string
  /** Resolved sovereign FQDN. Falls back to deploymentId-prefix when null. */
  sovereignFQDN?: string | null
  /** Coarse status of the deployment — drives pill colour + pulse. */
  status: ProvisioningStatus
  /** Number of jobs that reached a terminal state. */
  finished: number
  /** Total number of jobs in the current scope. */
  total: number
  /** Elapsed time in milliseconds since the earliest startedAt. */
  elapsedMs: number
  /**
   * When set, renders the Jobs↔Batches mode toggle. The toggle handler
   * receives the next mode (consumer is responsible for URL updates).
   */
  modeToggle?: {
    mode: 'jobs' | 'batches'
    onChange: (next: 'jobs' | 'batches') => void
  }
  /** Optional extra slot rendered after the progress bar (e.g. test hooks). */
  trailing?: ReactNode
}

const STATUS_TONE: Record<
  ProvisioningStatus,
  { dot: string; pillBg: string; pillBorder: string; pillFg: string; barFill: string; label: string }
> = {
  pending: {
    dot: 'rgba(148,163,184,0.7)',
    pillBg: 'rgba(148,163,184,0.10)',
    pillBorder: 'rgba(148,163,184,0.30)',
    pillFg: 'var(--color-text-dim)',
    barFill: '#94A3B8',
    label: 'Pending',
  },
  running: {
    dot: '#38BDF8',
    pillBg: 'rgba(56,189,248,0.10)',
    pillBorder: 'rgba(56,189,248,0.30)',
    pillFg: '#38BDF8',
    barFill: 'linear-gradient(90deg, #38BDF8, #818CF8)',
    label: 'Provisioning',
  },
  succeeded: {
    dot: '#4ADE80',
    pillBg: 'rgba(74,222,128,0.10)',
    pillBorder: 'rgba(74,222,128,0.30)',
    pillFg: '#4ADE80',
    barFill: '#4ADE80',
    label: 'Completed',
  },
  failed: {
    dot: '#F87171',
    pillBg: 'rgba(248,113,113,0.10)',
    pillBorder: 'rgba(248,113,113,0.35)',
    pillFg: '#F87171',
    barFill: '#F87171',
    label: 'Failed',
  },
}

function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s.toString().padStart(2, '0')}s`
  return `${s}s`
}

export function StatusStrip({
  deploymentId,
  sovereignFQDN,
  status,
  finished,
  total,
  elapsedMs,
  modeToggle,
  trailing,
}: StatusStripProps) {
  const tone = STATUS_TONE[status]
  const pct = total > 0 ? Math.min(100, Math.round((finished / total) * 100)) : 0
  const fqdnLabel = sovereignFQDN ?? `deployment ${deploymentId.slice(0, 8)}`

  return (
    <div className="status-strip" data-testid="sov-status-strip" role="region" aria-label="Provisioning status">
      <style>{STATUS_STRIP_CSS}</style>

      <Link
        to="/provision/$deploymentId"
        params={{ deploymentId }}
        className="status-strip-breadcrumb"
        data-testid="sov-status-strip-breadcrumb"
      >
        <span className="status-strip-breadcrumb-prefix">Sovereign</span>
        <span className="status-strip-breadcrumb-sep">/</span>
        <span className="status-strip-breadcrumb-fqdn" title={fqdnLabel}>
          {fqdnLabel}
        </span>
      </Link>

      <div
        className="status-strip-pill"
        data-testid="sov-status-strip-pill"
        data-status={status}
        style={{ background: tone.pillBg, borderColor: tone.pillBorder, color: tone.pillFg }}
      >
        <span
          className={`status-strip-dot${status === 'running' ? ' pulse' : ''}`}
          style={{ background: tone.dot }}
          aria-hidden
        />
        <span className="status-strip-pill-label">{tone.label}</span>
      </div>

      <div className="status-strip-progress" data-testid="sov-status-strip-progress">
        <div className="status-strip-bar">
          <div
            className="status-strip-bar-fill"
            data-testid="sov-status-strip-bar-fill"
            style={{ width: `${pct}%`, background: tone.barFill }}
          />
        </div>
        <span className="status-strip-progress-count" data-testid="sov-status-strip-count">
          {finished}/{total}
        </span>
        <span className="status-strip-progress-elapsed" data-testid="sov-status-strip-elapsed">
          {formatElapsed(elapsedMs)}
        </span>
      </div>

      {modeToggle ? (
        <div
          className="status-strip-mode-toggle"
          role="tablist"
          aria-label="Flow view mode"
          data-testid="sov-status-strip-mode-toggle"
        >
          {(['jobs', 'batches'] as const).map((m) => {
            const active = modeToggle.mode === m
            return (
              <button
                key={m}
                type="button"
                role="tab"
                aria-selected={active}
                className={`status-strip-mode-btn${active ? ' active' : ''}`}
                data-testid={`sov-status-strip-mode-${m}`}
                onClick={() => {
                  if (!active) modeToggle.onChange(m)
                }}
              >
                {m === 'jobs' ? 'Jobs' : 'Batches'}
              </button>
            )
          })}
        </div>
      ) : null}

      {trailing}
    </div>
  )
}

const STATUS_STRIP_CSS = `
.status-strip {
  display: flex;
  align-items: center;
  gap: 0.85rem;
  padding: 0.55rem 1rem;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-surface);
  flex-wrap: wrap;
  font-size: 0.78rem;
  color: var(--color-text-dim);
}

.status-strip-breadcrumb {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  text-decoration: none;
  color: var(--color-text-dim);
  font-size: 0.78rem;
  font-weight: 500;
  transition: color 0.12s ease;
  max-width: 36ch;
  white-space: nowrap;
  overflow: hidden;
}
.status-strip-breadcrumb:hover { color: var(--color-text); }
.status-strip-breadcrumb-prefix {
  font-size: 0.66rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--color-text-dim);
}
.status-strip-breadcrumb-sep { color: var(--color-text-dim); opacity: 0.5; }
.status-strip-breadcrumb-fqdn {
  color: var(--color-text-strong);
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.01em;
  font-size: 0.82rem;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.status-strip-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.18rem 0.7rem;
  border-radius: 999px;
  border: 1px solid;
  font-size: 0.7rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.status-strip-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.status-strip-dot.pulse {
  animation: status-strip-pulse 2s ease-in-out infinite;
}
@keyframes status-strip-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.4; transform: scale(0.65); }
}

.status-strip-progress {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
}
.status-strip-bar {
  width: 100px;
  height: 4px;
  border-radius: 2px;
  background: var(--color-border);
  overflow: hidden;
}
.status-strip-bar-fill {
  height: 100%;
  transition: width 0.4s ease;
}
.status-strip-progress-count,
.status-strip-progress-elapsed {
  font-size: 0.74rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.status-strip-mode-toggle {
  display: inline-flex;
  align-items: center;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 999px;
  overflow: hidden;
  margin-left: auto;
}
.status-strip-mode-btn {
  appearance: none;
  background: transparent;
  border: none;
  padding: 0.3rem 0.85rem;
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-text-dim);
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}
.status-strip-mode-btn:hover { color: var(--color-text); }
.status-strip-mode-btn.active {
  background: var(--color-accent);
  color: var(--color-bg);
}
`
