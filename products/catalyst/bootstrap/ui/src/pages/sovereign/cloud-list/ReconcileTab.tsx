/**
 * ReconcileTab — the lightweight ArgoCD/Flux MANAGEMENT tab on the recon
 * lens drill-in (issue #3996). Rendered inside ResourceDetailPage for the
 * manageable Flux reconciler kinds only (HelmRelease / Kustomization /
 * GitRepository / OCIRepository / HelmRepository / HelmChart).
 *
 * It is purely ADDITIVE — the existing Overview / YAML / Logs / Exec /
 * Events / Metrics / Tree tabs and the rest of the k9s-style explorer are
 * untouched. This tab adds, in one place:
 *   • the live reconcile status + applied source revision + suspended flag
 *     (read off the already-loaded resource object),
 *   • the OWNING controller's LOGS filtered to this object (new endpoint),
 *   • the Flux-native triggers: Reconcile now / Suspend / Resume.
 *
 * The cloud-list registry uses lowercase-singular kind ids (e.g.
 * "helmrelease"); the management endpoints address objects by their
 * PascalCase K8s Kind. wireKindFor() maps between them.
 */

import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { triggerReconcilerAction, type ReconcilerActionKind } from '@/lib/reconciler-manage.api'
import { ControllerLogsPane } from './ControllerLogsPane'

/** Map the cloud-list lowercase-singular kind id → the PascalCase K8s Kind
 *  the management endpoints address. Returns '' for a non-manageable kind. */
export function wireKindFor(apiKind: string): string {
  switch (apiKind.toLowerCase()) {
    case 'helmrelease':
      return 'HelmRelease'
    case 'kustomization':
      return 'Kustomization'
    case 'gitrepository':
      return 'GitRepository'
    case 'ocirepository':
      return 'OCIRepository'
    case 'helmrepository':
      return 'HelmRepository'
    case 'helmchart':
      return 'HelmChart'
    default:
      return ''
  }
}

/** isReconcilerManageable — true when the kind supports this tab. */
export function isReconcilerManageable(apiKind: string): boolean {
  return wireKindFor(apiKind) !== ''
}

/**
 * SuspendReading — the TRI-STATE answer to "is this reconciler suspended?".
 *
 * UAT row 197 / #6085. This used to be a `boolean` produced by
 * `Boolean(obj?.spec?.suspend)`, which collapses two completely different
 * facts onto the same value:
 *
 *   • the object was read and its `spec.suspend` is false/absent  → `no`
 *   • the object was never read (the parent's GET failed, and this tab is
 *     DELIBERATELY rendered on that branch by the #5210 graceful degrade,
 *     which passes `obj={null}`)                                  → `unknown`
 *
 * The second case was published as a confident `SUSPENDED: no` — a verdict
 * from absent evidence, over an object nobody read. `no` must require positive
 * evidence; absence is `unknown` and has to say so.
 */
type SuspendReading = 'yes' | 'no' | 'unknown'

/**
 * readSuspend — the tri-state, derived ONLY from what was actually read.
 *
 * Deliberately NOT exported: `react-refresh/only-export-components` already
 * fires twice on this file for `wireKindFor` / `isReconcilerManageable`, and a
 * third value export would grow that. The behaviour is covered through the
 * rendered surface in ReconcileTab.absent-evidence-197.test.tsx, which is the
 * level row 197 is actually written at.
 */
function readSuspend(obj: K8sObject | null | undefined): SuspendReading {
  // No object → nothing was read. That is the whole point of the type.
  if (!obj) return 'unknown'
  return (obj.spec as { suspend?: boolean } | undefined)?.suspend === true ? 'yes' : 'no'
}

