/**
 * InfrastructureCompute — Compute tab of the Infrastructure surface.
 * Two card sections: Clusters + Worker Nodes.
 *
 * Per founder spec: "compute (clusters and worker nodes)".
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall): the empty state is
 * the canonical empty state — never placeholder data. The cards render
 * from real backend data the moment it arrives.
 */

import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  getCompute,
  type ClusterItem,
  type ComputeResponse,
  type NodeItem,
} from '@/lib/infrastructure.types'

const STALE_MS = 30_000

interface InfrastructureComputeProps {
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialDataOverride?: ComputeResponse
}

export function InfrastructureCompute({
  initialDataOverride,
}: InfrastructureComputeProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/infrastructure/compute' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const query = useQuery<ComputeResponse>({
    queryKey: ['infra-compute', deploymentId],
    queryFn: () => getCompute(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
  })

  const data = initialDataOverride ?? query.data
  const isLoading = !initialDataOverride && query.isLoading && !data
  const clusters = data?.clusters ?? []
  const nodes = data?.nodes ?? []
  const isEmpty = !isLoading && clusters.length === 0 && nodes.length === 0

  return (
    <div data-testid="infrastructure-compute">
      {isLoading && (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="infrastructure-compute-loading"
        >
          Loading compute resources…
        </div>
      )}

      {isEmpty && !query.isError && (
        <div className="infra-empty" data-testid="infrastructure-compute-empty">
          <p className="title">No clusters or worker nodes yet.</p>
          <p className="sub">
            Once the Sovereign cluster comes up, every k3s cluster and node VM
            will appear here.
          </p>
        </div>
      )}

      {!isEmpty && (
        <>
          <section className="infra-section" data-testid="infrastructure-clusters-section">
            <h2>
              Clusters <span className="count" data-testid="infrastructure-clusters-count">{clusters.length}</span>
            </h2>
            {clusters.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No clusters reported.
              </p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-clusters-grid">
                {clusters.map((c) => <ClusterCard key={c.id} cluster={c} />)}
              </div>
            )}
          </section>

          <section className="infra-section" data-testid="infrastructure-nodes-section">
            <h2>
              Worker Nodes <span className="count" data-testid="infrastructure-nodes-count">{nodes.length}</span>
            </h2>
            {nodes.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No worker nodes reported.
              </p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-nodes-grid">
                {nodes.map((n) => <NodeCard key={n.id} node={n} />)}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function ClusterCard({ cluster }: { cluster: ClusterItem }) {
  return (
    <div
      className="infra-card"
      data-status={cluster.status}
      data-testid={`infrastructure-cluster-card-${cluster.id}`}
    >
      <span className="infra-card-status" data-status={cluster.status}>
        {cluster.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{cluster.name}</span>
        <span className="infra-card-kind">cluster</span>
      </div>
      <div className="infra-card-row"><span>Control plane</span><span className="v">{cluster.controlPlane}</span></div>
      <div className="infra-card-row"><span>Version</span><span className="v">{cluster.version}</span></div>
      <div className="infra-card-row"><span>Region</span><span className="v">{cluster.region}</span></div>
      <div className="infra-card-row"><span>Nodes</span><span className="v">{cluster.nodeCount}</span></div>
    </div>
  )
}

function NodeCard({ node }: { node: NodeItem }) {
  return (
    <div
      className="infra-card"
      data-status={node.status}
      data-testid={`infrastructure-node-card-${node.id}`}
    >
      <span className="infra-card-status" data-status={node.status}>
        {node.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{node.name}</span>
        <span className="infra-card-kind">{node.role}</span>
      </div>
      <div className="infra-card-row"><span>SKU</span><span className="v">{node.sku}</span></div>
      <div className="infra-card-row"><span>Region</span><span className="v">{node.region}</span></div>
      <div className="infra-card-row"><span>IP</span><span className="v">{node.ip}</span></div>
    </div>
  )
}
