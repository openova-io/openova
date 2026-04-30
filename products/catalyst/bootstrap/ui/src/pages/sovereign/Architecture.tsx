/**
 * Architecture — Sovereign Cloud / Architecture sub-page (default
 * landing under /cloud).
 *
 * P2 of issue openova-io/openova#309: replaces the legacy layered SVG
 * canvas with a force-directed Architecture graph. Containment is
 * just one of several edge types (`contains`, `runs-on`, `routes-to`,
 * `attached-to`, `peers-with`) — see the founder verbatim in #309
 * ("forget about the containment, just show it as another type of
 * relation").
 *
 * The body of this page is delegated to `ArchitectureGraphPage` in the
 * `widgets/architecture-graph` package; this file is a thin adapter
 * over `useCloud()`. The legacy `topologyLayout` SVG path is gone.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — every UI affordance ships in this first cut.
 *   #4 (never hardcode) — every visual token comes from CSS variables
 *      or the type/edge palette in widgets/architecture-graph/types.ts.
 */

import { ArchitectureGraphPage } from '@/widgets/architecture-graph'
import { useCloud } from './CloudPage'

export function Architecture() {
  const { deploymentId, data, isLoading, isError, refetch } = useCloud()
  return (
    <ArchitectureGraphPage
      deploymentId={deploymentId}
      data={data}
      isLoading={isLoading}
      isError={isError}
      onRefetch={refetch}
    />
  )
}
