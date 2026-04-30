/**
 * InfrastructureCompute — Compute tab. Flat table grouped by [Cluster ·
 * Node Pool], reads off the shared infrastructure tree provided by
 * InfrastructurePage.
 *
 * Per founder spec (issue #228): "Compute — flat table grouped
 * [Cluster · Node Pool], each row links back to topology. Bulk
 * actions: scale, drain."
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useInfrastructure } from './InfrastructurePage'
import {
  ScalePoolModal,
  ChangeSKUModal,
  AddNodePoolModal,
  NodeActionConfirm,
} from '@/components/CrudModals'
import type { ClusterSpec, NodePoolSpec, NodeSpec, RegionSpec } from '@/lib/infrastructure.types'
import type { CloudProvider } from '@/entities/deployment/model'

interface PoolRow {
  pool: NodePoolSpec
  cluster: ClusterSpec
  region: RegionSpec
}

interface NodeRow {
  node: NodeSpec
  cluster: ClusterSpec
  region: RegionSpec
}

export function InfrastructureCompute() {
  const { deploymentId, data, isLoading } = useInfrastructure()

  const { pools, nodes } = useMemo(() => {
    const pools: PoolRow[] = []
    const nodes: NodeRow[] = []
    if (!data) return { pools, nodes }
    for (const region of data.topology.regions) {
      for (const cluster of region.clusters) {
        for (const pool of cluster.nodePools) pools.push({ pool, cluster, region })
        for (const node of cluster.nodes) nodes.push({ node, cluster, region })
      }
    }
    return { pools, nodes }
  }, [data])

  // Bulk-action selection state (operator picks rows + clicks an
  // action in the bulk strip).
  const [selectedPools, setSelectedPools] = useState<string[]>([])
  const [selectedNodes, setSelectedNodes] = useState<string[]>([])

  const [scalePool, setScalePool] = useState<PoolRow | null>(null)
  const [changeSku, setChangeSku] = useState<PoolRow | null>(null)
  const [drainNode, setDrainNode] = useState<NodeRow | null>(null)
  const [addPoolFor, setAddPoolFor] = useState<{ cluster: ClusterSpec; provider: CloudProvider } | null>(null)

  const isEmpty = !isLoading && pools.length === 0 && nodes.length === 0

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

      {isEmpty && (
        <div className="infra-empty" data-testid="infrastructure-compute-empty">
          <p className="title">No clusters or worker nodes yet.</p>
          <p className="sub">
            Once the Sovereign cluster comes up, every k3s cluster and node VM
            will appear here.
          </p>
        </div>
      )}

      {!isEmpty && data && (
        <>
          <div className="infra-bulk-actions" data-testid="infrastructure-compute-bulk">
            <span className="label">Bulk · {selectedPools.length} pool{selectedPools.length === 1 ? '' : 's'} / {selectedNodes.length} node{selectedNodes.length === 1 ? '' : 's'}</span>
            <button
              type="button"
              data-testid="infrastructure-compute-bulk-scale"
              disabled={selectedPools.length !== 1}
              onClick={() => {
                const row = pools.find((p) => p.pool.id === selectedPools[0])
                if (row) setScalePool(row)
              }}
            >
              Scale
            </button>
            <button
              type="button"
              data-testid="infrastructure-compute-bulk-drain"
              disabled={selectedNodes.length !== 1}
              onClick={() => {
                const row = nodes.find((n) => n.node.id === selectedNodes[0])
                if (row) setDrainNode(row)
              }}
            >
              Drain
            </button>
          </div>

          <section className="infra-section" data-testid="infrastructure-pools-section">
            <h2>
              Node Pools <span className="count" data-testid="infrastructure-pools-count">{pools.length}</span>
            </h2>
            <FlatTable
              testId="infrastructure-pools-table"
              headers={['', 'Cluster', 'Pool', 'SKU', 'Replicas', 'Status', '']}
            >
              {pools.map(({ pool, cluster, region }) => (
                <tr key={pool.id} data-testid={`infrastructure-pool-row-${pool.id}`}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selectedPools.includes(pool.id)}
                      onChange={(e) => toggle(e.target.checked, pool.id, setSelectedPools)}
                      data-testid={`infrastructure-pool-row-${pool.id}-select`}
                    />
                  </td>
                  <td>
                    <Link
                      to={`/provision/$deploymentId/infrastructure/topology` as never}
                      params={{ deploymentId } as never}
                      data-testid={`infrastructure-pool-row-${pool.id}-cluster-link`}
                    >
                      {cluster.name}
                    </Link>
                    <div style={{ fontSize: '0.7rem', color: 'var(--color-text-dim)' }}>
                      {region.providerRegion}
                    </div>
                  </td>
                  <td style={{ fontFamily: 'monospace' }}>{pool.id}</td>
                  <td>{pool.sku}</td>
                  <td>{pool.replicas}</td>
                  <td>
                    <StatusBadge status={pool.status} />
                  </td>
                  <td style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      onClick={() => setScalePool({ pool, cluster, region })}
                      data-testid={`infrastructure-pool-row-${pool.id}-scale`}
                      style={rowBtn}
                    >
                      Scale
                    </button>
                    <button
                      type="button"
                      onClick={() => setChangeSku({ pool, cluster, region })}
                      data-testid={`infrastructure-pool-row-${pool.id}-change-sku`}
                      style={rowBtn}
                    >
                      Change SKU
                    </button>
                  </td>
                </tr>
              ))}
              {pools.length === 0 && (
                <tr>
                  <td colSpan={7} style={{ color: 'var(--color-text-dim)', textAlign: 'center', padding: 12 }}>
                    No pools reported.
                  </td>
                </tr>
              )}
            </FlatTable>

            {/* Per-cluster Add Pool buttons */}
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 8 }}>
              {data.topology.regions.flatMap((region) =>
                region.clusters.map((cluster) => (
                  <button
                    key={cluster.id}
                    type="button"
                    style={{
                      ...rowBtn,
                      borderColor: 'var(--color-accent)',
                      color: 'var(--color-accent)',
                    }}
                    onClick={() =>
                      setAddPoolFor({
                        cluster,
                        provider: region.provider as CloudProvider,
                      })
                    }
                    data-testid={`infrastructure-pool-add-for-${cluster.id}`}
                  >
                    + Add pool to {cluster.name}
                  </button>
                )),
              )}
            </div>
          </section>

          <section className="infra-section" data-testid="infrastructure-nodes-section">
            <h2>
              Worker Nodes <span className="count" data-testid="infrastructure-nodes-count">{nodes.length}</span>
            </h2>
            <FlatTable
              testId="infrastructure-nodes-table"
              headers={['', 'Cluster', 'Node', 'SKU', 'Role', 'IP', 'Status', '']}
            >
              {nodes.map(({ node, cluster }) => (
                <tr key={node.id} data-testid={`infrastructure-node-row-${node.id}`}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selectedNodes.includes(node.id)}
                      onChange={(e) => toggle(e.target.checked, node.id, setSelectedNodes)}
                      data-testid={`infrastructure-node-row-${node.id}-select`}
                    />
                  </td>
                  <td>{cluster.name}</td>
                  <td>{node.name}</td>
                  <td>{node.sku}</td>
                  <td>{node.role}</td>
                  <td style={{ fontFamily: 'monospace' }}>{node.ip}</td>
                  <td>
                    <StatusBadge status={node.status} />
                  </td>
                  <td style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      onClick={() => setDrainNode({ node, cluster, region: data.topology.regions.find((r) => r.clusters.some((c) => c.id === cluster.id))! })}
                      data-testid={`infrastructure-node-row-${node.id}-drain`}
                      style={rowBtn}
                    >
                      Drain
                    </button>
                  </td>
                </tr>
              ))}
              {nodes.length === 0 && (
                <tr>
                  <td colSpan={8} style={{ color: 'var(--color-text-dim)', textAlign: 'center', padding: 12 }}>
                    No nodes reported.
                  </td>
                </tr>
              )}
            </FlatTable>
          </section>
        </>
      )}

      {scalePool && (
        <ScalePoolModal
          open
          deploymentId={deploymentId}
          pool={scalePool.pool}
          onClose={() => setScalePool(null)}
        />
      )}
      {changeSku && (
        <ChangeSKUModal
          open
          deploymentId={deploymentId}
          pool={changeSku.pool}
          regionProvider={changeSku.region.provider as CloudProvider}
          onClose={() => setChangeSku(null)}
        />
      )}
      {drainNode && (
        <NodeActionConfirm
          open
          deploymentId={deploymentId}
          node={drainNode.node}
          action="drain"
          onClose={() => setDrainNode(null)}
        />
      )}
      {addPoolFor && (
        <AddNodePoolModal
          open
          deploymentId={deploymentId}
          clusterId={addPoolFor.cluster.id}
          regionProvider={addPoolFor.provider}
          onClose={() => setAddPoolFor(null)}
        />
      )}
    </div>
  )
}

