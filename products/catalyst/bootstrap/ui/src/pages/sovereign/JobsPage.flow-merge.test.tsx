/**
 * JobsPage.flow-merge.test.tsx — OpenovaFlow snapshot row merge.
 *
 * Regression coverage for TC-035 (iter-1, 2026-05-11):
 *
 *   For Sovereigns where some HelmReleases exist ONLY in the OpenovaFlow
 *   snapshot — verified live with deployment 12e194090631a885: GET
 *   /v1/flows/<id>/snapshot returns 2 leaf nodes (contabo:bp-openova-
 *   flow-server, contabo:bp-openova-flow-emitter) whose ids never appear
 *   in the legacy /api/v1/deployments/<id>/jobs payload — the JobsTable
 *   would silently drop those rows. JobsPage now wires `useFlowStream`
 *   alongside the legacy reducer + live-backfill, synthesizes a Job stub
 *   per FlowNode via `synthesizeJobFromFlowNode` (PR #1412), and appends
 *   the rows whose ids are absent from the legacy set.
 *
 *   Dedup rule: legacy wins. Legacy rows carry real execution timeline,
 *   appId, parentId, dependsOn, etc. — the flow synth is a minimal stub
 *   intended only to surface a row that would otherwise be missing.
 *
 * Cases:
 *   1. Legacy returns 5 jobs, flow stream empty → table shows the 5
 *      legacy rows, no flow rows (no behavior change).
 *   2. Legacy returns 5 jobs, flow stream has 2 distinct ids → table
 *      shows 7 rows with the `contabo:bp-…` flow ids present.
 *   3. Legacy returns 5 jobs INCLUDING one id that also appears in flow
 *      stream → table shows 5 rows (legacy wins dedup).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
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
import type { FlowNode, FlowInstance } from '@openova/flow-core'
import type { Job } from '@/lib/jobs.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/* ────────────────────────────────────────────────────────────────────
 * Module-level mock for useFlowStream — exact same pattern as
 * JobDetail.flow-fallback.test.tsx. Each test seeds `mockStreamState`
 * before render; the JobsPage's `useFlowStream({deploymentId})` reads
 * directly from this object (no async transport).
 * ──────────────────────────────────────────────────────────────────── */

const mockStreamState = {
  flow: null as FlowInstance | null,
  nodes: new Map<string, FlowNode>(),
  relationships: new Map(),
  streamStatus: 'completed' as const,
  streamError: null as string | null,
}

vi.mock('@/lib/openflow-adapter-sse', () => ({
  useFlowStream: () => mockStreamState,
}))

// eslint-disable-next-line import/first
import { JobsPage } from './JobsPage'

function makeLegacyJob(id: string, jobName: string): Job {
  return {
    id,
    jobName,
    displayName: jobName,
    type: 'install',
    appId: jobName.replace(/^install-/, 'bp-'),
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-05-11T10:00:00Z',
    finishedAt: '2026-05-11T10:01:00Z',
    durationMs: 60_000,
  }
}

