/**
 * ResourcesSearchPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * Cross-resource k8s search across the catalyst-cache. URL contract:
 *   /app/$deploymentId/resources/search?q=<query>
 *
 * Stub renders a chrome shell with the query echoed back so the route
 * resolves to a 200 with the expected query token. Real search wiring
 * lands in subsequent slices.
 */

import { useParams, useSearch } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface ResourcesSearchSearch {
  q?: string
}

export function ResourcesSearchPage() {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const search = useSearch({ strict: false }) as ResourcesSearchSearch
  const q = search.q ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-search-page">
        <h2 className="text-xl font-semibold text-[oklch(85%_0.01_250)]">Search</h2>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Search results for <code>{q || '(empty query)'}</code> in <code>{deploymentId}</code>{' '}
          (pending live data).
        </p>
      </div>
    </PortalShell>
  )
}
