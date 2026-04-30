/**
 * InfrastructureStorage.test.tsx — render lock-in for the Storage tab.
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

import { InfrastructureStorage } from './InfrastructureStorage'
import type { StorageResponse } from '@/lib/infrastructure.types'

function renderStorage(data: StorageResponse | undefined) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/infrastructure/storage',
    component: () => <InfrastructureStorage initialDataOverride={data} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/infrastructure/storage'],
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
    renderStorage({ pvcs: [], buckets: [], volumes: [] })
    expect(await screen.findByTestId('infrastructure-storage-empty')).toBeTruthy()
  })
})

describe('InfrastructureStorage — populated', () => {
  const sample: StorageResponse = {
    pvcs: [
      {
        id: 'p1',
        name: 'cnpg-pgdata',
        namespace: 'cnpg-system',
        capacity: '20Gi',
        used: '4Gi',
        storageClass: 'local-path',
        status: 'healthy',
      },
    ],
    buckets: [
      {
        id: 'b1',
        name: 'observability',
        endpoint: 's3.openova.io',
        capacity: '100Gi',
        used: '12Gi',
        retentionDays: '30',
      },
    ],
    volumes: [
      {
        id: 'v1',
        name: 'pgdata-vol',
        capacity: '50Gi',
        region: 'fsn1',
        attachedTo: 'node-cp-fsn1',
        status: 'healthy',
      },
    ],
  }

  it('renders PVC, bucket and volume cards', async () => {
    renderStorage(sample)
    expect(await screen.findByTestId('infrastructure-pvc-card-p1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-bucket-card-b1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-volume-card-v1')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-pvcs-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-buckets-count').textContent).toBe('1')
    expect(screen.getByTestId('infrastructure-volumes-count').textContent).toBe('1')
  })
})
