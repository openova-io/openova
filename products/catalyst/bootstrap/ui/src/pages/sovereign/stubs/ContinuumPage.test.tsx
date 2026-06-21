/**
 * ContinuumPage.test.tsx — lock-in for the live DR wiring (#3375 / #1101).
 *
 * The page was an explicit STUB; #3969 moved DR onto it but it was never
 * data-wired. These tests assert the REAL widgets now render from a
 * mocked continuum.api response, AND that the honest empty-state shows
 * when no Continuum CR exists (the backend 404 path) — NOT a crash, NOT
 * a fabricated green.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { ContinuumPage } from './ContinuumPage'
import * as continuumApi from '@/lib/continuum.api'

// useSession + useResolvedDeploymentId hit the network — stub them so the
// page renders deterministically with a known tier + deployment id.
vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => ({
    signedIn: true,
    email: 'owner@acme.io',
    sub: 'u-1',
    tier: 'owner',
    roles: ['catalyst-owner'],
    loading: false,
    refetch: vi.fn(),
    signOut: vi.fn(),
  }),
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-1', isLoading: false }),
}))

function renderPage(mode: 'list' | 'overview' | 'audit' | 'settings', path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path:
      mode === 'list'
        ? '/app/$deploymentId/continuum'
        : mode === 'overview'
          ? '/app/$deploymentId/continuum/$continuumId'
          : mode === 'audit'
            ? '/app/$deploymentId/continuum/$continuumId/audit'
            : '/app/$deploymentId/continuum/$continuumId/settings',
    component: () => <ContinuumPage mode={mode} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ContinuumPage — overview mode (live CR)', () => {
  beforeEach(() => {
    vi.spyOn(continuumApi, 'getContinuum').mockResolvedValue({
      name: 'dr-wp',
      namespace: 'acme',
      uid: 'u-cr-1',
      spec: {
        primaryRegion: 'hz-fsn-rtz-prod',
        hotStandbyRegions: ['hz-hel-rtz-prod'],
      },
      status: {
        phase: 'Healthy',
        leaseHolder: 'hz-fsn-rtz-prod',
        replicationLagSeconds: 8,
        primaryRegion: 'hz-fsn-rtz-prod',
        replicaRegion: 'hz-hel-rtz-prod',
      },
    })
  })

  it('renders StatusPanel from a mocked continuum.api response', async () => {
    renderPage('overview', '/app/dep-1/continuum/dr-wp')
    // The StatusPanel renders the live phase pill + green lag bucket.
    expect(await screen.findByTestId('continuum-status-panel')).toBeTruthy()
    expect(screen.getByTestId('continuum-status-phase-Healthy')).toBeTruthy()
    expect(screen.getByTestId('continuum-status-lag-bucket-green')).toBeTruthy()
    // Hot-standby region badge derived from spec.hotStandbyRegions.
    expect(screen.getByTestId('continuum-status-hotstandby-hz-hel-rtz-prod')).toBeTruthy()
  })
})

describe('ContinuumPage — overview mode (no CR → calm empty-state)', () => {
  beforeEach(() => {
    // The backend 404s when there is no live 2-region pair; getContinuum
    // throws "continuum get: HTTP 404".
    vi.spyOn(continuumApi, 'getContinuum').mockRejectedValue(
      new Error('continuum get: HTTP 404'),
    )
  })

  it('renders the calm "no DR pair yet" placeholder, not a crash or fake-green', async () => {
    renderPage('overview', '/app/dep-1/continuum/dr-wp')
    expect(await screen.findByTestId('continuum-overview-no-cr')).toBeTruthy()
    // The StatusPanel must NOT render a fabricated green status.
    expect(screen.queryByTestId('continuum-status-panel')).toBeNull()
  })
})

describe('ContinuumPage — overview mode (genuine error)', () => {
  beforeEach(() => {
    vi.spyOn(continuumApi, 'getContinuum').mockRejectedValue(
      new Error('continuum get: HTTP 500'),
    )
  })

  it('surfaces a non-404 error honestly (not as the empty-state)', async () => {
    renderPage('overview', '/app/dep-1/continuum/dr-wp')
    expect(await screen.findByTestId('continuum-overview-error')).toBeTruthy()
    expect(screen.queryByTestId('continuum-overview-no-cr')).toBeNull()
  })
})

describe('ContinuumPage — list (fleet) mode', () => {
  it('renders a row per fleet item linking to the overview', async () => {
    vi.spyOn(continuumApi, 'listFleetContinuums').mockResolvedValue({
      items: [
        {
          sovereign: 'dep-1',
          name: 'cont-omantel',
          namespace: 'qa-omantel',
          primaryRegion: 'fsn1',
          currentPrimary: 'fsn1',
          phase: 'Healthy',
          walLagSeconds: 2,
          healthy: true,
        },
      ],
      total: 1,
    })
    renderPage('list', '/app/dep-1/continuum')
    expect(await screen.findByTestId('continuum-list-row-0')).toBeTruthy()
    expect(screen.getByTestId('continuum-list-row-0').textContent).toContain('cont-omantel')
  })

  it('renders a calm empty-state when the fleet is empty', async () => {
    vi.spyOn(continuumApi, 'listFleetContinuums').mockResolvedValue({ items: [], total: 0 })
    renderPage('list', '/app/dep-1/continuum')
    expect(await screen.findByTestId('continuum-list-empty')).toBeTruthy()
  })
})

describe('ContinuumPage — audit mode', () => {
  it('renders SwitchoverHistory from the audit api', async () => {
    vi.spyOn(continuumApi, 'listContinuumAudit').mockResolvedValue({
      items: [
        {
          auditType: 'continuum-switchover',
          ts: '2026-05-08T14:23:11Z',
          actor: 'owner@acme.io',
          detail: 'switchover requested: hz-fsn-rtz-prod → hz-hel-rtz-prod',
          result: 'ok',
        },
      ],
      total: 1,
    })
    renderPage('audit', '/app/dep-1/continuum/dr-wp/audit')
    // SwitchoverHistory renders its table (not the empty placeholder).
    expect(await screen.findByTestId('continuum-history')).toBeTruthy()
  })
})

describe('ContinuumPage — settings mode (read-only RPO/RTO)', () => {
  it('renders the read-only policy summary from spec', async () => {
    vi.spyOn(continuumApi, 'getContinuum').mockResolvedValue({
      name: 'dr-wp',
      namespace: 'acme',
      uid: 'u-cr-1',
      spec: { rpoSeconds: 30, rtoSeconds: 60, autoFailover: true },
      status: {},
    })
    renderPage('settings', '/app/dep-1/continuum/dr-wp/settings')
    expect(await screen.findByTestId('continuum-settings-rpo')).toBeTruthy()
    expect(screen.getByTestId('continuum-settings-rpo').textContent).toBe('30')
    expect(screen.getByTestId('continuum-settings-rto').textContent).toBe('60')
    expect(screen.getByTestId('continuum-settings-autofailover').textContent).toBe('enabled')
  })
})
