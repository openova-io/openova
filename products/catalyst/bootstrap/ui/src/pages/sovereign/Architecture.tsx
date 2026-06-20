/**
 * Architecture — Sovereign Cloud / Architecture sub-page (default
 * landing under /cloud), now the ONE unified Cloud-graph (#3958).
 *
 * P2 of issue openova-io/openova#309 replaced the legacy layered SVG with
 * a force-directed graph. #3958 folds the reconcilers (Flux + non-Flux,
 * from the /reconciliation endpoint — the deleted /reconciliation page's
 * data, the fetch is reused here) into the SAME canvas as typed nodes,
 * with three independent channels: shape=category, border=family,
 * fill=status.
 *
 * The body is delegated to `ArchitectureGraphPage` in the
 * `widgets/architecture-graph` package; this file is the thin adapter
 * over `useCloud()` that ALSO polls the reconciliation set.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — every UI affordance ships in this first cut.
 *   #4 (never hardcode) — every visual token comes from CSS variables
 *      or the type/edge palette in widgets/architecture-graph/types.ts.
 */

import { useQuery } from '@tanstack/react-query'
import { ArchitectureGraphPage, type K8sSnapshot } from '@/widgets/architecture-graph'
import { fetchReconciliationDAG, type ReconciliationDAG } from '@/lib/reconciliation.api'
import { useCloud } from './CloudPage'

/** Live poll cadence for the reconciler set — a reconcile loop is
 *  continuous; a few seconds keeps the unified graph fresh through
 *  convergence without hammering the API. */
const RECON_POLL_MS = 4_000

export function Architecture() {
  const {
    deploymentId,
    data,
    isLoading,
    isError,
    refetch,
    k8sSnapshot,
    k8sRevision,
  } = useCloud()

  // Reconciler set — Flux HelmReleases + Kustomizations AND the
  // declarative non-Flux reconcilers (cert-manager / CNPG /
  // External-Secrets / Catalyst CRs). The same fetch the deleted
  // /reconciliation page used; here it feeds the unified canvas.
  const reconQuery = useQuery<ReconciliationDAG>({
    queryKey: ['reconciliation-dag', deploymentId],
    queryFn: () => fetchReconciliationDAG(deploymentId),
    enabled: !!deploymentId,
    refetchInterval: RECON_POLL_MS,
    staleTime: RECON_POLL_MS,
    placeholderData: (prev) => prev,
  })

  return (
    <ArchitectureGraphPage
      deploymentId={deploymentId}
      data={data}
      isLoading={isLoading}
      isError={isError}
      onRefetch={refetch}
      // Forward the shared SSE snapshot so the graph widget consumes
      // the SAME live snapshot the chip counts and every K8s list view
      // read from. Without this the widget would open a duplicate
      // EventSource and the per-page snapshot read by CloudListView /
      // K8sListPage would starve under the HTTP/1.1 connection budget,
      // leaving services / ingresses / deployments / statefulsets /
      // daemonsets / namespaces / nodes rendering "No X objects".
      k8sSnapshot={(k8sSnapshot as unknown as K8sSnapshot | null) ?? null}
      k8sRevision={k8sRevision}
      reconcilers={reconQuery.data?.nodes ?? null}
    />
  )
}
