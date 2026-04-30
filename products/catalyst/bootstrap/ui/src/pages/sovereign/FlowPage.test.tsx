/**
 * FlowPage.test.tsx — coverage for the new /flow route + the
 * embedded variant used inside JobDetail's Flow tab.
 *
 * Coverage:
 *   • resolveScope helper — `all`, `batch:<id>`, fallthrough.
 *   • Renders for ?scope=all (every job in the catalog).
 *   • Renders for ?scope=batch:<id> (filters to one batch).
 *   • Renders for ?scope=batch:<unknown> (empty placeholder).
 *   • Mode toggle (Jobs ↔ Batches) updates the URL ?view= param.
 *   • Single-click on a job bubble opens FloatingLogPane.
 *   • Click on empty canvas closes the floating pane.
 *   • Embedded variant: no PortalShell, no StatusStrip.
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
import { FlowPage, resolveScope, resolveMode } from './FlowPage'
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

function renderFlow(initialEntry: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <FlowPage disableStream disableJobsBackfill />,
    validateSearch: (raw: Record<string, unknown>): {
      scope?: string
      view?: 'jobs' | 'batches'
    } => {
      const out: { scope?: string; view?: 'jobs' | 'batches' } = {}
      const scope = raw?.scope
      if (typeof scope === 'string' && scope.length > 0) out.scope = scope
      const view = raw?.view
      if (view === 'jobs' || view === 'batches') out.view = view
      return out
    },
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const tree = rootRoute.addChildren([flowRoute, jobsRoute, detailRoute, homeRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return {
    ...render(
      <QueryClientProvider client={qc}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    ),
    router,
  }
}

describe('resolveScope', () => {
  it('returns kind=all for "all"', () => {
    expect(resolveScope('all')).toEqual({ kind: 'all' })
  })
  it('returns kind=batch with id for "batch:foo"', () => {
    expect(resolveScope('batch:foo')).toEqual({ kind: 'batch', batchId: 'foo' })
  })
  it('returns kind=all for unknown / falsy', () => {
    expect(resolveScope(undefined)).toEqual({ kind: 'all' })
    expect(resolveScope('')).toEqual({ kind: 'all' })
    expect(resolveScope('garbage')).toEqual({ kind: 'all' })
    expect(resolveScope('batch:')).toEqual({ kind: 'all' })
  })
})

describe('resolveMode', () => {
  it('returns "jobs" by default', () => {
    expect(resolveMode(undefined)).toBe('jobs')
    expect(resolveMode('')).toBe('jobs')
    expect(resolveMode('garbage')).toBe('jobs')
  })
  it('returns "batches" when explicit', () => {
    expect(resolveMode('batches')).toBe('batches')
  })
})

describe('FlowPage — scope=all', () => {
  it('renders the canvas SVG', async () => {
    renderFlow('/provision/d-1/flow?scope=all')
    expect(await screen.findByTestId('flow-canvas-svg')).toBeTruthy()
  })

  it('renders at least one job bubble (default catalog)', async () => {
    renderFlow('/provision/d-1/flow?scope=all')
    await screen.findByTestId('flow-canvas-svg')
    const bubbles = document.querySelectorAll('[data-testid^="flow-job-"]')
    expect(bubbles.length).toBeGreaterThan(0)
  })

  it('renders the StatusStrip with mode toggle', async () => {
    renderFlow('/provision/d-1/flow?scope=all')
    expect(await screen.findByTestId('sov-status-strip')).toBeTruthy()
    expect(await screen.findByTestId('sov-status-strip-mode-toggle')).toBeTruthy()
  })
})

describe('FlowPage — scope=batch:applications', () => {
  it('filters to applications batch only', async () => {
    // The jobsAdapter buckets every per-Application Job into the
    // 'applications' batch (see jobsAdapter.batchOf()). Phase 0 +
    // cluster-bootstrap have their own batches, so a scope-filter
    // to 'applications' must hide them.
    renderFlow('/provision/d-1/flow?scope=batch:applications')
    await screen.findByTestId('flow-canvas-svg')
    expect(screen.queryByTestId('flow-job-infrastructure:tofu-init')).toBeNull()
    expect(screen.queryByTestId('flow-job-cluster-bootstrap')).toBeNull()
    // bp-cilium IS in the applications batch and must be present.
    expect(screen.queryByTestId('flow-job-bp-cilium')).toBeTruthy()
  })
})

describe('FlowPage — scope=batch:nonexistent', () => {
  it('renders the empty-canvas placeholder', async () => {
    renderFlow('/provision/d-1/flow?scope=batch:nonexistent')
    expect(await screen.findByTestId('flow-canvas-empty')).toBeTruthy()
  })
})

describe('FlowPage — single-click opens floating log pane', () => {
  it('renders FloatingLogPane after a single-click on a bubble', async () => {
    renderFlow('/provision/d-1/flow?scope=all')
    await screen.findByTestId('flow-canvas-svg')
    const bubbles = document.querySelectorAll('[data-testid^="flow-job-"]')
    expect(bubbles.length).toBeGreaterThan(0)
    const target = bubbles[0] as Element
    // Single-click — debounced 220ms before the handler fires.
    fireEvent.click(target)
    // Wait past the 220ms debounce; testing-library's findBy* polls
    // every ~50ms so 1500ms is plenty of headroom for the render.
    const pane = await screen.findByTestId('floating-log-pane', undefined, {
      timeout: 1500,
    })
    expect(pane).toBeTruthy()
  })

  it('clicking on empty canvas background closes the pane', async () => {
    renderFlow('/provision/d-1/flow?scope=all')
    const svg = await screen.findByTestId('flow-canvas-svg')
    const bubbles = document.querySelectorAll('[data-testid^="flow-job-"]')
    const target = bubbles[0] as Element
    fireEvent.click(target)
    await screen.findByTestId('floating-log-pane', undefined, { timeout: 1500 })
    // Click directly on the SVG element (background, not a child).
    fireEvent.click(svg, { target: svg })
    // Pane unmounts synchronously when the background-click handler fires.
    expect(screen.queryByTestId('floating-log-pane')).toBeNull()
  })
})

describe('FlowPage — mode toggle', () => {
  it('clicking Batches updates URL ?view=batches', async () => {
    const { router } = renderFlow('/provision/d-1/flow?scope=all')
    await screen.findByTestId('sov-status-strip-mode-toggle')
    const batchesBtn = screen.getByTestId('sov-status-strip-mode-batches')
    fireEvent.click(batchesBtn)
    // Router state reflects the new search param.
    const view = (router.state.location.search as { view?: string }).view
    expect(view).toBe('batches')
  })
})

describe('FlowPage — embedded variant', () => {
  it('renders without StatusStrip when embedded prop is set', async () => {
    // Embedded variant is always rendered by JobDetail with a
    // scopeOverride; we simulate that here by mounting the component
    // directly inside a route that supplies the same params via the
    // `deploymentIdOverride` prop.
    const rootRoute = createRootRoute({ component: () => <Outlet /> })
    const flowRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/provision/$deploymentId/flow',
      component: () => (
        <FlowPage
          disableStream
          disableJobsBackfill
          embedded
          deploymentIdOverride="d-1"
          scopeOverride={{ kind: 'batch', batchId: 'applications' }}
          highlightJobId="bp-cilium"
        />
      ),
      validateSearch: () => ({}),
    })
    const tree = rootRoute.addChildren([flowRoute])
    const router = createRouter({
      routeTree: tree,
      history: createMemoryHistory({
        initialEntries: ['/provision/d-1/flow'],
      }),
    })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    render(
      <QueryClientProvider client={qc}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )
    expect(await screen.findByTestId('flow-page-embedded')).toBeTruthy()
    expect(screen.queryByTestId('sov-status-strip')).toBeNull()
    expect(screen.queryByTestId('sov-portal-shell')).toBeNull()
  })
})
