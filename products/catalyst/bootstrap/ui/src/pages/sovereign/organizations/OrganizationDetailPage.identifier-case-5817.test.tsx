/**
 * OrganizationDetailPage.identifier-case-5817.test.tsx — UAT row 7.
 *
 * The canonical-fields <dl> used to apply `capitalize` to EVERY value. That
 * mangles identifiers on screen: the Org slug `uatco` renders as `Uatco`, the
 * owner `emrah.baysal@openova.io` as `Emrah.baysal@openova.io`. Row 7's whole
 * assertion is that this page shows the CANONICAL fields, and the slug in
 * particular is the join key an operator types into kubectl and the API — a
 * value you cannot copy off the screen is not canonical.
 *
 * WHY THE ASSERTION IS ON THE CLASS AND NOT THE TEXT. `text-transform` changes
 * only what is PAINTED; `textContent` stays `uatco` either way, and jsdom does
 * not do layout. So a textContent assertion here would be the archetypal guard
 * that cannot go red — it passes identically before and after the fix. The
 * defect lives entirely in the class, so the class is what is pinned.
 *
 * Both directions are asserted. The identifier fields must NOT carry
 * `capitalize`; at least one enum field MUST. That second half is not
 * presentation-pinning for its own sake — it is the vacuity control. Without
 * it, deleting the class from every field (or breaking the query so nothing is
 * found) would satisfy the negative assertions for entirely the wrong reason.
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

const PARENT = parentRowFromSelf({ deploymentId: 'd1', sovereignFQDN: 'hw292.omani.works' })

// Modelled on the live hw292 Org the walk used, because the two values that
// expose the bug are exactly the two this Org carries: an all-lowercase slug
// and an email owner.
const UATCO = subOrgRowFromRecord({
  id: 'tnt-uatco', orgName: 'UAT Co', consoleHost: 'console.uatco.omani.homes',
  subdomain: 'uatco', parentDomain: 'omani.homes', plan: 'm', planSlug: 'm', kind: 'customer',
  tier: 'org', billingMode: 'real', isolation: 'vcluster', status: 'active',
  region: 'r', ownerEmail: 'emrah.baysal@openova.io',
  createdAt: '2026-08-06T00:00:00Z', updatedAt: '2026-08-06T00:00:00Z', lastError: '',
})

function renderDetail(org: string, rows: readonly OrgRow[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/$org',
    component: () => <OrganizationDetailPage org={org} initialOrgsOverride={rows} />,
  })
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/organizations', component: () => <div /> })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route, orgRoute]),
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

describe('#5817 — identifier fields render verbatim, enum fields may be title-cased', () => {
  it('does not title-case the slug or the owner email', async () => {
    renderDetail('uatco', [PARENT, UATCO])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()

    for (const testid of ['org-detail-slug', 'org-detail-owner']) {
      const dd = screen.getByTestId(testid)
      expect(
        dd.className.split(/\s+/),
        `${testid} carries a text-transform — the operator sees a value that ` +
          `differs from what the model emits and cannot copy it as a join key`,
      ).not.toContain('capitalize')
    }

    // The values themselves are still exactly what the model emits. This does
    // not detect the CSS bug (see the file header) but it does catch a "fix"
    // that mangles the string in JS instead of in CSS — a real risk, since
    // dropping the class invites someone to reach for toLowerCase() next.
    expect(screen.getByTestId('org-detail-slug').textContent).toBe('uatco')
    expect(screen.getByTestId('org-detail-owner').textContent).toBe('emrah.baysal@openova.io')
  })

  it('control: an enum field still opts in, so the class check demonstrably works', async () => {
    renderDetail('uatco', [PARENT, UATCO])
    expect(await screen.findByTestId('organization-detail-page')).toBeTruthy()

    // If this ever fails alongside the test above, the class query is broken
    // and the negative assertions above prove nothing.
    expect(
      screen.getByTestId('org-detail-kind').className.split(/\s+/),
      'no field carries capitalize at all — the assertions above would pass ' +
        'on an unstyled page and are no longer evidence of anything',
    ).toContain('capitalize')
  })
})
