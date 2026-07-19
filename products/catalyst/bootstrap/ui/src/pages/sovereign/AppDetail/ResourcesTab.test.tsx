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
// Per-kind canned list payloads keyed by the singular kind segment in
// the URL (`/k8s/pod?...`); defaults to an empty list for kinds the
// test doesn't seed.
let listResponses: Record<string, unknown[]> = {}

function kindFromUrl(url: string): string {
  const m = url.match(/\/k8s\/([^/?]+)\?/)
  return m ? m[1] : ''
}

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    fetchCalls.push(url)
    const items = listResponses[kindFromUrl(url)] ?? []
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({ items }),
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
  listResponses = {}
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
          namespace="acme/legal"
        />,
      ),
    )
    await waitFor(() => {
      expect(fetchCalls.length).toBeGreaterThan(0)
    })
    for (const url of fetchCalls) {
      expect(url).toContain('namespace=acme%2Flegal')
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

describe('ResourcesTab — mgmt-vCluster de-mangled display (#3939 / #3642)', () => {
  it('renders the de-mangled displayName but the drill-down href keeps host coords', async () => {
    // The #3642 migration moved gitea INTO the per-tier `mgmt` vCluster.
    // The loft syncer mirrors the in-vCluster pod down to host ns `mgmt`
    // with a mangled name; the catalyst-api flatten step surfaces the
    // de-mangled identity as top-level `displayName`/`vclusterNamespace`
    // while `metadata.{name,namespace}` stay the host coordinates.
    listResponses = {
      pod: [
        {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: {
            name: 'gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster',
            namespace: 'mgmt',
          },
          status: { phase: 'Running' },
          displayName: 'gitea-75d9f486fb-g8hsr',
          vclusterNamespace: 'gitea',
        },
      ],
    }

    const { findByTestId } = render(
      withProviders(
        <ResourcesTab
          applicationName="gitea"
          sovereignId="test-sov"
          namespace="gitea"
        />,
      ),
    )

    // The row keys off the HOST coordinates (mgmt + mangled name).
    const row = await findByTestId(
      'app-detail-resource-row-pod-mgmt-gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster',
    )

    // DISPLAY: the clean in-vCluster name, NOT the mangled host name.
    const link = row.querySelector('a') as HTMLAnchorElement
    expect(link.textContent).toBe('gitea-75d9f486fb-g8hsr')
    expect(link.textContent).not.toContain('-x-gitea-x-mgmt-vcluster')
    // Namespace cell shows the de-mangled vcluster namespace.
    expect(row.textContent).toContain('gitea')

    // DRILL-DOWN: the resource-tree href + the host-coord data attrs MUST
    // carry the HOST name/namespace so they resolve against the host
    // apiserver (the mothership holds only the host kubeconfig).
    const href = link.getAttribute('href') ?? ''
    expect(href).toContain('gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster')
    expect(href).toContain('/resource/pods/mgmt/')
    expect(link.getAttribute('data-host-name')).toBe(
      'gitea-75d9f486fb-g8hsr-x-gitea-x-mgmt-vcluster',
    )
    expect(link.getAttribute('data-host-namespace')).toBe('mgmt')
  })

  it('falls back to host metadata for pre-#3642 (non-synced) rows', async () => {
    // No displayName/vclusterNamespace on the wire → unchanged rendering.
    listResponses = {
      pod: [
        {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: { name: 'alloy-0', namespace: 'alloy' },
          status: { phase: 'Running' },
        },
      ],
    }

    const { findByTestId } = render(
      withProviders(
        <ResourcesTab
          applicationName="alloy"
          sovereignId="test-sov"
          namespace="alloy"
        />,
      ),
    )

    const row = await findByTestId('app-detail-resource-row-pod-alloy-alloy-0')
    const link = row.querySelector('a') as HTMLAnchorElement
    expect(link.textContent).toBe('alloy-0')
    expect(link.getAttribute('href')).toContain('/resource/pods/alloy/alloy-0/')
    expect(link.getAttribute('data-host-name')).toBe('alloy-0')
  })
})
