/**
 * InfrastructureTopology — hierarchical layered SVG canvas for the
 * Sovereign Infrastructure Topology tab (default landing).
 *
 * Per founder spec (issue #228):
 *   • 4 visual depths: Cloud → Region → Cluster → vCluster
 *   • Click node → graph zooms in (NOT accordion)
 *   • vClusters render dim until their parent cluster is zoomed
 *   • Right-side detail panel slides in (InfrastructureDetailPanel)
 *   • Layered, NOT force-directed — pure topologyLayout in
 *     `@/lib/topologyLayout`
 *
 * The 4 tabs (Topology / Compute / Storage / Network) are filtered
 * lenses over ONE backend response. Topology reads the tree directly;
 * the others use flatten* helpers.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (no compromise) — pure layout function, no `reactflow`, no
 *      simulation.
 *   #4 (never hardcode) — every status colour comes from the
 *      `--color-*` CSS variables the rest of the portal uses.
 */

import { useMemo, useState } from 'react'
import { useInfrastructure } from './InfrastructurePage'
import { topologyLayout, type LayoutNode, type ZoomState } from '@/lib/topologyLayout'
import type { TopologyStatus } from '@/lib/infrastructure.types'
import { InfrastructureDetailPanel, type DetailAction } from '@/components/InfrastructureDetailPanel'
import {
  AddRegionModal,
  AddClusterModal,
  AddVClusterModal,
  AddNodePoolModal,
  AddLBModal,
  DeleteCascadeConfirm,
} from '@/components/CrudModals'
import type { CloudProvider } from '@/entities/deployment/model'

const STATUS_FILL: Record<TopologyStatus, string> = {
  healthy: 'var(--color-success)',
  degraded: 'var(--color-warn)',
  failed: 'var(--color-danger)',
  unknown: 'var(--color-text-dim)',
}

const STATUS_RING: Record<TopologyStatus, string> = {
  healthy: 'var(--color-success)',
  degraded: 'var(--color-warn)',
  failed: 'var(--color-danger)',
  unknown: 'var(--color-border-strong)',
}

interface ModalState {
  kind:
    | 'none'
    | 'add-region'
    | 'add-cluster'
    | 'add-vcluster'
    | 'add-nodepool'
    | 'add-lb'
    | 'delete'
}

