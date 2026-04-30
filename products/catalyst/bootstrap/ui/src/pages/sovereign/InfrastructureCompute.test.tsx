/**
 * InfrastructureCompute.test.tsx — render lock-in for the Compute tab.
 *
 * Coverage:
 *   1. Empty state shows when the API returns no clusters / nodes.
 *   2. Cluster + node sections render their counts + cards.
 */

import { describe, it, expect, afterEach } from 'vitest'
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

import { InfrastructureCompute } from './InfrastructureCompute'
import type { ComputeResponse } from '@/lib/infrastructure.types'

function renderCompute(data: ComputeResponse | undefined) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/infrastructure/compute',
    component: () => <InfrastructureCompute initialDataOverride={data} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/infrastructure/compute'],
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

afterEach(() => cleanup())

describe('InfrastructureCompute — empty', () => {
  it('renders the empty state when there are no clusters or nodes', async () => {
    renderCompute({ clusters: [], nodes: [] })
    expect(await screen.findByTestId('infrastructure-compute-empty')).toBeTruthy()
  })
})

describe('InfrastructureCompute — populated', () => {
  const sample: ComputeResponse = {
    clusters: [
      {
        id: 'c1',
        name: 'omantel.omani.works',
        controlPlane: 'k3s',
        version: 'v1.30',
        region: 'fsn1',
        nodeCount: 3,
        status: 'healthy',
      },
    ],
    nodes: [
      {
        id: 'n-cp',
        name: 'control-plane',
        sku: 'cpx21',
        region: 'fsn1',
        role: 'control-plane',
        ip: '5.6.7.8',
        status: 'healthy',
      },
      {
        id: 'n-w-1',
        name: 'worker-1',
        sku: 'cpx41',
        region: 'fsn1',
        role: 'worker',
        ip: '',
        status: 'unknown',
      },
    ],
  }

  it('renders cluster cards', async () => {
    renderCompute(sample)
    expect(await screen.findByTestId('infrastructure-cluster-card-c1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-clusters-count').textContent).toBe('1')
  })

  it('renders node cards', async () => {
    renderCompute(sample)
    expect(await screen.findByTestId('infrastructure-node-card-n-cp')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-node-card-n-w-1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-nodes-count').textContent).toBe('2')
  })
})
