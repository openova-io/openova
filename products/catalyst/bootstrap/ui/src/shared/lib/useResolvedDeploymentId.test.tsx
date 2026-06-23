/**
 * useResolvedDeploymentId.test.tsx — #4193 lock-in.
 *
 * A chrooted Sovereign has TWO ids for the same cluster:
 *   • the OpenTofu deployment-record id (baked into cloud-list row links
 *     as `/provision/<tofu-id>/…`, so it lands in `params.deploymentId`);
 *   • the console-internal id (returned by `/api/v1/sovereign/self`, the
 *     id the backend per-deployment stores are keyed on).
 *
 * Ground truth this test pins:
 *   • Sovereign host + a `/provision/<tofu-id>` URL param  → the
 *     `/sovereign/self` console id WINS (the tofu id is ignored), so the
 *     #3996 reconciler logs/reconcile/suspend calls hit the id the store
 *     is keyed on and stop 404ing.
 *   • Sovereign host + clean URL (no param)                → console id.
 *   • Mothership host + URL param                          → URL param
 *     wins (the operator is genuinely scoped to that deployment record;
 *     `/sovereign/self` 404s there).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { createElement } from 'react'

// --- mocks -----------------------------------------------------------

// `useParams` is read by the hook; we drive it per-test via this ref.
let mockParams: { deploymentId?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  useParams: () => mockParams,
}))

// `DETECTED_MODE.mode` decides the resolution order; driven per-test.
let mockMode: 'sovereign' | 'catalyst-zero' | 'mothership' = 'sovereign'
vi.mock('@/shared/lib/detectMode', () => ({
  get DETECTED_MODE() {
    return { mode: mockMode, sovereignFQDN: 'demo.omani.homes' }
  },
}))

// --- helpers ---------------------------------------------------------

const TOFU_ID = '4635277cae4ffed9' // deployment-record id (URL param)
const CONSOLE_ID = '7bb723da8da06047' // console-internal id (/sovereign/self)

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return createElement(QueryClientProvider, { client: qc }, children)
}

function mockSelf(status: number, body?: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({
      status,
      ok: status >= 200 && status < 300,
      json: async () => body,
    })),
  )
}

// Import AFTER the mocks are registered.
import { useResolvedDeploymentId } from './useResolvedDeploymentId'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

beforeEach(() => {
  mockParams = {}
  mockMode = 'sovereign'
})

describe('useResolvedDeploymentId — #4193 two-id resolution', () => {
  it('Sovereign host: /sovereign/self console id WINS over a /provision/<tofu-id> URL param', async () => {
    mockMode = 'sovereign'
    mockParams = { deploymentId: TOFU_ID }
    mockSelf(200, { deploymentId: CONSOLE_ID, sovereignFQDN: 'demo.omani.homes' })

    const { result } = renderHook(() => useResolvedDeploymentId(), { wrapper })

    await waitFor(() => expect(result.current.deploymentId).toBe(CONSOLE_ID))
    // NOT the tofu id that the cloud-list row link baked into the URL.
    expect(result.current.deploymentId).not.toBe(TOFU_ID)
  })

  it('Sovereign host: clean URL (no param) resolves to the console id', async () => {
    mockMode = 'sovereign'
    mockParams = {}
    mockSelf(200, { deploymentId: CONSOLE_ID, sovereignFQDN: 'demo.omani.homes' })

    const { result } = renderHook(() => useResolvedDeploymentId(), { wrapper })

    await waitFor(() => expect(result.current.deploymentId).toBe(CONSOLE_ID))
  })

  it('Sovereign host: falls back to the URL param until /sovereign/self lands', () => {
    mockMode = 'sovereign'
    mockParams = { deploymentId: TOFU_ID }
    // /sovereign/self never resolves in this synchronous read — the
    // provisional value is the URL param so the page is not blank.
    mockSelf(200, { deploymentId: CONSOLE_ID, sovereignFQDN: 'demo.omani.homes' })

    const { result } = renderHook(() => useResolvedDeploymentId(), { wrapper })
    // First synchronous render: query in flight, param is the fallback.
    expect(result.current.deploymentId).toBe(TOFU_ID)
  })

  it('Mothership host: URL param is authoritative (/sovereign/self 404s)', async () => {
    mockMode = 'mothership'
    mockParams = { deploymentId: TOFU_ID }
    mockSelf(404)

    const { result } = renderHook(() => useResolvedDeploymentId(), { wrapper })

    // Stable: the param wins; /sovereign/self is never consulted for the id.
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.deploymentId).toBe(TOFU_ID)
  })

  it('Sovereign host: /sovereign/self 404 falls back to the URL param', async () => {
    mockMode = 'sovereign'
    mockParams = { deploymentId: TOFU_ID }
    mockSelf(404)

    const { result } = renderHook(() => useResolvedDeploymentId(), { wrapper })

    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.deploymentId).toBe(TOFU_ID)
  })
})
