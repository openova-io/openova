/**
 * InfrastructureNetwork.test.tsx — render lock-in for the Network tab.
 *
 * Coverage:
 *   1. Empty state.
 *   2. LB / Peering / Firewall tables with counts.
 *   3. Bulk-actions strip.
 *   4. Per-region Add LB triggers AddLBModal.
 *   5. Add Peering button opens AddPeeringModal.
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

import { CloudPage } from './CloudPage'
import { InfrastructureNetwork } from './InfrastructureNetwork'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderNetworkPage(data: HierarchicalInfrastructure) {
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
  const networkRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/network',
    component: InfrastructureNetwork,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([networkRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/network'],
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
  it('renders the empty state when no LBs / peerings / firewalls exist', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderNetworkPage(empty)
    expect(await screen.findByTestId('infrastructure-network-empty')).toBeTruthy()
  })
})

describe('InfrastructureNetwork — populated', () => {
  it('renders LB / Peering / Firewall tables with counts', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-lbs-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-peerings-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-firewalls-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-lbs-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-peerings-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-firewalls-count').textContent).toBe('1')
  })

  it('renders the bulk-actions strip', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-network-bulk')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-network-add-peering')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-network-edit-dns')).toBeTruthy()
  })

  it('opens AddPeeringModal when Add Peering is clicked', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-network-add-peering'))
    expect(screen.getByTestId('infrastructure-modal-add-peering')).toBeTruthy()
  })

  it('opens AddLBModal when per-region Add LB is clicked', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-network-add-lb-region-eu-central'))
    expect(screen.getByTestId('infrastructure-modal-add-lb')).toBeTruthy()
  })

  it('opens EditFirewallRulesModal from row-level edit', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-firewall-row-fw-eu-central-edit'))
    expect(screen.getByTestId('infrastructure-modal-edit-firewall-rules')).toBeTruthy()
  })
})
