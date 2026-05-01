/**
 * Regression — bug #476.
 *
 * Clicking any link in the Jobs list (eg `/jobs/infrastructure:tofu-apply`)
 * froze the browser indefinitely. Root cause: `adaptDerivedJobsToFlat`
 * synthesised a "Cluster Bootstrap" group whose slug was
 * `cluster-bootstrap` — colliding with the bare `cluster-bootstrap`
 * leaf job's id. byId.set() is last-wins, so the leaf overwrote the
 * group, leaving the leaf with `parentId === its own id`. The layout's
 * `isVisible()` walked that self-reference forever.
 *
 * Fix:
 *   1. Rename the group slug to `phase-1-bootstrap` so it cannot
 *      collide with any leaf id.
 *   2. Cycle-protect the layout's parent-chain walks so a malformed
 *      input degrades gracefully instead of hanging the browser.
 *
 * Locks both fixes: this test mounts JobDetail with a real
 * deployment id and the same wizard state defaults a freshly-loaded
 * provision page would carry, then asserts the page renders within
 * 2 seconds — anything longer and the regression has returned.
 */
import { it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import {
  RouterProvider, createRouter, createRootRoute, createRoute, createMemoryHistory, Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { JobDetail } from './JobDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})
afterEach(() => cleanup())

it('renders within 2s when given the colon-format URL the JobsTable links to (no infinite-loop hang) — bug #476', async () => {
  const deploymentId = 'd1'
  const jobId = 'infrastructure:tofu-apply'
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <JobDetail disableStream disableJobsBackfill />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, jobsRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs/${jobId}`],
    }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  // Either the populated job header OR the not-found panel must mount
  // within 2 seconds. Pre-fix this test timed out at 3000ms because
  // the parent-chain walk in flowLayoutOrganic looped forever.
  await waitFor(() => {
    const populated = screen.queryByTestId(`job-detail-${jobId}`)
    const notFound = screen.queryByTestId('job-detail-not-found')
    expect(populated ?? notFound).toBeTruthy()
  }, { timeout: 2000 })
})

it('renders within 2s for a plain (no-colon) jobId — bug #476 baseline', async () => {
  const deploymentId = 'd1'
  const jobId = 'some-plain-id'
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <JobDetail disableStream disableJobsBackfill />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, jobsRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs/${jobId}`],
    }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  await waitFor(() => {
    expect(screen.queryByTestId('job-detail-not-found')).toBeTruthy()
  }, { timeout: 2000 })
})