function toggle(checked: boolean, id: string, setter: React.Dispatch<React.SetStateAction<string[]>>) {
  setter((prev) => (checked ? [...prev, id] : prev.filter((x) => x !== id)))
}

function FlatTable({
  testId,
  headers,
  children,
}: {
  testId: string
  headers: string[]
  children: React.ReactNode
}) {
  return (
    <table
      data-testid={testId}
      style={{
        width: '100%',
        borderCollapse: 'separate',
        borderSpacing: 0,
        fontSize: '0.82rem',
      }}
    >
      <thead>
        <tr>
          {headers.map((h, i) => (
            <th
              key={i}
              style={{
                textAlign: 'left',
                fontWeight: 600,
                fontSize: '0.72rem',
                textTransform: 'uppercase',
                letterSpacing: '0.05em',
                color: 'var(--color-text-dim)',
                padding: '6px 8px',
                borderBottom: '1px solid var(--color-border)',
              }}
            >
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody style={{ verticalAlign: 'middle' }}>{children}</tbody>
      <style>{`
        tbody tr td {
          padding: 8px;
          border-bottom: 1px solid var(--color-border);
          color: var(--color-text);
        }
        tbody tr:hover { background: var(--color-bg-2); }
      `}</style>
    </table>
  )
}

function StatusBadge({ status }: { status: 'healthy' | 'degraded' | 'failed' | 'unknown' }) {
  return (
    <span
      data-status={status}
      style={{
        display: 'inline-block',
        fontSize: '0.65rem',
        textTransform: 'uppercase',
        letterSpacing: '0.06em',
        fontWeight: 700,
        padding: '0.1rem 0.45rem',
        borderRadius: 999,
        background:
          status === 'healthy'
            ? 'color-mix(in srgb, var(--color-success) 18%, transparent)'
            : status === 'degraded'
              ? 'color-mix(in srgb, var(--color-warn) 18%, transparent)'
              : status === 'failed'
                ? 'color-mix(in srgb, var(--color-danger) 18%, transparent)'
                : 'color-mix(in srgb, var(--color-text-dim) 18%, transparent)',
        color:
          status === 'healthy'
            ? 'var(--color-success)'
            : status === 'degraded'
              ? 'var(--color-warn)'
              : status === 'failed'
                ? 'var(--color-danger)'
                : 'var(--color-text-dim)',
      }}
    >
      {status}
    </span>
  )
}

const rowBtn: React.CSSProperties = {
  border: '1px solid var(--color-border)',
  background: 'transparent',
  color: 'var(--color-text)',
  padding: '3px 8px',
  borderRadius: 5,
  fontSize: '0.72rem',
  cursor: 'pointer',
}
