/**
 * ControllerLogsPane — the owning-Flux-controller log tail for ONE reconciler
 * object, extracted from ReconcileTab so the k9s-style **Logs** tab can render
 * the same thing (UAT row 195, issue #3996).
 *
 * WHY THIS EXISTS. `LogsTabContent` used to answer every non-Pod kind with
 *
 *   "Logs are streamed per-Pod. Drill into the Tree tab and pick a child Pod
 *    to see logs."
 *
 * which is true for a Deployment and WRONG for a Flux reconciler: a
 * Kustomization owns no Pod to drill into, and the logs that describe it are
 * the kustomize-controller's, which the platform already serves at
 *
 *   GET /api/v1/deployments/{id}/reconcilers/{kind}/{ns}/{name}/logs
 *
 * The endpoint was verified against hw292 read-only before this change:
 * `Kustomization/flux-system/bootstrap-kit` returns 200 with
 * `controller=kustomize-controller` and 33 lines, a HelmRelease returns a
 * DIFFERENT controller (helm-controller), and a bogus object returns 0 — so it
 * discriminates on object, controller and kind. The operator drilling a
 * reconciler and clicking **Logs** was the only thing that could not see it.
 *
 * The pane is presentation only; the fetch contract lives in
 * lib/reconciler-manage.api.ts and is shared with ReconcileTab, so both
 * surfaces tail the same window (LOG_TAIL_LINES) and cannot drift.
 */

import { useQuery } from '@tanstack/react-query'
import { fetchReconcilerLogs } from '@/lib/reconciler-manage.api'

/** Poll interval for the live tail — the reconcile loop is minutes-scale. */
export const CONTROLLER_LOG_POLL_MS = 5_000

export interface ControllerLogsPaneProps {
  deploymentId: string
  /** PascalCase K8s Kind the management endpoints address (wireKindFor). */
  wireKind: string
  ns: string
  name: string
  /** testid for the <pre> that holds the lines. */
  testId: string
}

export function ControllerLogsPane({
  deploymentId,
  wireKind,
  ns,
  name,
  testId,
}: ControllerLogsPaneProps) {
  const logsQuery = useQuery({
    // Same key shape as ReconcileTab's so the two surfaces share one cache
    // entry instead of double-polling the controller.
    queryKey: ['reconciler-logs', deploymentId, wireKind, ns, name],
    queryFn: () => fetchReconcilerLogs(deploymentId, wireKind, ns || '', name),
    enabled: !!deploymentId && !!wireKind && !!name,
    refetchInterval: CONTROLLER_LOG_POLL_MS,
    staleTime: CONTROLLER_LOG_POLL_MS,
    placeholderData: (prev) => prev,
  })

  return (
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
        data-testid={testId}
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
  )
}
