/**
 * CutoverProgressCard — 8-step checklist for the in-flight + completed
 * sovereignty cutover.
 *
 * Renders ALL 8 steps from CUTOVER_STEPS at all times so the operator
 * sees the complete chain on first paint, with rows transitioning
 * pending → running → done as the SSE stream emits updates. A failed
 * step renders red + halts visual progression of subsequent steps.
 *
 * This component is presentational: it consumes a `status: CutoverStatus`
 * prop and renders. The hook (useCutoverEvents) drives state.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — every visual affordance ships here on first cut:
 *      pending circle, running spinner, done check, failed alert,
 *      timestamps, sovereignty-achieved summary.
 *   #4 (never hardcode) — the step list comes from CUTOVER_STEPS, never
 *      inlined. Adding a step in shared/types/cutover.ts ripples here
 *      automatically.
 */

import { AlertCircle, Check, CircleDashed, Loader2 } from 'lucide-react'
import {
  CUTOVER_STEPS,
  type CutoverStatus,
  type CutoverStepDef,
  type CutoverStepStatus,
  type CutoverStepStatusRecord,
} from '@/shared/types/cutover'

export interface CutoverProgressCardProps {
  status: CutoverStatus
  /** Stream-level error to surface above the rows; trumps per-step error. */
  streamError?: string | null
}

/**
 * Find the current per-step record for a given step id. Returns a
 * `pending` placeholder if no record has been emitted yet — the rows
 * render at all times, never disappear.
 */
function recordFor(
  status: CutoverStatus,
  stepId: CutoverStepDef['id'],
): CutoverStepStatusRecord {
  const found = status.steps.find((s) => s.step === stepId)
  if (found) return found
  return { step: stepId, status: 'pending' }
}

const STEP_DOT_CLASSES: Record<CutoverStepStatus, string> = {
  pending:
    'border-[oklch(28%_0.02_250)] bg-[oklch(20%_0.02_250)] text-[oklch(60%_0.02_250)]',
  running:
    'border-[oklch(60%_0.18_220)] bg-[oklch(28%_0.10_220)] text-[oklch(85%_0.10_220)]',
  done: 'border-[oklch(60%_0.18_140)] bg-[oklch(28%_0.10_140)] text-[oklch(90%_0.12_140)]',
  failed:
    'border-[oklch(60%_0.18_30)] bg-[oklch(28%_0.10_30)] text-[oklch(90%_0.18_30)]',
}

const STEP_TEXT_CLASSES: Record<CutoverStepStatus, string> = {
  pending: 'text-[oklch(55%_0.02_250)]',
  running: 'text-[oklch(90%_0.05_220)]',
  done: 'text-[oklch(92%_0.04_140)]',
  failed: 'text-[oklch(92%_0.10_30)]',
}

function StepIcon({ status }: { status: CutoverStepStatus }) {
  switch (status) {
    case 'done':
      return <Check className="h-3.5 w-3.5" strokeWidth={3} />
    case 'running':
      return <Loader2 className="h-3.5 w-3.5 animate-spin" />
    case 'failed':
      return <AlertCircle className="h-3.5 w-3.5" />
    case 'pending':
    default:
      return <CircleDashed className="h-3.5 w-3.5" />
  }
}

function timestampLabel(rec: CutoverStepStatusRecord): string | null {
  // Prefer finishedAt for terminal states, startedAt while running.
  if (rec.status === 'done' || rec.status === 'failed') {
    if (rec.finishedAt) return formatRel(rec.finishedAt)
    if (rec.startedAt) return formatRel(rec.startedAt)
    return null
  }
  if (rec.status === 'running' && rec.startedAt) return formatRel(rec.startedAt)
  return null
}

function formatRel(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  // Render as a stable HH:MM:SS local — operators want a moment, not a
  // ticking "10s ago" string that re-renders the row constantly.
  const d = new Date(t)
  return d.toLocaleTimeString(undefined, { hour12: false })
}

function CutoverStepRow({
  def,
  rec,
}: {
  def: CutoverStepDef
  rec: CutoverStepStatusRecord
}) {
  const ts = timestampLabel(rec)
  return (
    <li
      data-testid={`cutover-step-${def.id}`}
      data-step-status={rec.status}
      className="flex items-start gap-3 rounded-lg border border-[var(--color-border)]/50 bg-[var(--color-bg-2)] p-3"
    >
      <span
        aria-hidden
        className={`mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full border ${STEP_DOT_CLASSES[rec.status]}`}
      >
        <StepIcon status={rec.status} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-3">
          <p
            className={`text-sm font-medium ${STEP_TEXT_CLASSES[rec.status]}`}
          >
            {def.label}
          </p>
          {ts ? (
            <span
              className="font-mono text-[10px] text-[var(--color-text-dim)]"
              data-testid={`cutover-step-${def.id}-ts`}
            >
              {ts}
            </span>
          ) : null}
        </div>
        <p className="mt-0.5 text-xs leading-snug text-[var(--color-text-dim)]">
          {def.description}
        </p>
        {rec.status === 'failed' && rec.message ? (
          <p
            data-testid={`cutover-step-${def.id}-error`}
            className="mt-2 rounded border-l-2 border-red-500/60 bg-red-500/5 px-2 py-1 font-mono text-[11px] text-red-300"
            role="alert"
          >
            {rec.message}
          </p>
        ) : null}
      </div>
    </li>
  )
}

