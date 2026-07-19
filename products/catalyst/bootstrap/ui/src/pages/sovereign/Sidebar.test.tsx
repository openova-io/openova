/**
 * Sidebar.test.tsx — wiring lock-in for the Sovereign-portal sidebar.
 *
 * Coverage (post issue #350 — accordion dropped):
 *   • Renders the OpenOva mark inside the 56px logo header
 *   • Surfaces the deployment id (or sovereignFQDN when supplied) as
 *     the Sovereign label in place of the canonical Org switcher
 *   • Renders Apps + Jobs + Dashboard + Cloud + Settings nav items
 *     (the explicit subset for the Sovereign-provision surface)
 *   • Active item carries `aria-current="page"`
 *   • Cloud is a SINGLE flat <Link> (no accordion, no chevron, no
 *     sub-items) that navigates to /provision/$id/cloud
 *   • Cloud highlight applies whenever the operator is on any
 *     /cloud/* path (including the legacy P3 sub-routes that now
 *     redirect to /cloud?view=… queries)
 *   • No bespoke operator/identity card — issue #750 moved identity
 *     to the canonical <ProfileMenu /> in PortalShell's top-right.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { Sidebar } from './Sidebar'

function renderSidebarAt(initialPath: string, sovereignFQDN?: string | null) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const SidebarHost = () => (
    <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
  )
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: SidebarHost,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: SidebarHost,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: SidebarHost,
  })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: SidebarHost,
  })
  // The legacy /cloud/<category>/<resource> paths still exist as
  // server-side redirect routes; the sidebar's active matcher must
  // also light up on those, so we register a representative subset
  // here. The sidebar has no knowledge of the redirect — it just
  // matches any `/cloud[/...]` segment.
  const cloudArchitectureRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/architecture',
    component: SidebarHost,
  })
  const cloudComputeClustersRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/compute/clusters',
    component: SidebarHost,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: SidebarHost,
  })
  const settingsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/settings',
    component: SidebarHost,
  })
  const tree = rootRoute.addChildren([
    provisionRoute,
    jobsRoute,
    dashboardRoute,
    cloudRoute.addChildren([cloudArchitectureRoute, cloudComputeClustersRoute]),
    wizardRoute,
    settingsRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  if (typeof window !== 'undefined') {
    window.localStorage.clear()
  }
})

afterEach(() => {
  cleanup()
  if (typeof window !== 'undefined') {
    window.localStorage.clear()
  }
})

describe('Sidebar — chrome', () => {
  it('renders the OpenOva mark + wordmark in the header', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const sidebar = await screen.findByTestId('admin-sidebar')
    expect(sidebar.querySelector('svg')).toBeTruthy()
    expect(within(sidebar).getByText('OpenOva')).toBeTruthy()
    expect(within(sidebar).getByText(/Sovereign/i)).toBeTruthy()
  })

  it('uses sovereignFQDN as the Sovereign label when supplied', async () => {
    renderSidebarAt('/provision/d-test-1234', 'omantel.omani.works')
    const label = await screen.findByTestId('sov-org-label')
    expect(label.textContent).toContain('omantel.omani.works')
  })

  it('falls back to a deploymentId-derived label when no FQDN is known', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const label = await screen.findByTestId('sov-org-label')
    expect(label.textContent).toContain('d-test-1')
  })
})

describe('Sidebar — top-level navigation', () => {
  it('renders Apps + Jobs + Dashboard + Cloud + Settings nav items', async () => {
    renderSidebarAt('/provision/d-test-1234')
    expect(await screen.findByTestId('sov-nav-apps')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-jobs')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-dashboard')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-cloud')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-settings')).toBeTruthy()
    // Canonical-but-omitted items must NOT render.
    expect(screen.queryByTestId('sov-nav-domains')).toBeNull()
    expect(screen.queryByTestId('sov-nav-billing')).toBeNull()
    expect(screen.queryByTestId('sov-nav-team')).toBeNull()
    // Legacy flat Infrastructure entry remains gone.
    expect(screen.queryByTestId('sov-nav-infrastructure')).toBeNull()
  })

  it('marks Apps active when on the provision root', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const apps = await screen.findByTestId('sov-nav-apps')
    expect(apps.getAttribute('aria-current')).toBe('page')
    const jobs = screen.getByTestId('sov-nav-jobs')
    expect(jobs.getAttribute('aria-current')).toBeNull()
  })

  it('marks Jobs active when on /provision/.../jobs', async () => {
    renderSidebarAt('/provision/d-test-1234/jobs')
    const jobs = await screen.findByTestId('sov-nav-jobs')
    expect(jobs.getAttribute('aria-current')).toBe('page')
    const apps = screen.getByTestId('sov-nav-apps')
    expect(apps.getAttribute('aria-current')).toBeNull()
  })
})

describe('Sidebar — Settings entry (issue #516: divert-to-wizard fix)', () => {
  it('Settings link href targets /provision/$id/settings, NOT /wizard', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const settings = (await screen.findByTestId('sov-nav-settings')) as HTMLAnchorElement
    const href = settings.getAttribute('href') ?? ''
    expect(href).toMatch(/\/provision\/d-test-1234\/settings$/)
    // Regression guard — the original bug had Settings pointing at /wizard.
    expect(href).not.toMatch(/\/wizard(\/|$)/)
  })

  it('Settings is highlighted on /provision/.../settings', async () => {
    renderSidebarAt('/provision/d-test-1234/settings')
    const settings = await screen.findByTestId('sov-nav-settings')
    expect(settings.getAttribute('aria-current')).toBe('page')
  })

  it('Settings is NOT highlighted on /wizard (post-fix: wizard ≠ settings)', async () => {
    renderSidebarAt('/wizard')
    const settings = await screen.findByTestId('sov-nav-settings')
    expect(settings.getAttribute('aria-current')).toBeNull()
  })
})

describe('Sidebar — Cloud entry (issue #350: accordion dropped)', () => {
  it('renders Cloud as a SINGLE flat link (not a button, no chevron, no sub-items)', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloud = await screen.findByTestId('sov-nav-cloud')
    // Flat <Link> renders as an anchor — NOT a <button>.
    expect(cloud.tagName).toBe('A')
    // No accordion contracts: no aria-expanded, no aria-controls, no
    // chevron testid, no sub-items.
    expect(cloud.getAttribute('aria-expanded')).toBeNull()
    expect(cloud.getAttribute('aria-controls')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-toggle')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-architecture')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-compute')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-network')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-storage')).toBeNull()
    // No second-level toggles either.
    expect(screen.queryByTestId('sov-nav-cloud-compute-toggle')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-network-toggle')).toBeNull()
    expect(screen.queryByTestId('sov-nav-cloud-storage-toggle')).toBeNull()
  })

  it('Cloud link href targets /provision/$id/cloud', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloud = (await screen.findByTestId('sov-nav-cloud')) as HTMLAnchorElement
    const href = cloud.getAttribute('href') ?? ''
    expect(href).toMatch(/\/provision\/d-test-1234\/cloud$/)
  })

  it('Cloud is highlighted on /cloud root', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud')
    const cloud = await screen.findByTestId('sov-nav-cloud')
    expect(cloud.getAttribute('aria-current')).toBe('page')
  })

  it('Cloud is highlighted on /cloud/architecture (legacy redirect path matches the sidebar matcher)', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud/architecture')
    const cloud = await screen.findByTestId('sov-nav-cloud')
    expect(cloud.getAttribute('aria-current')).toBe('page')
  })

  it('Cloud is highlighted on /cloud/compute/clusters (legacy redirect path)', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud/compute/clusters')
    const cloud = await screen.findByTestId('sov-nav-cloud')
    expect(cloud.getAttribute('aria-current')).toBe('page')
  })

  it('Apps is NOT highlighted when on /cloud — exact match guards the apps highlight', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud')
    const apps = await screen.findByTestId('sov-nav-apps')
    expect(apps.getAttribute('aria-current')).toBeNull()
    const cloud = screen.getByTestId('sov-nav-cloud')
    expect(cloud.getAttribute('aria-current')).toBe('page')
  })
})

describe('Sidebar — identity placement (issue #750)', () => {
  it('does NOT render the bespoke "Operator / Provisioning session" card', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const sidebar = await screen.findByTestId('admin-sidebar')
    // The bespoke widget was removed — identity now lives in the
    // top-right of PortalShell via <ProfileMenu />, mirroring the
    // wizard + Sovereign-console surfaces.
    expect(within(sidebar).queryByText('Operator')).toBeNull()
    expect(within(sidebar).queryByText('Provisioning session')).toBeNull()
  })
})
