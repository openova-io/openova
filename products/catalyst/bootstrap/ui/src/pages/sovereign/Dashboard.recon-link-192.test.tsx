/**
 * Dashboard.recon-link-192.test.tsx — UAT row 192 (#3925 / #3996 / #5831).
 *
 * Row 192, verbatim:
 *
 *   "The convergence **Reconciliation** link opens the cloud **RECON lens**
 *    (`view=graph&lens=reconciliation`), not the default cloud lens."
 *
 * Three walks across two environments recorded the SAME split: the deep-link
 * TARGET passes (arriving at /cloud?view=graph&lens=reconciliation selects the
 * reconciliation lens), while the LINK half returned
 * `[data-testid="wizard-link-reconciliation"].length === 0` and zero anchors
 * anywhere with an href containing `lens=reconciliation`. The cause was never
 * the lens: `be5477c43` replaced the 5-phase ConvergenceWizard — which owned
 * that link — with this treemap Dashboard, and the component has been orphaned
 * from the router ever since (#5831 made the orphan loud).
 *
 * So the affordance had no HOST, and this Dashboard is the convergence surface
 * the wizard became. These tests pin the link on it: present, and carrying the
 * lens the row names rather than the default cloud lens.
 *
 * WHAT THIS DELIBERATELY DOES NOT DO: assert the lens SELECTION on arrival.
 * That half has its own suite (widgets/architecture-graph/useCloudLens.test.tsx)
 * and was already passing on all three walks — re-asserting it here would make
 * this file green on the half that was never broken.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { Dashboard } from './Dashboard'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDashboard() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => <Dashboard disableStream initialDataOverride={{ items: [], total_count: 0 }} />,
  })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div data-testid="recon-target" />,
  })
  const decommRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <div data-testid="decomm-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([dashRoute, cloudRoute, decommRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/dashboard'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
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
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class FakeResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof FakeResizeObserver }).ResizeObserver =
      FakeResizeObserver
  }
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('row 192 — the convergence Reconciliation link has a host', () => {
  it('renders a Reconciliation link on the Dashboard', async () => {
    renderDashboard()
    const link = await screen.findByTestId('dashboard-link-reconciliation')
    expect(link.textContent).toContain('Reconciliation')
    // The affordance must be a real anchor an operator can click, not a
    // decorative span — "the link exists" was the whole failing half.
    expect(link.tagName.toLowerCase()).toBe('a')
  })

  it('points at the RECON lens, not the default cloud lens', async () => {
    renderDashboard()
    const href = (await screen.findByTestId('dashboard-link-reconciliation')).getAttribute('href') ?? ''

    // The exact observable three walks reported as absent: an anchor whose
    // href carries lens=reconciliation.
    expect(href).toContain('lens=reconciliation')
    expect(href).toContain('view=graph')
    expect(href).toContain('/cloud')

    // Negative half of the clause — "not the default cloud lens". A link that
    // merely reached /cloud would land on the Cloud lens chip-set and the row
    // would still fail.
    expect(href).not.toMatch(/lens=cloud\b/)
  })

  it('is reachable from the page, not only from the DOM query', async () => {
    renderDashboard()
    await screen.findByTestId('dashboard-page')
    // Anchored the way a walker looks for it: any anchor on the page whose
    // href selects the reconciliation lens. This is the assertion whose
    // count was 0 on hw290, hw290 (re-walk) and hw292.
    const anchors = Array.from(document.querySelectorAll('a')).filter((a) =>
      (a.getAttribute('href') ?? '').includes('lens=reconciliation'),
    )
    expect(anchors.length).toBeGreaterThan(0)
  })
})
