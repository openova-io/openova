/**
 * CloudComputePage — Sovereign Cloud / Compute landing page (P3 of
 * issue #309). Replaces the previous flat dump in CloudCompute.tsx.
 *
 * Renders a tile grid summarising the four resource types in the
 * Compute category: Clusters, vClusters, Node Pools, Worker Nodes.
 * Each tile is a <Link> to the per-resource list page.
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useCloud } from '../CloudPage'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'

interface ComputeTile {
  id: 'clusters' | 'vclusters' | 'node-pools' | 'worker-nodes'
  label: string
  tagline: string
}

const COMPUTE_TILES: readonly ComputeTile[] = [
  {
    id: 'clusters',
    label: 'Clusters',
    tagline: 'k3s / k8s control planes — one per region',
  },
  {
    id: 'vclusters',
    label: 'vClusters',
    tagline: 'Logical isolation per Sovereign tenant',
  },
  {
    id: 'node-pools',
    label: 'Node Pools',
    tagline: 'Worker pools grouped by SKU + role',
  },
  {
    id: 'worker-nodes',
    label: 'Worker Nodes',
    tagline: 'Individual VMs / kubelets reporting in',
  },
]

export function CloudComputePage() {
  const { deploymentId, data, isLoading } = useCloud()

  // Per-tile counts derived once from the shared infrastructure tree.
  const counts = useMemo(() => {
    const out: Record<ComputeTile['id'], number> = {
      'clusters': 0,
      'vclusters': 0,
      'node-pools': 0,
      'worker-nodes': 0,
    }
    if (!data) return out
    for (const region of data.topology.regions ?? []) {
      for (const cluster of region.clusters ?? []) {
        out['clusters'] += 1
        out['vclusters'] += cluster.vclusters?.length ?? 0
        out['node-pools'] += cluster.nodePools?.length ?? 0
        out['worker-nodes'] += cluster.nodes?.length ?? 0
      }
    }
    return out
  }, [data])

  return (
    <div data-testid="cloud-compute-page">
      <style>{CLOUD_LIST_CSS}</style>
      <header className="mb-3">
        <h1
          className="text-2xl font-bold text-[var(--color-text-strong)]"
          data-testid="cloud-compute-page-title"
        >
          Compute
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Clusters, vClusters, node pools and worker nodes for this Sovereign.
        </p>
      </header>

      {isLoading ? (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="cloud-compute-page-loading"
        >
          Loading compute resources…
        </div>
      ) : (
        <div className="cloud-list-tile-grid" data-testid="cloud-compute-page-tiles">
          {COMPUTE_TILES.map((tile) => (
            <Link
              key={tile.id}
              to={`/provision/$deploymentId/cloud/compute/${tile.id}` as never}
              params={{ deploymentId } as never}
              className="cloud-list-tile"
              data-testid={`cloud-compute-page-tile-${tile.id}`}
            >
              <div className="cloud-list-tile-name">
                <span>{tile.label}</span>
                <span
                  className="cloud-list-tile-count"
                  data-testid={`cloud-compute-page-tile-${tile.id}-count`}
                >
                  {counts[tile.id]}
                </span>
              </div>
              <p className="cloud-list-tile-tagline">{tile.tagline}</p>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}
