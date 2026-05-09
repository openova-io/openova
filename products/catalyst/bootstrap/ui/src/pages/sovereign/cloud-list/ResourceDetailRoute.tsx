/**
 * ResourceDetailRoute — TanStack Router adapter for ResourceDetailPage.
 * Reads $kind / $ns / $name / $tab from the route params, resolves the
 * deploymentId via PortalShell context (mothership) or
 * useResolvedDeploymentId (chroot), and renders ResourceDetailPage in
 * its target-state shape.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL shape
 * is owned by the router; this adapter is the single seam between the
 * router and the page component.
 */

import { useParams } from '@tanstack/react-router'

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useK8sCacheStream } from '@/widgets/architecture-graph/useK8sCacheStream'

import { ResourceDetailPage } from './ResourceDetailPage'
import { parseTabFromPath } from './resource.api'

export function ResourceDetailRoute() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    kind?: string
    ns?: string
    name?: string
    tab?: string
  }
  const { deploymentId: chrootDepId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? chrootDepId ?? ''
  const kind = params.kind ?? ''
  const ns = params.ns === '_' ? '' : params.ns ?? ''
  const name = params.name ?? ''
  const tab = parseTabFromPath(params.tab)

  const basePath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/cloud'
      : `/provision/${deploymentId}/cloud`

  // Subscribe to the page-level k8s SSE so EventsPanel sees Events.
  // ResourceDetailPage is rendered standalone (not inside CloudPage's
  // CloudContext), so we open the same shared subscription here. One
  // EventSource per page is the contract — adding the resource detail
  // page never adds a second connection because the user navigated AWAY
  // from CloudPage to reach this view.
  const { snapshot } = useK8sCacheStream(deploymentId, { enabled: !!deploymentId })
  // TODO (follow-up slice): wire whoami → claims.tier so the UI
  // mirrors the server-side tier-admin gate. For now we render the
  // buttons and let the server enforce — the user sees a forbidden
  // toast on click.
  const isTierAdmin = true

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
        isTierAdmin={isTierAdmin}
      />
    </div>
  )
}
