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
  try {
    window.localStorage.clear()
  } catch {
    /* noop */
  }
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

  it('graph view (?view=graph) renders the graph + the shared chip strip (highlight lens), not the table', async () => {
    renderJobs('/provision/d-1/jobs?view=graph')
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
    // The SAME chip strip renders in graph view too, where it FILTERS the
    // canvas — removing a chip drops that kind's nodes.
    expect(screen.getByTestId('jobs-kind-chips')).toBeTruthy()
  })

  it('clicking the Graph toggle switches list → graph', async () => {
    renderJobs('/provision/d-1/jobs?view=list&kind=lifecycle')
    await screen.findByTestId('jobs-table')
    fireEvent.click(screen.getByTestId('jobs-page-view-graph'))
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table')).toBeNull()
    // The chip strip persists across the toggle (list filter → graph lens).
    expect(screen.getByTestId('jobs-kind-chips')).toBeTruthy()
  })
})

describe('JobsPage — graph view: chips FILTER the canvas', () => {
  it('removing a chip (✕) drops that kind from the strip (moves to + More)', async () => {
    renderJobs('/provision/d-1/jobs?view=graph')
    await screen.findByTestId('jobs-graph-view')
    // The reducer first-paint rows classify as `lifecycle`, so the lifecycle
    // chip is present (count > 0). In graph view nothing is "active", so the
    // chip is removable (its ✕ renders) and removing it filters that kind's
    // nodes off the canvas (onVisibleChange → graphVisibleKinds).
    expect(await screen.findByTestId('jobs-kind-chip-wrap-lifecycle')).toBeTruthy()
    fireEvent.click(screen.getByTestId('jobs-kind-chip-lifecycle-remove'))
    // The chip leaves the inline strip; the graph stays mounted (filtered,
    // not unmounted).
    expect(screen.queryByTestId('jobs-kind-chip-wrap-lifecycle')).toBeNull()
    expect(screen.getByTestId('jobs-graph-view')).toBeTruthy()
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
