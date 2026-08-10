/**
 * JobsTable.single-mechanism-3646.test.tsx — UAT row 177 (issue #3646).
 *
 * Row 177, verbatim:
 *
 *   "Use the same Re-run button on a Failed row — one remediation mechanism
 *    across rows, no per-kind UI (the single-table/single… shape)."
 *
 * The row's ASSERTION is a shape claim, and until now that claim was only ever
 * checked by eye. Three walks reported it — hw291 from source, hw292 from live
 * DOM across 178 rows, hw292 again with the retryable leaf named — and all
 * three had to re-derive it by hand because nothing in the suite pinned it.
 * A shape nobody tests is a shape that regresses on the next feature: one
 * `kind === 'cron' ? <RunNowButton/> : …` in the Actions cell would break the
 * row and every existing jobs test would stay green.
 *
 * So this file pins the contract at the three places it can be broken:
 *
 *   1. the AFFORDANCE SET — across every JobKind the table serves, the Actions
 *      column renders exactly two things: the one shared retry button on a
 *      retryable row, and the empty dash on every other row;
 *   2. the WIRE — clicking that button on every kind POSTs the SAME endpoint
 *      template, so "one mechanism" is true of the remediation itself and not
 *      just of the pixels;
 *   3. the ONLY per-kind variation that is allowed — the button's LABEL.
 *
 * WHAT THIS FILE CANNOT DO. Row 177 also says the button is USED. Firing it on
 * a live Sovereign is a mutation and is not something a source test can stand
 * in for; the live click stays a walk item (retry target already identified:
 * jobName=task-syft-sbom on hw292, where PASS is runCount/latestExecutionId
 * moving, not the HTTP code). Everything short of that click is pinned here.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

/** Every POST the table's controls make, in order. */
const posted: { url: string; method: string }[] = []

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: async (url: string, init?: RequestInit) => {
    posted.push({ url, method: init?.method ?? 'GET' })
    return new Response(JSON.stringify({ executionId: 'e1', action: 'annotated' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  },
}))

import { JobsTable } from './JobsTable'
import { retryLabel } from './retryJobFeedback'
import type { Job, JobKind, JobStatus } from '@/lib/jobs.types'

afterEach(() => cleanup())
beforeEach(() => {
  posted.length = 0
})

/**
 * EVERY kind in the JobKind union — enumerated deliberately rather than
 * sampled. The live hw292 table served five of these (install 134, task 21,
 * step 14, cron 5, lifecycle 5, group 4); a walk can only ever observe the
 * kinds that happen to be present, which is exactly why "no per-kind UI" needs
 * a test that covers the kinds a given environment does NOT contain.
 */
const ALL_KINDS: readonly JobKind[] = [
  'install',
  'reconcile',
  'step',
  'mutation',
  'cron',
  'task',
  'reconciler',
  'lifecycle',
]

/**
 * `group` is the ninth JobKind and is deliberately NOT in the list above:
 * JobsTable filters `type === 'group'` out of `visibleJobs` (JobsTable.tsx
 * ~line 402) because group parents would dominate the sort — operators reach a
 * group through a leaf row's Parent chip. It gets its own assertion below
 * rather than a silent omission, since "the group row has no second control"
 * is part of the same shape claim. This is also why the live hw292 table
 * showed 178 rows against 183 from the jobs API.
 */
const GROUP_KIND: JobKind = 'group'

/** Statuses the table renders; the retryable ones are failed/degraded/failing. */
const ALL_STATUSES: readonly JobStatus[] = [
  'failed',
  'degraded',
  'failing',
  'succeeded',
  'running',
  'pending',
  'healthy',
]

