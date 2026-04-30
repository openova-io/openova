/**
 * InfrastructureStorage.test.tsx — render lock-in for the Storage tab.
 *
 * Coverage:
 *   1. Empty state.
 *   2. PVCs / Buckets / Volumes tables with counts.
 *   3. Bulk actions strip.
 *   4. Row-level Expand opens the ExpandPVCModal.
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
import { InfrastructureStorage } from './InfrastructureStorage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderStoragePage(data: HierarchicalInfrastructure) {
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
  const storageRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/storage',
    component: InfrastructureStorage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([storageRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/storage'],
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

describe('InfrastructureStorage — empty', () => {
  it('renders the empty state when no PVCs / buckets / volumes exist', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderStoragePage(empty)
    expect(await screen.findByTestId('infrastructure-storage-empty')).toBeTruthy()
  })
})

describe('InfrastructureStorage — populated', () => {
  it('renders PVC, bucket and volume tables with counts', async () => {
    renderStoragePage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-pvcs-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-buckets-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-volumes-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-pvcs-count').textContent).toBe('2')
    expect(screen.getByTestId('infrastructure-buckets-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-volumes-count').textContent).toBe('1')
  })

  it('renders the bulk-actions strip', async () => {
    renderStoragePage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-storage-bulk')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-storage-bulk-snapshot')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-storage-bulk-expand')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-storage-bulk-delete')).toBeTruthy()
  })

  it('opens ExpandPVCModal on row-level Expand', async () => {
    renderStoragePage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-pvc-row-pvc-postgres-data-expand'))
    expect(screen.getByTestId('infrastructure-modal-expand-pvc')).toBeTruthy()
  })
})
