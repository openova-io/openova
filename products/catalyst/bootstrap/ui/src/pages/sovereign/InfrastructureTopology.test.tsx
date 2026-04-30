/**
 * InfrastructureTopology.test.tsx — render lock-in for the Topology
 * canvas (default Infrastructure tab, issue #227).
 *
 * Coverage:
 *   1. Empty state shows when the API returns no nodes.
 *   2. With a small synthetic graph, the SVG canvas mounts and node
 *      groups render with the expected data-testid pattern.
 *   3. Clicking a node opens the right-rail detail panel.
 *   4. Closing the panel removes it from the DOM.
 */

import { describe, it, expect, afterEach } from 'vitest'
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

import { InfrastructureTopology } from './InfrastructureTopology'
import type { TopologyResponse } from '@/lib/infrastructure.types'

function renderTopology(data: TopologyResponse | undefined) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/infrastructure/topology',
    component: () => <InfrastructureTopology initialDataOverride={data} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/infrastructure/topology'],
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

describe('InfrastructureTopology — empty state', () => {
  it('renders the Provisioning… overlay when no nodes are returned', async () => {
    renderTopology({ nodes: [], edges: [] })
    expect(await screen.findByTestId('infrastructure-topology-empty')).toBeTruthy()
  })
})

describe('InfrastructureTopology — populated', () => {
  const sample: TopologyResponse = {
    nodes: [
      {
        id: 'cloud',
        kind: 'cloud',
        label: 'Hetzner',
        status: 'healthy',
        metadata: { provider: 'hetzner' },
      },
      {
        id: 'cluster',
        kind: 'cluster',
        label: 'omantel.omani.works',
        status: 'healthy',
        metadata: { fqdn: 'omantel.omani.works' },
      },
      {
        id: 'node-1',
        kind: 'node',
        label: 'worker-1',
        status: 'degraded',
        metadata: { role: 'worker' },
      },
    ],
    edges: [
      { from: 'cloud', to: 'cluster', relation: 'contains' },
      { from: 'cluster', to: 'node-1', relation: 'contains' },
    ],
  }

  it('renders the SVG canvas with node groups', async () => {
    renderTopology(sample)
    expect(await screen.findByTestId('infrastructure-topology-svg')).toBeTruthy()
    expect(screen.getByTestId('infra-node-cloud')).toBeTruthy()
    expect(screen.getByTestId('infra-node-cluster')).toBeTruthy()
    expect(screen.getByTestId('infra-node-node-1')).toBeTruthy()
  })

  it('renders edges between known nodes', async () => {
    renderTopology(sample)
    await screen.findByTestId('infrastructure-topology-svg')
    expect(screen.getByTestId('infra-edge-cloud-cluster')).toBeTruthy()
    expect(screen.getByTestId('infra-edge-cluster-node-1')).toBeTruthy()
  })

  it('opens the detail panel on node click and closes it on dismiss', async () => {
    renderTopology(sample)
    const node = await screen.findByTestId('infra-node-cluster')
    expect(screen.queryByTestId('infrastructure-topology-detail')).toBeNull()
    fireEvent.click(node)
    expect(screen.getByTestId('infrastructure-topology-detail')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-topology-detail-name').textContent).toBe('omantel.omani.works')
    fireEvent.click(screen.getByTestId('infrastructure-topology-detail-close'))
    expect(screen.queryByTestId('infrastructure-topology-detail')).toBeNull()
  })

  it('paints node status into a data-status attribute', async () => {
    renderTopology(sample)
    const degraded = await screen.findByTestId('infra-node-node-1')
    expect(degraded.getAttribute('data-status')).toBe('degraded')
  })
})
