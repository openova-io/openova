/**
 * ConvergenceWizard.test.tsx — #3925 surface-A state-aware Dashboard.
 *
 * Coverage:
 *   • deriveWizardPhase — status → phase mapping (the 5 phases)
 *   • hrRatio — multi-region HR census + componentStates fallback
 *   • wizard render — highlights the live phase + shows the Reconcile ratio
 *   • deep-links — phase ③ → /cloud (unified graph, #3958), phase ② → /jobs
 *   • Dashboard view toggle — auto-flip to treemap on ready; wizard while
 *     converging; the manual Progress ⇄ Treemap toggle flips both ways
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import {
  ConvergenceWizard,
  deriveWizardPhase,
  hrRatio,
} from './ConvergenceWizard'
import { Dashboard } from './Dashboard'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { TreemapData } from '@/lib/treemap.types'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('deriveWizardPhase', () => {
  const cases: Array<[string | undefined, ReturnType<typeof deriveWizardPhase>]> = [
    ['pending', 'infrastructure'],
    ['tofu-applying', 'infrastructure'],
    ['flux-bootstrapping', 'bootstrap'],
    ['phase1-watching', 'reconciliation'],
    ['ready', 'ready'],
    ['failed', 'health'],
    ['partial-failure', 'health'],
    [undefined, 'infrastructure'],
  ]
  for (const [status, expected] of cases) {
    it(`${status ?? '(none)'} → ${expected}`, () => {
      expect(deriveWizardPhase(status ? ({ status } as DeploymentSnapshot) : null)).toBe(expected)
    })
  }
})

describe('hrRatio', () => {
  it('sums the multi-region HR census', () => {
    const snap = {
      regions: [
        { region: 'a', primary: true, hrReady: 60, hrTotal: 63, degraded: false },
        { region: 'b', primary: false, hrReady: 48, hrTotal: 63, degraded: true },
      ],
    } as DeploymentSnapshot
    expect(hrRatio(snap)).toEqual({ ready: 108, total: 126 })
  })
  it('falls back to componentStates when no region census', () => {
    const snap = {
      componentStates: { cilium: 'installed', flux: 'installed', keycloak: 'installing' },
    } as unknown as DeploymentSnapshot
    expect(hrRatio(snap)).toEqual({ ready: 2, total: 3 })
  })
  it('returns 0/0 when nothing is known', () => {
    expect(hrRatio(null)).toEqual({ ready: 0, total: 0 })
  })
})

function renderWizard(snapshot: DeploymentSnapshot | null) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => <ConvergenceWizard snapshot={snapshot} deploymentId="d-1" />,
  })
  // #3958 — the wizard's reconciliation deep-link now lands on the
  // unified Cloud graph (/cloud), not the deleted /reconciliation page.
  const reconRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div data-testid="recon-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route, reconRoute, jobsRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/dashboard'] }),
  })
  return render(<RouterProvider router={router as never} />)
}

describe('ConvergenceWizard render', () => {
  it('highlights the live phase + shows the Reconcile ratio', async () => {
    renderWizard({
      status: 'phase1-watching',
      regions: [{ region: 'a', primary: true, hrReady: 52, hrTotal: 65, degraded: false }],
    } as DeploymentSnapshot)
    const recon = await screen.findByTestId('wizard-phase-reconciliation')
    expect(recon.getAttribute('data-active')).toBe('true')
    expect(screen.getByTestId('wizard-reconcile-ratio').textContent).toContain('52/65')
  })

  it('deep-links phase ③ to the unified Cloud graph reconciliation LENS and ② to Jobs', async () => {
    renderWizard({ status: 'phase1-watching' } as DeploymentSnapshot)
    const reconLink = await screen.findByTestId('wizard-link-reconciliation')
    const href = reconLink.getAttribute('href') ?? ''
    expect(href).toContain('/cloud')
    // #3996 follow-up — the link must select the Reconciliation lens, not
    // the default Cloud lens. The cloud route's validateSearch carries the
    // `lens` param through, so the rendered href must include it (a dropped
    // param would mean the deep-link lands on the default lens).
    expect(href).toContain('view=graph')
    expect(href).toContain('lens=reconciliation')
    const jobsLink = screen.getByTestId('wizard-link-jobs')
    expect(jobsLink.getAttribute('href')).toContain('/jobs')
  })
})

// ── Dashboard view toggle + auto-flip ─────────────────────────────────────
//
// The Dashboard derives its view from the deployment snapshot (fetched via
// useDeploymentEvents → GET /events). We stub fetch to return a chosen
// status so we can assert the auto-flip.

function stubEventsFetch(state: DeploymentSnapshot | undefined) {
  globalThis.fetch = ((url: string) => {
    const u = String(url)
    if (u.includes('/events')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ events: [], state, done: state !== undefined }),
      } as unknown as Response)
    }
    // deployment poll / anything else
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve(state ?? {}),
    } as unknown as Response)
  }) as typeof fetch
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class FakeRO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof FakeRO }).ResizeObserver = FakeRO
  }
}

const EMPTY_TREEMAP: TreemapData = { total_count: 0, items: [] } as unknown as TreemapData

function renderDashboard() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    // disableStream skips the EventSource (jsdom has none); the snapshot is
    // resolved via the GET /events replay path ({state, done:true}) — the
    // same seam the existing Dashboard suite uses.
    component: () => <Dashboard initialDataOverride={EMPTY_TREEMAP} disableStream />,
  })
  const reconRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div data-testid="recon-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const decomRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <div data-testid="decom-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([dashRoute, reconRoute, jobsRoute, decomRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/dashboard'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('Dashboard state-aware view', () => {
  beforeEach(() => {
    // default: not-ready (still converging)
    stubEventsFetch({ status: 'phase1-watching' } as DeploymentSnapshot)
  })

  it('renders the wizard (Progress) while converging', async () => {
    renderDashboard()
    expect(await screen.findByTestId('convergence-wizard')).toBeTruthy()
    // Treemap controller is hidden in progress view.
    expect(screen.queryByTestId('dashboard-treemap-frame')).toBeNull()
  })

  it('AUTO-FLIPS to the treemap when status is ready', async () => {
    stubEventsFetch({ status: 'ready' } as DeploymentSnapshot)
    renderDashboard()
    // The treemap frame appears once the ready snapshot resolves.
    await waitFor(() => {
      expect(screen.queryByTestId('dashboard-treemap-frame')).toBeTruthy()
    })
    expect(screen.queryByTestId('convergence-wizard')).toBeNull()
  })

  it('manual toggle flips Progress ⇄ Treemap both ways', async () => {
    renderDashboard()
    // Starts on Progress (converging).
    expect(await screen.findByTestId('convergence-wizard')).toBeTruthy()
    // Flip to Treemap.
    fireEvent.click(screen.getByTestId('dashboard-view-treemap'))
    await waitFor(() => {
      expect(screen.queryByTestId('dashboard-treemap-frame')).toBeTruthy()
    })
    expect(screen.queryByTestId('convergence-wizard')).toBeNull()
    // Flip back to Progress.
    fireEvent.click(screen.getByTestId('dashboard-view-progress'))
    await waitFor(() => {
      expect(screen.queryByTestId('convergence-wizard')).toBeTruthy()
    })
  })
})
