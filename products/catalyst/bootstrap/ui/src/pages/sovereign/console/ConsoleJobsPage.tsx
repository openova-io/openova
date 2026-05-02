/**
 * ConsoleJobsPage — Sovereign Console /console/jobs
 *
 * Shows the history of provisioning jobs (HelmRelease install runs) on
 * this Sovereign cluster. Reuses the same table shape as JobsPage.tsx
 * but targets /api/v1/sovereign/jobs (no deploymentId param).
 *
 * Phase 8b: renders a placeholder table until the sovereign jobs endpoint
 * is wired by Agent C.
 *
 * Related: GitHub issue #607
 */

import { Clipboard } from 'lucide-react'

export function ConsoleJobsPage() {
  return (
    <div data-testid="console-jobs-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Jobs</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Provisioning and maintenance job history for this Sovereign.
        </p>
      </div>

      {/* Placeholder — Phase 4 ticket wires the API */}
      <div
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-10 text-center"
        data-testid="jobs-placeholder"
      >
        <Clipboard className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
        <p className="text-sm font-medium text-[var(--color-text)]">Jobs history</p>
        <p className="mt-1 text-xs text-[var(--color-text-dim)]">
          /api/v1/sovereign/jobs integration pending (Phase 4).
        </p>
      </div>
    </div>
  )
}
