/**
 * ResourcesApplyPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * The kubectl-apply-from-the-browser surface is an upcoming feature in
 * the Resources family. This stub mounts the route so URLs resolve to
 * a 200 with the canonical "Resources / Apply" page-title token. Real
 * editor wiring lands in subsequent slices.
 *
 * URL: /app/$deploymentId/resources/apply
 */

import { useParams } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export function ResourcesApplyPage() {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-apply-page">
        <h2 className="text-xl font-semibold text-[oklch(85%_0.01_250)]">Apply</h2>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Apply YAML manifests to <code>{deploymentId}</code> (pending live editor).
        </p>
        <textarea
          className="h-64 w-full rounded border border-[--color-surface-border] bg-[--color-surface-1] p-2 font-mono text-xs"
          placeholder="apiVersion: v1&#10;kind: ConfigMap&#10;..."
          data-testid="apply-yaml-editor"
        />
      </div>
    </PortalShell>
  )
}
