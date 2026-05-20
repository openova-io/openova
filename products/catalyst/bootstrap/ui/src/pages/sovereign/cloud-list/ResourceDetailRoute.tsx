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

import { useMemo } from 'react'
import { useParams, useNavigate } from '@tanstack/react-router'

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { GRAPH_K8S_KINDS, useK8sCacheStream } from '@/widgets/architecture-graph/useK8sCacheStream'

import { ResourceDetailPage } from './ResourceDetailPage'
import { parseTabFromPath, resourceDetailHref, type ResourceDetailTab } from './resource.api'

/**
 * Kinds the resource-detail SSE subscribes to. The default
 * `GRAPH_K8S_KINDS` (used by CloudPage's canvas) intentionally omits
 * `event` because the canvas does not render Events as nodes — adding
 * the high-cardinality Event stream there would blow up the snapshot
 * for every operator browsing the cloud-list page.
 *
 * The resource-detail view IS the page that surfaces Events (the
 * EventsPanel tab #1099 Slice R4), so the focused detail subscription
 * adds `event` on top of the graph kinds. The server-side k8scache
 * Factory already registers `event` (events.k8s.io/v1) per
 * `products/catalyst/bootstrap/api/internal/k8scache/kinds.go:155`.
 *
 * Without this addition, EventsPanel never sees any event-keyed
 * snapshot entry and always renders its empty-state — the upstream
 * was dark even though the panel itself, the SSE encoder, and the
 * server-side registry all supported the wire. Closing that gap is
 * the smallest fix that flips Events from theater to operator-visible.
 */
const RESOURCE_DETAIL_KINDS: readonly string[] = Object.freeze([
  ...GRAPH_K8S_KINDS,
  'event',
])

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
  //
  // The kinds list MUST include `event` — see RESOURCE_DETAIL_KINDS
  // doc above for why we extend GRAPH_K8S_KINDS instead of taking
  // the default. The array reference is memoised so the SSE effect
  // doesn't tear down + reconnect on every render.
  const kinds = useMemo(() => RESOURCE_DETAIL_KINDS, [])
  const { snapshot } = useK8sCacheStream(deploymentId, {
    enabled: !!deploymentId,
    kinds,
  })
  // Render-then-enforce: the buttons mount unconditionally and the
  // server-side tier-admin gate is the authoritative check. The
  // useWhoami → claims.tier client-side mirror is a UX-only nicety
  // (avoids the post-click forbidden toast on un-privileged tiers);
  // the server gate is the source of truth and remains in place.
  const isTierAdmin = true

  // SPA in-place tab navigation — avoids the previous
  // `window.location.assign` codepath that hard-reloaded the page on
  // every tab click (which dropped in-flight resource fetches +
  // WebSocket log streams, causing the operator-visible "tab unclickable
  // before drift" pattern caught by founder #5 on t10).
  const navigate = useNavigate()
  const onTabChange = (next: ResourceDetailTab) => {
    navigate({
      to: resourceDetailHref(basePath, kind, ns || undefined, name, next) as never,
      replace: false,
    })
  }

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
        onTabChange={onTabChange}
      />
    </div>
  )
}
