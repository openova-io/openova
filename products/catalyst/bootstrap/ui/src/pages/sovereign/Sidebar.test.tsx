/**
 * Sidebar.test.tsx — pixel-port lock-in for Sidebar.tsx.
 *
 *   • Renders the OpenOva mark inside the 56px logo header
 *   • Surfaces the deployment id (or sovereignFQDN when supplied) as the
 *     "tenant" label in place of the canonical Tenant switcher
 *   • Renders Apps + Jobs + Settings nav items (the explicit subset for
 *     the Sovereign-provision surface)
 *   • Active item carries `aria-current="page"` + the accent-tinted
 *     class string the canonical surface uses
 *   • Operator card at the bottom (analog of canonical "User" card)
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
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <Sidebar deploymentId="d-test-1234" sovereignFQDN={sovereignFQDN ?? null} />,
  })
  const tree = rootRoute.addChildren([provisionRoute, jobsRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })
  return render(<RouterProvider router={router} />)
}

afterEach(() => cleanup())
beforeEach(() => {})

describe('Sidebar — chrome', () => {
  it('renders the OpenOva mark + wordmark in the header', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const sidebar = await screen.findByTestId('sov-sidebar')
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

describe('Sidebar — navigation', () => {
  it('renders exactly Apps + Jobs + Settings nav items', async () => {
    renderSidebarAt('/provision/d-test-1234')
    expect(await screen.findByTestId('sov-nav-apps')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-jobs')).toBeTruthy()
    expect(await screen.findByTestId('sov-nav-settings')).toBeTruthy()
    // Canonical-but-omitted items must NOT render: dashboard / domains /
    // billing / team. Their absence is part of the contract.
    expect(screen.queryByTestId('sov-nav-dashboard')).toBeNull()
    expect(screen.queryByTestId('sov-nav-domains')).toBeNull()
    expect(screen.queryByTestId('sov-nav-billing')).toBeNull()
    expect(screen.queryByTestId('sov-nav-team')).toBeNull()
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

describe('Sidebar — operator card', () => {
  it('renders Operator + "Provisioning session" footer text', async () => {
    renderSidebarAt('/provision/d-test-1234')
    const sidebar = await screen.findByTestId('sov-sidebar')
    expect(within(sidebar).getByText('Operator')).toBeTruthy()
    expect(within(sidebar).getByText('Provisioning session')).toBeTruthy()
  })
})
