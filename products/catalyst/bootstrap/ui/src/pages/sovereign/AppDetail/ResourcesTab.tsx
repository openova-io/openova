/**
 * ResourcesTab — placeholder for the EPIC-4 k9s-style live resource
 * view (per the EPIC-2 master brief §O3). Renders a "Coming in EPIC-4"
 * notice so the AppDetail tab set is complete in EPIC-2 (target-state
 * shape per docs/INVIOLABLE-PRINCIPLES.md #1) without pre-empting
 * EPIC-4's design.
 */

export interface ResourcesTabProps {
  applicationName: string
}

export function ResourcesTab({ applicationName }: ResourcesTabProps) {
  return (
    <div className="resources-tab" data-testid="app-resources-tab">
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-center">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Resources</h3>
        <p className="text-xs text-[var(--color-text-dim)]">
          Live k9s-style view of the Pods / Services / HPAs / PVCs backing{' '}
          <code className="font-mono text-[var(--color-text)]">{applicationName}</code>.
        </p>
        <p className="mt-3 text-xs italic text-[var(--color-text-dim)]" data-testid="app-resources-tab-coming">
          Coming in EPIC-4.
        </p>
      </div>
    </div>
  )
}
