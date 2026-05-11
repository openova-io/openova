/**
 * JobDetail.flow-fallback.test.tsx — OpenovaFlow snapshot fallback.
 *
 * Regression coverage for the 2026-05-11 cluster of iter-1 FAILs
 * (TC-019/020/021/023/024/025/027/028/033/034/036/037/038/039/040/041
 * /042/053/054/060/064 against the OpenovaFlow canvas matrix):
 *
 *   For Sovereigns that exist ONLY in the openova-flow snapshot (no
 *   legacy job-event-stream backing — verified live with deployment
 *   12e194090631a885 where GET /v1/flows/<id>/snapshot returns 2 leaf
 *   nodes but GET /v1/deployments/<id>/jobs/<id> 404s on those node
 *   ids), navigating to /sovereign/provision/<id>/jobs/<nodeId> landed
 *   in JobDetail. JobDetail built `jobsById` from the legacy reducer +
 *   live-jobs backfill, found nothing, and short-circuited to the
 *   "Job not found" panel — NEVER mounting FlowPage. The canvas, the
 *   surface that WOULD have painted the node, never got the chance.
 *
 *   The fix wires JobDetail to read the same `useFlowStream` hook
 *   FlowPage uses; when the legacy lookup misses, JobDetail
 *   synthesizes a Job stub from the FlowNode so the canvas mounts
 *   with the right hostJobId.
 *
 * Cases:
 *   1. Legacy `jobsById` has the job → FlowPage renders, no fallback
 *      path consulted (legacy still wins).
 *   2. Legacy empty, openova-flow snapshot has the node → FlowPage
 *      renders, fallback path mounted the synth Job.
 *   3. Both legacy AND flow snapshot empty → "Job not found" panel.
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
 * Module-level mock for useFlowStream — case 2 needs to make the SSE
 * adapter "see" a flow node without a real EventSource. Tests that
 * don't care about the stream get the default empty stream.
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

/**
 * Import JobDetail AFTER the mock so the module sees the mocked hook.
 */
// eslint-disable-next-line import/first
import { JobDetail } from './JobDetail'

function renderDetail(deploymentId: string, jobId: string, liveJobs: Job[]) {
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

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // Reset the mock stream to empty before each test; case 2 explicitly
  // seeds it before render.
  mockStreamState.flow = null
  mockStreamState.nodes = new Map()
  mockStreamState.relationships = new Map()
  mockStreamState.streamStatus = 'completed'
  mockStreamState.streamError = null
})

afterEach(() => cleanup())

describe('JobDetail — OpenovaFlow snapshot fallback (iter-1 FAIL cluster fix)', () => {
  it('Case 1 — legacy jobsById has the job: renders FlowPage, no fallback', async () => {
    const deploymentId = 'd-legacy'
    const jobId = `${deploymentId}:install-cilium`
    const liveJobs: Job[] = [
      {
        id: jobId,
        jobName: 'install-cilium',
        displayName: 'Install Cilium',
        type: 'install',
        appId: 'bp-cilium',
        parentId: '',
        dependsOn: [],
        childIds: [],
        status: 'running',
        startedAt: '2026-05-11T10:00:00Z',
        finishedAt: null,
        durationMs: 1_000,
      },
    ]
    // Intentionally seed the flow snapshot with a DIFFERENT node id so
    // we can prove the legacy lookup wins when it has a hit.
    mockStreamState.nodes = new Map<string, FlowNode>([
      [
        'contabo:bp-other',
        {
          id: 'contabo:bp-other',
          flowId: deploymentId,
          label: 'Other node',
          status: 'pending',
        },
      ],
    ])

    renderDetail(deploymentId, jobId, liveJobs)

    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeNull()
      expect(screen.queryByTestId(`job-detail-${jobId}`)).toBeTruthy()
      expect(screen.queryByTestId('job-detail-canvas')).toBeTruthy()
      expect(screen.queryByTestId('flow-page-embedded')).toBeTruthy()
    })
    // Legacy displayName wins.
    expect(screen.getByTestId('portal-header-title').textContent).toBe('Install Cilium')
  })

  it('Case 2 — legacy empty, openova-flow snapshot has the node: renders FlowPage via synth job', async () => {
    const deploymentId = '12e194090631a885'
    const jobId = 'contabo:bp-openova-flow-server'
    // No legacy jobs at all — mirrors the real-world wedge.
    const liveJobs: Job[] = []
    mockStreamState.nodes = new Map<string, FlowNode>([
      [
        jobId,
        {
          id: jobId,
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

    renderDetail(deploymentId, jobId, liveJobs)

    await waitFor(() => {
      // The fallback prevents the not-found short-circuit.
      expect(screen.queryByTestId('job-detail-not-found')).toBeNull()
      // FlowPage canvas mounts with the synth job's id.
      expect(screen.queryByTestId(`job-detail-${jobId}`)).toBeTruthy()
      expect(screen.queryByTestId('job-detail-canvas')).toBeTruthy()
      expect(screen.queryByTestId('flow-page-embedded')).toBeTruthy()
    })
    // Synth Job carried the FlowNode's label into the header.
    expect(screen.getByTestId('portal-header-title').textContent).toBe('openova-flow-server')
  })

  it('Case 3 — both legacy AND flow snapshot empty: renders "Job not found"', async () => {
    const deploymentId = 'd-empty'
    const jobId = 'contabo:bp-nothing-here'
    const liveJobs: Job[] = []
    mockStreamState.nodes = new Map()

    renderDetail(deploymentId, jobId, liveJobs)

    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-not-found')).toBeTruthy()
    })
    // And no canvas / embedded flow-page mounted.
    expect(screen.queryByTestId('job-detail-canvas')).toBeNull()
    expect(screen.queryByTestId('flow-page-embedded')).toBeNull()
  })
})
