/**
 * ClustersPage.test.tsx — list-page lock-in for /cloud/compute/clusters.
 */

import { describe, it, expect, afterEach, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
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
import { ClustersPage } from './ClustersPage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderClusters(data: HierarchicalInfrastructure) {
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
    component: () => <Outlet />,
  })
  const clustersRoute = createRoute({
    getParentRoute: () => computeRoute,
    path: '/clusters',
    component: ClustersPage,
  })
  const tree = rootRoute.addChildren([
    cloudRoute.addChildren([computeRoute.addChildren([clustersRoute])]),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute/clusters'],
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

describe('ClustersPage', () => {
  it('renders header + count badge + back link', async () => {
    renderClusters(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-clusters-page')).toBeTruthy()
    expect(screen.getByTestId('cloud-clusters-title').textContent).toContain('Clusters')
    expect(screen.getByTestId('cloud-clusters-count').textContent).toBe('2')
    expect(screen.getByTestId('cloud-clusters-back')).toBeTruthy()
  })

  it('renders 2 cluster rows from the fixture', async () => {
    renderClusters(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-clusters-row-cluster-eu-central-primary')).toBeTruthy()
    expect(screen.getByTestId('cloud-clusters-row-cluster-eu-helsinki-secondary')).toBeTruthy()
  })

  it('clicking a row opens the detail drawer', async () => {
    renderClusters(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-clusters-row-cluster-eu-central-primary'))
    expect(screen.getByTestId('cloud-clusters-detail')).toBeTruthy()
    expect(screen.getByTestId('cloud-clusters-detail-body').textContent).toContain('omantel-primary')
  })

  it('clicking the close button closes the detail drawer', async () => {
    renderClusters(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-clusters-row-cluster-eu-central-primary'))
    expect(screen.getByTestId('cloud-clusters-detail')).toBeTruthy()
    fireEvent.click(screen.getByTestId('cloud-clusters-detail-close'))
    expect(screen.queryByTestId('cloud-clusters-detail')).toBeNull()
  })

  it('search filters rows', async () => {
    renderClusters(infrastructureTopologyFixture)
    const search = (await screen.findByTestId('cloud-clusters-search')) as HTMLInputElement
    fireEvent.change(search, { target: { value: 'helsinki' } })
    expect(screen.queryByTestId('cloud-clusters-row-cluster-eu-central-primary')).toBeNull()
    expect(screen.getByTestId('cloud-clusters-row-cluster-eu-helsinki-secondary')).toBeTruthy()
  })

  it('empty data renders the empty state', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderClusters(empty)
    expect(await screen.findByTestId('cloud-clusters-empty')).toBeTruthy()
  })
})
