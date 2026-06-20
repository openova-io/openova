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
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  fetchReconcilerLogs,
  triggerReconcilerAction,
  type ReconcilerActionKind,
} from '@/lib/reconciler-manage.api'

const LOG_POLL_MS = 5_000

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

/** Read the Ready condition off a Flux object → a display state string. */
function readyStateOf(obj: K8sObject | null, suspended: boolean): string {
  if (suspended) return 'Suspended'
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
}

export function ReconcileTab({ deploymentId, apiKind, ns, name, obj, isTierAdmin = true }: ReconcileTabProps) {
  const qc = useQueryClient()
  const [actionMsg, setActionMsg] = useState<string>('')
  const wireKind = useMemo(() => wireKindFor(apiKind), [apiKind])

  const suspended = useMemo(
    () => Boolean((obj?.spec as { suspend?: boolean } | undefined)?.suspend),
    [obj],
  )
  const state = readyStateOf(obj, suspended)
  const revision = revisionOf(obj)
  const message = readyMessageOf(obj)

  const logsQuery = useQuery({
    queryKey: ['reconciler-logs', deploymentId, wireKind, ns, name],
    queryFn: () => fetchReconcilerLogs(deploymentId, wireKind, ns || '', name),
    enabled: !!deploymentId && !!wireKind && !!name,
    refetchInterval: LOG_POLL_MS,
    staleTime: LOG_POLL_MS,
    placeholderData: (prev) => prev,
  })

  const action = useMutation({
    mutationFn: (a: ReconcilerActionKind) =>
      triggerReconcilerAction(deploymentId, wireKind, ns || '', name, a),
    onSuccess: (res) => {
      setActionMsg(`${res.action} requested by ${res.requestedBy}`)
      void qc.invalidateQueries({ queryKey: ['reconciler-logs', deploymentId, wireKind, ns, name] })
      void qc.invalidateQueries({ queryKey: ['reconciliation-dag', deploymentId] })
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
        <KV label="Suspended" value={suspended ? 'yes' : 'no'} testId="reconcile-kv-suspended" />
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
          {suspended ? (
            <button
              type="button"
              disabled={!isTierAdmin || action.isPending}
              onClick={() => action.mutate('resume')}
              data-testid="reconcile-action-resume"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-semibold text-[var(--color-text)] hover:bg-[var(--color-surface-hover)] disabled:opacity-50"
            >
              Resume
            </button>
          ) : (
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
          {actionMsg ? (
            <span className="text-xs text-[var(--color-text-dim)]" data-testid="reconcile-action-msg">
              {actionMsg}
            </span>
          ) : null}
        </div>
      </div>

      {/* Logs — the owning controller's output filtered to this object. */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
            Controller logs
            {logsQuery.data ? (
              <span className="ml-1 normal-case text-[var(--color-accent)]"> · {logsQuery.data.controller}</span>
            ) : null}
          </span>
          {logsQuery.isFetching ? (
            <span className="text-[10px] uppercase tracking-wide text-emerald-400">live</span>
          ) : null}
        </div>
        <pre
          data-testid="reconcile-tab-logs"
          className="max-h-[48vh] min-h-[200px] overflow-auto rounded-md border border-[var(--color-border)] bg-[var(--log-viewer-bg,#0D1117)] p-3 font-mono text-xs text-[#c9d1d9]"
        >
          {logsQuery.isLoading ? (
            <span className="text-[#6e7681]">Loading controller logs…</span>
          ) : logsQuery.isError ? (
            <span className="text-[#6e7681]">
              Could not load logs: {(logsQuery.error as Error)?.message}
            </span>
          ) : (logsQuery.data?.lines.length ?? 0) === 0 ? (
            <span className="text-[#6e7681]">
              No recent controller lines mention this object.
            </span>
          ) : (
            logsQuery.data!.lines.map((l) => (
              <div key={l.lineNumber} className="flex gap-3">
                <span className="w-9 flex-shrink-0 select-none text-right text-[#6e7681]">{l.lineNumber}</span>
                <span className="whitespace-pre-wrap break-words">{l.message}</span>
              </div>
            ))
          )}
        </pre>
      </div>
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
