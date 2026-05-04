/**
 * useInflightDeployment.test.tsx — coverage for the deployments-list
 * React Query hook (issue #747).
 *
 * Contract under test:
 *   - inflight bucket picks up deployments in any of the live phases
 *     (provisioning, phase1-watching, ready-but-not-adopted, ...) and
 *     returns the most-recent by startedAt.
 *   - completed bucket holds everything else (failed, wiped, etc.).
 *   - adopted deployments are filtered server-side and never appear
 *     (the hook trusts the API and does not re-filter).
 *   - 401 / network errors degrade gracefully to empty buckets so the
 *     wizard renders rather than locking the operator out.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useInflightDeployment } from './useInflightDeployment'

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useInflightDeployment', () => {
  it('returns the most-recent in-flight deployment from a mix of statuses', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          deployments: [
            {
              id: 'old-failed',
              status: 'failed',
              startedAt: '2026-04-30T12:00:00Z',
              ownerEmail: 'alice@example.com',
            },
            {
              id: 'fresh-watching',
              status: 'phase1-watching',
              startedAt: '2026-05-04T12:00:00Z',
              ownerEmail: 'alice@example.com',
            },
            {
              id: 'older-watching',
              status: 'phase1-watching',
              startedAt: '2026-05-01T12:00:00Z',
              ownerEmail: 'alice@example.com',
            },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => useInflightDeployment({ ownerEmail: 'alice@example.com', enabled: true }),
      { wrapper: wrapper(qc) },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inflight?.id).toBe('fresh-watching')
    expect(result.current.completed.map((d) => d.id).sort()).toEqual([
      'old-failed',
      'older-watching',
    ])
    expect(result.current.all).toHaveLength(3)
  })

  it('returns null inflight when only failed/wiped deployments exist', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          deployments: [
            { id: 'a', status: 'failed', startedAt: '2026-05-01T00:00:00Z', ownerEmail: 'a@x.com' },
            { id: 'b', status: 'wiped', startedAt: '2026-05-02T00:00:00Z', ownerEmail: 'a@x.com' },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => useInflightDeployment({ ownerEmail: 'a@x.com', enabled: true }),
      { wrapper: wrapper(qc) },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inflight).toBeNull()
    expect(result.current.completed).toHaveLength(2)
  })

  it('treats ready-but-not-adopted as in-flight (handover not yet fired)', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          deployments: [
            {
              id: 'ready-not-adopted',
              status: 'ready',
              startedAt: '2026-05-04T10:00:00Z',
              ownerEmail: 'a@x.com',
              adoptedAt: null,
            },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => useInflightDeployment({ ownerEmail: 'a@x.com', enabled: true }),
      { wrapper: wrapper(qc) },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inflight?.id).toBe('ready-not-adopted')
  })

  it('skips deployments with adoptedAt set even if status is in-flight', async () => {
    // Defense-in-depth: even if the server forgets to filter (test
    // bypass, race in the SlimForHandover write), the client must
    // not redirect to an adopted deployment because the customer
    // owns the cluster now.
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          deployments: [
            {
              id: 'adopted',
              status: 'ready',
              startedAt: '2026-05-04T00:00:00Z',
              ownerEmail: 'a@x.com',
              adoptedAt: '2026-05-04T01:00:00Z',
            },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => useInflightDeployment({ ownerEmail: 'a@x.com', enabled: true }),
      { wrapper: wrapper(qc) },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inflight).toBeNull()
  })

  it('returns empty buckets on 401 (anonymous)', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response('', { status: 401 }),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(
      () => useInflightDeployment({ ownerEmail: null, enabled: true }),
      { wrapper: wrapper(qc) },
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inflight).toBeNull()
    expect(result.current.completed).toHaveLength(0)
    expect(result.current.all).toHaveLength(0)
  })

  it('does not fetch when enabled=false (session still loading)', () => {
    const fetchSpy = vi.spyOn(global, 'fetch')
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    renderHook(
      () => useInflightDeployment({ ownerEmail: 'a@x.com', enabled: false }),
      { wrapper: wrapper(qc) },
    )
    expect(fetchSpy).not.toHaveBeenCalled()
  })
})
