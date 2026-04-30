/**
 * CloudNetwork.test.tsx — render lock-in for the Network tab.
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
import { CloudNetwork } from './CloudNetwork'
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
    component: CloudNetwork,
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

describe('CloudNetwork — empty', () => {
  it('renders the empty state when no LBs / peerings / firewalls exist', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderNetworkPage(empty)
    expect(await screen.findByTestId('cloud-network-empty')).toBeTruthy()
  })
})

describe('CloudNetwork — populated', () => {
  it('renders LB / Peering / Firewall tables with counts', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-lbs-table')).toBeTruthy()
    expect(screen.getByTestId('cloud-peerings-table')).toBeTruthy()
    expect(screen.getByTestId('cloud-firewalls-table')).toBeTruthy()
    expect(screen.getByTestId('cloud-lbs-count').textContent).toBe('1')
    expect(screen.getByTestId('cloud-peerings-count').textContent).toBe('1')
    expect(screen.getByTestId('cloud-firewalls-count').textContent).toBe('1')
  })

  it('renders the bulk-actions strip', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-network-bulk')).toBeTruthy()
    expect(screen.getByTestId('cloud-network-add-peering')).toBeTruthy()
    expect(screen.getByTestId('cloud-network-edit-dns')).toBeTruthy()
  })

  it('opens AddPeeringModal when Add Peering is clicked', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-network-add-peering'))
    expect(screen.getByTestId('infrastructure-modal-add-peering')).toBeTruthy()
  })

  it('opens AddLBModal when per-region Add LB is clicked', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-network-add-lb-region-eu-central'))
    expect(screen.getByTestId('infrastructure-modal-add-lb')).toBeTruthy()
  })

  it('opens EditFirewallRulesModal from row-level edit', async () => {
    renderNetworkPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-firewall-row-fw-eu-central-edit'))
    expect(screen.getByTestId('infrastructure-modal-edit-firewall-rules')).toBeTruthy()
  })
})
