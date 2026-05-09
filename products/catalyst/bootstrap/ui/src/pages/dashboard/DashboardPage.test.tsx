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
 *   - The breadcrumb has the correct `aria-label` and stable testid.
 *
 * Routing: TanStack Router renders <Link to="/wizard"> via the provider.
 * We mount the bare DashboardPage with stub routes for /wizard and
 * /dashboard/applications so the Link components don't blow up.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
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
  it('renders the literal "Dashboard" label in the breadcrumb', () => {
    renderPage()
    const crumb = screen.getByTestId('dashboard-breadcrumb')
    expect(crumb).toBeInTheDocument()
    // The literal "Dashboard" string MUST be present — the matrix's
    // anti-regression test will fail otherwise.
    expect(within(crumb).getByText('Dashboard')).toBeInTheDocument()
  })

  it('keeps the H1 as "Sovereign Fleet" so the redesign is preserved', () => {
    renderPage()
    expect(
      screen.getByRole('heading', { level: 1, name: 'Sovereign Fleet' }),
    ).toBeInTheDocument()
  })

  it('exposes the breadcrumb with aria-label="Breadcrumb" for AT users', () => {
    renderPage()
    const crumb = screen.getByLabelText('Breadcrumb')
    expect(crumb.tagName.toLowerCase()).toBe('nav')
  })

  it('marks the current page in the trail with aria-current', () => {
    renderPage()
    const current = screen.getByText('Sovereign Fleet', { selector: 'li' })
    expect(current).toHaveAttribute('aria-current', 'page')
  })
})
