/**
 * JobKindChips.test.tsx — P1b (Refs #6703).
 *
 * The /jobs chip strip is a pure renderer over the JOB_KINDS catalogue.
 * These guards pin the operator-visible contract:
 *   • chips render the REAL engine labels (HelmRelease / CronJob / …),
 *     never a fabricated abstraction;
 *   • a non-active chip whose count is exactly 0 is HIDDEN (founder rule —
 *     don't show an engine with no rows), while the ACTIVE chip stays
 *     visible even at 0 so context survives navigating to an empty kind;
 *   • clicking a chip fires onChange with that kind id;
 *   • the overflow engines live in the `+ More` popover with their labels.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import type { JobKind } from '@/lib/jobs.types'
import { JobKindChips } from './JobKindChips'
import { JOB_ENGINE_LABELS } from '@/lib/jobs.types'

afterEach(cleanup)

function counts(overrides: Partial<Record<JobKind, number | null>>): Record<JobKind, number | null> {
  const base = {
    install: 0, reconcile: 0, step: 0, mutation: 0,
    cron: 0, task: 0, reconciler: 0, lifecycle: 0, group: 0,
  } as Record<JobKind, number | null>
  return { ...base, ...overrides }
}

describe('JobKindChips', () => {
  it('renders primary chips with their REAL engine labels', () => {
    render(
      <JobKindChips
        activeKind="install"
        counts={counts({ install: 5, cron: 3, mutation: 2, lifecycle: 1 })}
        onChange={() => {}}
      />,
    )
    // Labels come from JOB_ENGINE_LABELS — assert the actual engine names.
    expect(screen.getByTestId('jobs-kind-chip-install').textContent).toContain(JOB_ENGINE_LABELS.install) // HelmRelease
    expect(screen.getByTestId('jobs-kind-chip-cron').textContent).toContain(JOB_ENGINE_LABELS.cron) // CronJob
    expect(screen.getByTestId('jobs-kind-chip-mutation').textContent).toContain(JOB_ENGINE_LABELS.mutation) // Crossplane
    expect(screen.getByTestId('jobs-kind-chip-lifecycle').textContent).toContain(JOB_ENGINE_LABELS.lifecycle) // OpenTofu
    // The active chip's count badge shows the number.
    expect(screen.getByTestId('jobs-kind-chip-install-count').textContent).toBe('5')
  })

  it('HIDES a non-active chip whose count is exactly 0', () => {
    render(
      <JobKindChips
        activeKind="install"
        counts={counts({ install: 5, step: 0, task: 0 })}
        onChange={() => {}}
      />,
    )
    // step + task are primary but 0 and non-active → not rendered.
    expect(screen.queryByTestId('jobs-kind-chip-step')).toBeNull()
    expect(screen.queryByTestId('jobs-kind-chip-task')).toBeNull()
    // install stays (active).
    expect(screen.queryByTestId('jobs-kind-chip-install')).toBeTruthy()
  })

  it('keeps the ACTIVE chip visible even when its count is 0', () => {
    render(
      <JobKindChips
        activeKind="step"
        counts={counts({ step: 0, install: 4 })}
        onChange={() => {}}
      />,
    )
    const active = screen.getByTestId('jobs-kind-chip-step')
    expect(active).toBeTruthy()
    expect(active.getAttribute('data-active')).toBe('true')
    expect(screen.getByTestId('jobs-kind-chip-step-count').textContent).toBe('0')
  })

  it('fires onChange with the clicked kind id', () => {
    const onChange = vi.fn()
    render(
      <JobKindChips
        activeKind="install"
        counts={counts({ install: 5, cron: 3 })}
        onChange={onChange}
      />,
    )
    fireEvent.click(screen.getByTestId('jobs-kind-chip-cron'))
    expect(onChange).toHaveBeenCalledWith('cron')
  })

  it('exposes the overflow engines (Kustomization / Deployment) in the + More popover', () => {
    const onChange = vi.fn()
    render(
      <JobKindChips
        activeKind="install"
        counts={counts({ install: 5, reconcile: 2, reconciler: 1 })}
        onChange={onChange}
      />,
    )
    fireEvent.click(screen.getByTestId('jobs-kind-chip-more'))
    const reconcileItem = screen.getByTestId('jobs-kind-chip-more-item-reconcile')
    const reconcilerItem = screen.getByTestId('jobs-kind-chip-more-item-reconciler')
    expect(reconcileItem.textContent).toContain(JOB_ENGINE_LABELS.reconcile) // Kustomization
    expect(reconcilerItem.textContent).toContain(JOB_ENGINE_LABELS.reconciler) // Deployment
    fireEvent.click(reconcilerItem)
    expect(onChange).toHaveBeenCalledWith('reconciler')
  })
})