function row(kind: JobKind, status: JobStatus): Job {
  const jobName = `${kind}-${status}-x`
  return {
    id: `d-1:${jobName}`,
    jobName,
    displayName: jobName,
    type: kind === 'group' ? 'group' : 'install',
    kind,
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status,
    startedAt: '2026-08-10T10:00:00Z',
    finishedAt: status === 'running' || status === 'pending' ? null : '2026-08-10T10:01:00Z',
    durationMs: 60000,
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

/** Every Actions cell in the rendered table. */
function actionCells(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('td.jobs-cell-actions'))
}

describe('row 177 — ONE remediation mechanism, no per-kind UI', () => {
  it('renders exactly two Actions affordances across every kind × status', async () => {
    const jobs = ALL_KINDS.flatMap((k) => ALL_STATUSES.map((s) => row(k, s)))
    renderJobs(jobs)
    await screen.findByTestId('jobs-table')

    const cells = actionCells()
    // Control: if the Actions column did not render at all, every assertion
    // below would pass on an empty set.
    expect(cells.length).toBe(jobs.length)

    // Collapse each cell's testids to a SHAPE (the id suffix is per-row).
    const shapes = new Set<string>()
    for (const cell of cells) {
      const ids = Array.from(cell.querySelectorAll<HTMLElement>('[data-testid]')).map(
        (el) => el.getAttribute('data-testid') ?? '',
      )
      expect(ids.length).toBeGreaterThan(0)
      for (const id of ids) {
        shapes.add(
          id.startsWith('jobs-retry-')
            ? 'jobs-retry-*'
            : id.startsWith('jobs-cell-actions-empty-')
              ? 'jobs-cell-actions-empty-*'
              : `UNEXPECTED:${id}`,
        )
      }
    }

    expect([...shapes].sort()).toEqual(['jobs-cell-actions-empty-*', 'jobs-retry-*'])

    // And the same statement at the element level: no second kind of control
    // ever appears in an Actions cell.
    for (const cell of cells) {
      const buttons = cell.querySelectorAll('button')
      expect(buttons.length).toBeLessThanOrEqual(1)
      expect(cell.querySelectorAll('select').length).toBe(0)
      expect(cell.querySelectorAll('a').length).toBe(0)
    }
  })

  it('does not render a group parent as a second remediation surface', async () => {
    const failedLeaf = row('task', 'failed')
    const failedGroup = row(GROUP_KIND, 'failed')
    renderJobs([failedLeaf, failedGroup])
    await screen.findByTestId('jobs-table')

    // The group parent aggregates its children's failure (on hw292 the two
    // failed rows were ONE failure: task-syft-sbom and its parent group). It
    // must not therefore grow its own control — one leaf, one mechanism.
    expect(screen.queryByTestId(`jobs-retry-${failedGroup.jobName}`)).toBeNull()
    expect(screen.getByTestId(`jobs-retry-${failedLeaf.jobName}`)).toBeTruthy()
    expect(actionCells().length).toBe(1)
  })

  it('gates the one control on STATUS alone — never on kind', async () => {
    const jobs = ALL_KINDS.flatMap((k) => ALL_STATUSES.map((s) => row(k, s)))
    renderJobs(jobs)
    await screen.findByTestId('jobs-table')

    for (const kind of ALL_KINDS) {
      for (const status of ALL_STATUSES) {
        const jobName = `${kind}-${status}-x`
        const retryable = status === 'failed' || status === 'degraded' || status === 'failing'
        const btn = screen.queryByTestId(`jobs-retry-${jobName}`)
        if (retryable) {
          expect(btn, `${kind}/${status} must offer the shared control`).not.toBeNull()
        } else {
          expect(btn, `${kind}/${status} must offer no control`).toBeNull()
          expect(screen.queryByTestId(`jobs-cell-actions-empty-d-1:${jobName}`)).not.toBeNull()
        }
      }
    }
  })

  it('POSTs the same endpoint template from every kind', async () => {
    const jobs = ALL_KINDS.map((k) => row(k, 'failed'))
    renderJobs(jobs)
    await screen.findByTestId('jobs-table')

    for (const kind of ALL_KINDS) {
      fireEvent.click(screen.getByTestId(`jobs-retry-${kind}-failed-x`))
    }
    await waitFor(() => expect(posted.length).toBe(ALL_KINDS.length))

    // One endpoint SHAPE for all nine kinds — the difference between "one
    // button" and "one remediation mechanism". A per-kind route (…/cron/run,
    // …/steps/{n}/rerun) would show up here as a second template.
    const templates = new Set(
      posted.map((p) => p.url.replace(/\/jobs\/[^/]+\/retry$/, '/jobs/{jobName}/retry')),
    )
    expect([...templates]).toEqual(['/api/v1/deployments/d-1/jobs/{jobName}/retry'])
    expect(new Set(posted.map((p) => p.method))).toEqual(new Set(['POST']))

    // Each click addressed its OWN row — one shared template must not mean
    // one shared target.
    expect(new Set(posted.map((p) => p.url)).size).toBe(ALL_KINDS.length)
  })

  it('varies the LABEL by kind and nothing else', async () => {
    const jobs = ALL_KINDS.map((k) => row(k, 'failed'))
    renderJobs(jobs)
    await screen.findByTestId('jobs-table')

    for (const kind of ALL_KINDS) {
      const btn = screen.getByTestId(`jobs-retry-${kind}-failed-x`)
      // The verb comes from the shared label ladder, so the button and the
      // backend's dispatch stay described by one table.
      expect(btn.textContent).toBe(retryLabel(kind))
      expect(btn.getAttribute('data-kind')).toBe(kind)
      // Same element, same class — a per-kind widget would differ here.
      expect(btn.className).toContain('jobs-retry-btn')
      expect(btn.tagName.toLowerCase()).toBe('button')
    }

    // "Re-run" — the verb row 177 names — is what a task/step row reads.
    expect(screen.getByTestId('jobs-retry-task-failed-x').textContent).toBe('Re-run')
    expect(screen.getByTestId('jobs-retry-step-failed-x').textContent).toBe('Re-run')
  })
})
