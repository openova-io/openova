/**
 * ReconcilerDrillPanel — the reconciler-node drill-in for the unified
 * Cloud graph's Reconciliation lens (UAT row 193, issue #5223).
 *
 * The #3996 ArgoCD-like drill (status + owning-controller LOGS +
 * reconcile/suspend/resume) shipped on the per-kind cloud-list
 * ResourceDetailPage, but when #3958 folded the reconcilers into the ONE
 * Cloud graph and deleted the standalone /reconciliation page, clicking a
 * reconciler NODE on the canvas opened only the generic infrastructure
 * DetailPanel — no drill, no logs. This panel re-wires the graph click to
 * the SAME backend surface the ReconcileTab uses
 * (lib/reconciler-manage.api.ts):
 *
 *   GET  /api/v1/deployments/{id}/reconcilers                     — coords + rich row
 *   GET  /api/v1/deployments/{id}/reconcilers/{kind}/{ns}/{name}/logs
 *   POST /api/v1/deployments/{id}/reconcilers/{kind}/{ns}/{name}/{action}
 *
 * Coordinate resolution: the reconciliation-DAG feed the graph ingests
 * (lib/reconciliation.api.ts) does NOT carry a namespace, so the panel
 * resolves the exact `{kind, ns, name}` from the reconcilers LIST (which
 * carries both coordinates), falling back to the canonical `flux-system`
 * namespace for the Flux kinds when the list row is absent (the backend's
 * log filter also matches the bare object name, so the fallback still
 * scopes correctly on a normal Sovereign).
 *
 * Non-Flux declarative reconcilers (Certificate / CNPG Cluster /
 * ExternalSecret / the Catalyst CRs) have no owning Flux controller, so
 * the panel states that honestly instead of fabricating a log view.
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  fetchReconcilerLogs,
  fetchReconcilers,
  isManageableKind,
  triggerReconcilerAction,
  type ManagedReconciler,
  type ReconcilerActionKind,
} from '@/lib/reconciler-manage.api'
import { reconcilerCoordsForNode } from './reconcilerDrill'
import type { GraphNode } from './types'

/** The canonical Flux namespace on every Catalyst Sovereign (bootstrap-kit
 *  installs the controllers + the bp-* HelmReleases there); used only as
 *  the fallback when the reconcilers list did not return the object. */
const FLUX_NS_FALLBACK = 'flux-system'

/** How many controller-log lines the drill tails per fetch. */
const LOG_TAIL_LINES = 200

export interface ReconcilerDrillPanelProps {
  deploymentId: string
  node: GraphNode
  onClose: () => void
}

