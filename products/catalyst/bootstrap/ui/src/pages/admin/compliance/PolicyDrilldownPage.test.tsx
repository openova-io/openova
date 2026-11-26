/**
 * PolicyDrilldownPage.test.tsx — the per-policy page asks the API for
 * the violations of its one policy, shows the mode from the policy
 * list, and offers no control that changes the mode.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
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

const calls = vi.hoisted(() => ({ urls: [] as string[] }))

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'alpha', isLoading: false }),
}))

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: async (url: string) => {
    calls.urls.push(url)
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (url.includes('/compliance/policies')) {
      return json({
        count: 1,
        items: [{ name: 'probes-present', weight: 1, scope: 'all', mode: 'enforcing', violations: 1, source: 'kyverno' }],
      })
    }
    if (url.includes('/compliance/violations')) {
      return json({
        total: 1,
        offset: 0,
        limit: 500,
        items: [
          {
            resource: 'Deployment/acme-prod/billing-api',
            namespace: 'acme-prod',
            policy: 'probes-present',
            result: 'fail',
            environment: 'acme-prod',
            time: '2026-05-09T00:00:00Z',
          },
        ],
      })
    }
    return new Response('{}', { status: 404, headers: { 'Content-Type': 'application/json' } })
  },
}))

import { PolicyDrilldownPage } from './PolicyDrilldownPage'

beforeEach(() => {
  calls.urls = []
})

afterEach(() => {
  cleanup()
})

function renderPage() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const page = createRoute({
    getParentRoute: () => rootRoute,
    path: '/admin/compliance/policy/$policyName',
    component: () => <PolicyDrilldownPage disableStream />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([page]),
    history: createMemoryHistory({ initialEntries: ['/admin/compliance/policy/probes-present'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('PolicyDrilldownPage', () => {
  it('asks for the violations of this policy only', async () => {
    renderPage()
    await screen.findByTestId('policy-drilldown-row-0')
    const violationCalls = calls.urls.filter((u) => u.includes('/compliance/violations'))
    expect(violationCalls.length).toBeGreaterThan(0)
    for (const u of violationCalls) {
      expect(new URL(u, 'http://x').searchParams.get('policy')).toBe('probes-present')
    }
  })

  it('shows the mode from the policy list, read-only', async () => {
    renderPage()
    const mode = await screen.findByTestId('policy-drilldown-mode')
    expect(mode.textContent).toContain('Enforce')
    expect((await screen.findByTestId('policy-drilldown-mode-note')).textContent).toBe(
      'Mode is changed from the Policies screen',
    )
    expect(screen.queryByTestId('policy-mode-toggle-button-probes-present')).toBeNull()
    expect(screen.queryByRole('button', { name: /enforce|audit/i })).toBeNull()
  })
})
