/**
 * JobDetail.test.tsx — lock-in for the v3 JobDetail surface.
 *
 * v3 surface (PR #351 / #353):
 *   • No tab strip. Full-bleed FlowPage canvas + floating LogPane on
 *     the right (open by default on the host job's logs).
 *   • Two-line header: title + status chip on top; subtitle pieces
 *     beneath (jobId · appId · parent · last-update).
 *   • The previous Flow / Exec Log tabs are gone.
 *
 * Coverage:
 *   • Populated header renders for both catalog-format ids ("bp-cilium")
 *     and backend-format ids ("<deploymentId>:install-<x>").
 *   • The not-found panel renders when neither reducer-derived nor live
 *     jobs match.
 *   • [Bug #476] Clicking a job whose URL contains the backend's colon-
 *     format id (e.g. `infrastructure:tofu-apply`) MUST resolve within
 *     2 seconds — the v3 surface used to hang the browser when the
 *     `cluster-bootstrap` group slug collided with the bare leaf id.
 *
 * Companion: JobDetail.hang.regression.test.tsx — focused E2E for the
 *   #476 hang reproduction.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { JobDetail } from './JobDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import type { Job } from '@/lib/jobs.types'

function renderDetail(deploymentId: string, jobId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <JobDetail disableStream disableJobsBackfill />,
  })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, flowRoute, jobsRoute, homeRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs/${jobId}`],
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

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

/**
 * Regression-test helper — renders JobDetail with the live-jobs backfill
 * ENABLED, plus a fetch stub that returns the supplied liveJobs on the
 * `/jobs` URL and an empty event slice on the `/events` URL.
 */
function renderDetailWithLiveJobs(
  deploymentId: string,
  jobId: string,
  liveJobs: Job[],
) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <JobDetail disableStream />,
  })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, flowRoute, jobsRoute, homeRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs/${jobId}`],
    }),
  })
  globalThis.fetch = ((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.endsWith(`/v1/deployments/${encodeURIComponent(deploymentId)}/jobs`)) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ jobs: liveJobs }),
      } as unknown as Response)
    }
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)
  }) as typeof fetch
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('JobDetail — v3 surface (full-bleed canvas + LogPane, no tab strip)', () => {
  it('renders the two-line header with the populated jobId', async () => {
    renderDetail('d-1', 'bp-cilium')
    await waitFor(() => {
      const header = screen.queryByTestId('job-detail-header')
      const title = screen.queryByTestId('job-detail-title')
      expect(header).toBeTruthy()
      expect(title).toBeTruthy()
    })
  })

  it('mounts the embedded FlowPage canvas (full-bleed, no tabs)', async () => {
    renderDetail('d-1', 'bp-cilium')
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-canvas')).toBeTruthy()
      expect(screen.queryByTestId('flow-page-embedded')).toBeTruthy()
    })
  })

  it('does NOT render the v2 tablist or per-tab panels', async () => {
    renderDetail('d-1', 'bp-cilium')
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-canvas')).toBeTruthy()
    })
    expect(screen.queryByTestId('job-detail-tablist')).toBeNull()
    expect(screen.queryByTestId('job-detail-tab-flow')).toBeNull()
    expect(screen.queryByTestId('job-detail-tab-logs')).toBeNull()
    expect(screen.queryByTestId('job-detail-flow-panel')).toBeNull()
    expect(screen.queryByTestId('job-detail-logs-panel')).toBeNull()
  })
})

describe('JobDetail — backend-format jobId lookup (regression for #245 not-found)', () => {
  // Without the fix, the FlowPage navigated to JobDetail with a
  // backend-format id ("d1:install-cilium") and JobDetail looked it up
  // against deriveJobs() output only — which uses catalog ids
  // ("bp-cilium"). The lookup missed and JobDetail rendered the
  // not-found state for every Flow-canvas double-click.
  it('renders the populated job view when jobId is in the backend "<deploymentId>:install-<x>" format', async () => {
    const deploymentId = 'd1'
    const jobId = `${deploymentId}:install-cilium`
    const liveJobs: Job[] = [
      {
        id: jobId,
        jobName: 'Install Cilium',
        type: 'install',
        appId: 'bp-cilium',
        parentId: 'applications',
        childIds: [],
        dependsOn: ['cluster-bootstrap'],
        status: 'running',
        startedAt: '2026-04-29T10:00:00Z',
        finishedAt: null,
        durationMs: 5_000,
      },
    ]
    renderDetailWithLiveJobs(deploymentId, jobId, liveJobs)

    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeNull()
      expect(screen.queryByTestId(`job-detail-${jobId}`)).toBeTruthy()
    })
    expect(screen.getByTestId('job-detail-title').textContent).toBe('Install Cilium')
  })

  it('still renders not-found when no live job AND no reducer-derived job matches', async () => {
    const deploymentId = 'd1'
    renderDetailWithLiveJobs(deploymentId, `${deploymentId}:install-non-existent-component`, [])
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeTruthy()
    })
  })
})
