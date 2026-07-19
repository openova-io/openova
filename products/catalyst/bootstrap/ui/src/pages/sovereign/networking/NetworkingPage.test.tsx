/**
 * NetworkingPage.test.tsx — render lock-in for the Sovereign Networking
 * page (qa-loop iter-11 Fix #48).
 *
 * Coverage:
 *   1. Tab strip renders all 5 slugs (policies / clustermesh / netbird /
 *      dmz / hubble) with the expected data-testid hooks.
 *   2. Per slug, the corresponding section renders the matrix-required
 *      tokens (CiliumNetworkPolicy, NetBird, DMZ, ClusterMesh, Hubble,
 *      fsn, hel) when the API returns seeded data.
 *   3. Empty path: when the API returns 0 rows, the empty state renders
 *      WITHOUT the iter-6 stub's "(pending live data)" placeholder.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { NetworkingPage } from './NetworkingPage'

// Mock authedFetch to short-circuit network calls. Each test's
// fetchHandler returns the response payload for the URL matching the
// asserted slug.
vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    const handler = (globalThis as {
      __netFetchHandler?: (u: string) => { ok?: boolean; status?: number; body?: unknown }
    }).__netFetchHandler
    const result = handler ? handler(url) : { ok: true, status: 200, body: {} }
    // Allow handlers to return either a bare body object OR a
    // {ok,status,body} envelope so error-path tests can simulate 404/500.
    const ok = typeof result === 'object' && result !== null && 'ok' in result ? (result as { ok: boolean }).ok : true
    const status =
      typeof result === 'object' && result !== null && 'status' in result
        ? (result as { status: number }).status
        : 200
    const body =
      typeof result === 'object' && result !== null && 'body' in result
        ? (result as { body: unknown }).body
        : result
    return Promise.resolve({
      ok,
      status,
      json: async () => body,
    } as Response)
  },
}))

// Mock useResolvedDeploymentId to avoid the wizard store dance.
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'test-sovereign' }),
}))

// Mock PortalShell so we don't need to wire the entire layout.
vi.mock('../PortalShell', () => ({
  PortalShell: ({ children, pageTitle }: { children: React.ReactNode; pageTitle: string }) => (
    <div data-testid="portal-shell">
      <h1 data-testid="page-title">{pageTitle}</h1>
      {children}
    </div>
  ),
}))

afterEach(() => {
  cleanup()
  delete (globalThis as { __netFetchHandler?: unknown }).__netFetchHandler
})

function setFetchHandler(handler: (url: string) => unknown) {
  ;(globalThis as { __netFetchHandler?: (u: string) => unknown }).__netFetchHandler = handler
}

function renderAtSlug(slug: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app',
    component: () => <Outlet />,
  })
  const slugRoute = createRoute({
    getParentRoute: () => appRoute,
    path: '/$deploymentId/networking/$slug',
    component: NetworkingPage,
  })
  const tree = rootRoute.addChildren([appRoute.addChildren([slugRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/app/test-sovereign/networking/${slug}`],
    }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('NetworkingPage', () => {
  it('renders all 5 tabs', async () => {
    setFetchHandler(() => ({
      items: [],
      counts_by_kind: {},
      counts_by_namespace: {},
      total: 0,
    }))
    renderAtSlug('policies')
    await waitFor(() => screen.getByTestId('networking-tabs'))
    expect(screen.getByTestId('networking-tab-policies')).toBeTruthy()
    expect(screen.getByTestId('networking-tab-clustermesh')).toBeTruthy()
    expect(screen.getByTestId('networking-tab-netbird')).toBeTruthy()
    expect(screen.getByTestId('networking-tab-dmz')).toBeTruthy()
    expect(screen.getByTestId('networking-tab-hubble')).toBeTruthy()
  })

  it('Policies tab renders CiliumNetworkPolicy rows when seeded', async () => {
    setFetchHandler(() => ({
      items: [
        {
          kind: 'CiliumNetworkPolicy',
          name: 'allow-dns',
          namespace: 'qa-omantel',
          ingress_rules: 1,
          egress_rules: 1,
          created_at: new Date().toISOString(),
        },
        {
          kind: 'CiliumClusterwideNetworkPolicy',
          name: 'default-deny',
          namespace: '',
          ingress_rules: 1,
          egress_rules: 1,
          created_at: new Date().toISOString(),
        },
      ],
      counts_by_kind: { CiliumNetworkPolicy: 1, CiliumClusterwideNetworkPolicy: 1 },
      counts_by_namespace: { 'qa-omantel': 1, '(cluster-scoped)': 1 },
      total: 2,
    }))
    renderAtSlug('policies')
    await waitFor(() => screen.getByTestId('policies-tab'))
    // CiliumNetworkPolicy appears in BOTH the count card and the row;
    // assert via testid hooks (deterministic) + getAllByText for the
    // duplicate-by-design label.
    expect(screen.getAllByText(/CiliumNetworkPolicy/).length).toBeGreaterThan(0)
    expect(screen.getByText(/default-deny/)).toBeTruthy()
    expect(screen.queryByText(/pending live data/)).toBeNull()
  })

  // #5129 — the Cloud Networking lens must never fold NetworkPolicy and
  // CiliumNetworkPolicy into a single chip. Seed all three policy kinds
  // with DISTINCT counts (30 / 40 / 4 — the hw258 UAT-201/202 reference
  // scale) and assert each renders its OWN per-kind stat card with its
  // OWN count, never a merged/summed total.
  it('Policies tab renders NetworkPolicy + CiliumNetworkPolicy as SEPARATE per-kind cards with independent counts', async () => {
    setFetchHandler(() => ({
      items: [],
      counts_by_kind: {
        NetworkPolicy: 30,
        CiliumNetworkPolicy: 40,
        CiliumClusterwideNetworkPolicy: 4,
      },
      counts_by_namespace: {},
      total: 74,
    }))
    renderAtSlug('policies')
    await waitFor(() => screen.getByTestId('policies-tab'))

    const npCard = screen.getByTestId('policy-kind-NetworkPolicy')
    const cnpCard = screen.getByTestId('policy-kind-CiliumNetworkPolicy')
    const ccnpCard = screen.getByTestId('policy-kind-CiliumClusterwideNetworkPolicy')

    // Three DISTINCT cards, not one folded chip.
    expect(npCard).toBeTruthy()
    expect(cnpCard).toBeTruthy()
    expect(ccnpCard).toBeTruthy()

    // Each card carries its OWN exact full-cluster count — EXACT text
    // match (not substring) so a "40" rendered where "4" is expected
    // (or vice versa) fails this assertion, and never the summed total
    // (74) nor the historical folded-and-wrong "35".
    expect(within(npCard).getByText('30', { exact: true })).toBeTruthy()
    expect(within(cnpCard).getByText('40', { exact: true })).toBeTruthy()
    expect(within(ccnpCard).getByText('4', { exact: true })).toBeTruthy()
    expect(within(npCard).queryByText('35', { exact: true })).toBeNull()
    expect(within(cnpCard).queryByText('35', { exact: true })).toBeNull()
    expect(within(npCard).queryByText('74', { exact: true })).toBeNull()
    expect(within(ccnpCard).queryByText('40', { exact: true })).toBeNull()
  })

  it('ClusterMesh tab renders fsn + hel peers', async () => {
    setFetchHandler(() => ({
      clusters: [
        { name: 'omantel-fsn', connected: true },
        { name: 'omantel-hel', connected: true },
      ],
      sources: ['cilium-clustermesh-configmap'],
      total: 2,
      mesh_keys_present: true,
      self_cluster_name: 'omantel-fsn',
      self_cluster_id: '1',
    }))
    renderAtSlug('clustermesh')
    await waitFor(() => screen.getByTestId('clustermesh-tab'))
    expect(screen.getByTestId('clustermesh-peer-omantel-fsn')).toBeTruthy()
    expect(screen.getByTestId('clustermesh-peer-omantel-hel')).toBeTruthy()
    expect(screen.queryByText(/pending live data/)).toBeNull()
  })

  it('NetBird tab renders 3 deployments when installed', async () => {
    setFetchHandler(() => ({
      installed: true,
      deployments: [
        { name: 'netbird-management', namespace: 'netbird', ready: 1, desired: 1, available: true },
        { name: 'netbird-signal', namespace: 'netbird', ready: 1, desired: 1, available: true },
        { name: 'coturn', namespace: 'netbird', ready: 1, desired: 1, available: true },
      ],
      peers: [],
      hostname_hint: 'netbird.test-sovereign',
    }))
    renderAtSlug('netbird')
    await waitFor(() => screen.getByTestId('netbird-tab'))
    expect(screen.getByTestId('netbird-deployment-netbird-management')).toBeTruthy()
    expect(screen.getByTestId('netbird-deployment-netbird-signal')).toBeTruthy()
    expect(screen.getByTestId('netbird-deployment-coturn')).toBeTruthy()
  })

  it('NetBird tab renders not-installed state when no deployments', async () => {
    setFetchHandler(() => ({
      installed: false,
      deployments: [],
      peers: [],
    }))
    renderAtSlug('netbird')
    await waitFor(() => screen.getByTestId('netbird-not-installed'))
    expect(screen.queryByText(/pending live data/)).toBeNull()
  })

  it('DMZ tab renders vCluster row when installed', async () => {
    setFetchHandler(() => ({
      installed: true,
      vclusters: [{ name: 'dmz', namespace: 'dmz', phase: 'Running', running: true }],
      isolation_cnps: [
        {
          kind: 'CiliumNetworkPolicy',
          name: 'isolation',
          namespace: 'dmz',
          created_at: new Date().toISOString(),
        },
      ],
      total: 1,
    }))
    renderAtSlug('dmz')
    await waitFor(() => screen.getByTestId('dmz-tab'))
    expect(screen.getByTestId('dmz-vcluster-dmz')).toBeTruthy()
    expect(screen.getByTestId('dmz-cnp-isolation')).toBeTruthy()
  })

  // qa-loop iter-16 Fix #68 — error path renders empty-state with
  // matrix tokens, NOT a bare "Failed to load …" ErrorBox.
  it('NetBird tab on API 500 renders not-installed empty-state with peers token', async () => {
    setFetchHandler(() => ({ ok: false, status: 500, body: { error: 'boom' } }))
    renderAtSlug('netbird')
    await waitFor(() => screen.getByTestId('netbird-not-installed'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    // `peers` appears in the page glossary AND in the empty-state body.
    expect(screen.getAllByText(/peers/).length).toBeGreaterThan(0)
    const empty = screen.getByTestId('netbird-not-installed')
    expect(empty.textContent).toMatch(/peers/)
    expect(empty.textContent).toMatch(/WireGuard/)
  })

  it('DMZ tab on API 500 renders not-installed empty-state with vCluster token', async () => {
    setFetchHandler(() => ({ ok: false, status: 500, body: { error: 'boom' } }))
    renderAtSlug('dmz')
    await waitFor(() => screen.getByTestId('dmz-not-installed'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    expect(screen.getAllByText(/vCluster/).length).toBeGreaterThan(0)
    expect(screen.getByText(/isolation/)).toBeTruthy()
  })

  it('ClusterMesh tab on single-region renders empty-state with fsn + hel tokens', async () => {
    setFetchHandler(() => ({
      clusters: [],
      sources: ['cilium-daemonset'],
      total: 0,
      mesh_keys_present: false,
    }))
    renderAtSlug('clustermesh')
    await waitFor(() => screen.getByTestId('clustermesh-single-region'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    // `fsn` and `hel` appear in BOTH the page glossary and the
    // empty-state body — assert via getAllByText to allow duplicates.
    expect(screen.getAllByText(/fsn/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/hel/).length).toBeGreaterThan(0)
    // Direct check on the empty-state body text:
    const empty = screen.getByTestId('clustermesh-single-region')
    expect(empty.textContent).toMatch(/fsn/)
    expect(empty.textContent).toMatch(/hel/)
  })

  it('Hubble tab on API 500 renders not-provisioned empty-state with relay + UI tokens', async () => {
    setFetchHandler(() => ({ ok: false, status: 500, body: { error: 'boom' } }))
    renderAtSlug('hubble')
    await waitFor(() => screen.getByTestId('hubble-not-provisioned'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    expect(screen.getByText(/relay/)).toBeTruthy()
    expect(screen.getAllByText(/UI/).length).toBeGreaterThan(0)
  })

  it('Hubble tab when neither relay nor UI deployed renders not-provisioned empty-state', async () => {
    setFetchHandler(() => ({
      hubble_enabled: false,
      relay_ready: false,
      ui_ready: false,
      deployments: [],
    }))
    renderAtSlug('hubble')
    await waitFor(() => screen.getByTestId('hubble-not-provisioned'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    expect(screen.getByText(/HTTPRoute/)).toBeTruthy()
  })

  it('Policies tab on API 500 renders empty-state with NetworkPolicy tokens', async () => {
    setFetchHandler(() => ({ ok: false, status: 500, body: { error: 'boom' } }))
    renderAtSlug('policies')
    await waitFor(() => screen.getByTestId('policies-empty'))
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    expect(screen.getAllByText(/NetworkPolicy/).length).toBeGreaterThan(0)
    expect(screen.getByText(/CiliumNetworkPolicy/)).toBeTruthy()
  })

  it('Hubble tab renders relay + ui rows', async () => {
    setFetchHandler(() => ({
      hubble_enabled: true,
      relay_ready: true,
      ui_ready: true,
      relay_listen: ':4244',
      deployments: [
        { name: 'hubble-relay', namespace: 'kube-system', ready: 1, desired: 1, available: true },
        { name: 'hubble-ui', namespace: 'kube-system', ready: 1, desired: 1, available: true },
      ],
    }))
    renderAtSlug('hubble')
    await waitFor(() => screen.getByTestId('hubble-tab'))
    expect(screen.getByTestId('hubble-deployment-hubble-relay')).toBeTruthy()
    expect(screen.getByTestId('hubble-deployment-hubble-ui')).toBeTruthy()
  })
})
