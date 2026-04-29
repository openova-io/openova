/**
 * JobCard — pixel-port of the per-row markup inside
 * core/console/src/components/JobsPage.svelte.
 *
 * Each row is a `<button>` (canonical: `<button class="w-full ...">`)
 * that toggles inline expansion of an ordered step list. The expanded
 * panel renders the same step-status iconography as the Svelte source
 * (success check / running spinner / failed X / pending number bubble).
 *
 * Two affordances on top of the canonical version:
 *   1. The row's app-name is rendered as a Tanstack <Link> when the
 *      Job is per-component (see jobs.ts, `noAppLink === false`).
 *      Clicking the app-name navigates to that component's AppDetail.
 *      Phase 0 / cluster-bootstrap rows pass `noAppLink === true` and
 *      render the title as plain text — there is no per-job route.
 *   2. The expanded toggle is preserved from the canonical button, so
 *      keyboard activation (`Enter` / `Space`) still works.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every colour
 * is a CSS variable + every layout value is a Tailwind utility — the
 * file mirrors the canonical class strings 1:1 so visual diff is zero.
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Job, JobStep } from './jobs'
import { fmtTime, statusBadge } from './jobs'

interface JobCardProps {
  job: Job
  /** Stable deployment id — needed for the AppDetail link target. */
  deploymentId: string
  /** Default expansion state (canonical: running rows expand by default). */
  defaultExpanded?: boolean
}

export function JobCard({ job, deploymentId, defaultExpanded }: JobCardProps) {
  const [expanded, setExpanded] = useState<boolean>(
    defaultExpanded ?? job.status === 'running',
  )
  const badge = statusBadge(job.status)
  const completedN = job.steps.filter((s) => s.status === 'succeeded').length
  const total = job.steps.length

  return (
    <div
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]"
      data-testid={`sov-job-card-${job.id}`}
    >
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-4 p-4 text-left"
        data-job-kind={job.app === 'infrastructure' ? 'provision' : job.app === 'cluster-bootstrap' ? 'bootstrap' : 'install'}
        data-job-status={job.status}
        aria-expanded={expanded}
        data-testid={`job-row-${job.id}`}
      >
        {/* Status icon (running spinner / success check / failed X / pending clock) */}
        <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-accent)]/10">
          {job.status === 'running' ? (
            <div className="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" />
          ) : job.status === 'succeeded' ? (
            <svg className="h-5 w-5 text-[var(--color-success)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
            </svg>
          ) : job.status === 'failed' ? (
            <svg className="h-5 w-5 text-[var(--color-danger)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          ) : (
            <svg className="h-5 w-5 text-[var(--color-text-dim)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
          )}
        </div>

        {/* Title + meta */}
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            {job.noAppLink ? (
              <p
                className="truncate text-sm font-semibold text-[var(--color-text-strong)]"
                data-testid={`sov-job-title-${job.id}`}
              >
                {job.title}
              </p>
            ) : (
              <Link
                to="/provision/$deploymentId/app/$componentId"
                params={{ deploymentId, componentId: job.app }}
                className="truncate text-sm font-semibold text-[var(--color-text-strong)] hover:text-[var(--color-accent)] no-underline"
                onClick={(e) => e.stopPropagation()}
                data-testid={`sov-job-title-link-${job.id}`}
              >
                {job.title}
              </Link>
            )}
            <span
              className={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide ${badge.classes}`}
              data-testid={`sov-job-badge-${job.id}`}
            >
              {badge.text}
            </span>
          </div>
          <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
            {completedN}/{total} steps
            {fmtTime(job.updatedAt) ? ` · last update ${fmtTime(job.updatedAt)}` : ''}
          </p>
          {job.status === 'running' && total > 0 ? (
            <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-border)]">
              <div
                className="h-full rounded-full bg-[var(--color-accent)] transition-all"
                style={{ width: `${Math.round((completedN / total) * 100)}%` }}
              />
            </div>
          ) : null}
        </div>

        {/* Caret */}
        <svg
          className={`h-4 w-4 shrink-0 text-[var(--color-text-dim)] transition-transform ${expanded ? 'rotate-180' : ''}`}
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={2}
        >
          <path strokeLinecap="round" strokeLinejoin="round" d="M19 9l-7 7-7-7" />
        </svg>
      </button>

      {expanded ? (
        <div
          className="border-t border-[var(--color-border)] p-4"
          data-testid={`job-expansion-${job.id}`}
        >
          {job.steps.length === 0 ? (
            <p className="text-xs text-[var(--color-text-dimmer)]">
              No steps yet — events will appear here as the job runs.
            </p>
          ) : (
            <ol className="flex flex-col gap-3">
              {job.steps.map((step) => (
                <StepRow key={step.index} step={step} />
              ))}
            </ol>
          )}
        </div>
      ) : null}
    </div>
  )
}

interface StepRowProps {
  step: JobStep
}

function StepRow({ step }: StepRowProps) {
  return (
    <li className="flex items-start gap-3" data-testid={`sov-step-${step.index}`}>
      <div className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center">
        {step.status === 'succeeded' ? (
          <div className="flex h-6 w-6 items-center justify-center rounded-full bg-[var(--color-success)]">
            <svg className="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
            </svg>
          </div>
        ) : step.status === 'running' ? (
          <div className="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" />
        ) : step.status === 'failed' ? (
          <div className="flex h-6 w-6 items-center justify-center rounded-full bg-[var(--color-danger)]">
            <svg className="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </div>
        ) : (
          <div className="flex h-6 w-6 items-center justify-center rounded-full border border-[var(--color-border-strong)] text-[10px] text-[var(--color-text-dimmer)]">
            {step.index + 1}
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1">
        <p
          className={`text-sm ${
            step.status === 'succeeded'
              ? 'text-[var(--color-text)]'
              : step.status === 'running'
              ? 'text-[var(--color-accent)] font-medium'
              : step.status === 'failed'
              ? 'text-[var(--color-danger)] font-medium'
              : 'text-[var(--color-text-dimmer)]'
          }`}
        >
          {step.name}
        </p>
        <p className="mt-0.5 flex items-center gap-2 text-[11px] text-[var(--color-text-dimmer)]">
          {fmtTime(step.startedAt) ? <span>started {fmtTime(step.startedAt)}</span> : null}
          {step.message ? <span>· {step.message}</span> : null}
        </p>
      </div>
    </li>
  )
}
