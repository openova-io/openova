/**
 * DashboardPage.test.tsx — anti-regression coverage for the qa-loop
 * iter-7 TC-383 fix.
 *
 * The matrix locks the literal string "Dashboard" into the page body
 * (matrix `must_contain: ["Dashboard"]`). The EPIC-6 redesign re-titled
 * the page to "Sovereign Fleet" — the breadcrumb above the H1 restores
 * the literal so the redesign and the contract co-exist.
 *
 * What this file proves:
 *   - The breadcrumb renders with the literal text "Dashboard".
 *   - The page H1 still reads "Sovereign Fleet" (no redesign rollback).
 *   - The breadcrumb is a <nav> with `aria-label="Breadcrumb"` for AT.
 *   - The current page in the trail is marked with `aria-current=page`.
 *
 * Routing: TanStack Router renders <Link to="/wizard"> via the provider.
 * We mount the bare DashboardPage with stub routes for /wizard and
 * /dashboard/applications so the Link components don't blow up.
 *
 * Assertion style: vanilla vitest matchers (no @testing-library/jest-dom)
 * because the existing src/test/setup.ts doesn't import jest-dom and
 * adding it here would diverge from the established convention.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { DashboardPage } from './DashboardPage'

afterEach(() => cleanup())

// Stub the fleet fetch so DashboardPage's useFleet query resolves
// quickly and the page mounts past the loading state. The breadcrumb
// renders independently of fetch state, but TanStack Router needs at
// least one tick before the route component is mounted.
let originalFetch: typeof globalThis.fetch
beforeEach(() => {
  originalFetch = globalThis.fetch
  globalThis.fetch = (async () =>
    new Response(JSON.stringify({ sovereigns: [], total: 0, page: 1, pageSize: 25 }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })) as never
})
afterEach(() => {
  globalThis.fetch = originalFetch
})

function makeRouter() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard',
    component: DashboardPage,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div>wizard</div>,
  })
  const crossRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard/applications',
    component: () => <div>cross-sov</div>,
  })
  return createRouter({
    routeTree: rootRoute.addChildren([dashRoute, wizardRoute, crossRoute]),
    history: createMemoryHistory({ initialEntries: ['/dashboard'] }),
  })
}

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: Infinity },
      mutations: { retry: false },
    },
  })
  const router = makeRouter()
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('DashboardPage breadcrumb (qa-loop iter-7 TC-383)', () => {
  it('renders the literal "Dashboard" label in the breadcrumb', async () => {
    renderPage()
    const crumb = await waitFor(() => screen.getByTestId('dashboard-breadcrumb'))
    // The literal "Dashboard" string MUST be present — the matrix's
    // anti-regression test will fail otherwise.
    const dash = within(crumb).getByText('Dashboard')
    expect(dash).not.toBeNull()
    // Belt-and-braces: the page body as a whole MUST contain
    // "Dashboard" verbatim, since that's exactly what the matrix's
    // must_contain check looks for in the rendered DOM.
    const root = screen.getByTestId('dashboard-page')
    expect(root.textContent ?? '').toContain('Dashboard')
  })

  it('keeps the H1 as "Sovereign Fleet" so the redesign is preserved', async () => {
    renderPage()
    await waitFor(() => screen.getByTestId('dashboard-page'))
    const h1 = screen.getByRole('heading', { level: 1, name: 'Sovereign Fleet' })
    expect(h1).not.toBeNull()
  })

  it('exposes the breadcrumb with aria-label="Breadcrumb" for AT users', async () => {
    renderPage()
    const crumb = await waitFor(() => screen.getByLabelText('Breadcrumb'))
    expect(crumb.tagName.toLowerCase()).toBe('nav')
  })

  it('marks the current page in the trail with aria-current', async () => {
    renderPage()
    await waitFor(() => screen.getByTestId('dashboard-breadcrumb'))
    const current = screen.getByText('Sovereign Fleet', { selector: 'li' })
    expect(current.getAttribute('aria-current')).toBe('page')
  })
})
