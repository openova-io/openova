/**
 * JobsTable.cutoverRerun.test.tsx — UAT row 165 lock-in (issue #3379).
 *
 * Row 165, verbatim:
 *
 *   "On a failed `cutover-step-*` row, a Re-run button is present
 *    (per-row, gated to Failed) — the operator can re-drive a failed
 *    cutover step from the browser."
 *
 * Both halves are asserted here:
 *
 *   • PRESENT + labelled "Re-run" on a `failed` cutover-step row.
 *   • ABSENT on the SAME row when its status is succeeded / running /
 *     pending — the "gated to Failed" clause. This is the direction that
 *     catches a regression where the gate is loosened to "any row".
 *
 * The row shape mirrors the backend projection in
 * products/catalyst/bootstrap/api/internal/handler/cutover_activity_bridge.go:
 * a leaf whose JobName is `cutover-step-<slug>` (jobs.ActivityStepJobName)
 * and whose Kind is `step`.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobsTable } from './JobsTable'
import type { Job, JobStatus } from '@/lib/jobs.types'

afterEach(() => cleanup())

const CUTOVER_STEP_JOB_NAME = 'cutover-step-egress-block-test'

/** One projected cutover step leaf in the given lifecycle state. */
function cutoverStepRow(status: JobStatus): Job {
  return {
    id: `d-1:${CUTOVER_STEP_JOB_NAME}`,
    jobName: CUTOVER_STEP_JOB_NAME,
    displayName: 'egress-block-test',
    type: 'install',
    kind: 'step',
    appId: '',
    parentId: 'd-1:cutover',
    dependsOn: ['d-1:cutover-step-helmrepository-patches'],
    childIds: [],
    status,
    startedAt: '2026-07-26T10:00:00Z',
    finishedAt: status === 'running' || status === 'pending' ? null : '2026-07-26T10:11:00Z',
    durationMs: status === 'running' || status === 'pending' ? 0 : 660000,
  }
}

function renderJobs(jobs: Job[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsTable jobs={jobs} deploymentId="d-1" />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute, detailRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs'] }),
  })
  return render(<RouterProvider router={router} />)
}

describe('UAT row 165 — per-row Re-run on a FAILED cutover-step row', () => {
  it('renders the Re-run control on a failed cutover-step row', async () => {
    renderJobs([cutoverStepRow('failed')])
    await screen.findByTestId('jobs-table')

    const btn = screen.getByTestId(`jobs-retry-${CUTOVER_STEP_JOB_NAME}`)
    expect(btn).toBeTruthy()
    // Row 165 names the verb: "Re-run", not the generic "Retry reconcile".
    expect(btn.textContent).toBe('Re-run')
    // The control is dispatched on the backend-stamped kind, so a
    // regression that loses the `step` kind is visible here too.
    expect(btn.getAttribute('data-kind')).toBe('step')
  })

  it.each<JobStatus>(['succeeded', 'running', 'pending'])(
    'does NOT render the Re-run control on a %s cutover-step row',
    async (status) => {
      renderJobs([cutoverStepRow(status)])
      await screen.findByTestId('jobs-table')

      expect(screen.queryByTestId(`jobs-retry-${CUTOVER_STEP_JOB_NAME}`)).toBeNull()
      // The Actions cell still renders — it shows the empty marker, so the
      // absence is the GATE firing, not the whole column disappearing.
      expect(
        screen.getByTestId(`jobs-cell-actions-empty-d-1:${CUTOVER_STEP_JOB_NAME}`),
      ).toBeTruthy()
    },
  )

  it('gates per-row: only the failed step in a chain offers Re-run', async () => {
    const succeeded: Job = {
      ...cutoverStepRow('succeeded'),
      id: 'd-1:cutover-step-gitea-mirror',
      jobName: 'cutover-step-gitea-mirror',
      displayName: 'gitea-mirror',
    }
    renderJobs([succeeded, cutoverStepRow('failed')])
    await screen.findByTestId('jobs-table')

    expect(screen.getByTestId(`jobs-retry-${CUTOVER_STEP_JOB_NAME}`)).toBeTruthy()
    expect(screen.queryByTestId('jobs-retry-cutover-step-gitea-mirror')).toBeNull()
  })
})
