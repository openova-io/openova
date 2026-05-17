/**
 * RuntimeAlertsPage — standalone Falco runtime-security alerts surface
 * (slice C11-008, Wave-2 Family-E).
 *
 * Routes:
 *   /admin/compliance/runtime  (mothership)
 *   /compliance/runtime        (chroot Sovereign Console)
 *
 * The FAIL evidence for C11-008 was "/compliance/runtime returns Not
 * Found (404) — no global Falco event feed surfaced". This page
 * resolves that: it mounts the FalcoAlerts widget standalone so the
 * operator can deep-link to "runtime alerts" without navigating to
 * the full SRE / Security dashboard.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) — deployment
 * id resolves via the shared `useResolvedDeploymentId` hook.
 */

import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { FalcoAlerts } from './FalcoAlerts'

export interface RuntimeAlertsPageProps {
  /** Test seam — deployment id override. */
  deploymentIdOverride?: string
}

export function RuntimeAlertsPage({ deploymentIdOverride }: RuntimeAlertsPageProps = {}) {
  const { deploymentId: resolved } = useResolvedDeploymentId()
  const deploymentId = deploymentIdOverride ?? resolved ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Runtime security alerts">
      <div data-testid="runtime-alerts-page" className="mx-auto max-w-7xl px-6 py-4">
        <nav
          aria-label="breadcrumb"
          data-testid="runtime-alerts-breadcrumb"
          className="mb-2 text-xs text-[var(--color-text-dim)]"
        >
          <span>Compliance</span>
          <span className="mx-1">/</span>
          <span className="text-[var(--color-text)]">Runtime</span>
        </nav>
        <h1
          className="mb-1 text-xl font-semibold text-[var(--color-text-strong)]"
          data-testid="runtime-alerts-title"
        >
          Runtime security alerts
        </h1>
        <p className="mb-4 text-sm text-[var(--color-text-dim)]" data-testid="runtime-alerts-subtitle">
          Falco runtime-security events from the per-node DaemonSet. Streams kernel-syscall
          alerts via Falcosidekick → Kubernetes Events. Filter by priority chip; widen to
          NOTICE/INFO/DEBUG to see lower-severity activity.
        </p>
        <FalcoAlerts sovereignId={deploymentId} />
      </div>
    </PortalShell>
  )
}