export function ReconcilerDrillPanel({ deploymentId, node, onClose }: ReconcilerDrillPanelProps) {
  const coords = useMemo(() => reconcilerCoordsForNode(node), [node])
  const manageable = isManageableKind(coords.kind)

  // Resolve the exact coordinates (esp. namespace) from the reconcilers
  // list — the same feed the #3996 management list reads. Shared query key
  // so switching nodes reuses one list fetch.
  const listQuery = useQuery({
    queryKey: ['reconcilers-manage', deploymentId],
    queryFn: () => fetchReconcilers(deploymentId),
    enabled: !!deploymentId && manageable,
    staleTime: 15_000,
    retry: 1,
  })

  const listRow: ManagedReconciler | null = useMemo(() => {
    const rows = listQuery.data?.reconcilers ?? []
    return (
      rows.find((r) => r.kind === coords.kind && r.name === coords.name) ??
      // HelmRelease DAG ids are `bp-<app>` while some feeds name the HR
      // without the prefix — tolerate either spelling before giving up.
      rows.find(
        (r) =>
          r.kind === coords.kind &&
          (r.name === coords.name.replace(/^bp-/, '') || `bp-${r.name}` === coords.name),
      ) ??
      null
    )
  }, [listQuery.data, coords])

  const logNamespace = listRow?.namespace ?? coords.namespace ?? FLUX_NS_FALLBACK
  const logName = listRow?.name ?? coords.name

  const logsQuery = useQuery({
    queryKey: ['reconciler-logs', deploymentId, coords.kind, logNamespace, logName],
    queryFn: () => fetchReconcilerLogs(deploymentId, coords.kind, logNamespace, logName, LOG_TAIL_LINES),
    // Wait for the list resolution (or its failure) before tailing so the
    // first fetch already uses the exact namespace.
    enabled: !!deploymentId && manageable && (listQuery.isSuccess || listQuery.isError),
    retry: 1,
  })

  const [actionBusy, setActionBusy] = useState<ReconcilerActionKind | null>(null)
  const [actionMsg, setActionMsg] = useState<string | null>(null)
  const [actionErr, setActionErr] = useState<string | null>(null)

  async function fireAction(action: ReconcilerActionKind) {
    setActionBusy(action)
    setActionErr(null)
    setActionMsg(null)
    try {
      const res = await triggerReconcilerAction(deploymentId, coords.kind, logNamespace, logName, action)
      setActionMsg(`${res.action} requested at ${res.requestedAt}`)
      void listQuery.refetch()
      void logsQuery.refetch()
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : String(e))
    } finally {
      setActionBusy(null)
    }
  }

  const state = listRow?.state ?? node.metadata?.state ?? ''
  const message = listRow?.message ?? node.metadata?.message ?? ''

  return (
    <aside
      role="dialog"
      aria-label={`${node.label} reconciler drill-in`}
      data-testid="reconciler-drill-panel"
      className="fixed right-0 top-14 z-30 flex h-[calc(100vh-3.5rem)] w-[28rem] flex-col gap-3 border-l border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 shadow-xl"
    >
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p
            data-testid="reconciler-drill-name"
            className="truncate text-base font-semibold text-[var(--color-text-strong)]"
          >
            {node.label}
          </p>
          <p
            data-testid="reconciler-drill-kind"
            className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]"
          >
            {coords.kind}
            {state ? ` · ${state}` : ''}
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          data-testid="reconciler-drill-close"
          className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
          aria-label="Close reconciler drill-in"
        >
          ×
        </button>
      </header>

      {/* Status strip — coordinates + revision + last reconcile. */}
      <section className="flex flex-col gap-1.5" data-testid="reconciler-drill-status">
        <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
          Status
        </h3>
        <dl className="grid grid-cols-3 gap-x-2 gap-y-1.5 text-xs">
          <dt className="text-[var(--color-text-dim)]">Object</dt>
          <dd className="col-span-2 truncate font-mono text-[var(--color-text)]" data-testid="reconciler-drill-object">
            {logNamespace ? `${logNamespace}/` : ''}
            {logName}
          </dd>
          {listRow?.revision ? (
            <>
              <dt className="text-[var(--color-text-dim)]">Revision</dt>
              <dd className="col-span-2 truncate font-mono text-[var(--color-text)]">{listRow.revision}</dd>
            </>
          ) : null}
          {listRow?.lastReconcile ? (
            <>
              <dt className="text-[var(--color-text-dim)]">Last reconcile</dt>
              <dd className="col-span-2 truncate text-[var(--color-text)]">{listRow.lastReconcile}</dd>
            </>
          ) : null}
          {listRow?.controller ? (
            <>
              <dt className="text-[var(--color-text-dim)]">Controller</dt>
              <dd className="col-span-2 truncate font-mono text-[var(--color-text)]">{listRow.controller}</dd>
            </>
          ) : null}
        </dl>
        {message ? (
          <p
            data-testid="reconciler-drill-message"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-xs text-[var(--color-text)]"
          >
            {message}
          </p>
        ) : null}
      </section>

      {manageable ? (
        <>
          {/* Actions — the #3996 Flux-native triad. */}
          <section className="flex flex-col gap-1.5" data-testid="reconciler-drill-actions">
            <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
              Actions
            </h3>
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                data-testid="reconciler-drill-action-reconcile"
                disabled={actionBusy !== null}
                onClick={() => void fireAction('reconcile')}
                className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)] disabled:opacity-50"
              >
                {actionBusy === 'reconcile' ? 'Reconciling…' : 'Reconcile now'}
              </button>
              {listRow?.suspended ? (
                <button
                  type="button"
                  data-testid="reconciler-drill-action-resume"
                  disabled={actionBusy !== null}
                  onClick={() => void fireAction('resume')}
                  className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)] disabled:opacity-50"
                >
                  {actionBusy === 'resume' ? 'Resuming…' : 'Resume'}
                </button>
              ) : (
                <button
                  type="button"
                  data-testid="reconciler-drill-action-suspend"
                  disabled={actionBusy !== null}
                  onClick={() => void fireAction('suspend')}
                  className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)] disabled:opacity-50"
                >
                  {actionBusy === 'suspend' ? 'Suspending…' : 'Suspend'}
                </button>
              )}
            </div>
            {actionMsg ? (
              <p role="status" className="text-xs text-[var(--color-success,#16a34a)]" data-testid="reconciler-drill-action-ok">
                {actionMsg}
              </p>
            ) : null}
            {actionErr ? (
              <p role="alert" className="text-xs text-[var(--color-danger)]" data-testid="reconciler-drill-action-error">
                {actionErr}
              </p>
            ) : null}
          </section>

          {/* Controller logs — the row-193 drill. */}
          <section className="flex min-h-0 flex-1 flex-col gap-1.5" data-testid="reconciler-drill-logs">
            <div className="flex items-center justify-between">
              <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
                Controller logs{logsQuery.data ? ` (${logsQuery.data.total})` : ''}
              </h3>
              <button
                type="button"
                data-testid="reconciler-drill-logs-refresh"
                onClick={() => void logsQuery.refetch()}
                className="rounded-md border border-[var(--color-border)] px-2 py-0.5 text-[10px] text-[var(--color-text)] hover:bg-[var(--color-bg)]"
              >
                Refresh
              </button>
            </div>
            {logsQuery.isLoading || (!logsQuery.data && !logsQuery.isError) ? (
              <p className="text-xs text-[var(--color-text-dim)]" data-testid="reconciler-drill-logs-loading">
                Tailing {logNamespace}/{logName} from the owning controller…
              </p>
            ) : logsQuery.isError ? (
              <p className="text-xs text-[var(--color-danger)]" data-testid="reconciler-drill-logs-error">
                {(logsQuery.error as Error | undefined)?.message ?? 'Controller logs unavailable'}
              </p>
            ) : logsQuery.data && logsQuery.data.lines.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]" data-testid="reconciler-drill-logs-empty">
                No recent {logsQuery.data.controller} log lines mention {logNamespace}/{logName}.
              </p>
            ) : (
              <div className="min-h-0 flex-1 overflow-y-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-2">
                <ol className="flex flex-col gap-0.5">
                  {logsQuery.data!.lines.map((l) => (
                    <li
                      key={l.lineNumber}
                      data-testid={`reconciler-drill-log-line-${l.lineNumber}`}
                      className="whitespace-pre-wrap break-all font-mono text-[10px] leading-relaxed text-[var(--color-text)]"
                    >
                      {l.message}
                    </li>
                  ))}
                </ol>
              </div>
            )}
          </section>
        </>
      ) : (
        // Non-Flux declarative reconciler — no owning Flux controller to
        // tail; say so honestly instead of fabricating a log view.
        <section className="flex flex-col gap-1.5" data-testid="reconciler-drill-nonflux">
          <p className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5 text-xs text-[var(--color-text-dim)]">
            Controller-log drill applies to the Flux reconciler kinds
            (HelmRelease / Kustomization / sources). {coords.kind} is a
            declarative reconciler — its live state and message above are the
            drill surface.
          </p>
        </section>
      )}
    </aside>
  )
}
