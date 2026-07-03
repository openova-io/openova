/**
 * ConvergenceWizard — the state-aware 5-phase wizard half of the
 * Convergence-Monitor Dashboard (#3925 surface A).
 *
 * While a Sovereign is converging (or running an operation) the Dashboard
 * renders THIS instead of the treemap: a 5-phase progress wizard
 *
 *   Infrastructure → Bootstrap → Reconciliation → Health → Ready
 *
 * with the live phase highlighted and, in the Reconciliation phase, the
 * live HelmRelease ratio ("Reconcile 52/65"). The moment status flips
 * `ready` the Dashboard AUTO-FLIPS to the treemap; a persistent toggle
 * flips back to this wizard (DECISION: banner+toggle, not auto-flip-back —
 * the user keeps control once they toggle).
 *
 * Deep-links (ticket §2 surface-C / §5):
 *   • Phase ③ Reconciliation → the Reconciliation page (the Flux DAG).
 *   • Phase ② Bootstrap + any operation → the Jobs page (the finite jobs).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the phase set + ratio come from the
 * deployment status + the region HR census, never hardcoded.
 */

import { Link } from '@tanstack/react-router'
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import type { DeploymentSnapshot, RegionHealth } from './useDeploymentEvents'

/** The five wizard phases, in order. */
export type WizardPhase =
  | 'infrastructure'
  | 'bootstrap'
  | 'reconciliation'
  | 'health'
  | 'ready'

export const WIZARD_PHASES: ReadonlyArray<{ id: WizardPhase; label: string }> = [
  { id: 'infrastructure', label: 'Infrastructure' },
  { id: 'bootstrap', label: 'Bootstrap' },
  { id: 'reconciliation', label: 'Reconciliation' },
  { id: 'health', label: 'Health' },
  { id: 'ready', label: 'Ready' },
]

const PHASE_INDEX: Record<WizardPhase, number> = {
  infrastructure: 0,
  bootstrap: 1,
  reconciliation: 2,
  health: 3,
  ready: 4,
}

/**
 * deriveWizardPhase maps the deployment status (+ HR census) to the live
 * wizard phase. The mapping mirrors the provision lifecycle:
 *
 *   pending / tofu-applying      → Infrastructure (OpenTofu applies infra)
 *   flux-bootstrapping           → Bootstrap       (k3s→cilium→flux land)
 *   phase1-watching              → Reconciliation   (HRs reconcile; show ratio)
 *   (HRs all installed, settling) → Health
 *   ready                        → Ready
 *
 * A non-ready, non-in-flight status (failed/partial) pins Health so the
 * operator sees "stuck just short of ready" rather than a false Ready.
 */
export function deriveWizardPhase(snap: DeploymentSnapshot | null | undefined): WizardPhase {
  const status = (snap?.status ?? '').trim().toLowerCase()
  switch (status) {
    case 'pending':
    case 'provisioning':
    case 'tofu-applying':
      return 'infrastructure'
    case 'flux-bootstrapping':
      return 'bootstrap'
    case 'phase1-watching':
      return 'reconciliation'
    case 'ready':
      return 'ready'
    case 'failed':
    case 'partial-failure':
      // Stuck just short — Health is the "almost there / needs attention"
      // phase; the chip + banner carry the degraded signal.
      return 'health'
    default:
      // Unknown/empty (first paint before snapshot resolves) — start at
      // Infrastructure rather than falsely claim Ready.
      return 'infrastructure'
  }
}

/**
 * isFailedStatus — true when the deployment is in a terminal/partial
 * failure state. The pinned wizard phase then renders RED (semantic
 * failed colour, #4704 follow-up) instead of the in-progress blue, so a
 * stuck prov is visually distinct from a converging one.
 */
export function isFailedStatus(snap: DeploymentSnapshot | null | undefined): boolean {
  const s = (snap?.status ?? '').trim().toLowerCase()
  return s === 'failed' || s === 'partial-failure'
}

