/**
 * ResourceDetailPage.suspend-flip-197.test.tsx — UAT row 197 (#6085), the
 * PAGE-LEVEL half.
 *
 * ReconcileTab.absent-evidence-197.test.tsx locks the tab's own contract: it
 * calls `onActionApplied` after an action the server accepted. That is a SEAM,
 * and a seam nobody wires is not a fix — the row fails identically whether the
 * callback is absent or merely unconnected. This file walks the operator's
 * actual clause end-to-end against the page that owns the object:
 *
 *   click Suspend → the object is RE-READ → the control becomes Resume
 *
 * The re-read is the whole mechanism. `obj` lives in ResourceDetailPage's
 * `useState`, filled by a one-shot `useEffect` fetch, so the previous
 * `invalidateQueries` calls in ReconcileTab could not reach it at ANY key
 * spelling. That is why the hw293 walk found `spec.suspend` flipping correctly
 * on the live object while the page kept offering `Suspend`, and Resume was
 * reachable only by POSTing the endpoint by hand.
 *
 * THE CONTROL is the second case: an action the server REJECTS must NOT
 * re-read and must NOT flip the control. Without it this file would pass
 * against a page that simply refetched on every render or flipped the button
 * optimistically — neither of which is the clause, and the second of which
 * would publish a state change that never happened.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const getResource = vi.fn()
const getResourceTree = vi.fn()
const fetchReconcilerLogs = vi.fn()
const triggerReconcilerAction = vi.fn()

vi.mock('./resource.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./resource.api')>()
  return {
    ...actual,
    getResource: (...a: unknown[]) => getResource(...a),
    getResourceTree: (...a: unknown[]) => getResourceTree(...a),
  }
})

vi.mock('@/lib/reconciler-manage.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/reconciler-manage.api')>()
  return {
    ...actual,
    fetchReconcilerLogs: (...a: unknown[]) => fetchReconcilerLogs(...a),
    triggerReconcilerAction: (...a: unknown[]) => triggerReconcilerAction(...a),
  }
})

import { ResourceDetailPage } from './ResourceDetailPage'

/** The live hw293 subject: `GitRepository/flux-system/catalyst-tenant-uat107vc`. */
const RUNNING = {
  apiVersion: 'source.toolkit.fluxcd.io/v1',
  kind: 'GitRepository',
  metadata: { name: 'catalyst-tenant-uat107vc', namespace: 'flux-system' },
  spec: { suspend: false },
  status: {
    artifact: { revision: 'main@sha1:04ff0b8e20' },
    conditions: [{ type: 'Ready', status: 'True', reason: 'Succeeded', message: 'stored artifact' }],
  },
}

const SUSPENDED = { ...RUNNING, spec: { suspend: true } }

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ResourceDetailPage
        deploymentId="dep-1"
        basePath="/cloud"
        kind="gitrepository"
        ns="flux-system"
        name="catalyst-tenant-uat107vc"
        tab={'reconcile' as never}
      />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  getResourceTree.mockResolvedValue({ kind: 'GitRepository', name: 'catalyst-tenant-uat107vc' })
  fetchReconcilerLogs.mockResolvedValue({
    controller: 'source-controller',
    object: 'flux-system/catalyst-tenant-uat107vc',
    lines: [],
    total: 0,
  })
})

afterEach(cleanup)

describe('row 197 — the Suspend/Resume control must flip after its own action', () => {
  it('re-reads the object after a successful Suspend, and Resume appears', async () => {
    // The live object as the apiserver would answer it: running first, then
    // suspended once the action has landed.
    getResource.mockResolvedValueOnce(RUNNING).mockResolvedValue(SUSPENDED)
    triggerReconcilerAction.mockResolvedValue({
      kind: 'GitRepository',
      namespace: 'flux-system',
      name: 'catalyst-tenant-uat107vc',
      action: 'suspend',
      requestedAt: '2026-08-11T10:00:00Z',
      requestedBy: 'sovereign-admin',
    })

    renderPage()

    // Before: the object read false, so Suspend is the only control — and that
    // reading is CORRECT, which is what makes the flip below meaningful.
    const suspendBtn = await screen.findByTestId('reconcile-action-suspend')
    expect(screen.queryByTestId('reconcile-action-resume')).toBeNull()

    fireEvent.click(suspendBtn)

    await waitFor(() => expect(triggerReconcilerAction).toHaveBeenCalled())
    // THE ROW. The page re-read the object and the surface now offers the other
    // half of the clause. On the pre-fix page this stayed `Suspend` forever.
    await waitFor(() => expect(screen.getByTestId('reconcile-action-resume')).toBeTruthy())
    expect(screen.queryByTestId('reconcile-action-suspend')).toBeNull()

    // …and the status block agrees, rather than still printing the pre-action
    // reading beside a control that has moved on.
    const kv = screen.getByTestId('reconcile-kv-suspended')
    expect((kv.lastElementChild?.textContent ?? '').trim()).toBe('yes')

    // Exactly two reads: the initial one and the post-action one. A page that
    // polls would satisfy the flip assertion above without implementing it.
    expect(getResource).toHaveBeenCalledTimes(2)
  })

  it('CONTROL — a REJECTED action neither re-reads nor flips the control', async () => {
    getResource.mockResolvedValue(RUNNING)
    triggerReconcilerAction.mockRejectedValue(new Error('403 forbidden'))

    renderPage()

    const suspendBtn = await screen.findByTestId('reconcile-action-suspend')
    fireEvent.click(suspendBtn)

    await waitFor(() =>
      expect(screen.getByTestId('reconcile-action-msg').textContent).toContain('403'),
    )
    // Nothing changed server-side, so nothing may change here.
    expect(screen.queryByTestId('reconcile-action-resume')).toBeNull()
    expect(screen.getByTestId('reconcile-action-suspend')).toBeTruthy()
    expect(getResource).toHaveBeenCalledTimes(1)
  })
})
