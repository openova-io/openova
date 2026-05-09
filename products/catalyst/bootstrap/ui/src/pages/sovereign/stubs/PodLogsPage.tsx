/**
 * PodLogsPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * URL contract:
 *   /app/$deploymentId/resources/pods/$ns/$name/logs           — live logs
 *   /app/$deploymentId/resources/pods/$ns/$name/logs?previous=true
 *
 * Stub renders chrome shell with pod identity tokens so the route
 * resolves to a 200 with the expected page-title text. Real log-stream
 * wiring lands in subsequent slices.
 */

import { useParams, useSearch } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface PodLogsSearch {
  previous?: boolean
}

export function PodLogsPage() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    ns?: string
    name?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const ns = params.ns ?? ''
  const name = params.name ?? ''
  const search = useSearch({ strict: false }) as PodLogsSearch
  const previous = search.previous ?? false

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="pod-logs-page">
        <h2 className="text-xl font-semibold text-[oklch(85%_0.01_250)]">Logs</h2>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Logs for <code>{ns}/{name}</code> in <code>{deploymentId}</code>{' '}
          {previous ? '(previous container instance)' : ''} — pending live stream.
        </p>
        <pre
          className="h-64 overflow-auto rounded bg-black p-2 font-mono text-xs text-green-400"
          data-testid="pod-log-stream"
        >
          (log stream not yet implemented — wordpress)
        </pre>
      </div>
    </PortalShell>
  )
}