function SovereigntyAchievedSummary({ status }: { status: CutoverStatus }) {
  return (
    <div
      data-testid="cutover-achieved-summary"
      className="mt-4 rounded-xl border border-green-500/40 bg-green-500/5 p-5"
    >
      <div className="flex items-start gap-3">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-green-500/15 text-green-400">
          <Check className="h-5 w-5" strokeWidth={3} />
        </span>
        <div className="flex-1">
          <h4 className="text-base font-semibold text-green-300">
            Sovereignty achieved
          </h4>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            This Sovereign cluster is now fully independent of the openova-io
            mothership. Every container pull and every Flux reconcile sources
            from local Gitea + Harbor.
          </p>
          <dl className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
            <div data-testid="cutover-summary-mirrored-sha">
              <dt className="text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
                Mirrored commit
              </dt>
              <dd className="mt-0.5 truncate font-mono text-xs text-[var(--color-text-strong)]">
                {status.mirroredCommitSHA?.slice(0, 12) ?? '—'}
              </dd>
            </div>
            <div data-testid="cutover-summary-harbor-projects">
              <dt className="text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
                Harbor projects
              </dt>
              <dd className="mt-0.5 font-mono text-xs text-[var(--color-text-strong)]">
                {status.harborProjectCount ?? '—'}
              </dd>
            </div>
            <div data-testid="cutover-summary-egress">
              <dt className="text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
                Egress block
              </dt>
              <dd className="mt-0.5 font-mono text-xs text-[var(--color-text-strong)]">
                {status.egressTestPassed === true
                  ? 'passed'
                  : status.egressTestPassed === false
                    ? 'failed'
                    : '—'}
              </dd>
            </div>
          </dl>
        </div>
      </div>
    </div>
  )
}

export function CutoverProgressCard({
  status,
  streamError,
}: CutoverProgressCardProps) {
  // Compute progress: count `done` steps for the % bar.
  const doneCount = status.steps.filter((s) => s.status === 'done').length
  const failed = status.steps.find((s) => s.status === 'failed') ?? null
  const pct = Math.round((doneCount / CUTOVER_STEPS.length) * 100)

  return (
    <section
      data-testid="cutover-progress-card"
      aria-label="Sovereignty cutover progress"
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="mb-4 flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold uppercase tracking-wider text-[var(--color-text)]">
            Cutover in progress
          </h3>
          <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
            {failed
              ? 'Cutover halted on a failed step. Review the error and re-trigger after the underlying issue is fixed.'
              : status.state === 'sovereign'
                ? `All ${CUTOVER_STEPS.length} steps completed.`
                : `Step ${doneCount} of ${CUTOVER_STEPS.length} complete — the operator is not required to keep this tab open.`}
          </p>
        </div>
        <span
          data-testid="cutover-progress-pct"
          className="font-mono text-2xl font-bold tabular-nums text-[var(--color-text-strong)]"
        >
          {pct}%
        </span>
      </header>

      <div
        className="mb-4 h-1 w-full overflow-hidden rounded bg-[var(--color-border)]/40"
        aria-hidden
      >
        <div
          className={`h-full transition-all duration-500 ${
            failed ? 'bg-red-500' : status.state === 'sovereign' ? 'bg-green-500' : 'bg-[--color-brand-500]'
          }`}
          style={{ width: `${pct}%` }}
        />
      </div>

      {streamError ? (
        <p
          data-testid="cutover-stream-error"
          role="alert"
          className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs text-amber-300"
        >
          {streamError}
        </p>
      ) : null}

      {status.settledRollOverrides ? (
        /* #5391 — the settled-roll gate passed WITH named operator
         * override(s). This is an AUDIT surface: a walk must be able to
         * SEE that specific stuck HelmReleases were excluded from the
         * pre-flight, in-progress AND after sovereignty is achieved. */
        <div
          data-testid="cutover-settled-roll-overrides"
          role="status"
          className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs text-amber-300"
        >
          <p className="font-semibold">
            Settled-roll pre-flight passed with named override
            {status.settledRollOverrides.includes('\n') ? 's' : ''}
          </p>
          <ul className="mt-1 list-inside list-disc font-mono text-[11px]">
            {status.settledRollOverrides.split('\n').map((entry) => (
              <li key={entry} className="truncate" title={entry}>
                {entry}
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[11px] text-amber-300/80">
            Each excluded HelmRelease was validated as genuinely stuck
            (Stalled or DependencyNotReady); its running images were still
            mirrored. Resolve the underlying failure after cutover.
          </p>
        </div>
      ) : null}

      <ol className="flex flex-col gap-2">
        {CUTOVER_STEPS.map((def) => (
          <CutoverStepRow key={def.id} def={def} rec={recordFor(status, def.id)} />
        ))}
      </ol>

      {status.state === 'sovereign' ? (
        <SovereigntyAchievedSummary status={status} />
      ) : null}
    </section>
  )
}
