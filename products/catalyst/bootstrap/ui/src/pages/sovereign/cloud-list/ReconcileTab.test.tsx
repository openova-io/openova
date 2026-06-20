/**
 * ReconcileTab.test.tsx — wiring lock-in for the #3996 management tab:
 * reads status/revision/suspended off the live object, renders controller
 * LOGS, and fires reconcile / suspend / resume through the action client.
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

const fetchReconcilerLogs = vi.fn()
const triggerReconcilerAction = vi.fn()

vi.mock('@/lib/reconciler-manage.api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/reconciler-manage.api')>(
    '@/lib/reconciler-manage.api',
  )
  return {
    ...actual,
    fetchReconcilerLogs: (...a: unknown[]) => fetchReconcilerLogs(...a),
    triggerReconcilerAction: (...a: unknown[]) => triggerReconcilerAction(...a),
  }
})

import { ReconcileTab, wireKindFor, isReconcilerManageable } from './ReconcileTab'

afterEach(cleanup)

function renderTab(obj: K8sObject | null, apiKind = 'helmrelease') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ReconcileTab deploymentId="dep-1" apiKind={apiKind} ns="flux-system" name="bp-keycloak" obj={obj} />
    </QueryClientProvider>,
  )
}

const READY_HR: K8sObject = {
  apiVersion: 'helm.toolkit.fluxcd.io/v2',
  kind: 'HelmRelease',
  metadata: { name: 'bp-keycloak', namespace: 'flux-system' },
  spec: { suspend: false },
  status: {
    lastAppliedRevision: '0.4.1',
    conditions: [{ type: 'Ready', status: 'True', reason: 'ReconciliationSucceeded', message: 'ok' }],
  },
} as unknown as K8sObject

beforeEach(() => {
  fetchReconcilerLogs.mockReset()
  triggerReconcilerAction.mockReset()
  fetchReconcilerLogs.mockResolvedValue({
    controller: 'helm-controller',
    object: 'flux-system/bp-keycloak',
    lines: [{ lineNumber: 1, message: 'reconciling HelmRelease flux-system/bp-keycloak' }],
    total: 1,
  })
  triggerReconcilerAction.mockResolvedValue({
    kind: 'HelmRelease',
    namespace: 'flux-system',
    name: 'bp-keycloak',
    action: 'reconcile',
    requestedAt: '2026-06-20T10:01:00Z',
    requestedBy: 'emrah.baysal@openova.io',
  })
})

describe('wireKindFor / isReconcilerManageable', () => {
  it('maps the six manageable kinds and rejects others', () => {
    expect(wireKindFor('helmrelease')).toBe('HelmRelease')
    expect(wireKindFor('gitrepository')).toBe('GitRepository')
    expect(isReconcilerManageable('kustomization')).toBe(true)
    expect(isReconcilerManageable('pod')).toBe(false)
    expect(wireKindFor('configmap')).toBe('')
  })
})

describe('ReconcileTab', () => {
  it('shows state/revision/suspended off the live object and renders logs', async () => {
    renderTab(READY_HR)
    expect(screen.getByTestId('reconcile-kv-state').textContent).toContain('Reconciled')
    expect(screen.getByTestId('reconcile-kv-revision').textContent).toContain('0.4.1')
    expect(screen.getByTestId('reconcile-kv-suspended').textContent).toContain('no')
    await waitFor(() =>
      expect(screen.getByTestId('reconcile-tab-logs').textContent).toContain(
        'flux-system/bp-keycloak',
      ),
    )
  })

  it('fires Reconcile now with the PascalCase wire kind', async () => {
    renderTab(READY_HR)
    fireEvent.click(screen.getByTestId('reconcile-action-reconcile'))
    await waitFor(() =>
      expect(triggerReconcilerAction).toHaveBeenCalledWith(
        'dep-1',
        'HelmRelease',
        'flux-system',
        'bp-keycloak',
        'reconcile',
      ),
    )
  })

  it('shows Suspend for a running reconciler and triggers it', async () => {
    renderTab(READY_HR)
    fireEvent.click(screen.getByTestId('reconcile-action-suspend'))
    await waitFor(() =>
      expect(triggerReconcilerAction).toHaveBeenCalledWith(
        'dep-1',
        'HelmRelease',
        'flux-system',
        'bp-keycloak',
        'suspend',
      ),
    )
  })

  it('shows Resume (not Suspend) when the object is suspended', () => {
    const suspended = {
      ...READY_HR,
      spec: { suspend: true },
    } as unknown as K8sObject
    renderTab(suspended)
    expect(screen.getByTestId('reconcile-kv-suspended').textContent).toContain('yes')
    expect(screen.getByTestId('reconcile-kv-state').textContent).toContain('Suspended')
    expect(screen.getByTestId('reconcile-action-resume')).toBeTruthy()
    expect(screen.queryByTestId('reconcile-action-suspend')).toBeNull()
  })

  it('renders the not-flux note for a non-manageable kind', () => {
    renderTab(READY_HR, 'configmap')
    expect(screen.getByTestId('reconcile-tab-not-flux')).toBeTruthy()
  })
})