/** hrRatio sums the HelmRelease census across regions → {ready, total}. */
export function hrRatio(snap: DeploymentSnapshot | null | undefined): { ready: number; total: number } {
  const regions: RegionHealth[] = snap?.regions ?? []
  if (regions.length > 0) {
    let ready = 0
    let total = 0
    for (const r of regions) {
      ready += r.hrReady ?? 0
      total += r.hrTotal ?? 0
    }
    return { ready, total }
  }
  // Single-region / pre-census: fall back to componentStates if present.
  const cs = snap?.componentStates ?? snap?.result?.componentStates ?? null
  if (cs) {
    const entries = Object.values(cs)
    const ready = entries.filter((s) => s === 'installed').length
    return { ready, total: entries.length }
  }
  return { ready: 0, total: 0 }
}

export interface ConvergenceWizardProps {
  snapshot: DeploymentSnapshot | null
  deploymentId: string
}

export function ConvergenceWizard({ snapshot, deploymentId }: ConvergenceWizardProps) {
  const phase = deriveWizardPhase(snapshot)
  const activeIdx = PHASE_INDEX[phase]
  const ratio = hrRatio(snapshot)
  const failed = isFailedStatus(snapshot)

  return (
    <div
      data-testid="convergence-wizard"
      data-phase={phase}
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6"
    >
      <style>{WIZARD_CSS}</style>
      <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">
        Converging to desired state
      </h2>
      <p className="mb-5 text-xs text-[var(--color-text-dim)]">
        The Sovereign is provisioning. This wizard auto-flips to the resource
        treemap the moment it reaches Ready.
      </p>

      <ol className="flex flex-col gap-2 sm:flex-row sm:items-stretch sm:gap-0">
        {WIZARD_PHASES.map((p, i) => {
          const isActive = i === activeIdx
          const isDone = i < activeIdx
          // #4704 follow-up — semantic colours: done=green, active=blue
          // (in-progress), pending=grey, FAILED=red. A failed/partial
          // deployment pins its phase red so a stuck prov never renders
          // as a calm in-progress blue.
          const isFailed = failed && isActive
          const stateCls = isDone
            ? 'wizard-phase-done'
            : isFailed
              ? 'wizard-phase-failed'
              : isActive
                ? 'wizard-phase-active'
                : 'wizard-phase-pending'
          return (
            <li
              key={p.id}
              data-testid={`wizard-phase-${p.id}`}
              data-active={isActive ? 'true' : 'false'}
              data-done={isDone ? 'true' : 'false'}
              data-failed={isFailed ? 'true' : 'false'}
              className={`wizard-phase ${stateCls} flex flex-1 flex-col gap-1 rounded-lg border px-3 py-3 sm:mx-1`}
            >
              <span className="flex items-center gap-2 text-sm font-semibold">
                <span className="wizard-phase-dot" aria-hidden>
                  {isDone ? '✓' : isFailed ? '✕' : isActive ? '●' : i + 1}
                </span>
                {p.label}
              </span>
              {/* #4704 follow-up — every phase drills down to its
                  log-bearing surface: Infrastructure → the finite Jobs
                  table (each row opens JobDetail with the live LogPane
                  for the tofu/provision steps). Mode-aware + id-safe via
                  sovereignPathOrDeployments (Task B). */}
              {p.id === 'infrastructure' ? (
                <span className="text-[11px] text-[var(--color-text-dim)]">
                  <Link
                    to={sovereignPathOrDeployments('jobs', { deploymentId }) as never}
                    data-testid="wizard-link-infrastructure"
                    className="text-[var(--color-accent)] no-underline hover:underline"
                  >
                    view logs →
                  </Link>
                </span>
              ) : null}
              {/* Health drills into the same reconciliation graph the
                  Reconciliation phase uses — the recon objects (and their
                  logs) are what "settling" means. */}
              {p.id === 'health' ? (
                <span className="text-[11px] text-[var(--color-text-dim)]">
                  <Link
                    to={'/provision/$deploymentId/cloud' as never}
                    params={{ deploymentId } as never}
                    search={{ view: 'graph', lens: 'reconciliation' } as never}
                    data-testid="wizard-link-health"
                    className="text-[var(--color-accent)] no-underline hover:underline"
                  >
                    view graph →
                  </Link>
                </span>
              ) : null}
              {/* Reconciliation phase shows the live HR ratio + a deep-link. */}
              {p.id === 'reconciliation' ? (
                <span className="text-[11px] text-[var(--color-text-dim)]">
                  {ratio.total > 0 ? (
                    <span data-testid="wizard-reconcile-ratio" className="font-mono">
                      Reconcile {ratio.ready}/{ratio.total}
                    </span>
                  ) : (
                    'Reconcile'
                  )}
                  {' · '}
                  {/* #3958 — the standalone /reconciliation DAG page is
                      gone; reconcilers now live on the unified Cloud
                      graph. #3996 follow-up: deep-link must select the
                      Reconciliation LENS (not the default Cloud lens) so
                      the operator lands on the reconciler chip-set —
                      `lens=reconciliation` is carried through the cloud
                      route's validateSearch and seeds CloudLensProvider. */}
                  <Link
                    to={'/provision/$deploymentId/cloud' as never}
                    params={{ deploymentId } as never}
                    search={{ view: 'graph', lens: 'reconciliation' } as never}
                    data-testid="wizard-link-reconciliation"
                    className="text-[var(--color-accent)] no-underline hover:underline"
                  >
                    view graph →
                  </Link>
                </span>
              ) : null}
              {/* Bootstrap phase deep-links the Jobs page (finite jobs).
                  #4704 Task B: a bare '/jobs' on the mothership collapses
                  into /provision/jobs (the literal lands in $deploymentId)
                  — build the mode-aware per-deployment path instead. */}
              {p.id === 'bootstrap' ? (
                <span className="text-[11px] text-[var(--color-text-dim)]">
                  <Link
                    to={sovereignPathOrDeployments('jobs', { deploymentId }) as never}
                    data-testid="wizard-link-jobs"
                    className="text-[var(--color-accent)] no-underline hover:underline"
                  >
                    view jobs →
                  </Link>
                </span>
              ) : null}
            </li>
          )
        })}
      </ol>
    </div>
  )
}

