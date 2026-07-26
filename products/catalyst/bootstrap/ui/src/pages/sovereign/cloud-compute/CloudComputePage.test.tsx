/**
 * CloudComputePage.test.tsx — landing page for /cloud/compute (P3 of
 * #309). Asserts that the four tiles render with counts derived from
 * the fixture topology.
 */

import { describe, it, expect, afterEach, beforeEach } from 'vitest'
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

import { CloudPage } from '../CloudPage'
import { CloudComputePage } from './CloudComputePage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderLanding(data: HierarchicalInfrastructure) {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch

  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <CloudPage disableStream initialDataOverride={data} deploymentsOverride={[]} />,
  })
  const computeRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/compute',
    component: CloudComputePage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([computeRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute'],
    }),
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

beforeEach(() => {
  if (typeof window !== 'undefined') window.localStorage.clear()
})
afterEach(() => cleanup())

describe('CloudComputePage', () => {
  it('renders 4 tiles (clusters / vclusters / node-pools / worker-nodes)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-compute-page-tile-clusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-vclusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-node-pools')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-worker-nodes')).toBeTruthy()
  })

  // The worker-nodes tile counts WORKER nodes only, never control planes
  // (#4739, CloudComputePage.tsx `role !== 'control-plane'` filter). The
  // fixture carries 6 nodes total but only 4 with role 'worker' —
  // eu-w-0 / eu-w-1 / eu-w-2 (infrastructure-topology.fixture.ts) plus
  // hel-w-0 — the other 2 (eu-cp-0, hel-cp-0) are role 'control-plane'.
  // This assertion previously expected 6, i.e. it pinned the exact
  // over-count #4739 fixed.
  it('counts derive from the fixture (2 clusters, 4 vclusters, 3 node-pools, 4 worker nodes)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect((await screen.findByTestId('cloud-compute-page-tile-clusters-count')).textContent).toBe('2')
    expect(screen.getByTestId('cloud-compute-page-tile-vclusters-count').textContent).toBe('4')
    expect(screen.getByTestId('cloud-compute-page-tile-node-pools-count').textContent).toBe('3')
    expect(screen.getByTestId('cloud-compute-page-tile-worker-nodes-count').textContent).toBe('4')
  })

  it('excludes control-plane nodes from the worker-nodes tile (#4739)', async () => {
    renderLanding(infrastructureTopologyFixture)
    const allNodes = (infrastructureTopologyFixture.topology.regions ?? [])
      .flatMap((r) => r.clusters ?? [])
      .flatMap((c) => c.nodes ?? [])
    const workers = allNodes.filter((n) => n.role !== 'control-plane')
    expect(allNodes.length).toBe(6)
    expect(workers.length).toBe(4)
    expect(
      (await screen.findByTestId('cloud-compute-page-tile-worker-nodes-count')).textContent,
    ).toBe(String(workers.length))
  })

  it('each tile is a Link to the per-resource list page', async () => {
    renderLanding(infrastructureTopologyFixture)
    const clustersLink = (await screen.findByTestId('cloud-compute-page-tile-clusters')) as HTMLAnchorElement
    expect(clustersLink.tagName).toBe('A')
    expect(clustersLink.getAttribute('href') ?? '').toMatch(/\/cloud\/compute\/clusters$/)
  })
})
