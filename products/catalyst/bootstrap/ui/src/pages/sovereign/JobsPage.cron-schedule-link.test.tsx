/**
 * JobsPage.cron-schedule-link.test.tsx — P3 (Refs #6703).
 *
 * The consolidated Schedule view is reached from the /jobs CronJob chip via a
 * contextual "View schedule" link. This locks that the affordance:
 *   • renders ONLY when the cron chip is active in list view;
 *   • is absent for another kind, and absent in graph view;
 *   • points at the `/jobs/schedule` route.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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

interface JobsSearch {
  view?: 'graph' | 'list'
  kind?: string
}

function renderJobs(initialEntry: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsPage disableStream disableJobsBackfill />,
    validateSearch: (raw: Record<string, unknown>): JobsSearch => {
      const out: JobsSearch = {}
      if (raw.view === 'graph' || raw.view === 'list') out.view = raw.view
      if (typeof raw.kind === 'string' && raw.kind.length > 0) out.kind = raw.kind
      return out
    },
  })
  const scheduleRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/schedule',
    component: () => <div data-testid="schedule-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const tree = rootRoute.addChildren([jobsRoute, scheduleRoute, homeRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('JobsPage — View schedule affordance', () => {
  it('shows the link when the cron chip is active, pointing at /jobs/schedule', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=cron')
    const link = await screen.findByTestId('jobs-cron-view-schedule')
    expect(link).toBeTruthy()
    expect(link.getAttribute('href')).toContain('/provision/d-1/jobs/schedule')
  })

  it('hides the link for a non-cron kind', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    await screen.findByTestId('jobs-kind-dropdown')
    expect(screen.queryByTestId('jobs-cron-view-schedule')).toBeNull()
  })

  it('hides the link in graph view', async () => {
    renderJobs('/provision/d-1/jobs?view=graph&kind=cron')
    await screen.findByTestId('jobs-graph-view')
    expect(screen.queryByTestId('jobs-cron-view-schedule')).toBeNull()
  })
})
