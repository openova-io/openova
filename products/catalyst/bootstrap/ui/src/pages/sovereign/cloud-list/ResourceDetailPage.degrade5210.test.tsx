/**
 * ResourceDetailPage.degrade5210.test.tsx — #5210 graceful-degrade lock-in.
 *
 * When the parent resource-GET (the k9s lens `/k8s/{kind}/{ns}/{name}`)
 * fails on a Sovereign — e.g. a transient apiserver auth blip after cutover
 * returns 500 Unauthorized — the ResourceDetailPage previously hid the WHOLE
 * tab body behind the parent-GET error, which also hid the Flux reconciler's
 * own working management surface (logs + reconcile / suspend / resume). This
 * asserts the reconcile tab still renders (degraded, obj=null) alongside the
 * error banner so the operator can still drive the reconciler (UAT 193/195).
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const getResource = vi.fn()
const getResourceTree = vi.fn()

vi.mock('./resource.api', async () => {
  const actual = await vi.importActual<typeof import('./resource.api')>('./resource.api')
  return {
    ...actual,
    getResource: (...a: unknown[]) => getResource(...a),
    getResourceTree: (...a: unknown[]) => getResourceTree(...a),
  }
})

const fetchReconcilerLogs = vi.fn()

vi.mock('@/lib/reconciler-manage.api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/reconciler-manage.api')>(
    '@/lib/reconciler-manage.api',
  )
  return {
    ...actual,
    fetchReconcilerLogs: (...a: unknown[]) => fetchReconcilerLogs(...a),
  }
})

import { ResourceDetailPage } from './ResourceDetailPage'

afterEach(cleanup)

beforeEach(() => {
  getResource.mockReset()
  getResourceTree.mockReset()
  fetchReconcilerLogs.mockReset()
  // The lens parent-GET fails the way the Sovereign reported it (#5210).
  getResource.mockRejectedValue(new Error('Unauthorized'))
  getResourceTree.mockResolvedValue({ kind: 'HelmRelease', name: 'bp-keycloak', children: [] })
  fetchReconcilerLogs.mockResolvedValue({
    controller: 'helm-controller',
    object: 'flux-system/bp-keycloak',
    lines: [{ lineNumber: 1, message: 'reconciling' }],
    total: 1,
  })
})

function renderPage(tab: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ResourceDetailPage
        deploymentId="dep-1"
        basePath="/cloud"
        kind="helmrelease"
        ns="flux-system"
        name="bp-keycloak"
        tab={tab as never}
      />
    </QueryClientProvider>,
  )
}

describe('ResourceDetailPage — #5210 degrade', () => {
  it('keeps the reconcile management surface when the parent resource-GET fails', async () => {
    renderPage('reconcile')

    // The error banner surfaces (operator sees the lens GET failed) …
    await waitFor(() => expect(screen.getByTestId('resource-detail-error')).toBeTruthy())
    // … AND the reconcile tab body still renders (degraded, obj=null) so
    // reconcile / suspend / resume + logs remain usable.
    expect(screen.getByTestId('resource-detail-tab-content-reconcile')).toBeTruthy()
    expect(screen.getByTestId('reconcile-tab')).toBeTruthy()
  })

  it('still hides non-reconciler tab bodies on a failed parent-GET', async () => {
    renderPage('overview')

    await waitFor(() => expect(screen.getByTestId('resource-detail-error')).toBeTruthy())
    // Overview has no independent endpoint — it stays gated behind the error.
    expect(screen.queryByTestId('resource-detail-tab-content-overview')).toBeNull()
  })
})
