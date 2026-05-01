/**
 * FlowPage.test.tsx — coverage for the recursive Job-tree FlowPage
 * (issue #351).
 *
 * Coverage:
 *   • resolveFolded helper — empty / single id / comma list.
 *   • resolveDepth helper — '1', '2', '3', 'all', fallthrough.
 *   • Renders the canvas SVG with at least one job bubble.
 *   • Renders the StatusStrip + FoldControls (no batches toggle).
 *   • Embedded variant: no PortalShell, no StatusStrip.
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
import { FlowPage, resolveFolded } from './FlowPage'
import { resolveDepth } from './FoldControls'
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
      folded?: string
      depth?: string
    } => {
      const out: { folded?: string; depth?: string } = {}
      if (typeof raw?.folded === 'string') out.folded = raw.folded
      if (typeof raw?.depth === 'string') out.depth = raw.depth
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

describe('resolveFolded', () => {
  it('returns an empty Set for missing / non-string', () => {
    expect(resolveFolded(undefined).size).toBe(0)
    expect(resolveFolded(123).size).toBe(0)
  })
  it('parses a single id', () => {
    const s = resolveFolded('bootstrap-kit')
    expect(s.has('bootstrap-kit')).toBe(true)
    expect(s.size).toBe(1)
  })
  it('parses a comma-separated list and trims whitespace', () => {
    const s = resolveFolded('a,b , c')
    expect([...s].sort()).toEqual(['a', 'b', 'c'])
  })
})

describe('resolveDepth', () => {
  it('defaults to 2', () => {
    expect(resolveDepth(undefined)).toBe(2)
    expect(resolveDepth('')).toBe(2)
    expect(resolveDepth('garbage')).toBe(2)
  })
  it('parses 1 / 3 / all', () => {
    expect(resolveDepth('1')).toBe(1)
    expect(resolveDepth('3')).toBe(3)
    expect(resolveDepth('all')).toBe('all')
  })
})

describe('FlowPage — render', () => {
  it('renders the canvas SVG', async () => {
    renderFlow('/provision/d-1/flow')
    expect(await screen.findByTestId('flow-canvas-svg')).toBeTruthy()
  })

  it('renders at least one job bubble (default catalog)', async () => {
    renderFlow('/provision/d-1/flow')
    await screen.findByTestId('flow-canvas-svg')
    const bubbles = document.querySelectorAll('[data-testid^="flow-job-"]')
    expect(bubbles.length).toBeGreaterThan(0)
  })

  it('renders the StatusStrip and FoldControls (no batches toggle)', async () => {
    renderFlow('/provision/d-1/flow')
    expect(await screen.findByTestId('sov-status-strip')).toBeTruthy()
    expect(await screen.findByTestId('fold-controls')).toBeTruthy()
    // The legacy jobs/batches mode toggle MUST NOT mount.
    expect(screen.queryByTestId('sov-status-strip-mode-toggle')).toBeNull()
  })
})

describe('FlowPage — embedded variant', () => {
  it('renders without StatusStrip / PortalShell when embedded', async () => {
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
          hostJobId="bp-cilium"
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