/* #4704 follow-up — colours reference the theme tokens (the SAME
 * semantic mapping as shared/lib/statusColors.ts): active/in-progress =
 * blue (--color-accent), done/success = green (--color-success),
 * failed = red (--color-danger), pending = grey. Tints via color-mix so
 * both themes stay correct without per-theme hex forks. */
const WIZARD_CSS = `
.wizard-phase { border-color: var(--color-border); background: var(--color-bg); transition: all 0.2s ease; }
.wizard-phase-dot {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border-radius: 50%; font-size: 11px; font-weight: 700;
  background: var(--color-border); color: var(--color-text-dim);
}
.wizard-phase-pending { opacity: 0.6; }
.wizard-phase-active { border-color: var(--color-accent); background: color-mix(in srgb, var(--color-accent) 8%, transparent); }
.wizard-phase-active .wizard-phase-dot { background: var(--color-accent); color: #fff; animation: wizard-pulse 2s ease-in-out infinite; }
.wizard-phase-done { border-color: color-mix(in srgb, var(--color-success) 35%, transparent); }
.wizard-phase-done .wizard-phase-dot { background: var(--color-success); color: #fff; }
.wizard-phase-failed { border-color: var(--color-danger); background: color-mix(in srgb, var(--color-danger) 8%, transparent); }
.wizard-phase-failed .wizard-phase-dot { background: var(--color-danger); color: #fff; }
@keyframes wizard-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.5; } }
`