export function InfrastructureTopology() {
  const { deploymentId, data, isLoading, isError, refetch } = useInfrastructure()

  const [zoom, setZoom] = useState<ZoomState>({
    zoomedClusterId: null,
    zoomedRegionId: null,
  })
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [modal, setModal] = useState<ModalState>({ kind: 'none' })

  const layout = useMemo(() => {
    if (!data) return null
    return topologyLayout(data, { zoom })
  }, [data, zoom])

  const nodeById = useMemo(() => {
    const m = new Map<string, LayoutNode>()
    if (layout) for (const n of layout.nodes) m.set(n.id, n)
    return m
  }, [layout])

  const selectedNode = selectedId ? nodeById.get(selectedId) ?? null : null

  function onNodeClick(node: LayoutNode) {
    setSelectedId(node.id)
    if (node.kind === 'cluster') {
      setZoom({
        zoomedClusterId: node.id,
        zoomedRegionId: node.ref.kind === 'cluster' ? node.ref.regionId : null,
      })
    } else if (node.kind === 'region') {
      setZoom({
        zoomedRegionId: node.id,
        zoomedClusterId: null,
      })
    } else if (node.kind === 'cloud') {
      setZoom({ zoomedClusterId: null, zoomedRegionId: null })
    }
  }

  // Build per-kind action lists for the detail panel.
  const detailActions = useMemo<DetailAction[]>(() => {
    if (!selectedNode) return []
    const actions: DetailAction[] = []
    if (selectedNode.kind === 'cloud') {
      actions.push({
        key: 'add-region',
        label: '+ Add region',
        onClick: () => setModal({ kind: 'add-region' }),
      })
    }
    if (selectedNode.kind === 'region') {
      actions.push({
        key: 'add-cluster',
        label: '+ Add cluster',
        onClick: () => setModal({ kind: 'add-cluster' }),
      })
      actions.push({
        key: 'add-lb',
        label: '+ Add load balancer',
        onClick: () => setModal({ kind: 'add-lb' }),
      })
      actions.push({
        key: 'delete',
        label: 'Delete region',
        onClick: () => setModal({ kind: 'delete' }),
        danger: true,
      })
    }
    if (selectedNode.kind === 'cluster') {
      actions.push({
        key: 'add-vcluster',
        label: '+ Add vCluster',
        onClick: () => setModal({ kind: 'add-vcluster' }),
      })
      actions.push({
        key: 'add-nodepool',
        label: '+ Add node pool',
        onClick: () => setModal({ kind: 'add-nodepool' }),
      })
      actions.push({
        key: 'delete',
        label: 'Delete cluster',
        onClick: () => setModal({ kind: 'delete' }),
        danger: true,
      })
    }
    if (selectedNode.kind === 'vcluster') {
      actions.push({
        key: 'delete',
        label: 'Delete vCluster',
        onClick: () => setModal({ kind: 'delete' }),
        danger: true,
      })
    }
    return actions
  }, [selectedNode])

  const hasNodes = !!layout && layout.nodes.length > 0

  return (
    <div data-testid="infrastructure-topology" className="relative">
      {(zoom.zoomedClusterId || zoom.zoomedRegionId) && (
        <div
          data-testid="infrastructure-topology-zoom-status"
          className="mb-2 flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5 text-xs"
        >
          <span className="text-[var(--color-text-dim)]">
            {zoom.zoomedClusterId
              ? `Zoomed: cluster ${zoom.zoomedClusterId}`
              : `Zoomed: region ${zoom.zoomedRegionId}`}
          </span>
          <button
            type="button"
            data-testid="infrastructure-topology-zoom-reset"
            onClick={() => setZoom({ zoomedClusterId: null, zoomedRegionId: null })}
            className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-0.5 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg)]"
          >
            Reset zoom
          </button>
        </div>
      )}

      <div
        className="relative w-full overflow-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)]"
        data-testid="infrastructure-topology-canvas"
        style={{ minHeight: 480 }}
      >
        {isLoading && (
          <div
            className="flex h-[480px] items-center justify-center text-sm text-[var(--color-text-dim)]"
            data-testid="infrastructure-topology-loading"
          >
            Loading topology…
          </div>
        )}

        {isError && !data && (
          <div
            className="flex h-[480px] flex-col items-center justify-center gap-2 px-6 text-center text-sm"
            data-testid="infrastructure-topology-error"
          >
            <p className="font-medium text-[var(--color-danger)]">
              Couldn&rsquo;t load topology
            </p>
            <p className="text-[var(--color-text-dim)]">
              The Catalyst API is temporarily unreachable. Retry will start
              automatically.
            </p>
            <button
              type="button"
              onClick={refetch}
              className="mt-2 rounded-md border border-[var(--color-border)] bg-transparent px-3 py-1 text-xs hover:bg-[var(--color-bg)]"
            >
              Retry
            </button>
          </div>
        )}

        {!hasNodes && !isLoading && !isError && (
          <div
            className="flex h-[480px] flex-col items-center justify-center gap-2 px-6 text-center text-sm"
            data-testid="infrastructure-topology-empty"
          >
            <div className="h-3 w-3 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" />
            <p className="font-medium text-[var(--color-text)]">Provisioning&hellip;</p>
            <p className="text-[var(--color-text-dim)]">
              Topology will appear here as soon as the Sovereign cluster
              reports its first nodes.
            </p>
          </div>
        )}

        {hasNodes && layout && (
          <svg
            data-testid="infrastructure-topology-svg"
            width={layout.width}
            height={layout.height}
            viewBox={`0 0 ${layout.width} ${layout.height}`}
            role="img"
            aria-label="Sovereign infrastructure topology"
            style={{ display: 'block', minWidth: '100%' }}
          >
            <defs>
              <marker
                id="infra-topology-arrow"
                viewBox="0 0 10 10"
                refX="9"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M0,0 L10,5 L0,10 Z" fill="var(--color-border-strong)" />
              </marker>
            </defs>

            {/* Depth row labels — anchor the layered intent. */}
            <g data-testid="infrastructure-topology-depth-labels">
              {(['Cloud', 'Region', 'Cluster', 'vCluster'] as const).map((label, i) => {
                const sample = layout.nodes.find((n) => n.depth === i)
                if (!sample) return null
                return (
                  <text
                    key={label}
                    x={8}
                    y={sample.y - 6}
                    fontSize={10}
                    fontWeight={600}
                    fill="var(--color-text-dim)"
                    style={{ textTransform: 'uppercase', letterSpacing: '0.08em' }}
                  >
                    {label}
                  </text>
                )
              })}
            </g>

            {/* Edges first so they sit beneath the nodes. */}
            <g data-testid="infrastructure-topology-edges">
              {layout.edges.map((e) => (
                <polyline
                  key={e.id}
                  data-testid={`infra-edge-${e.fromId}-${e.toId}`}
                  points={e.points.map((p) => `${p.x},${p.y}`).join(' ')}
                  fill="none"
                  stroke="var(--color-border-strong)"
                  strokeWidth={1.5}
                  markerEnd="url(#infra-topology-arrow)"
                  opacity={0.6}
                />
              ))}
            </g>

            {/* Nodes. */}
            <g data-testid="infrastructure-topology-nodes">
              {layout.nodes.map((n) => {
                const fill = STATUS_FILL[n.status]
                const ring = STATUS_RING[n.status]
                const isSelected = selectedId === n.id
                return (
                  <g
                    key={n.id}
                    data-testid={`infra-node-${n.id}`}
                    data-kind={n.kind}
                    data-depth={n.depth}
                    data-status={n.status}
                    data-dim={n.dim ? 'true' : 'false'}
                    transform={`translate(${n.x}, ${n.y})`}
                    style={{ cursor: 'pointer', opacity: n.dim ? 0.35 : 1 }}
                    onClick={() => onNodeClick(n)}
                    tabIndex={0}
                    role="button"
                    aria-label={`${n.label} — ${n.kind} — ${n.status}`}
                    aria-pressed={isSelected}
                    onKeyDown={(ev) => {
                      if (ev.key === 'Enter' || ev.key === ' ') {
                        ev.preventDefault()
                        onNodeClick(n)
                      }
                    }}
                  >
                    <rect
                      width={n.width}
                      height={n.height}
                      rx={10}
                      ry={10}
                      fill="var(--color-bg)"
                      stroke={ring}
                      strokeWidth={isSelected ? 2.5 : 1.5}
                    />
                    <circle cx={14} cy={n.height / 2} r={6} fill={fill} />
                    <text
                      x={28}
                      y={n.height / 2 - 6}
                      fill="var(--color-text-strong)"
                      fontSize={12}
                      fontWeight={600}
                      dominantBaseline="middle"
                    >
                      {truncate(n.label, Math.floor(n.width / 8))}
                    </text>
                    <text
                      x={28}
                      y={n.height / 2 + 12}
                      fill="var(--color-text-dim)"
                      fontSize={10}
                      fontWeight={500}
                      dominantBaseline="middle"
                    >
                      {n.sublabel}
                    </text>
                  </g>
                )
              })}
            </g>
          </svg>
        )}
      </div>

      {/* Top-level Add Region button — visible at all times when the
          tree has at least one cloud. Founder spec: every CRUD action
          must be reachable from the Topology view. */}
      {hasNodes && data && (
        <div className="mt-3 flex items-center justify-end gap-2">
          <button
            type="button"
            data-testid="infrastructure-topology-add-region"
            onClick={() => setModal({ kind: 'add-region' })}
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5 text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)]"
          >
            + Add region
          </button>
        </div>
      )}

      <InfrastructureDetailPanel
        node={selectedNode}
        onClose={() => setSelectedId(null)}
        actions={detailActions}
      />

      {/* CRUD modals — opened from the detail panel actions. */}
      {data && (
        <>
          <AddRegionModal
            open={modal.kind === 'add-region'}
            deploymentId={deploymentId}
            defaultProvider={inferDefaultProvider(data)}
            onClose={() => setModal({ kind: 'none' })}
          />

          {selectedNode?.kind === 'region' && (
            <>
              <AddClusterModal
                open={modal.kind === 'add-cluster'}
                deploymentId={deploymentId}
                regionId={selectedNode.id}
                regionProvider={
                  selectedNode.ref.kind === 'region'
                    ? (selectedNode.ref.data.provider as CloudProvider)
                    : 'hetzner'
                }
                onClose={() => setModal({ kind: 'none' })}
              />
              <AddLBModal
                open={modal.kind === 'add-lb'}
                deploymentId={deploymentId}
                regionId={selectedNode.id}
                onClose={() => setModal({ kind: 'none' })}
              />
            </>
          )}

          {selectedNode?.kind === 'cluster' && (
            <>
              <AddVClusterModal
                open={modal.kind === 'add-vcluster'}
                deploymentId={deploymentId}
                clusterId={selectedNode.id}
                onClose={() => setModal({ kind: 'none' })}
              />
              <AddNodePoolModal
                open={modal.kind === 'add-nodepool'}
                deploymentId={deploymentId}
                clusterId={selectedNode.id}
                regionProvider={inferProviderForCluster(data, selectedNode.id)}
                onClose={() => setModal({ kind: 'none' })}
              />
            </>
          )}

          {selectedNode && (
            <DeleteCascadeConfirm
              open={modal.kind === 'delete'}
              deploymentId={deploymentId}
              resource={resourceForKind(selectedNode.kind)}
              resourceId={selectedNode.id}
              resourceLabel={selectedNode.label}
              onClose={() => setModal({ kind: 'none' })}
            />
          )}
        </>
      )}
    </div>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}

function inferDefaultProvider(data: ReturnType<typeof useInfrastructure>['data']): CloudProvider {
  const first = data?.cloud[0]
  return ((first?.provider ?? 'hetzner') as CloudProvider)
}

function inferProviderForCluster(
  data: ReturnType<typeof useInfrastructure>['data'],
  clusterId: string,
): CloudProvider {
  if (!data) return 'hetzner'
  for (const r of data.topology.regions) {
    for (const c of r.clusters) {
      if (c.id === clusterId) return r.provider as CloudProvider
    }
  }
  return 'hetzner'
}

function resourceForKind(
  kind: 'cloud' | 'region' | 'cluster' | 'vcluster',
): 'regions' | 'clusters' | 'vclusters' {
  if (kind === 'region') return 'regions'
  if (kind === 'cluster') return 'clusters'
  if (kind === 'vcluster') return 'vclusters'
  return 'regions'
}
