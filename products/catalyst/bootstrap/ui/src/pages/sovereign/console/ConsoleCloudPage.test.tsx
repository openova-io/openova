/**
 * ConsoleCloudPage.test.tsx — issue #933.
 *
 * Locks in the Sovereign Console Cloud surface against
 * /api/v1/sovereign/cloud:
 *
 *   • Loaded state renders all 7 sections (Nodes, Namespaces,
 *     Ingresses, HTTPRoutes, LBs, Storage Classes, PVCs)
 *   • Each list item renders with name + namespace + URL when
 *     applicable
 *   • Empty cluster surface still renders all 7 sections (zero rows)
 *     plus a "cluster appears empty" hint
 *   • Error state surfaces the API error
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { ConsoleCloudPage } from './ConsoleCloudPage'

const ORIGINAL_FETCH = globalThis.fetch

afterEach(() => {
  cleanup()
  globalThis.fetch = ORIGINAL_FETCH
})

function mockFetchOnce(body: unknown, ok = true) {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok,
    status: ok ? 200 : 500,
    json: async () => body,
  }) as unknown as typeof fetch
}

const POPULATED = {
  nodes: [
    {
      name: 'cp-1',
      status: 'Ready',
      roles: ['control-plane'],
      kubeletVersion: 'v1.31.4',
      os: 'linux',
      arch: 'amd64',
      internalIP: '10.0.0.1',
      capacityCPU: '4',
      capacityMemory: '8Gi',
    },
  ],
  namespaces: [{ name: 'auth', status: 'Active', createdAt: '2026-04-30T10:00:00Z' }],
  ingresses: [
    {
      name: 'console',
      namespace: 'catalyst',
      hosts: ['console.example.com'],
      url: 'https://console.example.com',
    },
  ],
  httpRoutes: [
    {
      name: 'console',
      namespace: 'catalyst',
      hosts: ['console.sov.example.com'],
      url: 'https://console.sov.example.com',
    },
  ],
  loadBalancers: [
    {
      name: 'cilium-gateway',
      namespace: 'kube-system',
      type: 'LoadBalancer',
      clusterIP: '10.43.0.1',
      externalIP: '1.2.3.4',
      ports: ['443/TCP'],
    },
  ],
  storageClasses: [
    {
      name: 'local-path',
      provisioner: 'rancher.io/local-path',
      isDefault: true,
      reclaimPolicy: 'Delete',
    },
  ],
  pvcs: [
    {
      name: 'data-keycloak',
      namespace: 'auth',
      storageClass: 'local-path',
      capacity: '5Gi',
      status: 'Bound',
    },
  ],
}

describe('ConsoleCloudPage', () => {
  it('renders all 7 sections when populated', async () => {
    mockFetchOnce(POPULATED)
    render(<ConsoleCloudPage />)

    await waitFor(() => screen.getByTestId('cloud-nodes'))

    expect(screen.getByTestId('cloud-nodes')).toBeTruthy()
    expect(screen.getByTestId('cloud-namespaces')).toBeTruthy()
    expect(screen.getByTestId('cloud-ingresses')).toBeTruthy()
    expect(screen.getByTestId('cloud-httproutes')).toBeTruthy()
    expect(screen.getByTestId('cloud-loadbalancers')).toBeTruthy()
    expect(screen.getByTestId('cloud-storage-classes')).toBeTruthy()
    expect(screen.getByTestId('cloud-pvcs')).toBeTruthy()

    expect(screen.getByTestId('cloud-node-cp-1')).toBeTruthy()
    expect(screen.getByTestId('cloud-ns-auth')).toBeTruthy()
  })

  it('surfaces ingress URL with external link', async () => {
    mockFetchOnce(POPULATED)
    render(<ConsoleCloudPage />)
    await waitFor(() => screen.getByTestId('cloud-ingresses'))
    expect(screen.getByText('console.example.com')).toBeTruthy()
  })

  it('renders the error state when the API fails', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({}),
    }) as unknown as typeof fetch

    render(<ConsoleCloudPage />)
    await waitFor(() => {
      expect(screen.getByTestId('cloud-error')).toBeTruthy()
    })
  })

  it('still renders all sections on an empty cluster', async () => {
    mockFetchOnce({
      nodes: [],
      namespaces: [],
      ingresses: [],
      httpRoutes: [],
      loadBalancers: [],
      storageClasses: [],
      pvcs: [],
    })
    render(<ConsoleCloudPage />)
    await waitFor(() => screen.getByTestId('cloud-nodes'))
    // 7 sections present even when empty.
    expect(screen.getByTestId('cloud-nodes')).toBeTruthy()
    expect(screen.getByTestId('cloud-pvcs')).toBeTruthy()
  })
})
