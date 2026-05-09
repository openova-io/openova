/**
 * LogsTab — placeholder for the EPIC-4 logs / events stream view (per
 * the EPIC-2 master brief §O3). Renders a "Coming in EPIC-4" notice so
 * the AppDetail tab set is complete in EPIC-2 (target-state shape per
 * docs/INVIOLABLE-PRINCIPLES.md #1) without pre-empting EPIC-4's
 * design.
 */

export interface LogsTabProps {
  applicationName: string
}

export function LogsTab({ applicationName }: LogsTabProps) {
  return (
    <div className="logs-tab" data-testid="app-logs-tabpanel">
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-center">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Logs / Events</h3>
        <p className="text-xs text-[var(--color-text-dim)]">
          Live stream of Application logs + Kubernetes events for{' '}
          <code className="font-mono text-[var(--color-text)]">{applicationName}</code>.
        </p>
        <p className="mt-3 text-xs italic text-[var(--color-text-dim)]" data-testid="app-logs-tabpanel-coming">
          Coming in EPIC-4.
        </p>
      </div>
    </div>
  )
}
