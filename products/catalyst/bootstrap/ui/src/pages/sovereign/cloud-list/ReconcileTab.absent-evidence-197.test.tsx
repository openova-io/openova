/**
 * ReconcileTab.absent-evidence-197.test.tsx — UAT row 197 (#6085).
 *
 * Row 197's clause: **Suspend/Resume** → `spec.suspend` flips on the live
 * object. The 2026-08-10 hw293 walk measured the object-level flip working in
 * BOTH directions (absent → true, then true → false), and the row still failed,
 * because the operator cannot perform the Resume half from this surface: the
 * `Resume` control NEVER RENDERS.
 *
 * Two distinct mechanisms, both in ReconcileTab.tsx, and BOTH are the same
 * defect class — a verdict published from ABSENT evidence:
 *
 *  (1) STALE AFTER ACTION. `suspended` is `useMemo`'d off the `obj` prop, and
 *      the mutation's `onSuccess` invalidates only `reconciler-logs` and
 *      `reconciliation-dag`. It cannot invalidate the query that produced
 *      `obj` — because there ISN'T one: ResourceDetailPage fetches the object
 *      in a `useEffect` into `useState` (ResourceDetailPage.tsx:150-168), so no
 *      react-query key exists for it at any spelling. The button therefore
 *      cannot flip after its own action, and Resume stays unreachable forever.
 *      The fix is a refetch seam the parent owns, not another query key.
 *
 *  (2) FAIL-OPEN ON AN ERRORED READ. `Boolean(obj?.spec?.suspend)` collapses
 *      "the object fetch failed" into the confident reading `false`, and
 *      ResourceDetailPage DELIBERATELY renders this tab on the `objErr` branch
 *      (the #5210 graceful degrade, which passes `obj={null}`). So a failed
 *      object GET produces a full status block asserting `SUSPENDED: no` over
 *      an object nobody read — and offers `Suspend` as though the current state
 *      were known. `readyStateOf` has the identical shape: no conditions →
 *      `'Reconciling'`, a confident positive verdict synthesized from nothing.
 *
 * `blocked` / `missing` / `no` must rest on POSITIVE evidence. Absence is
 * `unknown`, and `unknown` must be SAID.
 *
 * THE CONTROLS share the suspect property: they render the very same Suspended
 * KV and the very same action row off an object that IS present, one suspended
 * and one not. They must stay green — the fix has to DISCRIMINATE "read it and
 * it said false" from "never read it", not relabel every reading `unknown`.
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

import { ReconcileTab } from './ReconcileTab'

afterEach(cleanup)

function renderTab(obj: K8sObject | null, onActionApplied?: () => void) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ReconcileTab
        deploymentId="dep-1"
        apiKind="gitrepository"
        ns="flux-system"
        name="catalyst-tenant-uat107vc"
        obj={obj}
        onActionApplied={onActionApplied}
      />
    </QueryClientProvider>,
  )
}

/**
 * The KV widget renders `<div testid><div>LABEL</div><div>VALUE</div></div>`,
 * so the value cell is the last child. Reading it directly keeps every
 * assertion below an EXACT match on the rendered verdict rather than a
 * substring search over label+value concatenated.
 */
function suspendValue(): string {
  const kv = screen.getByTestId('reconcile-kv-suspended')
  return (kv.lastElementChild?.textContent ?? '').trim()
}

/** The live hw293 subject of the row, running and not suspended. */
const RUNNING_GITREPO: K8sObject = {
  apiVersion: 'source.toolkit.fluxcd.io/v1',
  kind: 'GitRepository',
  metadata: { name: 'catalyst-tenant-uat107vc', namespace: 'flux-system' },
  spec: { suspend: false },
  status: {
    artifact: { revision: 'main@sha1:04ff0b8e20' },
    conditions: [{ type: 'Ready', status: 'True', reason: 'Succeeded', message: 'stored artifact' }],
  },
} as unknown as K8sObject

const SUSPENDED_GITREPO: K8sObject = {
  ...RUNNING_GITREPO,
  spec: { suspend: true },
} as unknown as K8sObject

beforeEach(() => {
  fetchReconcilerLogs.mockReset()
  triggerReconcilerAction.mockReset()
  fetchReconcilerLogs.mockResolvedValue({
    controller: 'source-controller',
    object: 'flux-system/catalyst-tenant-uat107vc',
    lines: [],
    total: 0,
  })
  triggerReconcilerAction.mockResolvedValue({
    kind: 'GitRepository',
    namespace: 'flux-system',
    name: 'catalyst-tenant-uat107vc',
    action: 'suspend',
    requestedAt: '2026-08-11T10:00:00Z',
    requestedBy: 'sovereign-admin',
  })
})

