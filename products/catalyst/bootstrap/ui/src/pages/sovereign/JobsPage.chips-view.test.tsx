/**
 * JobsPage.chips-view.test.tsx — P1b (Refs #6703).
 *
 * Lock-in for the /jobs List⇄Graph toggle + per-kind chip strip that
 * mirrors the /cloud (Resources) UX. Asserts:
 *   • the segmented List/Graph toggle renders;
 *   • `?view=graph` renders the graph (JobsGraphView), not the table;
 *   • `?view=list` renders the table + the JobKindChips strip;
 *   • clicking the Graph toggle switches list → graph (and the chip strip
 *     hides in graph view);
 *   • the list filters to ONE kind — the reducer first-paint rows all
 *     classify as `lifecycle`, so `?kind=lifecycle` shows them and a
 *     different kind (`install`) hides them.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
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
    // Mirror the production route so navigate() round-trips ?view=/?kind=.
    validateSearch: (raw: Record<string, unknown>): JobsSearch => {
      const out: JobsSearch = {}
      if (raw.view === 'graph' || raw.view === 'list') out.view = raw.view
      if (typeof raw.kind === 'string' && raw.kind.length > 0) out.kind = raw.kind
      return out
    },
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
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
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

describe('JobsPage — List⇄Graph toggle', () => {
  it('renders the segmented view toggle', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    expect(await screen.findByTestId('jobs-page-view-toggle')).toBeTruthy()
    expect(screen.getByTestId('jobs-page-view-list')).toBeTruthy()
    expect(screen.getByTestId('jobs-page-view-graph')).toBeTruthy()
  })

  it('list view (default) renders the table + the JobKindChips strip', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    expect(await screen.findByTestId('jobs-table')).toBeTruthy()
    expect(screen.getByTestId('jobs-kind-chips')).toBeTruthy()
    expect(screen.queryByTestId('jobs-graph-view')).toBeNull()
  })

  it('graph view (?view=graph) renders the graph, not the table or chips', async () => {
    renderJobs('/provision/d-1/jobs?view=graph')
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
    // Chip strip is list-only.
    expect(screen.queryByTestId('jobs-kind-chips')).toBeNull()
  })

  it('clicking the Graph toggle switches list → graph', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    await screen.findByTestId('jobs-table')
    fireEvent.click(screen.getByTestId('jobs-page-view-graph'))
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
  })
})

describe('JobsPage — list view filters to one kind (mirrors /cloud)', () => {
  it('?kind=lifecycle shows the reducer first-paint rows', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    await screen.findByTestId('jobs-table')
    // cluster-bootstrap + bp-cilium are lifecycle-classified reducer rows.
    expect(screen.queryByTestId('jobs-table-row-cluster-bootstrap')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-bp-cilium')).toBeTruthy()
  })

  it('?kind=install hides the lifecycle-classified reducer rows', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=install')
    await screen.findByTestId('jobs-table')
    // Under the install lens the lifecycle first-paint rows are filtered out.
    expect(screen.queryByTestId('jobs-table-row-cluster-bootstrap')).toBeNull()
    expect(screen.queryByTestId('jobs-table-row-bp-cilium')).toBeNull()
    // The empty-state row is what the operator sees instead.
    expect(screen.getByTestId('jobs-table-empty')).toBeTruthy()
  })
})
