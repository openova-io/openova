/**
 * networking.api.test.ts — URL-shape lock-in for the Networking REST
 * client wrappers (qa-loop iter-15 Fix #61).
 *
 * This test exists because the iter-11 implementation accidentally
 * double-prefixed `/api` (built `${API_BASE}/api/v1/...` while
 * `API_BASE` already ends in `/api`). The result was a 404 on every
 * Networking page request, the TanStack Query landed in error state,
 * the page rendered the ErrorBox, and the iter-15 PW assertions for
 * tokens like `fsn`, `hel`, `NetBird`, `vCluster`, and `peers` all
 * missed because the data path never resolved.
 *
 * The fix mirrors the URL scheme used by every other admin/sovereign
 * page (compliance.api.ts, userAccess.api.ts, AppsPage.tsx) which is
 * `${API_BASE}/v1/...`. This test asserts the wire URL by capturing
 * the argument passed to authedFetch.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const capturedURLs: string[] = []

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    capturedURLs.push(url)
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({}),
    } as Response)
  },
}))

beforeEach(() => {
  capturedURLs.length = 0
})

afterEach(() => {
  capturedURLs.length = 0
})

describe('networking.api URL builder', () => {
  it('builds /api/v1/sovereigns/.../networking/policies (single /api prefix)', async () => {
    const { getNetworkingPolicies } = await import('./networking.api')
    await getNetworkingPolicies('sovereign-omantel.biz')
    expect(capturedURLs).toHaveLength(1)
    // Hard guard against the iter-11 regression: the URL must contain
    // exactly ONE `/api/` segment, not `/api/api/`.
    expect(capturedURLs[0]).not.toMatch(/\/api\/api\//)
    expect(capturedURLs[0]).toMatch(
      /\/v1\/sovereigns\/sovereign-omantel\.biz\/networking\/policies$/,
    )
  })

  it('builds the same shape for clustermesh, netbird, dmz, hubble', async () => {
    const {
      getNetworkingClusterMesh,
      getNetworkingNetBird,
      getNetworkingDMZ,
      getNetworkingHubble,
    } = await import('./networking.api')
    await getNetworkingClusterMesh('sov-x')
    await getNetworkingNetBird('sov-x')
    await getNetworkingDMZ('sov-x')
    await getNetworkingHubble('sov-x')
    expect(capturedURLs).toHaveLength(4)
    for (const u of capturedURLs) {
      expect(u).not.toMatch(/\/api\/api\//)
      expect(u).toMatch(/\/v1\/sovereigns\/sov-x\/networking\/[a-z]+$/)
    }
  })

  it('encodes sovereignId path segment', async () => {
    const { getNetworkingPolicies } = await import('./networking.api')
    await getNetworkingPolicies('sov/with/slashes')
    expect(capturedURLs[0]).toContain('sov%2Fwith%2Fslashes')
  })
})
