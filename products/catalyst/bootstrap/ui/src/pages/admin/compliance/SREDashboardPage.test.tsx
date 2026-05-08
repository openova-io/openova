/**
 * SREDashboardPage.test.tsx — smoke tests for the SRE Lead compliance
 * dashboard surface (slice U1, #1096).
 *
 * What we assert:
 *   1. Empty state when no scorecard data — renders the canonical
 *      empty-state copy.
 *   2. Populated state — sovereign-score pill renders, treemap
 *      surface mounts.
 *   3. Filter chips populate from the scorecard's app organizations.
 *   4. SecLead variant renders title + security palette legend.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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
import { SREDashboardPage } from './SREDashboardPage'
import { SecLeadDashboardPage } from './SecLeadDashboardPage'
import type { ScorecardResponse, Score } from './compliance.api'

class FakeEventSource {
  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  closed = false
  constructor(url: string) {
    this.url = url
  }
  close = () => {
    this.closed = true
  }
}

beforeEach(() => {
  // @ts-expect-error — install fake on the global
  globalThis.EventSource = FakeEventSource
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class FakeResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof FakeResizeObserver }).ResizeObserver =
      FakeResizeObserver
  }
})

afterEach(() => {
  cleanup()
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

function renderRoute(routePath: string, ui: React.ReactElement) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const pageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: routePath,
    component: () => ui,
  })
  // Stub the U4 drilldown so onLeafClick navigation has a target.
  const drilldown = createRoute({
    getParentRoute: () => rootRoute,
    path: '/admin/compliance/policy/$policyName',
    component: () => <div data-testid="drilldown-target" />,
  })
  const tree = rootRoute.addChildren([pageRoute, drilldown])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [routePath] }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

const NOW = '2026-05-09T00:00:00Z'

function scorecard(apps: Score[]): ScorecardResponse {
  return {
    sovereign: {
      scope: 'sovereign',
      id: 'alpha',
      total: 78,
      numerator: 780,
      denominator: 1000,
      updatedAt: NOW,
    },
    organizations: [],
    environments: [],
    applications: apps,
    generatedAt: NOW,
  }
}

function appScore(id: string, total: number | null, org = 'acme'): Score {
  return {
    scope: 'application',
    id,
    applicationRef: id,
    organizationRef: org,
    environmentRef: `${org}-prod`,
    total,
    numerator: total ?? 0,
    denominator: 100,
    updatedAt: NOW,
  }
}

describe('SREDashboardPage', () => {
  it('renders empty state when scorecard is empty', async () => {
    renderRoute(
      '/admin/compliance/sre',
      <SREDashboardPage disableStream initialDataOverride={scorecard([])} />,
    )
    expect(await screen.findByTestId('compliance-empty')).toBeTruthy()
  })

  it('renders sovereign-score pill with the rollup total', async () => {
    renderRoute(
      '/admin/compliance/sre',
      <SREDashboardPage disableStream initialDataOverride={scorecard([appScore('billing', 80)])} />,
    )
    const pill = await screen.findByTestId('compliance-sovereign-score-value')
    expect(pill.textContent).toContain('78')
  })

  it('renders the treemap surface when applications are populated', async () => {
    renderRoute(
      '/admin/compliance/sre',
      <SREDashboardPage
        disableStream
        initialDataOverride={scorecard([appScore('billing', 80), appScore('orders', 60)])}
      />,
    )
    expect(await screen.findByTestId('compliance-treemap-surface')).toBeTruthy()
  })

  it('populates organization filter chip from app scorecard', async () => {
    renderRoute(
      '/admin/compliance/sre',
      <SREDashboardPage
        disableStream
        initialDataOverride={scorecard([
          appScore('billing', 80, 'acme'),
          appScore('forum', 70, 'beta'),
        ])}
      />,
    )
    const select = (await screen.findByTestId('compliance-filter-org')) as HTMLSelectElement
    const options = Array.from(select.options).map((o) => o.value)
    expect(options).toContain('acme')
    expect(options).toContain('beta')
  })

  it('renders title from the page', async () => {
    renderRoute(
      '/admin/compliance/sre',
      <SREDashboardPage disableStream initialDataOverride={scorecard([])} />,
    )
    expect((await screen.findByTestId('compliance-dashboard-title')).textContent).toContain('SRE Lead')
  })
})

describe('SecLeadDashboardPage', () => {
  it('renders the Security-Lead title', async () => {
    renderRoute(
      '/admin/compliance/security',
      <SecLeadDashboardPage disableStream initialDataOverride={scorecard([])} />,
    )
    expect((await screen.findByTestId('compliance-dashboard-title')).textContent).toContain('Security Lead')
  })

  it('renders security-palette legend ("High risk" → "Hardened")', async () => {
    renderRoute(
      '/admin/compliance/security',
      <SecLeadDashboardPage disableStream initialDataOverride={scorecard([])} />,
    )
    const legend = await screen.findByTestId('compliance-legend')
    expect(legend.textContent).toContain('High risk')
    expect(legend.textContent).toContain('Hardened')
  })
})