/** Read the Ready condition off a Flux object → a display state string. */
function readyStateOf(obj: K8sObject | null, suspend: SuspendReading): string {
  // Row 197 — an unread object has no state to report. It previously fell
  // through to `'Reconciling'`, the identical defect one field over: a
  // confident positive verdict synthesized from an empty condition list.
  if (!obj) return 'Unknown'
  if (suspend === 'yes') return 'Suspended'
  const conds = ((obj?.status as { conditions?: unknown } | undefined)?.conditions ?? []) as {
    type?: string
    status?: string
    reason?: string
  }[]
  const ready = conds.find((c) => c.type === 'Ready')
  if (!ready) return 'Reconciling'
  if (ready.status === 'True') return 'Reconciled'
  if (ready.status === 'False' && (ready.reason ?? '').toLowerCase() === 'stalled') return 'Degraded'
  return 'Reconciling'
}

function readyMessageOf(obj: K8sObject | null): string {
  const conds = ((obj?.status as { conditions?: unknown } | undefined)?.conditions ?? []) as {
    type?: string
    message?: string
  }[]
  return conds.find((c) => c.type === 'Ready')?.message ?? ''
}

function revisionOf(obj: K8sObject | null): string {
  const status = (obj?.status ?? {}) as Record<string, unknown>
  const artifact = (status.artifact ?? {}) as Record<string, unknown>
  return (
    (status.lastAppliedRevision as string) ||
    (status.lastAttemptedRevision as string) ||
    (artifact.revision as string) ||
    ''
  )
}

export interface ReconcileTabProps {
  deploymentId: string
  /** lowercase-singular kind id from the cloud-list registry. */
  apiKind: string
  ns: string
  name: string
  /** The already-loaded live object (status/spec for the status block). */
  obj: K8sObject | null
  /** Mirrors the page's RBAC hint; the server is the authoritative gate. */
  isTierAdmin?: boolean
  /**
   * UAT row 197 / #6085 — THE REFETCH SEAM. Called after an action the server
   * accepted, so the owner of `obj` can re-read it and this tab can reflect
   * the flip it just caused.
   *
   * This is a callback and not another `invalidateQueries` key because `obj`
   * is NOT a react-query query at all: ResourceDetailPage fetches it in a
   * `useEffect` into `useState`. No key exists to invalidate at any spelling,
   * which is why the old `onSuccess` — invalidating `reconciler-logs` and
   * `reconciliation-dag` — could never flip this button after its own action.
   * That is what made the Resume half of row 197 unreachable from this surface.
   */
  onActionApplied?: () => void
}

