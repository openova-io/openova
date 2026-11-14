/**
 * CostViewLink.test.tsx — the console's link to the cost view of the
 * billing engine (CTL-355). The link opens the route of the Billing
 * menu's application section, and it is rendered only when the billing
 * engine registered its console UI on this Sovereign.
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
} from '@tanstack/react-router'
import type { SidebarEntry } from '@/lib/console-ui.api'

const sidebar = vi.hoisted(() => ({
  getSidebarEntries: vi.fn<(id: string) => Promise<SidebarEntry[]>>(),
}))
vi.mock('@/lib/console-ui.api', () => ({ getSidebarEntries: sidebar.getSidebarEntries }))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-1', isLoading: false }),
}))

import { CostViewLink } from './CostViewLink'
import { BILLING_SECTIONS } from '../bss/billing-sections'

function entry(id: string, enabled: boolean): SidebarEntry {
  return { id, label: id, route: `/apps/${id}/dashboard`, order: 45, source: 'blueprint', enabled }
}

function renderLink(installedOverride?: readonly string[]) {
  const rootRoute = createRootRoute({
    component: () => (
      <div data-testid="harness">
        <CostViewLink installedOverride={installedOverride} />
      </div>
    ),
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/apps/$appId/$view',
    component: () => null,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  sidebar.getSidebarEntries.mockReset()
})
afterEach(() => cleanup())

describe('CostViewLink — the link to the cost view of the billing engine (CTL-355)', () => {
  it('opens the route of the Billing menu application section', async () => {
    renderLink(['bp-chargeback'])
    const link = (await screen.findByTestId('cost-view-link-open')) as HTMLAnchorElement
    const section = BILLING_SECTIONS.find((s) => s.id === 'chargeback')
    expect(section?.to).toBe('/apps/bp-chargeback/dashboard')
    expect(link.getAttribute('href')).toBe(section?.to)
    expect(screen.getByTestId('cost-view-link').textContent).toContain('Catalyst BSS')
    // The test seam fetches nothing.
    expect(sidebar.getSidebarEntries).not.toHaveBeenCalled()
  })

  it('renders nothing when the billing engine is not installed', async () => {
    renderLink([])
    expect(await screen.findByTestId('harness')).toBeTruthy()
    expect(screen.queryByTestId('cost-view-link')).toBeNull()
  })

  it('renders the link when the Sovereign lists the billing engine as enabled', async () => {
    sidebar.getSidebarEntries.mockResolvedValue([entry('bp-agenity', true), entry('bp-chargeback', true)])
    renderLink()
    expect(await screen.findByTestId('cost-view-link-open')).toBeTruthy()
    expect(sidebar.getSidebarEntries).toHaveBeenCalledWith('dep-1')
  })

  it('renders nothing when the entry of the billing engine is switched off', async () => {
    sidebar.getSidebarEntries.mockResolvedValue([entry('bp-chargeback', false)])
    renderLink()
    await waitFor(() => expect(sidebar.getSidebarEntries).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId('harness')).toBeTruthy()
    expect(screen.queryByTestId('cost-view-link')).toBeNull()
  })

  it('renders nothing when the installed set cannot be read', async () => {
    sidebar.getSidebarEntries.mockRejectedValue(new Error('unreachable'))
    renderLink()
    await waitFor(() => expect(sidebar.getSidebarEntries).toHaveBeenCalledTimes(1))
    expect(await screen.findByTestId('harness')).toBeTruthy()
    expect(screen.queryByTestId('cost-view-link')).toBeNull()
  })
})
