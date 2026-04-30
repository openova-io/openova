/**
 * InfrastructureTopology — SVG canvas for the Sovereign Infrastructure
 * Topology tab (the DEFAULT tab — opens by default per founder spec).
 *
 * This is the same family of layered-DAG canvas that
 * widgets/job-deps-graph/JobDependenciesGraph uses — a deterministic
 * layered layout, no force-directed simulation, no `reactflow`. The
 * topology layer keys (cloud → region → cluster → node | lb → pvc |
 * volume | network) come from the infrastructure.types.ts layout
 * function.
 *
 * Click a node → a detail panel slides in from the right WITHOUT
 * navigation (still the Topology tab). Closing the panel returns to
 * the bare canvas.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (no compromise) — empty state shows the canvas frame with a
 *      "Provisioning…" overlay rather than a stub fallback. Real cluster
 *      data flows in once the backend's live-cluster integration lands.
 *   #4 (never hardcode) — every status colour comes from the
 *      `--color-*` CSS variables the rest of the portal uses.
 */

import { useMemo, useState } from 'react'
import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  getTopology,
  topologyLayout,
  type TopologyNode,
  type TopologyResponse,
  type TopologyStatus,
} from '@/lib/infrastructure.types'

const STATUS_FILL: Record<TopologyStatus, string> = {
  healthy:  'var(--color-success)',
  degraded: 'var(--color-warn)',
  failed:   'var(--color-danger)',
  unknown:  'var(--color-text-dim)',
}

const STATUS_RING: Record<TopologyStatus, string> = {
  healthy:  'var(--color-success)',
  degraded: 'var(--color-warn)',
  failed:   'var(--color-danger)',
  unknown:  'var(--color-border-strong)',
}

const NODE_WIDTH = 200
const NODE_HEIGHT = 64
const STALE_MS = 30_000

interface InfrastructureTopologyProps {
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialDataOverride?: TopologyResponse
}

export function InfrastructureTopology({
  initialDataOverride,
}: InfrastructureTopologyProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/infrastructure/topology' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const query = useQuery<TopologyResponse>({
    queryKey: ['infra-topology', deploymentId],
    queryFn: () => getTopology(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
  })

  const data = initialDataOverride ?? query.data
  const layout = useMemo(() => {
    if (!data) return null
    return topologyLayout(data.nodes, data.edges, {
      nodeWidth: NODE_WIDTH,
      nodeHeight: NODE_HEIGHT,
    })
  }, [data])

  const nodesById = useMemo(() => {
    const m = new Map<string, TopologyNode>()
    if (data) for (const n of data.nodes) m.set(n.id, n)
    return m
  }, [data])

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const selectedNode = selectedId ? nodesById.get(selectedId) ?? null : null

  const isLoading = !initialDataOverride && query.isLoading && !data
  const hasNodes = !!data && data.nodes.length > 0

  return (
    <div data-testid="infrastructure-topology" className="relative">
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

        {query.isError && !data && (
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
          </div>
        )}

        {!hasNodes && !isLoading && !query.isError && (
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

            {/* Edges first so they sit beneath the nodes. */}
            <g data-testid="infrastructure-topology-edges">
              {layout.edges.map((e) => (
                <polyline
                  key={`${e.from}->${e.to}`}
                  data-testid={`infra-edge-${e.from}-${e.to}`}
                  points={e.points.map((p) => `${p.x},${p.y}`).join(' ')}
                  fill="none"
                  stroke="var(--color-border-strong)"
                  strokeWidth={1.5}
                  markerEnd="url(#infra-topology-arrow)"
                />
              ))}
            </g>

            {/* Nodes. */}
            <g data-testid="infrastructure-topology-nodes">
              {layout.nodes.map((n) => {
                const node = nodesById.get(n.id)
                if (!node) return null
                const fill = STATUS_FILL[node.status]
                const ring = STATUS_RING[node.status]
                const isSelected = selectedId === n.id
                return (
                  <g
                    key={n.id}
                    data-testid={`infra-node-${n.id}`}
                    data-kind={node.kind}
                    data-status={node.status}
                    transform={`translate(${n.x}, ${n.y})`}
                    style={{ cursor: 'pointer' }}
                    onClick={() => setSelectedId(n.id)}
                    tabIndex={0}
                    role="button"
                    aria-label={`${node.label} — ${node.kind} — ${node.status}`}
                    aria-pressed={isSelected}
                    onKeyDown={(ev) => {
                      if (ev.key === 'Enter' || ev.key === ' ') {
                        ev.preventDefault()
                        setSelectedId(n.id)
                      }
                    }}
                  >
                    <rect
                      width={NODE_WIDTH}
                      height={NODE_HEIGHT}
                      rx={10}
                      ry={10}
                      fill="var(--color-bg)"
                      stroke={ring}
                      strokeWidth={isSelected ? 2.5 : 1.5}
                    />
                    <circle cx={14} cy={NODE_HEIGHT / 2} r={6} fill={fill} />
                    <text
                      x={28}
                      y={NODE_HEIGHT / 2 - 6}
                      fill="var(--color-text-strong)"
                      fontSize={12}
                      fontWeight={600}
                      dominantBaseline="middle"
                    >
                      {truncate(node.label, 22)}
                    </text>
                    <text
                      x={28}
                      y={NODE_HEIGHT / 2 + 12}
                      fill="var(--color-text-dim)"
                      fontSize={10}
                      fontWeight={500}
                      dominantBaseline="middle"
                    >
                      {node.kind}
                    </text>
                  </g>
                )
              })}
            </g>
          </svg>
        )}
      </div>

      {/* Detail panel — slides in from the right. NOT a separate route
          per founder spec ("Click a node → detail panel slides in from
          the right (NOT a separate route — keeps you on the Topology
          view)"). */}
      {selectedNode && (
        <aside
          role="dialog"
          aria-label={`${selectedNode.label} details`}
          data-testid="infrastructure-topology-detail"
          className="fixed right-0 top-14 z-30 flex h-[calc(100vh-3.5rem)] w-80 flex-col gap-3 border-l border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 shadow-xl"
        >
          <header className="flex items-start justify-between gap-2">
            <div className="min-w-0">
              <p
                className="truncate text-base font-semibold text-[var(--color-text-strong)]"
                data-testid="infrastructure-topology-detail-name"
              >
                {selectedNode.label}
              </p>
              <p className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
                {selectedNode.kind}
              </p>
            </div>
            <button
              type="button"
              onClick={() => setSelectedId(null)}
              data-testid="infrastructure-topology-detail-close"
              className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
              aria-label="Close detail panel"
            >
              ✕
            </button>
          </header>

          <div
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] p-2 text-xs"
            data-status={selectedNode.status}
          >
            <span className="text-[var(--color-text-dim)]">Status: </span>
            <span
              data-testid="infrastructure-topology-detail-status"
              style={{ color: STATUS_FILL[selectedNode.status] }}
            >
              {selectedNode.status}
            </span>
          </div>

          <div
            className="flex-1 overflow-auto"
            data-testid="infrastructure-topology-detail-meta"
          >
            {Object.entries(selectedNode.metadata).length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No additional metadata for this node.
              </p>
            ) : (
              <dl className="grid grid-cols-3 gap-x-2 gap-y-1.5 text-xs">
                {Object.entries(selectedNode.metadata).map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt className="col-span-1 truncate text-[var(--color-text-dim)]">
                      {k}
                    </dt>
                    <dd className="col-span-2 truncate font-mono text-[var(--color-text)]">
                      {v}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
          </div>
        </aside>
      )}
    </div>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}
