/**
 * ResourcesListPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * URL contracts:
 *   /app/$deploymentId/resources                       — kind landing
 *   /app/$deploymentId/resources/$kind                 — list of <kind>
 *   /app/$deploymentId/resources/$kind/$ns             — list of <kind> in <ns>
 *
 * The full Cloud / k8s-cache table view lives in CloudPage. This stub
 * mounts the path-based target-state URLs so they resolve to a 200 with
 * a canonical "Resources" page-title token + the kind/ns from the URL —
 * other Fix Authors will replace the body with the live tables.
 */

import { useParams, useSearch } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface ResourcesListSearch {
  search?: string
  region?: string
}

export function ResourcesListPage() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    kind?: string
    ns?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const kind = params.kind ?? 'all'
  const ns = params.ns ?? null
  const search = useSearch({ strict: false }) as ResourcesListSearch

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-list-page">
        <h2 className="text-xl font-semibold text-[oklch(85%_0.01_250)]">{kind}</h2>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Cluster <code>{deploymentId}</code>
          {ns && (<> · namespace <code>{ns}</code></>)}
          {search.search && (<> · search=<code>{search.search}</code></>)}
          {search.region && (<> · region=<code>{search.region}</code></>)}
        </p>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Resource list (pending live data binding).
        </p>
      </div>
    </PortalShell>
  )
}