export function ReconcileTab({
  deploymentId,
  apiKind,
  ns,
  name,
  obj,
  isTierAdmin = true,
  onActionApplied,
}: ReconcileTabProps) {
  const qc = useQueryClient()
  const [actionMsg, setActionMsg] = useState<string>('')
  const wireKind = useMemo(() => wireKindFor(apiKind), [apiKind])

  // Row 197 — tri-state, never `Boolean(...)`. See readSuspend above.
  const suspend = useMemo(() => readSuspend(obj), [obj])
  const suspendKnown = suspend !== 'unknown'
  const state = readyStateOf(obj, suspend)
  const revision = revisionOf(obj)
  const message = readyMessageOf(obj)

  const action = useMutation({
    mutationFn: (a: ReconcilerActionKind) =>
      triggerReconcilerAction(deploymentId, wireKind, ns || '', name, a),
    onSuccess: (res) => {
      setActionMsg(`${res.action} requested by ${res.requestedBy}`)
      void qc.invalidateQueries({ queryKey: ['reconciler-logs', deploymentId, wireKind, ns, name] })
      void qc.invalidateQueries({ queryKey: ['reconciliation-dag', deploymentId] })
      // Row 197 — re-read the OBJECT this tab renders its verdict from. The two
      // invalidations above touch the logs pane and the DAG; neither reaches
      // `obj`, so without this the Suspended reading and the Suspend/Resume
      // control stay frozen at whatever they were BEFORE the action the
      // operator just performed. Fires only on success: a rejected action
      // changed nothing, and asking for a refetch would imply otherwise.
      onActionApplied?.()
    },
    onError: (e: unknown) => setActionMsg(e instanceof Error ? e.message : 'action failed'),
  })

  if (!wireKind) {
    return (
      <div
        data-testid="reconcile-tab-not-flux"
        className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
      >
        This kind is not a Flux-managed reconciler — reconcile / suspend / resume are unavailable.
      </div>
    )
  }

  return (
    <div data-testid="reconcile-tab" className="space-y-4">
      {/* Status block — read off the live object. */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3" data-testid="reconcile-tab-status">
        <KV label="State" value={state} testId="reconcile-kv-state" />
        <KV label="Revision" value={revision || '—'} testId="reconcile-kv-revision" />
        {/* Row 197 — prints the tri-state VERBATIM. `unknown` is a real value
            here, not a styling of `no`: the walk's fresh page load, taken while
            the live object genuinely read `spec.suspend: true`, printed
            `SUSPENDED | no` because an undefined `obj` was rendered as a
            negative fact. */}
        <KV label="Suspended" value={suspend} testId="reconcile-kv-suspended" />
      </div>
      {message ? (
        <p className="text-sm text-[var(--color-text-dim)]" data-testid="reconcile-tab-message">
          {message}
        </p>
      ) : null}

      {/* Triggers — RBAC-gated server-side; the UI hides them for viewers. */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Flux actions</div>
        <div className="flex flex-wrap items-center gap-2" data-testid="reconcile-tab-actions">
          <button
            type="button"
            disabled={!isTierAdmin || action.isPending}
            onClick={() => action.mutate('reconcile')}
            data-testid="reconcile-action-reconcile"
            className="rounded-md border border-[var(--color-accent)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-semibold text-[var(--color-accent)] hover:bg-[var(--color-surface-hover)] disabled:opacity-50"
          >
            Reconcile now
          </button>
          {/* Row 197 — WHICH control to offer is itself a claim about the
              current state. With the old fail-open boolean, an unread object
              silently asserted "not suspended" and offered Suspend ALONE, which
              is exactly why the walk could not reach Resume from this surface
              and had to POST the endpoint by hand.

              When the reading is `unknown` we make no such claim: BOTH halves
              are offered, and the reason line below says why. Each action is
              idempotent server-side (Flux `spec.suspend` is a desired-state
              field, not a toggle), so offering both is safe — and after either
              one lands, `onActionApplied` re-reads the object and this collapses
              back to the single correct control. */}
          {suspendKnown && suspend === 'no' ? null : (
            <button
              type="button"
              disabled={!isTierAdmin || action.isPending}
              onClick={() => action.mutate('resume')}
              data-testid="reconcile-action-resume"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-semibold text-[var(--color-text)] hover:bg-[var(--color-surface-hover)] disabled:opacity-50"
            >
              Resume
            </button>
          )}
          {suspendKnown && suspend === 'yes' ? null : (
            <button
              type="button"
              disabled={!isTierAdmin || action.isPending}
              onClick={() => action.mutate('suspend')}
              data-testid="reconcile-action-suspend"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-semibold text-[var(--color-text)] hover:bg-[var(--color-surface-hover)] disabled:opacity-50"
            >
              Suspend
            </button>
          )}
          {suspendKnown ? null : (
            <span
              className="text-xs text-yellow-400"
              data-testid="reconcile-state-unknown-reason"
            >
              This object could not be read, so its current suspend state is
              unknown — both actions are offered rather than guessing one.
            </span>
          )}
          {actionMsg ? (
            <span className="text-xs text-[var(--color-text-dim)]" data-testid="reconcile-action-msg">
              {actionMsg}
            </span>
          ) : null}
        </div>
      </div>

      {/* Logs — the owning controller's output filtered to this object. The
          pane itself lives in ControllerLogsPane so the k9s **Logs** tab
          renders the identical surface (UAT row 195) rather than a second
          copy that can drift from this one. */}
      <ControllerLogsPane
        deploymentId={deploymentId}
        wireKind={wireKind}
        ns={ns}
        name={name}
        testId="reconcile-tab-logs"
      />
    </div>
  )
}

function KV({ label, value, testId }: { label: string; value: string; testId: string }) {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3" data-testid={testId}>
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">{label}</div>
      <div className="mt-1 break-words font-mono text-sm text-[var(--color-text)]">{value || '—'}</div>
    </div>
  )
}
