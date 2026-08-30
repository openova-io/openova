/**
 * JobsPage.chips-view.test.tsx — P1b (Refs #6703).
 *
 * Lock-in for the /jobs List⇄Graph toggle + per-kind chip strip that
 * mirrors the /cloud (Resources) UX. Asserts:
 *   • the segmented List/Graph toggle renders;
 *   • `?view=graph` renders the graph (JobsGraphView), not the table;
 *   • `?view=list` renders the table + the JobKindChips strip;
 *   • clicking the Graph toggle switches list → graph;
 *   • the chip strip is present in BOTH views (P2, Refs #6703 — founder:
 *     "the graph view same as cloud graph, it should still contain the
 *     chips"); in graph view a chip HIGHLIGHTS (dims the rest) rather than
 *     filters, and none is active by default;
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

  it('graph view (?view=graph) renders the graph + chips, not the table', async () => {
    renderJobs('/provision/d-1/jobs?view=graph')
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
    // P2: the chip strip stays present in graph view (highlight, not filter).
    expect(screen.getByTestId('jobs-kind-chips')).toBeTruthy()
    // No chip is highlighted by default — every chip renders inactive.
    const activeChips = document.querySelectorAll(
      '[data-testid^="jobs-kind-chip-"][data-active="true"]',
    )
    expect(activeChips.length).toBe(0)
  })

  it('clicking the Graph toggle switches list → graph (chips persist)', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    await screen.findByTestId('jobs-table')
    fireEvent.click(screen.getByTestId('jobs-page-view-graph'))
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
    expect(screen.getByTestId('jobs-kind-chips')).toBeTruthy()
  })

  it('graph-view chip toggles the highlight on and back off', async () => {
    renderJobs('/provision/d-1/jobs?view=graph')
    await screen.findByTestId('jobs-graph-view')
    // Every reducer first-paint row classifies as `lifecycle` (OpenTofu), so
    // that is the only chip with a non-zero count / present in graph view.
    const lifecycleChip = screen.getByTestId('jobs-kind-chip-lifecycle')
    expect(lifecycleChip.getAttribute('data-active')).toBe('false')
    fireEvent.click(lifecycleChip)
    expect(lifecycleChip.getAttribute('data-active')).toBe('true')
    // Because ALL nodes are lifecycle here, highlighting that kind dims none
    // (a dimmed node is a NON-matching one) — the widget stays fully bright.
    expect(document.querySelectorAll('[data-dimmed="true"]').length).toBe(0)
    // Clicking the active chip again clears the highlight.
    fireEvent.click(lifecycleChip)
    expect(lifecycleChip.getAttribute('data-active')).toBe('false')
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
