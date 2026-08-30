/**
 * JobKindChips.test.tsx — the /jobs chip strip contract (P1b, Refs #6703).
 *
 * The standalone `JobKindChips` clone was deleted; the /jobs chip strip is
 * now the shared `KindChipStrip` fed the `JOB_KINDS` catalogue + the
 * `jobs-kind` testidPrefix. These guards pin the SAME operator-visible
 * contract the clone had, now proving it through the shared component:
 *   • chips render the REAL engine labels (HelmRelease / CronJob / …);
 *   • a non-active chip whose count is exactly 0 is HIDDEN, while the
 *     ACTIVE chip stays visible even at 0;
 *   • clicking a chip fires onChange with that kind id;
 *   • the overflow engines live in the `+ More` popover with their labels.
 *
 * The established `jobs-kind-chips` / `jobs-kind-chip-<id>` testids are
 * unchanged — the shared component derives them from the `jobs-kind`
 * prefix.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import type { JobKind } from '@/lib/jobs.types'
import { KindChipStrip } from '../shared/KindChipStrip'
import { JOB_KINDS, type JobChipKind } from './jobKinds'
import { JOB_ENGINE_LABELS } from '@/lib/jobs.types'

const STORAGE_KEY = 'sov-jobs-hidden-kinds'

beforeEach(() => {
  try {
    window.localStorage.clear()
  } catch {
    /* noop */
  }
})
afterEach(cleanup)

function counts(overrides: Partial<Record<JobKind, number | null>>): Record<JobChipKind, number | null> {
  const base = {
    install: 0, reconcile: 0, step: 0, mutation: 0,
    cron: 0, task: 0, reconciler: 0, lifecycle: 0,
  } as Record<JobChipKind, number | null>
  return { ...base, ...(overrides as Record<JobChipKind, number | null>) }
}

/** Renders the /jobs chip strip exactly as JobsPage does. */
function renderJobsChips(props: {
  activeKind: JobChipKind | null
  counts: Record<JobChipKind, number | null>
  onChange?: (k: JobChipKind) => void
}) {
  return render(
    <KindChipStrip<JobChipKind>
      catalogue={JOB_KINDS}
      activeKind={props.activeKind}
      counts={props.counts}
      onChange={props.onChange ?? (() => {})}
      testidPrefix="jobs-kind"
      storageKey={STORAGE_KEY}
    />,
  )
}

describe('JobKindChips (via shared KindChipStrip)', () => {
  it('renders primary chips with their REAL engine labels', () => {
    renderJobsChips({
      activeKind: 'install',
      counts: counts({ install: 5, cron: 3, mutation: 2, lifecycle: 1 }),
    })
    // Labels come from JOB_ENGINE_LABELS — assert the actual engine names.
    expect(screen.getByTestId('jobs-kind-chip-install').textContent).toContain(JOB_ENGINE_LABELS.install) // HelmRelease
    expect(screen.getByTestId('jobs-kind-chip-cron').textContent).toContain(JOB_ENGINE_LABELS.cron) // CronJob
    expect(screen.getByTestId('jobs-kind-chip-mutation').textContent).toContain(JOB_ENGINE_LABELS.mutation) // Crossplane
    expect(screen.getByTestId('jobs-kind-chip-lifecycle').textContent).toContain(JOB_ENGINE_LABELS.lifecycle) // OpenTofu
    // The active chip's count badge shows the number.
    expect(screen.getByTestId('jobs-kind-chip-install-count').textContent).toBe('5')
  })

  it('HIDES a non-active chip whose count is exactly 0', () => {
    renderJobsChips({
      activeKind: 'install',
      counts: counts({ install: 5, step: 0, task: 0 }),
    })
    // step + task are primary but 0 and non-active → not rendered.
    expect(screen.queryByTestId('jobs-kind-chip-step')).toBeNull()
    expect(screen.queryByTestId('jobs-kind-chip-task')).toBeNull()
    // install stays (active).
    expect(screen.queryByTestId('jobs-kind-chip-install')).toBeTruthy()
  })

  it('keeps the ACTIVE chip visible even when its count is 0', () => {
    renderJobsChips({
      activeKind: 'step',
      counts: counts({ step: 0, install: 4 }),
    })
    const active = screen.getByTestId('jobs-kind-chip-step')
    expect(active).toBeTruthy()
    expect(active.getAttribute('data-active')).toBe('true')
    expect(screen.getByTestId('jobs-kind-chip-step-count').textContent).toBe('0')
  })

  it('fires onChange with the clicked kind id', () => {
    const onChange = vi.fn()
    renderJobsChips({
      activeKind: 'install',
      counts: counts({ install: 5, cron: 3 }),
      onChange,
    })
    fireEvent.click(screen.getByTestId('jobs-kind-chip-cron'))
    expect(onChange).toHaveBeenCalledWith('cron')
  })

  it('exposes the overflow engines (Kustomization / Deployment) in the + More popover', () => {
    const onChange = vi.fn()
    renderJobsChips({
      activeKind: 'install',
      counts: counts({ install: 5, reconcile: 2, reconciler: 1 }),
      onChange,
    })
    fireEvent.click(screen.getByTestId('jobs-kind-chip-more'))
    const reconcileItem = screen.getByTestId('jobs-kind-chip-more-item-reconcile')
    const reconcilerItem = screen.getByTestId('jobs-kind-chip-more-item-reconciler')
    expect(reconcileItem.textContent).toContain(JOB_ENGINE_LABELS.reconcile) // Kustomization
    expect(reconcilerItem.textContent).toContain(JOB_ENGINE_LABELS.reconciler) // Deployment
    fireEvent.click(reconcilerItem)
    expect(onChange).toHaveBeenCalledWith('reconciler')
  })
})
