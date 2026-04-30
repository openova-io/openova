/**
 * InfrastructureNetwork.test.tsx — render lock-in for the Network tab.
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

import { InfrastructureNetwork } from './InfrastructureNetwork'
import type { NetworkResponse } from '@/lib/infrastructure.types'

function renderNetwork(data: NetworkResponse | undefined) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/infrastructure/network',
    component: () => <InfrastructureNetwork initialDataOverride={data} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/infrastructure/network'],
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

describe('InfrastructureNetwork — empty', () => {
  it('renders the empty state when no LBs / DRGs / peerings exist', async () => {
    renderNetwork({ loadBalancers: [], drgs: [], peerings: [] })
    expect(await screen.findByTestId('infrastructure-network-empty')).toBeTruthy()
  })
})

describe('InfrastructureNetwork — populated', () => {
  const sample: NetworkResponse = {
    loadBalancers: [
      {
        id: 'lb1',
        name: 'ingress-lb',
        publicIP: '203.0.113.10',
        ports: '80,443,6443',
        targetHealth: '3/3 healthy',
        region: 'fsn1',
        status: 'healthy',
      },
    ],
    drgs: [
      {
        id: 'drg1',
        name: 'sovereign-vpc',
        cidr: '10.0.0.0/16',
        region: 'fsn1',
        peers: 'metro-vpc',
        status: 'healthy',
      },
    ],
    peerings: [
      {
        id: 'p1',
        name: 'sovereign↔metro',
        vpcPair: 'sovereign-vpc <-> metro-vpc',
        subnets: '10.0.0.0/24,10.1.0.0/24',
        status: 'healthy',
      },
    ],
  }

  it('renders LB / DRG / peering cards', async () => {
    renderNetwork(sample)
    expect(await screen.findByTestId('infrastructure-lb-card-lb1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-drg-card-drg1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-peering-card-p1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-lbs-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-drgs-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-peerings-count').textContent).toBe('1')
  })
})