function renderJobs(deploymentId: string, liveJobs: Job[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    // disableStream gates `useDeploymentEvents` (the legacy SSE event
    // stream) — JSDOM has no EventSource and `useDeploymentEvents`
    // unconditionally attaches one. `useFlowStream` is mocked at
    // module level above so `disableStream` doesn't affect flow data.
    // `disableJobsBackfill=false` keeps the legacy backfill fetcher
    // active so the injected `liveJobs` list reaches mergeJobs.
    component: () => <JobsPage disableStream />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([
    jobsRoute,
    homeRoute,
    detailRoute,
    jobDetailRoute,
    flowRoute,
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}/jobs`] }),
  })

  // Stub fetch: legacy /jobs returns the injected list; the SSE
  // event-stream endpoint returns an inert empty payload (the
  // useDeploymentEvents reducer is harmless without a real stream).
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

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // Reset the mock stream to empty before each test.
  mockStreamState.flow = null
  mockStreamState.nodes = new Map()
  mockStreamState.relationships = new Map()
  mockStreamState.streamStatus = 'completed'
  mockStreamState.streamError = null
})

afterEach(() => cleanup())

describe('JobsPage — OpenovaFlow snapshot merge (TC-035 fix)', () => {
  it('Case 1 — legacy 5 / flow empty: 5 rows, no behavior change', async () => {
    const deploymentId = 'd-case1'
    const liveJobs: Job[] = [
      makeLegacyJob(`${deploymentId}:install-cilium`, 'install-cilium'),
      makeLegacyJob(`${deploymentId}:install-cert-manager`, 'install-cert-manager'),
      makeLegacyJob(`${deploymentId}:install-flux`, 'install-flux'),
      makeLegacyJob(`${deploymentId}:install-keycloak`, 'install-keycloak'),
      makeLegacyJob(`${deploymentId}:install-gitea`, 'install-gitea'),
    ]
    // mockStreamState.nodes left empty from beforeEach reset.

    renderJobs(deploymentId, liveJobs)

    await screen.findByTestId('jobs-table')
    await waitFor(() => {
      // All 5 legacy rows present by their deterministic per-id testid.
      for (const j of liveJobs) {
        expect(screen.queryByTestId(`jobs-table-row-${j.id}`)).toBeTruthy()
      }
    })
    // No flow-only rows appended.
    expect(screen.queryByTestId('jobs-table-row-contabo:bp-openova-flow-server')).toBeNull()
    expect(screen.queryByTestId('jobs-table-row-contabo:bp-openova-flow-emitter')).toBeNull()
  })

  it('Case 2 — legacy 5 / flow has 2 distinct ids: table shows 7 rows', async () => {
    const deploymentId = '12e194090631a885'
    const liveJobs: Job[] = [
      makeLegacyJob(`${deploymentId}:install-cilium`, 'install-cilium'),
      makeLegacyJob(`${deploymentId}:install-cert-manager`, 'install-cert-manager'),
      makeLegacyJob(`${deploymentId}:install-flux`, 'install-flux'),
      makeLegacyJob(`${deploymentId}:install-keycloak`, 'install-keycloak'),
      makeLegacyJob(`${deploymentId}:install-gitea`, 'install-gitea'),
    ]
    mockStreamState.nodes = new Map<string, FlowNode>([
      [
        'contabo:bp-openova-flow-server',
        {
          id: 'contabo:bp-openova-flow-server',
          flowId: deploymentId,
          label: 'openova-flow-server',
          status: 'succeeded',
        },
      ],
      [
        'contabo:bp-openova-flow-emitter',
        {
          id: 'contabo:bp-openova-flow-emitter',
          flowId: deploymentId,
          label: 'openova-flow-emitter',
          status: 'succeeded',
        },
      ],
    ])

    renderJobs(deploymentId, liveJobs)

    await screen.findByTestId('jobs-table')
    await waitFor(() => {
      // All 5 legacy rows present.
      for (const j of liveJobs) {
        expect(screen.queryByTestId(`jobs-table-row-${j.id}`)).toBeTruthy()
      }
      // Both flow-only rows appended.
      expect(
        screen.queryByTestId('jobs-table-row-contabo:bp-openova-flow-server'),
      ).toBeTruthy()
      expect(
        screen.queryByTestId('jobs-table-row-contabo:bp-openova-flow-emitter'),
      ).toBeTruthy()
    })
  })

  it('Case 3 — legacy 5 / flow has 1 ID-collision + 1 new: legacy wins, total 6 rows', async () => {
    const deploymentId = 'd-case3'
    const collidingId = `${deploymentId}:install-cilium`
    const liveJobs: Job[] = [
      makeLegacyJob(collidingId, 'install-cilium'),
      makeLegacyJob(`${deploymentId}:install-cert-manager`, 'install-cert-manager'),
      makeLegacyJob(`${deploymentId}:install-flux`, 'install-flux'),
      makeLegacyJob(`${deploymentId}:install-keycloak`, 'install-keycloak'),
      makeLegacyJob(`${deploymentId}:install-gitea`, 'install-gitea'),
    ]
    mockStreamState.nodes = new Map<string, FlowNode>([
      [
        // SAME id as a legacy entry — legacy must win dedup, so the
        // synth Job's `pending` status / empty appId never reaches the
        // table.
        collidingId,
        {
          id: collidingId,
          flowId: deploymentId,
          label: 'cilium-from-flow',
          status: 'pending',
        },
      ],
      [
        'contabo:bp-openova-flow-server',
        {
          id: 'contabo:bp-openova-flow-server',
          flowId: deploymentId,
          label: 'openova-flow-server',
          status: 'succeeded',
        },
      ],
    ])

    renderJobs(deploymentId, liveJobs)

    await screen.findByTestId('jobs-table')
    await waitFor(() => {
      // All 5 legacy rows present.
      for (const j of liveJobs) {
        expect(screen.queryByTestId(`jobs-table-row-${j.id}`)).toBeTruthy()
      }
      // The non-colliding flow id is appended.
      expect(
        screen.queryByTestId('jobs-table-row-contabo:bp-openova-flow-server'),
      ).toBeTruthy()
    })
    // The colliding id appears ONCE — legacy row only. Count
    // direct DOM nodes with that testid; querySelectorAll is the
    // canonical seam (used in JobsPage.test.tsx for the same anti-
    // duplication check).
    const collidedRows = document.querySelectorAll(
      `[data-testid="jobs-table-row-${collidingId}"]`,
    )
    expect(collidedRows.length).toBe(1)
  })
})
