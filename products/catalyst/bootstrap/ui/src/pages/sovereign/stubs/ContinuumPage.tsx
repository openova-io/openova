/**
 * ContinuumPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * The Continuum DR feature (PostgreSQL HA across regions, switchover,
 * audit trail, RPO/RTO knobs) is owned by other Fix Authors. This stub
 * mounts the target-state routes so URLs resolve to a 200 with the
 * canonical "Continuum"/"DR" page-title token + the continuumId from
 * the URL — enough for the test matrix to confirm the SPA route is
 * wired. Real data wiring lands in subsequent slices.
 *
 * URL shapes mounted:
 *   /app/$deploymentId/continuum                          — fleet list
 *   /app/$deploymentId/continuum/$continuumId             — overview
 *   /app/$deploymentId/continuum/$continuumId/audit       — switchover audit
 *   /app/$deploymentId/continuum/$continuumId/settings    — RPO/RTO knobs
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the label/sub
 * routes are derived from URL params, not static.
 */

import { useParams } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface ContinuumPageProps {
  /** Sub-page mode: list / overview / audit / settings. */
  mode?: 'list' | 'overview' | 'audit' | 'settings'
}

export function ContinuumPage({ mode = 'list' }: ContinuumPageProps = {}) {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    continuumId?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const continuumId = params.continuumId ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Continuum">
      <div className="p-6 space-y-4" data-testid="continuum-page">
        <h2 className="text-xl font-semibold text-[var(--color-text)]">DR — Continuum</h2>
        {mode === 'list' && (
          <div data-testid="continuum-list">
            <p className="text-sm text-[oklch(55%_0.01_250)]">Continuum DR list (pending live data).</p>
            <ul className="mt-2 text-sm">
              <li><code>cont-omantel</code></li>
            </ul>
          </div>
        )}
        {mode === 'overview' && (
          <div data-testid="continuum-overview">
            <p className="text-sm text-[oklch(55%_0.01_250)]">
              Topology + WAL replication + Switchover for <code>{continuumId}</code> (pending live data).
            </p>
            <ul className="mt-2 text-sm">
              <li>Topology</li>
              <li>WAL</li>
              <li>Switchover (last status: <code>completed</code>)</li>
            </ul>
          </div>
        )}
        {mode === 'audit' && (
          <div data-testid="continuum-audit">
            <p className="text-sm text-[oklch(55%_0.01_250)]">
              Switchover audit trail for <code>{continuumId}</code> (pending live data).
            </p>
            <table className="mt-2 text-sm">
              <thead><tr><th>Event</th><th>Duration</th></tr></thead>
              <tbody><tr><td>Switchover</td><td>—</td></tr></tbody>
            </table>
          </div>
        )}
        {mode === 'settings' && (
          <div data-testid="continuum-settings">
            <p className="text-sm text-[oklch(55%_0.01_250)]">
              RPO / RTO knobs for <code>{continuumId}</code> (pending live data).
            </p>
            <form className="mt-2 space-y-2 text-sm">
              <label className="block">RPO (seconds)<input className="ml-2 w-24 rounded border px-1" defaultValue="30" /></label>
              <label className="block">RTO (seconds)<input className="ml-2 w-24 rounded border px-1" defaultValue="60" /></label>
              <button type="button" className="rounded bg-[--color-brand-500] px-3 py-1 text-white">Save</button>
            </form>
          </div>
        )}
      </div>
    </PortalShell>
  )
}
