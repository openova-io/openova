/**
 * OrganizationDetailPage.test.tsx — the org detail surface (issue #3378
 * §4 + DoD 6): the identity card renders the CR fields, the Enter-org
 * button shows on a sub-org and is ABSENT on the parent.
 */

import { describe, it, expect, afterEach } from 'vitest'
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

import { OrganizationDetailPage } from './OrganizationDetailPage'
import { parentRowFromSelf, subOrgRowFromRecord, type OrgRow } from '@/lib/organizations.api'

const PARENT = parentRowFromSelf({ deploymentId: 'd1', sovereignFQDN: 'hw130.omantel.biz' })
const SUB = subOrgRowFromRecord({
  id: 'tnt-1', orgName: 'ACME Corp', consoleHost: 'console.acme.omani.homes', subdomain: 'acme',
  parentDomain: 'omani.homes', plan: 'pro', kind: 'customer', tier: 'org', billingMode: 'real',
  isolation: 'vcluster', status: 'active', region: 'r', ownerEmail: 'o@acme.example',
  createdAt: '2026-06-13T00:00:00Z', updatedAt: '2026-06-13T00:00:00Z', lastError: '',
})

function renderDetail(org: string, rows: readonly OrgRow[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/$org',
    component: () => <OrganizationDetailPage org={org} initialOrgsOverride={rows} />,
  })
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/organizations', component: () => <div /> })
  const tree = rootRoute.addChildren([route, orgRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/organizations/${org}`] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('OrganizationDetailPage — §4 identity + DoD 6 Enter-org', () => {
  it('renders the identity card with the CR fields for a sub-org', async () => {
    renderDetail('acme', [PARENT, SUB])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.getByTestId('org-detail-kind').textContent).toBe('customer')
    expect(screen.getByTestId('org-detail-billing').textContent).toBe('real')
    expect(screen.getByTestId('org-detail-isolation').textContent).toBe('vcluster')
  })

  it('shows the Enter-org button on a sub-org', async () => {
    renderDetail('acme', [PARENT, SUB])
    expect(await screen.findByTestId('enter-org-button')).toBeTruthy()
  })

  it('HIDES the Enter-org button on the parent (§5: already inside it)', async () => {
    renderDetail(PARENT.slug, [PARENT, SUB])
    expect(await screen.findByTestId('org-detail-parent-badge')).toBeTruthy()
    expect(screen.queryByTestId('enter-org-button')).toBeNull()
  })

  it('renders a not-found state for an unknown slug', async () => {
    renderDetail('ghost', [PARENT, SUB])
    expect(await screen.findByTestId('org-detail-not-found')).toBeTruthy()
  })
})
