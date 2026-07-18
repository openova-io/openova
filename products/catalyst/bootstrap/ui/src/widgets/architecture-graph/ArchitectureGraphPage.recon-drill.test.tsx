/**
 * ArchitectureGraphPage.recon-drill.test.tsx — the row-193 (issue #5223)
 * integration lock-in: on the unified Cloud graph, CLICKING a reconciler
 * node opens the ReconcilerDrillPanel (the #3996 drill re-wired), not the
 * generic infrastructure DetailPanel and not nothing.
 *
 * This is the exact regression the hw255 walk caught: the Reconciliation
 * lens rendered its nodes but a click produced NO drill-in.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// The drill panel resolves coordinates + logs through the #3996 client —
// mock the wire, keep everything else real.
const listSpy = vi.fn(() =>
  Promise.resolve({
    reconcilers: [
      {
        kind: 'HelmRelease',
        name: 'bp-keycloak',
        namespace: 'flux-system',
        state: 'Reconciled' as const,
        message: 'Helm install succeeded',
        revision: '1.2.3',
        suspended: false,
        lastReconcile: '2026-07-19T00:00:00Z',
        controller: 'helm-controller',
      },
    ],
    reconciled: 1,
    total: 1,
  }),
)
const logsSpy = vi.fn(() =>
  Promise.resolve({
    controller: 'helm-controller',
    object: 'flux-system/bp-keycloak',
    lines: [{ lineNumber: 1, message: 'reconciling flux-system/bp-keycloak' }],
    total: 1,
  }),
)
vi.mock('@/lib/reconciler-manage.api', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/lib/reconciler-manage.api')>()
  return {
    ...orig,
    fetchReconcilers: () => listSpy(),
    fetchReconcilerLogs: () => logsSpy(),
  }
})

import { ArchitectureGraphPage } from './ArchitectureGraphPage'
import type { ReconciliationNode } from '@/lib/reconciliation.api'

const RECONCILERS: ReconciliationNode[] = [
  {
    id: 'bp-keycloak',
    label: 'bp-keycloak',
    kind: 'HelmRelease',
    state: 'Reconciled',
  },
]

function renderGraphWithReconciler() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ArchitectureGraphPage
        deploymentId="dep-test-1"
        data={null}
        isLoading={false}
        isError={false}
        onRefetch={() => {}}
        k8sSnapshot={new Map()}
        k8sRevision={0}
        reconcilers={RECONCILERS}
      />
    </QueryClientProvider>,
  )
}

afterEach(() => {
  cleanup()
  listSpy.mockClear()
  logsSpy.mockClear()
})

describe('ArchitectureGraphPage — reconciler-node drill (row 193)', () => {
  it('clicking a reconciler node opens the ReconcilerDrillPanel wired to the logs endpoint', async () => {
    renderGraphWithReconciler()

    // Switch to the Reconciliation lens so the control-category chip set
    // (incl. HelmRelease) is active.
    fireEvent.change(screen.getByTestId('cloud-architecture-lens'), {
      target: { value: 'reconciliation' },
    })

    const node = await screen.findByTestId('arch-graph-node-HelmRelease-recon:bp-keycloak')
    expect(screen.queryByTestId('reconciler-drill-panel')).toBeNull()

    fireEvent.click(node)

    // The drill panel opens (NOT the generic infrastructure panel)…
    expect(screen.getByTestId('reconciler-drill-panel')).toBeTruthy()
    expect(screen.queryByTestId('infrastructure-detail-panel')).toBeNull()

    // …and the logs drill actually fires against the #3996 endpoint.
    await waitFor(() => expect(logsSpy).toHaveBeenCalled())
    const line = await screen.findByTestId('reconciler-drill-log-line-1')
    expect(line.textContent).toContain('reconciling flux-system/bp-keycloak')

    // Close restores the no-panel state.
    fireEvent.click(screen.getByTestId('reconciler-drill-close'))
    expect(screen.queryByTestId('reconciler-drill-panel')).toBeNull()
  })
})
