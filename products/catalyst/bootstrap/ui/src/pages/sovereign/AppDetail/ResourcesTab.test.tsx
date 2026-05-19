/**
 * ResourcesTab.test.tsx — TBD-V2 (issue #1928) regression lock.
 *
 * Founder report (2026-05-19): Application detail Resources tab empty
 * because the SPA built `?namespace=default` into every kind list URL
 * regardless of where the workload actually installed (e.g. `harbor`,
 * `alloy`, `cert-manager`). Proof: `?namespace=default` returned 163
 * bytes (empty), `?namespace=harbor` returned 66272 bytes (real data).
 *
 * This test asserts the URL the ResourcesTab fires contains the
 * `namespace` prop value verbatim, so a future refactor of the URL
 * builder cannot silently re-introduce the hardcoded default.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const fetchCalls: string[] = []

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    fetchCalls.push(url)
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({ items: [] }),
    } as Response)
  },
}))

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'test-sov' }),
}))

vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign' },
}))

vi.mock('@/shared/config/urls', () => ({
  API_BASE: '/api',
}))

import { ResourcesTab } from './ResourcesTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => {
  cleanup()
  fetchCalls.length = 0
})

describe('ResourcesTab — namespace plumbing (TBD-V2 #1928)', () => {
  it('builds list URL with the supplied namespace (not "default")', async () => {
    render(
      withProviders(
        <ResourcesTab
          applicationName="alloy"
          sovereignId="test-sov"
          namespace="harbor"
        />,
      ),
    )
    await waitFor(() => {
      expect(fetchCalls.length).toBeGreaterThan(0)
    })
    // Every fired list URL must carry namespace=harbor, NOT namespace=default.
    for (const url of fetchCalls) {
      expect(url).toContain('namespace=harbor')
      expect(url).not.toMatch(/[?&]namespace=default(&|$)/)
    }
  })

  it('encodes the namespace correctly when it contains special chars', async () => {
    render(
      withProviders(
        <ResourcesTab
          applicationName="legal-co"
          sovereignId="test-sov"
          namespace="tenant/legal"
        />,
      ),
    )
    await waitFor(() => {
      expect(fetchCalls.length).toBeGreaterThan(0)
    })
    for (const url of fetchCalls) {
      expect(url).toContain('namespace=tenant%2Flegal')
    }
  })

  it('passes the labelSelector prop verbatim through to the URL', async () => {
    render(
      withProviders(
        <ResourcesTab
          applicationName="alloy"
          sovereignId="test-sov"
          namespace="alloy"
          labelSelector="app.kubernetes.io/name=alloy"
        />,
      ),
    )
    await waitFor(() => {
      expect(fetchCalls.length).toBeGreaterThan(0)
    })
    for (const url of fetchCalls) {
      // labelSelector URL-encoded by URLSearchParams style:
      // app.kubernetes.io%2Fname%3Dalloy
      expect(url).toContain('labelSelector=app.kubernetes.io%2Fname%3Dalloy')
    }
  })

  it('disableNetwork seam suppresses all list calls', () => {
    render(
      withProviders(
        <ResourcesTab
          applicationName="alloy"
          sovereignId="test-sov"
          namespace="alloy"
          disableNetwork
        />,
      ),
    )
    expect(fetchCalls.length).toBe(0)
  })
})
