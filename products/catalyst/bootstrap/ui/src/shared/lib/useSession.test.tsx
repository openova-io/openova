/**
 * useSession.test.tsx — coverage for the /whoami React Query hook
 * (issue #689).
 *
 * The hook backs the ProfileMenu and the StepReview anonymous-launch
 * branch; the contract under test is:
 *   - 200 → signedIn=true, email/sub populated
 *   - 401 → signedIn=false, email=null
 *   - signOut() invalidates the cache and clears local state
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useSession } from './useSession'

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

describe('useSession', () => {
  it('returns signedIn=true with email when /whoami returns 200', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({ email: 'owner@example.com', sub: 'u-123', verified: true }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useSession(), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.signedIn).toBe(true)
    expect(result.current.email).toBe('owner@example.com')
    expect(result.current.sub).toBe('u-123')
  })

  it('returns signedIn=false when /whoami returns 401', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 }),
    )
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useSession(), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.signedIn).toBe(false)
    expect(result.current.email).toBeNull()
  })

  it('signOut() DELETEs /auth/session and clears the cached identity', async () => {
    let signedOut = false
    const fetchSpy = vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        if (signedOut) {
          // After sign-out the cookie has been cleared server-side, so
          // a follow-up /whoami returns 401. Mirror that behaviour.
          return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
        }
        return new Response(
          JSON.stringify({ email: 'owner@example.com', sub: 'u-123', verified: true }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      if (url.endsWith('/v1/auth/session')) {
        signedOut = true
        return new Response(null, { status: 204 })
      }
      return new Response(null, { status: 404 })
    })

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useSession(), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.signedIn).toBe(true))

    await act(async () => {
      await result.current.signOut()
    })

    // The DELETE was issued.
    const calls = fetchSpy.mock.calls.map((c) =>
      typeof c[0] === 'string' ? c[0] : (c[0] as URL).toString(),
    )
    expect(calls.some((u) => u.endsWith('/v1/auth/session'))).toBe(true)

    // After signOut the cached query is null → signedIn flips to false.
    await waitFor(() => expect(result.current.signedIn).toBe(false))
  })
})
