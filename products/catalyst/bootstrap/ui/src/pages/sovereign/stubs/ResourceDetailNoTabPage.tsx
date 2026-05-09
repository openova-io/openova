/**
 * ResourceDetailNoTabPage — adapter for the target-state URL shape
 *   /app/$deploymentId/resources/$kind/$ns/$name
 * (qa-loop iter-6 Cluster-A `spa-target-state-routes-missing`).
 *
 * The canonical ResourceDetailRoute under the `/cloud` tree expects a
 * 4th `$tab` segment (`/cloud/resource/$kind/$ns/$name/$tab`). The
 * matrix asserts the 3-segment shape (no tab) — this adapter mirrors
 * the canonical seam and forwards into the same ResourceDetailPage
 * with the default tab pre-selected.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the tab
 * default comes from `parseTabFromPath(undefined)` — the same parser
 * the canonical route uses — so the two routes can never drift on
 * which tab is "default".
 */

import { useParams } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { ResourceDetailPage } from '../cloud-list/ResourceDetailPage'
import { parseTabFromPath } from '../cloud-list/resource.api'
import { useK8sCacheStream } from '@/widgets/architecture-graph/useK8sCacheStream'

export function ResourceDetailNoTabPage() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    kind?: string
    ns?: string
    name?: string
  }
  const { deploymentId: chrootDepId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? chrootDepId ?? ''
  const kind = params.kind ?? ''
  const ns = params.ns === '_' ? '' : params.ns ?? ''
  const name = params.name ?? ''
  const tab = parseTabFromPath(undefined)

  const { snapshot } = useK8sCacheStream(deploymentId, { enabled: !!deploymentId })

  const basePath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/cloud'
      : `/provision/${deploymentId}/cloud`

  return (
    <div className="mx-auto max-w-5xl px-4 py-6">
      <ResourceDetailPage
        deploymentId={deploymentId}
        basePath={basePath}
        kind={kind}
        ns={ns}
        name={name}
        tab={tab}
        k8sSnapshot={snapshot}
        isTierAdmin
      />
    </div>
  )
}
