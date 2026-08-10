/**
 * ResourceDetailPage.recon-logs-195.test.tsx — UAT row 195 (issue #3996).
 *
 * Row 195, verbatim:
 *
 *   "Drill a reconciler → its controller **logs** render."
 *
 * The row was stamped green, then downgraded by an evidence audit that OPENED
 * the screenshot on file and found the Logs tab showing
 *
 *   "Logs are streamed per-Pod. Drill into the Tree tab and pick a child Pod
 *    to see logs."
 *
 * — the literal opposite of the clause. A later read-only re-walk proved the
 * API half is sound (the logs route returns 200 with controller=
 * kustomize-controller and 33 lines for Kustomization/flux-system/
 * bootstrap-kit), which localises the defect precisely: not the endpoint, not
 * the ReconcileTab, but the **Logs tab**, which answered every non-Pod kind
 * with the per-Pod hint — sending the operator to a Tree tab that holds no Pod
 * to pick, on the one kind whose logs the platform already serves.
 *
 * These tests assert the tab the CLAUSE names, and keep the per-Pod hint for
 * the kinds where it is the honest answer.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const fetchReconcilerLogs = vi.fn()

vi.mock('@/lib/reconciler-manage.api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/reconciler-manage.api')>(
    '@/lib/reconciler-manage.api',
  )
  return { ...actual, fetchReconcilerLogs: (...a: unknown[]) => fetchReconcilerLogs(...a) }
})

import { ResourceDetailPage } from './ResourceDetailPage'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

afterEach(() => cleanup())

/** A Kustomization — the exact object the hw292 re-walk read logs for. */
const kustomization: K8sObject = {
  apiVersion: 'kustomize.toolkit.fluxcd.io/v1',
  kind: 'Kustomization',
  metadata: { name: 'bootstrap-kit', namespace: 'flux-system', uid: 'uid-k' },
  spec: { suspend: false } as Record<string, unknown>,
  status: { conditions: [{ type: 'Ready', status: 'True' }] },
}

/** The verbatim line shape the live controller emits for this object. */
const LIVE_LINE =
  '{"level":"info","msg":"server-side apply completed","controller":"kustomization",' +
  '"Kustomization":{"name":"bootstrap-kit","namespace":"flux-system"}}'

beforeEach(() => {
  fetchReconcilerLogs.mockReset()
  fetchReconcilerLogs.mockResolvedValue({
    controller: 'kustomize-controller',
    object: 'flux-system/bootstrap-kit',
    lines: [{ lineNumber: 1, message: LIVE_LINE }],
    total: 33,
  })
})

function renderLogsTab(kind: string, name: string, ns = 'flux-system', obj = kustomization) {
  // The app mounts every page under one QueryClientProvider (src/main.tsx);
  // the pane's live tail is a react-query subscription like ReconcileTab's.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ResourceDetailPage
        deploymentId="dep-1"
        basePath="/cloud"
        kind={kind}
        ns={ns}
        name={name}
        tab="logs"
        initialObj={obj}
      />
    </QueryClientProvider>,
  )
}

describe('row 195 — the Logs tab of a Flux reconciler renders CONTROLLER logs', () => {
  it('renders the owning controller log lines, not the per-Pod placeholder', async () => {
    renderLogsTab('kustomization', 'bootstrap-kit')

    // The residual the audit recorded: the placeholder must be gone.
    expect(screen.queryByTestId('resource-detail-logs-not-pod')).toBeNull()

    const pane = await screen.findByTestId('resource-detail-logs-controller')
    await waitFor(() => expect(pane.textContent).toContain('server-side apply completed'))
    // Named controller — the difference between "a log view" and "ITS
    // controller's logs", which is what the clause asks for.
    expect(screen.getByText(/kustomize-controller/)).toBeTruthy()
  })

  it('addresses the logs endpoint with the object it is showing', async () => {
    renderLogsTab('kustomization', 'bootstrap-kit')
    await waitFor(() => expect(fetchReconcilerLogs).toHaveBeenCalled())
    // PascalCase wire kind + the object's own coordinates — a guess here
    // returns an empty pane that still LOOKS like a log view.
    expect(fetchReconcilerLogs).toHaveBeenCalledWith('dep-1', 'Kustomization', 'flux-system', 'bootstrap-kit')
  })

  it('covers every manageable reconciler kind, not just Kustomization', async () => {
    for (const [uiKind, wireKind] of [
      ['helmrelease', 'HelmRelease'],
      ['gitrepository', 'GitRepository'],
      ['ocirepository', 'OCIRepository'],
      ['helmrepository', 'HelmRepository'],
      ['helmchart', 'HelmChart'],
    ] as const) {
      fetchReconcilerLogs.mockClear()
      const view = renderLogsTab(uiKind, 'bp-keycloak')
      await screen.findByTestId('resource-detail-logs-controller')
      expect(fetchReconcilerLogs).toHaveBeenCalledWith('dep-1', wireKind, 'flux-system', 'bp-keycloak')
      view.unmount()
    }
  })
})

describe('row 195 — the per-Pod hint survives where it is the honest answer', () => {
  it('keeps the tree-view hint for a non-reconciler, non-Pod kind', () => {
    renderLogsTab('configmap', 'cm-1', 'default')
    // A ConfigMap has no owning Flux controller; fabricating a log view for it
    // would be the failure mode this row's sibling guard (the drill panel's
    // non-Flux branch) exists to prevent.
    expect(screen.getByTestId('resource-detail-logs-not-pod')).toBeTruthy()
    expect(screen.queryByTestId('resource-detail-logs-controller')).toBeNull()
    expect(fetchReconcilerLogs).not.toHaveBeenCalled()
  })
})
