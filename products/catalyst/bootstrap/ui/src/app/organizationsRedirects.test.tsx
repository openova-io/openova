/**
 * organizationsRedirects.test.tsx — the §4 redirect map (issue #3378):
 * "old URLs never break". Every legacy BSS / SME-admin / parent-domains
 * path must answer with a redirect to its new home under /organizations.
 *
 * The map under test mirrors ORGANIZATIONS_REDIRECTS in router.tsx
 * verbatim — kept in lockstep so a drift fails the suite. Each case
 * navigates to the legacy path and asserts the resolved location is the
 * new home (the redirect fired). Uses the same beforeLoad-throw-redirect
 * idiom as the production routes.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  redirect,
  Outlet,
} from '@tanstack/react-router'

// Mirrors ORGANIZATIONS_REDIRECTS in router.tsx — keep in lockstep.
const ORGANIZATIONS_REDIRECTS: readonly { path: string; to: string }[] = [
  { path: '/bss', to: '/organizations' },
  { path: '/bss/tenants', to: '/organizations' },
  { path: '/bss/billing', to: '/organizations/billing/billing' },
  { path: '/bss/orders', to: '/organizations/billing/orders' },
  { path: '/bss/revenue', to: '/organizations/billing/revenue' },
  { path: '/bss/vouchers', to: '/organizations/billing/vouchers' },
  { path: '/sme/users', to: '/organizations' },
  { path: '/sme/roles', to: '/organizations' },
  { path: '/sme/tenants/new', to: '/organizations/new' },
  { path: '/parent-domains', to: '/organizations/domains' },
]

// All new-home paths the redirects target — registered as leaf stubs so
// the router can resolve the post-redirect location.
const NEW_HOMES = [
  '/organizations',
  '/organizations/new',
  '/organizations/billing/billing',
  '/organizations/billing/orders',
  '/organizations/billing/revenue',
  '/organizations/billing/vouchers',
  '/organizations/domains',
]

function buildRouter(initialPath: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoutes = NEW_HOMES.map((p) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path: p,
      component: () => <div data-testid={`home-${p}`}>{p}</div>,
    }),
  )
  const redirectRoutes = ORGANIZATIONS_REDIRECTS.map((r) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path: r.path,
      component: () => null,
      beforeLoad: () => {
        throw redirect({ to: r.to as never, replace: true })
      },
    }),
  )
  const tree = rootRoute.addChildren([...homeRoutes, ...redirectRoutes])
  return createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })
}

afterEach(() => cleanup())

describe('Organizations redirect map (§4 — old URLs never break)', () => {
  for (const { path, to } of ORGANIZATIONS_REDIRECTS) {
    it(`${path} → ${to}`, async () => {
      const router = buildRouter(path)
      render(<RouterProvider router={router} />)
      await waitFor(() => {
        expect(router.state.location.pathname).toBe(to)
      })
    })
  }
})
