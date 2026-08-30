/**
 * SovereignSidebar.orgscope.test.tsx — #4110 Org-console scoping.
 *
 * Asserts that an Org-scoped customer session sees ONLY its own-estate nav
 * (Apps / Catalog / Users / Settings) and NONE of the sovereign-admin nav
 * (Dashboard / Cloud / Jobs / Compliance / Organizations), while a
 * Sovereign-admin session still sees the full nav (zero regression).
 *
 * #6723: the former static `sandbox` (Agenity) row is gone from FLAT_NAV —
 * Agenity is a Blueprint-sourced mapped entry now and MUST keep rendering on
 * an Org-scoped console exactly as the static row did (scope follows the
 * entry's source, not the session): `sov-console-nav-bp-bp-agenity` is
 * present in orgScoped mode while an Application candidate
 * (`sov-console-nav-bp-app:*`, a Sovereign-level mapping) is absent.
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

// Mock the scope hook so the test drives orgScoped directly.
const scopeMock = vi.fn()
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => scopeMock(),
}))
// Merged sidebar entries: one Blueprint-sourced (Agenity, as bp-agenity's
// consoleUI projects it) and one Application candidate the sovereign-admin
// enabled. The scope rule under test is what each session gets to see.
vi.mock('@/lib/console-ui.api', () => ({
  getSidebarEntries: async () => [
    {
      id: 'bp-agenity',
      label: 'Agenity',
      route: '/apps/bp-agenity/dashboard',
      order: 40,
      source: 'blueprint',
      enabled: true,
    },
    {
      id: 'app:grafana',
      label: 'Observability',
      route: '/app/grafana',
      order: 5,
      source: 'application',
      enabled: true,
    },
  ],
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'demo-deployment' }),
}))

import { SovereignSidebar } from './SovereignSidebar'

function renderSidebar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const host = createRoute({
    getParentRoute: () => rootRoute,
    path: '/apps',
    component: () => <SovereignSidebar sovereignFQDN="demo.omani.homes" />,
  })
  const catchAll = createRoute({
    getParentRoute: () => rootRoute,
    path: '/$',
    component: () => <SovereignSidebar sovereignFQDN="demo.omani.homes" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([host, catchAll]),
    history: createMemoryHistory({ initialEntries: ['/apps'] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('SovereignSidebar — #4110 Org-console scope', () => {
  beforeEach(() => scopeMock.mockReset())
  afterEach(() => cleanup())

  it('Org-scoped session hides every sovereign-admin nav item', async () => {
    scopeMock.mockReturnValue({ orgScoped: true, org: 'demo', loading: false })
    renderSidebar()
    // Own-estate nav present.
    expect(await screen.findByTestId('sov-console-nav-apps')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-catalog')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-users')).toBeTruthy()
    // #6723 — Agenity is Blueprint-sourced and renders for an Org session
    // exactly as the former static row did (the row itself is gone).
    const agenity = await screen.findByTestId('sov-console-nav-bp-bp-agenity')
    expect(agenity.textContent).toContain('Agenity')
    expect(agenity.getAttribute('data-nav-source')).toBe('blueprint')
    expect(screen.queryByTestId('sov-console-nav-sandbox')).toBeNull()
    // …while an Application candidate is a Sovereign-level mapping and is
    // NOT rendered on an Org-scoped console.
    expect(screen.queryByTestId('sov-console-nav-bp-app:grafana')).toBeNull()
    expect(screen.getByTestId('sov-console-nav-settings')).toBeTruthy()
    // Sovereign-admin nav HIDDEN.
    expect(screen.queryByTestId('sov-console-nav-dashboard')).toBeNull()
    expect(screen.queryByTestId('sov-console-nav-cloud')).toBeNull()
    expect(screen.queryByTestId('sov-console-nav-jobs')).toBeNull()
    expect(screen.queryByTestId('sov-console-nav-compliance')).toBeNull()
    expect(screen.queryByTestId('sov-console-nav-organizations')).toBeNull()
    // #4196 — Billing is a sovereign-admin surface (voucher issuance) and
    // must NOT leak onto an Org-scoped customer console.
    expect(screen.queryByTestId('sov-console-nav-billing')).toBeNull()
  })

  it('Sovereign-admin session shows the full nav (zero regression)', async () => {
    scopeMock.mockReturnValue({ orgScoped: false, org: null, loading: false })
    renderSidebar()
    expect(await screen.findByTestId('sov-console-nav-dashboard')).toBeTruthy()
    // #6723 — a Sovereign session sees Blueprint entries AND enabled
    // Application candidates.
    expect(await screen.findByTestId('sov-console-nav-bp-bp-agenity')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-bp-app:grafana')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-cloud')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-jobs')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-organizations')).toBeTruthy()
    expect(screen.getByTestId('sov-console-nav-apps')).toBeTruthy()
    // #4196 — Billing is a top-level sovereign-admin menu, links to /billing.
    const billing = screen.getByTestId('sov-console-nav-billing') as HTMLAnchorElement
    expect(billing).toBeTruthy()
    expect(billing.getAttribute('href')).toBe('/billing')
  })
})
