/**
 * ReconcilerDrillPanel.test.tsx — regression lock-in for UAT row 193
 * (issue #5223): clicking a reconciler node on the unified Cloud graph
 * must open a DRILL-IN wired to the #3996 reconciler-management surface
 * (GET .../reconcilers + GET .../reconcilers/{kind}/{ns}/{name}/logs).
 *
 * Before this fix the Reconciliation lens rendered 89 reconciler nodes but
 * a click opened only the generic infrastructure panel — the drill+logs
 * shipped on the since-deleted /reconciliation page and was never re-wired.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// ── Mock the management API (list + logs + action) ────────────────────
const listSpy = vi.fn((...listArgs: unknown[]) => {
  void listArgs
  return Promise.resolve({
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
  })
})
const logsSpy = vi.fn((...logsArgs: unknown[]) => {
  void logsArgs
  return Promise.resolve({
    controller: 'helm-controller',
    object: 'flux-system/bp-keycloak',
    lines: [
      { lineNumber: 1, message: 'reconciling HelmRelease flux-system/bp-keycloak' },
      { lineNumber: 2, message: 'release bp-keycloak upgraded' },
    ],
    total: 2,
  })
})
const actionSpy = vi.fn((...actionArgs: unknown[]) => {
  void actionArgs
  return Promise.resolve({
    kind: 'HelmRelease',
    namespace: 'flux-system',
    name: 'bp-keycloak',
    action: 'reconcile' as const,
    requestedAt: '2026-07-19T01:00:00Z',
    requestedBy: 'operator@test',
  })
})
vi.mock('@/lib/reconciler-manage.api', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/lib/reconciler-manage.api')>()
  return {
    ...orig,
    fetchReconcilers: (...args: unknown[]) => listSpy(...args),
    fetchReconcilerLogs: (...args: unknown[]) => logsSpy(...args),
    triggerReconcilerAction: (...args: unknown[]) => actionSpy(...args),
  }
})

import { ReconcilerDrillPanel } from './ReconcilerDrillPanel'
import { isReconcilerNode, reconcilerCoordsForNode } from './reconcilerDrill'
import type { GraphNode } from './types'

const HR_NODE: GraphNode = {
  id: 'recon:bp-keycloak',
  type: 'HelmRelease',
  label: 'bp-keycloak',
  sublabel: 'HelmRelease · Reconciled',
  status: 'healthy',
  metadata: { kind: 'HelmRelease', state: 'Reconciled' },
}

const CERT_NODE: GraphNode = {
  id: 'recon:certificate/cert-ns/wildcard-cert',
  type: 'Certificate',
  label: 'wildcard-cert',
  sublabel: 'Certificate · Reconciled',
  status: 'healthy',
  metadata: { kind: 'Certificate', state: 'Reconciled' },
}

function renderPanel(node: GraphNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ReconcilerDrillPanel deploymentId="dep-test-1" node={node} onClose={() => {}} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  listSpy.mockClear()
  logsSpy.mockClear()
  actionSpy.mockClear()
})

afterEach(() => {
  cleanup()
})

describe('reconcilerCoordsForNode — the three DAG id dialects', () => {
  it('HelmRelease: bare bp-<app> id', () => {
    expect(reconcilerCoordsForNode(HR_NODE)).toEqual({
      kind: 'HelmRelease',
      namespace: null,
      name: 'bp-keycloak',
    })
  })

  it('Kustomization: kustomization/<tier> id', () => {
    expect(
      reconcilerCoordsForNode({
        id: 'recon:kustomization/bootstrap-kit',
        type: 'Kustomization',
        label: 'bootstrap-kit',
        metadata: { kind: 'Kustomization', state: 'Reconciled' },
      }),
    ).toEqual({ kind: 'Kustomization', namespace: null, name: 'bootstrap-kit' })
  })

  it('declarative: <kindlower>/<ns>/<name> id (empty ns for cluster-scoped)', () => {
    expect(reconcilerCoordsForNode(CERT_NODE)).toEqual({
      kind: 'Certificate',
      namespace: 'cert-ns',
      name: 'wildcard-cert',
    })
    expect(
      reconcilerCoordsForNode({
        id: 'recon:organization//acme',
        type: 'Organization',
        label: 'acme',
        metadata: { kind: 'Organization', state: 'Reconciled' },
      }),
    ).toEqual({ kind: 'Organization', namespace: null, name: 'acme' })
  })
})

describe('isReconcilerNode', () => {
  it('true for recon:-namespaced ids, false otherwise', () => {
    expect(isReconcilerNode(HR_NODE)).toBe(true)
    expect(isReconcilerNode({ id: 'Cluster:hw270' })).toBe(false)
  })
})

describe('ReconcilerDrillPanel — row 193 drill + logs', () => {
  it('a Flux reconciler node drills into the owning-controller logs', async () => {
    renderPanel(HR_NODE)
    expect(screen.getByTestId('reconciler-drill-panel')).toBeTruthy()
    expect(screen.getByTestId('reconciler-drill-name').textContent).toBe('bp-keycloak')

    // Coordinates resolve via the reconcilers LIST (kind+name → namespace)…
    await waitFor(() => expect(listSpy).toHaveBeenCalled())
    // …and the logs drill fires against the resolved coordinate.
    await waitFor(() => expect(logsSpy).toHaveBeenCalled())
    expect(logsSpy.mock.calls[0]).toEqual([
      'dep-test-1',
      'HelmRelease',
      'flux-system',
      'bp-keycloak',
      200,
    ])

    const line1 = await screen.findByTestId('reconciler-drill-log-line-1')
    expect(line1.textContent).toContain('reconciling HelmRelease flux-system/bp-keycloak')
    expect(screen.getByTestId('reconciler-drill-log-line-2').textContent).toContain(
      'release bp-keycloak upgraded',
    )
    // The status strip surfaces the resolved object coordinate.
    expect(screen.getByTestId('reconciler-drill-object').textContent).toBe(
      'flux-system/bp-keycloak',
    )
  })

  it('Reconcile-now fires the #3996 action against the resolved coordinate', async () => {
    renderPanel(HR_NODE)
    await waitFor(() => expect(listSpy).toHaveBeenCalled())
    fireEvent.click(screen.getByTestId('reconciler-drill-action-reconcile'))
    await waitFor(() => expect(actionSpy).toHaveBeenCalled())
    expect(actionSpy.mock.calls[0]).toEqual([
      'dep-test-1',
      'HelmRelease',
      'flux-system',
      'bp-keycloak',
      'reconcile',
    ])
    expect((await screen.findByTestId('reconciler-drill-action-ok')).textContent).toContain(
      'reconcile requested',
    )
  })

  it('a non-Flux declarative reconciler states the no-controller-logs truth (no fabricated log view)', () => {
    renderPanel(CERT_NODE)
    expect(screen.getByTestId('reconciler-drill-panel')).toBeTruthy()
    expect(screen.getByTestId('reconciler-drill-nonflux')).toBeTruthy()
    expect(screen.queryByTestId('reconciler-drill-logs')).toBeNull()
    expect(logsSpy).not.toHaveBeenCalled()
  })
})
