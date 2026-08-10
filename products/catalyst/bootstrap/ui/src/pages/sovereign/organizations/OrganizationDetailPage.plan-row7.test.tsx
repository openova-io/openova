/**
 * OrganizationDetailPage.plan-row7.test.tsx — UAT rows 7 and 25, render layer.
 *
 * ROW 7 asserts the Org detail page shows the canonical fields INCLUDING the
 * purchased plan (`planSlug m`, the #4292 split where `tier` is the isolation
 * class and the plan is carried separately). Eight of the nine already
 * rendered; the plan rendered nowhere, because it was dropped at every layer
 * beneath this one — the API response struct, the wire key, and OrgRow.
 * These assert the VALUE painted, not merely that a testid exists: a `Plan`
 * label over an empty <dd> is the state row 7 already failed on.
 *
 * ROW 25 is asserted here at the level the operator actually experiences it —
 * the parent row's link RESOLVES instead of falling through to
 * org-detail-not-found, on renders where self-discovery succeeded AND on
 * renders where it did not.
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
import {
  parentRowFromSelf,
  subOrgRowFromRecord,
  SOVEREIGN_PARENT_SLUG,
  type OrgRow,
} from '@/lib/organizations.api'
import type { OrgRecord } from '@/lib/bss.api'

const record: OrgRecord = {
  id: 'tnt-1',
  orgName: 'UAT Co',
  consoleHost: 'console.uatco.omani.homes',
  subdomain: 'uatco',
  parentDomain: 'omani.homes',
  plan: null,
  planSlug: 'm',
  kind: 'customer',
  tier: 'org',
  billingMode: 'real',
  isolation: 'vcluster',
  status: 'active',
  region: 'me-east-215-a',
  ownerEmail: 'owner@uatco.example',
  createdAt: '2026-08-10T00:00:00Z',
  updatedAt: '2026-08-10T00:00:00Z',
  lastError: '',
}

function renderDetail(org: string, rows: readonly OrgRow[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/$org',
    component: () => <OrganizationDetailPage org={org} initialOrgsOverride={rows} />,
  })
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations',
    component: () => <div />,
  })
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

describe('row 7 — the Org detail page names the purchased plan', () => {
  it('renders the plan the Organization bought', async () => {
    renderDetail('uatco', [subOrgRowFromRecord(record)])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.getByTestId('org-detail-plan').textContent).toBe('m')
  })

  // Discriminating: a different plan must paint differently. A hardcoded
  // literal would satisfy the case above.
  it('paints the actual plan, not a fixed one', async () => {
    renderDetail('uatco', [subOrgRowFromRecord({ ...record, planSlug: 'xl' })])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.getByTestId('org-detail-plan').textContent).toBe('xl')
  })

  // The sovereign-root row buys nothing — the field is omitted rather than
  // rendered blank or defaulted.
  it('omits the plan on the parent row instead of inventing one', async () => {
    renderDetail(SOVEREIGN_PARENT_SLUG, [
      parentRowFromSelf({ deploymentId: 'd1', sovereignFQDN: 'hw293.omantel.biz' }),
    ])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.queryByTestId('org-detail-plan')).toBeNull()
  })

  // CONTROL — the eight fields that already rendered must still render. A
  // change to this <dl> that dropped one would otherwise ship green.
  it('does not disturb the fields that already rendered', async () => {
    renderDetail('uatco', [subOrgRowFromRecord(record)])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.getByTestId('org-detail-slug').textContent).toBe('uatco')
    expect(screen.getByTestId('org-detail-kind').textContent).toBe('customer')
    expect(screen.getByTestId('org-detail-tier').textContent).toBe('org')
    expect(screen.getByTestId('org-detail-billing').textContent).toBe('real')
    expect(screen.getByTestId('org-detail-isolation').textContent).toBe('vcluster')
    expect(screen.getByTestId('org-detail-status').textContent).toBe('active')
    expect(screen.getByTestId('org-detail-owner').textContent).toBe('owner@uatco.example')
  })
})

describe('row 25 — the parent row resolves at its stable slug', () => {
  it('the parent row is found, not 404, when self-discovery succeeded', async () => {
    renderDetail(SOVEREIGN_PARENT_SLUG, [
      parentRowFromSelf({ deploymentId: 'd1', sovereignFQDN: 'hw293.omantel.biz' }),
      subOrgRowFromRecord(record),
    ])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.queryByTestId('org-detail-not-found')).toBeNull()
    expect(screen.getByTestId('org-detail-identity')).toBeTruthy()
  })

  // The half that used to 404: the very same URL, resolved on a render where
  // GET /v1/sovereign/self failed.
  it('resolves identically when self-discovery returned null', async () => {
    renderDetail(SOVEREIGN_PARENT_SLUG, [parentRowFromSelf(null), subOrgRowFromRecord(record)])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.queryByTestId('org-detail-not-found')).toBeNull()
    expect(screen.getByTestId('org-detail-identity')).toBeTruthy()
  })

  // CONTROL — a genuinely absent Org must STILL report not-found. A fix that
  // made resolution always succeed would pass both cases above while
  // destroying the page's only honest failure state.
  it('an Org that is really absent still reports not-found', async () => {
    renderDetail('no-such-org', [parentRowFromSelf(null), subOrgRowFromRecord(record)])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()
    expect(screen.getByTestId('org-detail-not-found')).toBeTruthy()
  })
})
