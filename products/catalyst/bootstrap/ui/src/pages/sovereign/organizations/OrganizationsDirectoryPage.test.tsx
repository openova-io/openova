/**
 * OrganizationsDirectoryPage.test.tsx — the §5 empty-state law (issue
 * #3378): on a sovereign with zero sub-orgs the directory shows the
 * parent org as the first citizen with its badges (never blank) + the
 * Create button. Uses the initialOrgsOverride seam so the test is
 * deterministic without the fetch path.
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

import { OrganizationsDirectoryPage } from './OrganizationsDirectoryPage'
import { parentRowFromSelf, subOrgRowFromRecord, type OrgRow } from '@/lib/organizations.api'

function renderDirectory(orgs: readonly OrgRow[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dirRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations',
    component: () => <OrganizationsDirectoryPage initialOrgsOverride={orgs} />,
  })
  // Register the routes the directory links to so TanStack's <Link>
  // type-resolution doesn't throw on mount.
  const newRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/new',
    component: () => <div data-testid="orgs-new-stub" />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/$org',
    component: () => <div data-testid="orgs-detail-stub" />,
  })
  // Section-nav targets — register stubs so the directory's <Link>s
  // resolve at render.
  const sectionPaths = [
    '/organizations/commerce/plans',
    '/organizations/commerce/addons',
    '/organizations/commerce/bundles',
    '/organizations/commerce/industries',
    '/organizations/commerce/apps',
    // #4196 — Billing is now its own top-level menu; the directory's
    // quick-links jump straight into /billing/*.
    '/billing/revenue',
    '/billing/vouchers',
    '/organizations/domains',
  ]
  const sectionRoutes = sectionPaths.map((p) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path: p,
      component: () => <div data-testid={`section-${p}`} />,
    }),
  )
  const tree = rootRoute.addChildren([dirRoute, newRoute, detailRoute, ...sectionRoutes])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/organizations'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('OrganizationsDirectoryPage — §5 empty-state law', () => {
  it('shows exactly the parent row (first citizen) with zero sub-orgs', async () => {
    const parent = parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw130.omantel.biz' })
    renderDirectory([parent])
    expect(await screen.findByTestId('organizations-directory-page')).toBeTruthy()
    // exactly one row, and it is the parent
    expect(screen.getByTestId(`organizations-row-${parent.id}`)).toBeTruthy()
    expect(screen.getByTestId(`organizations-parent-badge-${parent.id}`)).toBeTruthy()
    // result count reads 1/1 — never blank
    expect(screen.getByTestId('organizations-result-count').textContent).toBe('1/1')
  })

  it('renders the parent showback billing badge (§5 day-one showback)', async () => {
    const parent = parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw130.omantel.biz' })
    renderDirectory([parent])
    const badge = await screen.findByTestId(`organizations-cell-billing-${parent.id}`)
    expect(badge.getAttribute('data-mode')).toBe('showback')
  })

  it('exposes the Create organization button (the internal door entry)', async () => {
    const parent = parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw130.omantel.biz' })
    renderDirectory([parent])
    const btn = await screen.findByTestId('orgs-create-button')
    expect(btn.getAttribute('href')).toContain('/organizations/new')
  })

  // #4170/#4196 — Vouchers is a discoverable section link (sovereign-admin
  // issues prepaid signup codes day-one, before the parent leaves showback).
  // On a single-org Sovereign (console.omantel.biz) the parent is showback,
  // yet the Vouchers entry must still be present and point at the native
  // Billing menu's Vouchers section (NOT BillingModeGate-wrapped — see
  // router.tsx).
  it('exposes a Vouchers section link day-one (showback parent)', async () => {
    const parent = parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw130.omantel.biz' })
    renderDirectory([parent])
    await screen.findByTestId('organizations-directory-page')
    const voucherLink = screen.getByTestId('organizations-nav-vouchers')
    expect(voucherLink.getAttribute('href')).toBe('/billing/vouchers')
    expect(voucherLink.textContent).toContain('Vouchers')
  })

  it('lists the parent FIRST, then sub-orgs (parent is first citizen)', async () => {
    const parent = parentRowFromSelf({ deploymentId: 'dep-1', sovereignFQDN: 'hw130.omantel.biz' })
    const sub = subOrgRowFromRecord({
      id: 'tnt-1',
      orgName: 'ACME Corp',
      consoleHost: 'console.acme.omani.homes',
      subdomain: 'acme',
      parentDomain: 'omani.homes',
      plan: 'pro',
      kind: 'customer',
      tier: 'org',
      billingMode: 'real',
      isolation: 'vcluster',
      status: 'active',
      region: 'me-east-215-a',
      ownerEmail: 'owner@acme.example',
      createdAt: '2026-06-13T00:00:00Z',
      updatedAt: '2026-06-13T00:00:00Z',
      lastError: '',
    })
    renderDirectory([parent, sub])
    const table = await screen.findByTestId('organizations-table')
    const rows = table.querySelectorAll('tbody tr')
    expect(rows.length).toBe(2)
    // first row carries the parent marker
    expect(rows[0].getAttribute('data-parent')).toBe('true')
    expect(rows[1].getAttribute('data-parent')).toBe('false')
    expect(screen.getByTestId('organizations-result-count').textContent).toBe('2/2')
  })
})
