/**
 * ConsoleCloudPage — Sovereign Console /console/cloud
 *
 * Topology / architecture / infrastructure view for this Sovereign.
 * Reuses the CloudPage visual concept without the deploymentId param.
 *
 * Phase 8b placeholder — full graph wiring is Phase 4 work.
 *
 * Related: GitHub issue #607
 */

import { Cloud } from 'lucide-react'

export function ConsoleCloudPage() {
  return (
    <div data-testid="console-cloud-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Cloud</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Infrastructure topology and resource explorer for this Sovereign.
        </p>
      </div>

      {/* Placeholder — Phase 4 wires the topology graph */}
      <div
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-10 text-center"
        data-testid="cloud-placeholder"
      >
        <Cloud className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
        <p className="text-sm font-medium text-[var(--color-text)]">Cloud topology</p>
        <p className="mt-1 text-xs text-[var(--color-text-dim)]">
          Topology graph integration pending (Phase 4).
        </p>
      </div>
    </div>
  )
}