describe('row 197 (2) — an unread object must never be published as a negative fact', () => {
  it('reads `unknown`, NOT `no`, when the object was never read', () => {
    renderTab(null)
    // Assert on the KV's VALUE cell, exactly — not on the block's textContent.
    // Two traps live here and both produce a test that cannot fail:
    //   • `toContain('unknown')` alone passes on the string "unknown" AND on
    //     any longer string that happens to embed it;
    //   • `not.toContain('no')` is worse than useless — "unknown" itself
    //     contains "no" (u-n-k-**no**-w-n), so it fails on the CORRECT output,
    //     while against the buggy block text "Suspendedno" the "no" is preceded
    //     by a letter, so a word-boundary variant would pass on the DEFECT.
    // An exact match on the value cell has neither hole.
    expect(suspendValue()).toBe('unknown')
  })

  it('does not synthesize a `Reconciling` state from absent conditions', () => {
    renderTab(null)
    const state = screen.getByTestId('reconcile-kv-state').textContent ?? ''
    expect(state).not.toContain('Reconciling')
    expect(state).toContain('Unknown')
  })

  it('offers BOTH Suspend and Resume when the current state is unknown', () => {
    // This is the operator-visible half of row 197: with `suspended` fail-open
    // to false, only `Suspend` was ever offered, so the Resume half of the
    // clause was unreachable from this surface and had to be performed by
    // POSTing the endpoint by hand. When the state is unknown, withholding
    // either control is itself a claim about which one applies.
    renderTab(null)
    expect(screen.getByTestId('reconcile-action-resume')).toBeTruthy()
    expect(screen.getByTestId('reconcile-action-suspend')).toBeTruthy()
    // …and the operator is told WHY both are on offer.
    expect(screen.getByTestId('reconcile-state-unknown-reason').textContent).toContain(
      'could not be read',
    )
  })
})

describe('row 197 (1) — the surface must re-read the object after its own action', () => {
  it('notifies the parent to refetch after a successful action', async () => {
    const onActionApplied = vi.fn()
    renderTab(RUNNING_GITREPO, onActionApplied)

    fireEvent.click(screen.getByTestId('reconcile-action-suspend'))

    await waitFor(() => expect(triggerReconcilerAction).toHaveBeenCalled())
    // Without this the button cannot flip after its own action: the object is
    // parent `useState`, so there is no query key any invalidation can reach.
    await waitFor(() => expect(onActionApplied).toHaveBeenCalledTimes(1))
  })

  it('does NOT ask for a refetch when the action failed', async () => {
    const onActionApplied = vi.fn()
    triggerReconcilerAction.mockRejectedValue(new Error('403 forbidden'))
    renderTab(RUNNING_GITREPO, onActionApplied)

    fireEvent.click(screen.getByTestId('reconcile-action-suspend'))

    await waitFor(() =>
      expect(screen.getByTestId('reconcile-action-msg').textContent).toContain('403'),
    )
    expect(onActionApplied).not.toHaveBeenCalled()
  })
})

describe('CONTROLS — a READ object must still publish its real reading', () => {
  it('CONTROL — a present object reading false still says `no` and offers only Suspend', () => {
    renderTab(RUNNING_GITREPO)
    expect(suspendValue()).toBe('no')
    expect(screen.getByTestId('reconcile-kv-state').textContent).toContain('Reconciled')
    expect(screen.getByTestId('reconcile-action-suspend')).toBeTruthy()
    expect(screen.queryByTestId('reconcile-action-resume')).toBeNull()
    expect(screen.queryByTestId('reconcile-state-unknown-reason')).toBeNull()
  })

  it('CONTROL — a present object reading true still says `yes` and offers only Resume', () => {
    renderTab(SUSPENDED_GITREPO)
    expect(suspendValue()).toBe('yes')
    expect(screen.getByTestId('reconcile-kv-state').textContent).toContain('Suspended')
    expect(screen.getByTestId('reconcile-action-resume')).toBeTruthy()
    expect(screen.queryByTestId('reconcile-action-suspend')).toBeNull()
    expect(screen.queryByTestId('reconcile-state-unknown-reason')).toBeNull()
  })
})
