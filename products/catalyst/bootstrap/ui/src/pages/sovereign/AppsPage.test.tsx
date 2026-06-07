/**
 * AppsPage.test.tsx — pixel-port lock-in for the Sovereign Apps surface.
 *
 *   • Page heading + tagline render
 *   • Both tabs render with counts pulled from the resolved catalog
 *     (Deployments + Catalog), the canonical .tab/.active class string
 *   • Card grid renders one .app-card per Application descriptor on
 *     first paint (waterfall — no spinner state)
 *   • Search filter narrows the visible cards by title / description /
 *     family
 *   • Sidebar nav surfaces are present (PortalShell wiring)
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
  // #3090 — Catalog-tab cards must link to the CLASS page.
  const catalogDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <div data-testid="catalog-detail-target" />,
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
    catalogDetailRoute,
    jobsRoute,
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}`] }),
  })
  // AppsPage's liveAppsQuery (useQuery, enabled on Sovereign mode) needs a
  // QueryClient in context even when disabled — mirror AppsPage.handover.test.
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
  // Stub fetch so useDeploymentEvents history-replay path resolves
  // synchronously without making real network calls.
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

describe('AppsPage — tabs', () => {
  it('renders Deployments + Catalog tabs', async () => {
    renderProvision('d-1')
    const tabs = await screen.findByTestId('sov-tabs')
    expect(within(tabs).getByTestId('sov-tab-installed')).toBeTruthy()
    expect(within(tabs).getByTestId('sov-tab-catalog')).toBeTruthy()
  })

  it('Deployments tab is active by default', async () => {
    renderProvision('d-1')
    const installed = await screen.findByTestId('sov-tab-installed')
    expect(installed.className).toContain('active')
  })

  it('clicking Catalog flips active to Catalog', async () => {
    renderProvision('d-1')
    const catalog = await screen.findByTestId('sov-tab-catalog')
    fireEvent.click(catalog)
    expect(catalog.className).toContain('active')
    const installed = screen.getByTestId('sov-tab-installed')
    expect(installed.className).not.toContain('active')
  })

  it('tabs render counts that mirror the catalog', async () => {
    renderProvision('d-1')
    const tabs = await screen.findByTestId('sov-tabs')
    // Catalog count > 0 because BOOTSTRAP_KIT (11+) is always present.
    const catalog = within(tabs).getByTestId('sov-tab-catalog')
    const countSpan = catalog.querySelector('.tab-count')
    expect(countSpan).toBeTruthy()
    const n = Number((countSpan!.textContent ?? '').trim())
    expect(n).toBeGreaterThan(0)
  })
})

describe('AppsPage — banner pollution gate (founder #475)', () => {
  it('renders no role="alert" or role="status" inside main on first paint', async () => {
    renderProvision('d-1')
    // Wait for the page to settle.
    await screen.findByText('Applications')
    // The Apps page main surface must be free of inline banners. Toasts
    // (which would render inside the global tray, not main) are allowed.
    const main = document.querySelector('main')
    expect(main).toBeTruthy()
    expect(main!.querySelector('[role="alert"]')).toBeNull()
    expect(main!.querySelector('[role="status"]')).toBeNull()
  })

  it('does not import or render the legacy FailureCard test ids', async () => {
    renderProvision('d-1')
    await screen.findByText('Applications')
    // These test ids were the inline banner anchors. They must not paint
    // anywhere on the Apps surface; the failure UX moves to global toasts.
    expect(screen.queryByTestId('sov-failure-card')).toBeNull()
    expect(screen.queryByTestId('sov-phase1-unavailable-banner')).toBeNull()
  })
})

describe('AppsPage — card grid', () => {
  it('renders one .app-card per Application from first paint', async () => {
    renderProvision('d-1')
    // Deployments tab is active — bootstrap-kit cards are always
    // counted as deployed, so the grid is non-empty.
    fireEvent.click(await screen.findByTestId('sov-tab-catalog'))
    const grid = await screen.findByTestId('sov-apps-grid')
    const cards = within(grid).getAllByTestId(/^sov-app-card-bp-/)
    expect(cards.length).toBeGreaterThan(0)
  })

  it('grid uses the canonical .apps-grid class (auto-fit minmax 360px)', async () => {
    renderProvision('d-1')
    fireEvent.click(await screen.findByTestId('sov-tab-catalog'))
    const grid = await screen.findByTestId('sov-apps-grid')
    expect(grid.className).toContain('apps-grid')
  })

  it('search filter narrows the visible cards', async () => {
    renderProvision('d-1')
    fireEvent.click(await screen.findByTestId('sov-tab-catalog'))
    const before = within(await screen.findByTestId('sov-apps-grid')).getAllByTestId(/^sov-app-card-bp-/)
    fireEvent.change(screen.getByTestId('sov-search'), { target: { value: 'cilium' } })
    const after = within(await screen.findByTestId('sov-apps-grid')).getAllByTestId(/^sov-app-card-bp-/)
    expect(after.length).toBeLessThan(before.length)
    // Still see Cilium.
    expect(after.some((c) => c.getAttribute('data-testid') === 'sov-app-card-bp-cilium')).toBe(true)
  })
})

describe('AppsPage — #3090 per-tab card link target (class vs instance)', () => {
  it('Deployments-tab card links to the INSTANCE page /app/$id', async () => {
    renderProvision('d-1')
    // Deployments tab is active by default; bootstrap-kit apps (cilium)
    // always count as deployed so the card is present.
    const card = await screen.findByTestId('sov-app-card-bp-cilium')
    expect(card.getAttribute('href')).toBe('/provision/d-1/app/bp-cilium')
  })

  it('Catalog-tab card links to the CLASS page /catalog/$blueprint', async () => {
    renderProvision('d-1')
    fireEvent.click(await screen.findByTestId('sov-tab-catalog'))
    const card = await screen.findByTestId('sov-app-card-bp-cilium')
    expect(card.getAttribute('href')).toBe('/provision/d-1/catalog/bp-cilium')
  })

  it('the SAME blueprint card resolves to DIFFERENT targets per tab', async () => {
    renderProvision('d-1')
    // Deployments first.
    const onDeployments = (
      await screen.findByTestId('sov-app-card-bp-cilium')
    ).getAttribute('href')
    // Flip to Catalog.
    fireEvent.click(screen.getByTestId('sov-tab-catalog'))
    const onCatalog = (
      await screen.findByTestId('sov-app-card-bp-cilium')
    ).getAttribute('href')
    expect(onDeployments).not.toBe(onCatalog)
    expect(onDeployments).toContain('/app/bp-cilium')
    expect(onCatalog).toContain('/catalog/bp-cilium')
  })
})
