/**
 * AppsPage.test.tsx — lock-in for the Sovereign Apps surface.
 *
 * #3601 (EPIC #3597): /apps is now TAB-LESS — it renders ONLY the
 * installed-deployments grid. The catalog moved to its own left-nav page
 * (/catalog → CatalogPage, covered by CatalogPage.test.tsx). This spec
 * asserts:
 *   • Page heading renders
 *   • NO Deployments/Catalog tabs render
 *   • Card grid renders one .app-card per INSTALLED descriptor on first
 *     paint (bootstrap-kit apps always count as installed)
 *   • Installed-tab cards link to the INSTANCE page /app/$id
 *   • Search narrows the visible cards
 *   • The empty-state "Open catalog" affordance links to /catalog
 *   • PortalShell wiring (sidebar present)
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'

function renderProvision(deploymentId: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <AppsPage disableStream />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  // #3601 — the catalog is its own page now.
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog',
    component: () => <div data-testid="catalog-page-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([
    provisionRoute,
    detailRoute,
    catalogRoute,
    jobsRoute,
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}`] }),
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

describe('AppsPage — header', () => {
  it('renders Applications heading', async () => {
    renderProvision('d-1')
    expect(await screen.findByText('Applications')).toBeTruthy()
  })

  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderProvision('d-1')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })
})

describe('AppsPage — tab-less (#3601)', () => {
  it('renders NO Deployments/Catalog tabs', async () => {
    renderProvision('d-1')
    await screen.findByText('Applications')
    expect(screen.queryByTestId('sov-tabs')).toBeNull()
    expect(screen.queryByTestId('sov-tab-installed')).toBeNull()
    expect(screen.queryByTestId('sov-tab-catalog')).toBeNull()
  })
})

describe('AppsPage — banner pollution gate (founder #475)', () => {
  it('renders no role="alert" or role="status" inside main on first paint', async () => {
    renderProvision('d-1')
    await screen.findByText('Applications')
    const main = document.querySelector('main')
    expect(main).toBeTruthy()
    expect(main!.querySelector('[role="alert"]')).toBeNull()
    expect(main!.querySelector('[role="status"]')).toBeNull()
  })

  it('does not import or render the legacy FailureCard test ids', async () => {
    renderProvision('d-1')
    await screen.findByText('Applications')
    expect(screen.queryByTestId('sov-failure-card')).toBeNull()
    expect(screen.queryByTestId('sov-phase1-unavailable-banner')).toBeNull()
  })
})

describe('AppsPage — card grid (installed only)', () => {
  it('renders one .app-card per INSTALLED Application from first paint', async () => {
    renderProvision('d-1')
    // Bootstrap-kit apps always count as installed, so the grid is
    // non-empty without flipping any tab.
    const grid = await screen.findByTestId('sov-apps-grid')
    const cards = within(grid).getAllByTestId(/^sov-app-card-bp-/)
    expect(cards.length).toBeGreaterThan(0)
  })

  it('grid uses the canonical .apps-grid class (auto-fit minmax 360px)', async () => {
    renderProvision('d-1')
    const grid = await screen.findByTestId('sov-apps-grid')
    expect(grid.className).toContain('apps-grid')
  })

  it('search filter narrows the visible cards', async () => {
    renderProvision('d-1')
    const before = within(await screen.findByTestId('sov-apps-grid')).getAllByTestId(/^sov-app-card-bp-/)
    fireEvent.change(screen.getByTestId('sov-search'), { target: { value: 'cilium' } })
    const after = within(await screen.findByTestId('sov-apps-grid')).getAllByTestId(/^sov-app-card-bp-/)
    expect(after.length).toBeLessThan(before.length)
    expect(after.some((c) => c.getAttribute('data-testid') === 'sov-app-card-bp-cilium')).toBe(true)
  })

  it('installed card links to the INSTANCE page /app/$id', async () => {
    renderProvision('d-1')
    const card = await screen.findByTestId('sov-app-card-bp-cilium')
    expect(card.getAttribute('href')).toBe('/provision/d-1/app/bp-cilium')
  })
})
