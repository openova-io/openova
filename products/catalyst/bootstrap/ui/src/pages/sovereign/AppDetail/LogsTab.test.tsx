/**
 * LogsTab.test.tsx — #3939 (#3642 sibling) regression lock.
 *
 * After the #3642 host->mgmt-vCluster migration the loft-sh syncer
 * mirrors each in-vCluster pod down to host ns `mgmt` with a MANGLED
 * name. The catalyst-api k8s-list surface now hands the Pod row back
 * with `metadata.{name,namespace}` = HOST coordinates plus a top-level
 * de-mangled `displayName`.
 *
 * The Logs tab must:
 *   1. SHOW the de-mangled `displayName` in the Pod picker.
 *   2. Stream the log against the HOST coordinates (host pod name +
 *      host namespace `mgmt`), NOT the app namespace (`gitea`) — the
 *      mothership holds only the host kubeconfig.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const fetchCalls: string[] = []
let podItems: unknown[] = []

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    fetchCalls.push(url)
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({ items: podItems }),
    } as Response)
  },
}))

vi.mock('@/shared/lib/oidc', () => ({
  loadTokens: async () => ({ accessToken: 'tok' }),
}))

vi.mock('@/shared/config/urls', () => ({
  API_BASE: '/api',
}))

import { LogsTab } from './LogsTab'

// Capture every WebSocket URL the component opens.
const wsUrls: string[] = []
class MockWebSocket {
  static OPEN = 1
  static CLOSED = 3
  readyState = 0
  binaryType = ''
  onopen: (() => void) | null = null
  onmessage: ((ev: unknown) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  constructor(url: string) {
    wsUrls.push(url)
  }
  close() {
    this.readyState = MockWebSocket.CLOSED
  }
}

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

beforeEach(() => {
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket)
})

afterEach(() => {
  cleanup()
  fetchCalls.length = 0
  wsUrls.length = 0
  podItems = []
  vi.unstubAllGlobals()
})

describe('LogsTab — mgmt-vCluster de-mangled picker + host-coord stream (#3939 / #3642)', () => {
  it('shows the de-mangled pod name but streams against the host namespace + host pod name', async () => {
    podItems = [
      {
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: {
          // HOST coordinates: synced into host ns `mgmt` with a mangled
          // name.
          name: 'gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster',
          namespace: 'mgmt',
        },
        spec: { containers: [{ name: 'gitea' }] },
        displayName: 'gitea-75d9f486fb-g8hsr',
        vclusterNamespace: 'gitea',
      },
    ]

    const { findByTestId } = render(
      withProviders(
        <LogsTab
          applicationName="gitea"
          sovereignId="test-sov"
          // App namespace — deliberately DIFFERENT from the pod's host ns.
          namespace="gitea"
          blueprint="bp-gitea"
        />,
      ),
    )

    // Picker LABEL shows the de-mangled in-vCluster name.
    const picker = (await findByTestId('app-logs-pod-picker')) as HTMLSelectElement
    await waitFor(() => {
      expect(picker.options.length).toBeGreaterThan(0)
    })
    const optionLabels = Array.from(picker.options).map((o) => o.textContent)
    expect(optionLabels).toContain('gitea-75d9f486fb-g8hsr')
    expect(optionLabels.join('')).not.toContain('-x-gitea-x-mgmt-vcluster')

    // The <option> VALUE stays the host pod name (drives the WS URL).
    const optionValues = Array.from(picker.options).map((o) => o.value)
    expect(optionValues).toContain(
      'gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster',
    )

    // The log STREAM resolves against the HOST namespace (`mgmt`) + the
    // host pod name — NOT the app namespace (`gitea`).
    await waitFor(() => {
      expect(wsUrls.length).toBeGreaterThan(0)
    })
    const streamUrl = wsUrls[wsUrls.length - 1]
    expect(streamUrl).toContain(
      '/k8s/logs/mgmt/gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster/gitea',
    )
    // Must NOT stream against the app namespace.
    expect(streamUrl).not.toContain('/k8s/logs/gitea/')
  })

  it('falls back to the app namespace for pre-#3642 (non-synced) pods', async () => {
    podItems = [
      {
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: { name: 'alloy-0', namespace: 'alloy' },
        spec: { containers: [{ name: 'alloy' }] },
      },
    ]

    render(
      withProviders(
        <LogsTab applicationName="alloy" sovereignId="test-sov" namespace="alloy" />,
      ),
    )

    await waitFor(() => {
      expect(wsUrls.length).toBeGreaterThan(0)
    })
    expect(wsUrls[wsUrls.length - 1]).toContain('/k8s/logs/alloy/alloy-0/alloy')
  })
})
