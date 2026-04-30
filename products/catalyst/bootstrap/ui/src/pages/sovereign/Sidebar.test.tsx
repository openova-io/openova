/**
 * Sidebar.test.tsx — wiring lock-in for the Sovereign-portal sidebar.
 *
 * Coverage:
 *   • Renders the OpenOva mark inside the 56px logo header
 *   • Surfaces the deployment id (or sovereignFQDN when supplied) as
 *     the "tenant" label in place of the canonical Tenant switcher
 *   • Renders Apps + Jobs + Dashboard + Cloud accordion + Settings
 *     nav items (the explicit subset for the Sovereign-provision
 *     surface)
 *   • Active item carries `aria-current="page"` and the accent-tinted
 *     class string the canonical surface uses
 *   • Cloud accordion: toggles open/closed, persists state in
 *     localStorage, auto-expands when on a /cloud/* route, sub-items
 *     route to /cloud/{architecture,compute,network,storage}.
 *   • Operator card at the bottom (analog of canonical "User" card)
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within, fireEvent } from '@testing-library/react'
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
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const cloudArchitectureRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/architecture',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const cloudComputeRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/compute',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const cloudNetworkRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/network',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const cloudStorageRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/storage',
    component: () => (
      <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />
    ),
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />,
  })
  const tree = rootRoute.addChildren([
    provisionRoute,
    jobsRoute,
    cloudRoute.addChildren([
      cloudArchitectureRoute,
      cloudComputeRoute,
      cloudNetworkRoute,
      cloudStorageRoute,
    ]),
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  // Each test starts with a clean localStorage so the persisted
  // accordion-expanded state doesn't leak between cases.
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
    // SVG logo present
    expect(sidebar.querySelector('svg')).toBeTruthy()
    expect(within(sidebar).getByText('OpenOva')).toBeTruthy()
    // Sovereign label (replaces Tenant switcher)
    expect(within(sidebar).getByText(/Sovereign/i)).toBeTruthy()
  })

  it('uses sovereignFQDN as the tenant label when supplied', async () => {
    renderSidebarAt('/provision/d-test-1234', 'omantel.omani.works')
    const label = await screen.findByTestId('sov-tenant-label')
    expect(label.textContent).toContain('omantel.omani.works')
  })

  it('falls back to a deploymentId-derived label when no FQDN is known', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const label = await screen.findByTestId('sov-tenant-label')
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
    // Canonical-but-omitted items must NOT render: domains / billing /
    // team are tenant-console concerns, not Sovereign-provision ones.
    expect(screen.queryByTestId('sov-nav-domains')).toBeNull()
    expect(screen.queryByTestId('sov-nav-billing')).toBeNull()
    expect(screen.queryByTestId('sov-nav-team')).toBeNull()
    // The legacy `infrastructure` flat nav item is gone — replaced by
    // the Cloud accordion.
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

describe('Sidebar — Cloud accordion', () => {
  it('renders the Cloud header as a button (not a link)', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.tagName).toBe('BUTTON')
    expect(cloudHeader.getAttribute('aria-controls')).toBe('sov-nav-cloud-group')
  })

  it('starts collapsed when the operator is NOT on a /cloud/* route', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('false')
    // Sub-items are not rendered when collapsed.
    expect(screen.queryByTestId('sov-nav-cloud-architecture')).toBeNull()
  })

  it('starts EXPANDED when the operator is on a /cloud/* route', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud/architecture')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByTestId('sov-nav-cloud-architecture')).toBeTruthy()
    expect(screen.getByTestId('sov-nav-cloud-compute')).toBeTruthy()
    expect(screen.getByTestId('sov-nav-cloud-network')).toBeTruthy()
    expect(screen.getByTestId('sov-nav-cloud-storage')).toBeTruthy()
  })

  it('clicking the Cloud header toggles expanded state', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(cloudHeader)
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByTestId('sov-nav-cloud-architecture')).toBeTruthy()

    fireEvent.click(cloudHeader)
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('false')
  })

  it('persists expanded state to localStorage under sov-nav-cloud-expanded', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')

    fireEvent.click(cloudHeader)
    expect(window.localStorage.getItem('sov-nav-cloud-expanded')).toBe('true')

    fireEvent.click(cloudHeader)
    expect(window.localStorage.getItem('sov-nav-cloud-expanded')).toBe('false')
  })

  it('restores expanded state from localStorage on mount', async () => {
    window.localStorage.setItem('sov-nav-cloud-expanded', 'true')
    renderSidebarAt('/provision/d-test-1234')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.getAttribute('aria-expanded')).toBe('true')
  })

  it('marks Cloud active and the active sub-item with aria-current when on a /cloud/* route', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud/compute')
    const cloudHeader = await screen.findByTestId('sov-nav-cloud')
    expect(cloudHeader.getAttribute('aria-current')).toBe('page')

    const compute = screen.getByTestId('sov-nav-cloud-compute')
    expect(compute.getAttribute('aria-current')).toBe('page')

    const architecture = screen.getByTestId('sov-nav-cloud-architecture')
    expect(architecture.getAttribute('aria-current')).toBeNull()
  })

  it('sub-item links target /cloud/{suffix}', async () => {
    renderSidebarAt('/provision/d-test-1234/cloud/architecture')
    const compute = await screen.findByTestId('sov-nav-cloud-compute')
    const href = compute.getAttribute('href') ?? ''
    expect(href).toMatch(/\/cloud\/compute$/)
  })

  it('exposes a chevron toggle indicator', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const toggle = await screen.findByTestId('sov-nav-cloud-toggle')
    expect(toggle).toBeTruthy()
  })
})

describe('Sidebar — operator card', () => {
  it('renders Operator + "Provisioning session" footer text', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const sidebar = await screen.findByTestId('admin-sidebar')
    expect(within(sidebar).getByText('Operator')).toBeTruthy()
    expect(within(sidebar).getByText('Provisioning session')).toBeTruthy()
  })
})
