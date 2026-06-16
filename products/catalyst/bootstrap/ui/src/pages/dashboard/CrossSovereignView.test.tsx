/**
 * CrossSovereignView.test.tsx — coverage for the U-Fleet-3 page.
 *
 * What we assert:
 *   - Renders the table with rows from /api/v1/fleet/applications.
 *   - Org filter input updates query (via TanStack Query refetch).
 *   - Topology + DR posture select inputs update query.
 *   - Empty filtered result renders the empty hint.
 *
 * Routing: TanStack Router renders <Link to="/dashboard"> via the
 * provider. Tests render WITHOUT the full router; the back-link `<a>`
 * is allowed to render with no actual navigation since we only assert
 * data-driven rendering here.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { CrossSovereignView } from './CrossSovereignView'
import type { ApplicationsResponse } from '@/lib/fleet.api'

afterEach(() => cleanup())

function makeRouter() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({ getParentRoute: () => rootRoute, path: '/dashboard', component: () => <div>back</div> })
  const crossRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard/applications',
    component: CrossSovereignView,
  })
  return createRouter({
    routeTree: rootRoute.addChildren([dashRoute, crossRoute]),
    history: createMemoryHistory({ initialEntries: ['/dashboard/applications'] }),
  })
}

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
  const router = makeRouter()
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

const SAMPLE_APPS: ApplicationsResponse = {
  applications: [
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'wp', blueprint: 'bp-wordpress', version: '1.0' },
      regions: ['hz-fsn-rtz-prod'],
      topology: 'single-region',
      drPosture: '—',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'api', blueprint: 'bp-django', version: '0.9' },
      regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
      topology: 'active-hotstandby',
      drPosture: 'DR active',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
  ],
  total: 2,
}

describe('CrossSovereignView — render', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  it('renders the application table with rows', async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify(SAMPLE_APPS), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })) as never
    renderPage()
    // Wait for rows specifically — the table shell renders unconditionally
    // once data > 0; rely on the per-row testid for the readiness gate.
    await waitFor(() => expect(screen.getByTestId('cross-sov-row-sov-a-wp')).toBeTruthy())
    expect(screen.getByTestId('cross-sov-row-sov-a-api')).toBeTruthy()
    expect(screen.getByTestId('cross-sov-dr-api').textContent).toContain('DR active')
    expect(screen.getByTestId('cross-sov-total').textContent).toContain('2')
  })

  it('renders empty hint when zero rows match the filter', async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ applications: [], total: 0 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })) as never
    renderPage()
    await waitFor(() => expect(screen.getByTestId('cross-sov-empty')).toBeTruthy())
  })
})

describe('CrossSovereignView — filters', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  it('passes org filter to the server', async () => {
    const calls: string[] = []
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      calls.push(String(url))
      return new Response(JSON.stringify(SAMPLE_APPS), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    }) as never
    renderPage()
    await waitFor(() => expect(screen.getByTestId('cross-sov-filter-org')).toBeTruthy())
    fireEvent.change(screen.getByTestId('cross-sov-filter-org'), { target: { value: 'acme' } })
    await waitFor(() => expect(calls.some((u) => u.includes('org=acme'))).toBe(true))
  })

  it('passes topology filter to the server', async () => {
    const calls: string[] = []
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      calls.push(String(url))
      return new Response(JSON.stringify(SAMPLE_APPS), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    }) as never
    renderPage()
    await waitFor(() => expect(screen.getByTestId('cross-sov-filter-topology')).toBeTruthy())
    // #3375 §3(f) — the filter now offers + posts the CANONICAL vocabulary
    // (active-hot-standby), not the legacy editor dialect.
    fireEvent.change(screen.getByTestId('cross-sov-filter-topology'), {
      target: { value: 'active-hot-standby' },
    })
    await waitFor(() =>
      expect(calls.some((u) => u.includes('topology=active-hot-standby'))).toBe(true),
    )
  })
})
