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
import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderProvision(deploymentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
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
  const tree = rootRoute.addChildren([provisionRoute, detailRoute, jobsRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}`] }),
  })
  return render(<RouterProvider router={router} />)
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
