/**
 * JobsPage.degraft.test.tsx — lock-in for issue #4704.
 *
 * PR #4221 grafted the 16-phase `BootstrapProgress` widget on top of the
 * finite-jobs table at /provision/<id>/jobs. That violated the agreed
 * console information architecture: Jobs is the FINITE-work surface;
 * provisioning progress is a transient boot sequence that belongs on the
 * provision Dashboard (`/provision/<id>/dashboard`), whose Progress ⇄
 * Treemap pane auto-flips to the FleetTreemap on `status==ready` (that
 * behaviour has its own suite in ConvergenceWizard.test.tsx).
 *
 * This file asserts the de-graft holds: while a provisioning run is
 * in-flight — the exact window the #4221 graft used to render in —
 * /provision/<id>/jobs renders ONLY the jobs table, with zero
 * BootstrapProgress DOM.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { JobsPage } from './JobsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderJobs(deploymentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => (
      // disableStream suppresses the deployment-events SSE (jsdom has no
      // EventSource); disableJobsBackfill suppresses the live /jobs poll.
      <JobsPage disableStream disableJobsBackfill />
    ),
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const tree = rootRoute.addChildren([jobsRoute, homeRoute, jobDetailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs`],
    }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('JobsPage — #4704 pure finite-jobs table (BootstrapProgress de-grafted)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    // `done: false` keeps the deployment in-flight — the exact window the
    // #4221 graft used to mount the BootstrapProgress timeline in.
    globalThis.fetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ events: [], state: undefined, done: false }),
      } as unknown as Response),
    ) as unknown as typeof fetch
  })

  it('renders ONLY the jobs table while provisioning is in-flight — no BootstrapProgress section', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    // The #4221 graft section must never come back on this surface.
    expect(screen.queryByTestId('sov-jobs-bootstrap-progress')).toBeNull()
    // Nor the widget's own landmark, under any other wrapper.
    expect(
      document.querySelector('nav[aria-label="Bootstrap provisioning progress"]'),
    ).toBeNull()
  })
})
