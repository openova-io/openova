/**
 * JobDetail.test.tsx — lock-in for the v3 JobDetail surface (this PR).
 *
 * Coverage:
 *   • Tab strip has EXACTLY two tabs labeled "Flow" and "Exec Log".
 *   • Default-active tab = Flow.
 *   • Dependencies + Apps tabs are GONE (removed in v3).
 *   • Flow tab renders the embedded FlowPage (data-testid='flow-page-embedded').
 *   • Exec Log tab renders the ExecutionLogs viewer.
 *   • [Regression] When the URL jobId is in the BACKEND format
 *     (`<deploymentId>:install-cilium`), JobDetail looks it up via
 *     mergeJobs(reducerJobs, liveJobs) — NOT deriveJobs alone — and
 *     renders the populated job view rather than the not-found state.
 *     This locks in the fix for the regression where every Flow-canvas
 *     double-click landed on "is not part of this deployment".
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
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
 * `/jobs` URL and an empty event slice on the `/events` URL. Lets us
 * assert that backend-format ids (e.g. `d1:install-cilium`) resolve via
 * mergeJobs() rather than falling through to the not-found state.
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
    // Live backfill ENABLED so useLiveJobsBackfill fetches the stubbed
    // jobs payload. SSE is still disabled (jsdom can't drive it).
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
  // URL-aware fetch stub: /jobs → liveJobs; everything else → empty
  // events slice (matches the default useDeploymentEvents shape).
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

describe('JobDetail — v3 tab strip', () => {
  it('renders exactly 2 tabs labeled Flow + Exec Log', async () => {
    // Use a known fixture job id — `bp-cilium` lives in the bootstrap-kit
    // batch and is part of the default-application catalog.
    renderDetail('d-1', 'bp-cilium')
    const tablist = await screen.findByTestId('job-detail-tablist')
    const tabs = tablist.querySelectorAll('[role="tab"]')
    expect(tabs.length).toBe(2)
    const labels = Array.from(tabs).map((t) => (t.textContent ?? '').trim())
    expect(labels).toEqual(['Flow', 'Exec Log'])
  })

  it('Flow tab is active by default', async () => {
    renderDetail('d-1', 'bp-cilium')
    const flowTab = await screen.findByTestId('job-detail-tab-flow')
    expect(flowTab.getAttribute('aria-selected')).toBe('true')
  })

  it('does NOT render Dependencies or Apps tabs (v2 retired)', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    expect(screen.queryByTestId('job-detail-tab-dependencies')).toBeNull()
    expect(screen.queryByTestId('job-detail-tab-apps')).toBeNull()
    expect(screen.queryByTestId('job-detail-deps-panel')).toBeNull()
    expect(screen.queryByTestId('job-detail-apps-panel')).toBeNull()
  })

  it('Flow tab panel mounts the embedded FlowPage canvas', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    expect(screen.queryByTestId('job-detail-flow-panel')).toBeTruthy()
    expect(screen.queryByTestId('flow-page-embedded')).toBeTruthy()
  })

  it('clicking Exec Log tab swaps the active panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    const logTab = screen.getByTestId('job-detail-tab-logs')
    fireEvent.click(logTab)
    expect(logTab.getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByTestId('job-detail-logs-panel')).toBeTruthy()
    expect(screen.queryByTestId('job-detail-flow-panel')).toBeNull()
  })
})

describe('JobDetail — backend-format jobId lookup (regression for #245 not-found)', () => {
  // Without the fix, the FlowPage navigated to JobDetail with a
  // backend-format id ("d1:install-cilium") and JobDetail looked it up
  // against deriveJobs() output only — which uses catalog ids
  // ("bp-cilium"). The lookup missed and JobDetail rendered the
  // not-found state for every Flow-canvas double-click.
  //
  // The fix mirrors JobsPage / FlowPage: deriveJobs → adaptDerivedJobsToFlat
  // → mergeJobs(reducerJobs, liveJobs). When the live API returns a row
  // whose id matches the URL jobId, mergeJobs surfaces it.
  it('renders the populated job view when jobId is in the backend "<deploymentId>:install-<x>" format', async () => {
    const deploymentId = 'd1'
    const jobId = `${deploymentId}:install-cilium`
    const liveJobs: Job[] = [
      {
        id: jobId,
        jobName: 'Install Cilium',
        appId: 'bp-cilium',
        batchId: 'applications',
        dependsOn: ['cluster-bootstrap'],
        status: 'running',
        startedAt: '2026-04-29T10:00:00Z',
        finishedAt: null,
        durationMs: 5_000,
      },
    ]
    renderDetailWithLiveJobs(deploymentId, jobId, liveJobs)

    // Header populated → NOT the not-found state.
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeNull()
      expect(screen.queryByTestId(`job-detail-${jobId}`)).toBeTruthy()
    })
    expect(screen.getByTestId('job-detail-title').textContent).toBe('Install Cilium')
    // Tablist still mounts (Flow + Exec Log).
    const tablist = screen.getByTestId('job-detail-tablist')
    const tabs = tablist.querySelectorAll('[role="tab"]')
    expect(tabs.length).toBe(2)
  })

  it('still renders not-found when no live job AND no reducer-derived job matches', async () => {
    const deploymentId = 'd1'
    // Backend format id but the live API has no rows; reducer derives
    // catalog ids only — no match either way.
    renderDetailWithLiveJobs(deploymentId, `${deploymentId}:install-cilium`, [])
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeTruthy()
    })
  })
})

describe('JobDetail — Exec Log tab wires the real execution id (regression for #305)', () => {
  // Without the fix, the JobDetail page constructed a synthetic
  // executionId of `${jobId}:latest` and passed it to <ExecutionLogs>.
  // The catalyst-api never had a `:latest` route — every log fetch
  // returned 404 and the viewer rendered "Failed to load log page".
  //
  // The fix routes JobDetail through useJobDetail() which fetches
  // `/api/v1/deployments/{depId}/jobs/{jobId}` and uses `executions[0].id`
  // as the real exec id.
  function renderDetailWithJobAndExecutions(
    deploymentId: string,
    jobId: string,
    job: Job,
    executions: Array<{ id: string; jobId: string; deploymentId: string; status: string; startedAt: string; lineCount: number }>,
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
      // /jobs (list) → emit the same job so mergeJobs surfaces it.
      if (url.endsWith(`/v1/deployments/${encodeURIComponent(deploymentId)}/jobs`)) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ jobs: [job] }),
        } as unknown as Response)
      }
      // /jobs/{jobId} (detail) → emit the executions[].
      if (
        url.endsWith(
          `/v1/deployments/${encodeURIComponent(deploymentId)}/jobs/${encodeURIComponent(jobId)}`,
        )
      ) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ job, executions }),
        } as unknown as Response)
      }
      // /executions/{execId}/logs → empty page (the test does not exercise
      // pagination, just that the URL is constructed against the REAL exec id).
      if (url.includes('/v1/actions/executions/')) {
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({ lines: [], total: 0, executionFinished: false }),
        } as unknown as Response)
      }
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({ events: [], state: undefined, done: false }),
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

  it('uses executions[0].id (NOT `${jobId}:latest`) when fetching log lines', async () => {
    const deploymentId = 'd1'
    const jobId = `${deploymentId}:install-seaweedfs`
    const realExecId = 'df9893393d6cd84da027c4115674c1a0'

    const job: Job = {
      id: jobId,
      jobName: 'install-seaweedfs',
      appId: 'seaweedfs',
      batchId: 'bootstrap-kit',
      dependsOn: [],
      status: 'running',
      startedAt: '2026-04-30T09:00:00Z',
      finishedAt: null,
      durationMs: 0,
    }

    // Spy on fetch to capture every URL the component requests.
    const seenUrls: string[] = []
    renderDetailWithJobAndExecutions(deploymentId, jobId, job, [
      {
        id: realExecId,
        jobId,
        deploymentId,
        status: 'running',
        startedAt: '2026-04-30T09:00:00Z',
        lineCount: 1,
      },
    ])
    const inner = globalThis.fetch
    globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      seenUrls.push(url)
      return inner(input, init)
    }) as typeof fetch

    fireEvent.click(await screen.findByTestId('job-detail-tab-logs'))

    await waitFor(() => {
      // Real exec id appears in at least one log fetch URL.
      expect(seenUrls.some((u) => u.includes(`/v1/actions/executions/${realExecId}/logs`)))
        .toBe(true)
      // Synthetic `:latest` id MUST NOT appear in any URL.
      expect(seenUrls.some((u) => u.includes(`${jobId}:latest`))).toBe(false)
    })
  })

  it('renders the placeholder (not the log viewer) when executions[] is empty', async () => {
    const deploymentId = 'd1'
    const jobId = `${deploymentId}:install-seaweedfs`
    const job: Job = {
      id: jobId,
      jobName: 'install-seaweedfs',
      appId: 'seaweedfs',
      batchId: 'bootstrap-kit',
      dependsOn: [],
      status: 'running',
      startedAt: null,
      finishedAt: null,
      durationMs: 0,
    }
    renderDetailWithJobAndExecutions(deploymentId, jobId, job, [])
    fireEvent.click(await screen.findByTestId('job-detail-tab-logs'))
    await waitFor(() => {
      // One of the placeholder testids must be present; the GitLab-CI
      // viewer must NOT mount with a fake id.
      const placeholder =
        screen.queryByTestId('job-detail-logs-pending') ||
        screen.queryByTestId('job-detail-logs-empty') ||
        screen.queryByTestId('job-detail-logs-loading')
      expect(placeholder).toBeTruthy()
      expect(screen.queryByTestId('execution-logs')).toBeNull()
    })
  })
})
