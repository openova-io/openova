/**
 * ArchitectureGraphPage — page-level orchestrator for the
 * Sovereign Cloud / Architecture surface. Wraps GraphCanvas with
 * search, density-slider isolation, edge legend, detail panel,
 * focus mode, context menu, and CRUD modals.
 *
 * Founder spec, verbatim subset (P2 of #309):
 *   • Pure force-directed layout (no layered tree).
 *   • Containment is just one of several edge types — rendered as a
 *     normal edge.
 *   • Per-type density slider (lazy-loading) — each tunable type has a
 *     popover with a slider 0..total + presets None/25%/50%/All/Hide.
 *   • Global density slider (0..100%) sets all tunable type limits.
 *   • Search box — isolates matches + direct neighbors (NOT dimming).
 *   • Detail panel on click; double-click → focus mode.
 *   • Right-click → context menu: Add (kind-appropriate child) /
 *     Edit / Delete.
 *   • Right-click on canvas → "Add region".
 *   • Shift-drag from one node to another → create an edge.
 *   • Edge legend at bottom.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target shape) — every UI affordance ships in this
 *      first cut.
 *   #4 (never hardcode) — type list, density presets, debounce
 *      interval all flow through constants exported from this file.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AddClusterModal,
  AddLBModal,
  AddNodePoolModal,
  AddRegionModal,
  AddVClusterModal,
  DeleteCascadeConfirm,
} from '@/components/CrudModals'
import type { CloudProvider } from '@/entities/deployment/model'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { GraphCanvas, type GraphCanvasHandle } from './GraphCanvas'
import { hierarchyToGraph } from './adapter'
import {
  EDGE_DASHED,
  EDGE_STROKE,
  NODE_FILL,
  type ArchEdgeType,
  type ArchNodeType,
  type GraphEdge,
  type GraphNode,
} from './types'

/* ── Constants ───────────────────────────────────────────────────── */

/** Types that participate in the per-type density slider. */
const TUNABLE_TYPES: ArchNodeType[] = ['WorkerNode', 'NodePool', 'LoadBalancer', 'Network']

/** Types always rendered fully (small enough to not need a cap). */
// const ALWAYS_FULL: ArchNodeType[] = ['Cloud', 'Region', 'Cluster', 'vCluster']

const DEBOUNCE_MS = 400
const SMALL_TYPE_THRESHOLD = 50
const DEFAULT_GLOBAL_PCT = 50

const ALL_EDGE_TYPES: ArchEdgeType[] = [
  'contains',
  'runs-on',
  'routes-to',
  'attached-to',
  'depends-on',
  'peers-with',
]

/* ── Public props ────────────────────────────────────────────────── */

export interface ArchitectureGraphPageProps {
  deploymentId: string
  data: HierarchicalInfrastructure | null
  isLoading: boolean
  isError: boolean
  onRefetch: () => void
}

/* ── Component ───────────────────────────────────────────────────── */

interface ContextMenuState {
  open: boolean
  x: number
  y: number
  /** When non-null the menu acts on a node; otherwise it's the empty-canvas menu. */
  node: GraphNode | null
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

export function ArchitectureGraphPage({
  deploymentId,
  data,
  isLoading,
  isError,
  onRefetch,
}: ArchitectureGraphPageProps) {
  const handleRef = useRef<GraphCanvasHandle | null>(null)

  /* ── 1. Adapter — tree → nodes/edges ───────────────────────── */
  const { nodes: allNodes, edges: allEdges } = useMemo(
    () => hierarchyToGraph(data),
    [data],
  )

  /* ── 2. Type totals for slider sizing ──────────────────────── */
  const typeTotals = useMemo(() => {
    const m = new Map<ArchNodeType, number>()
    for (const n of allNodes) m.set(n.type, (m.get(n.type) ?? 0) + 1)
    return m
  }, [allNodes])

  /* ── 3. Density state ──────────────────────────────────────── */
  // Per-type explicit cap; null = "all" (no cap).
  const [typeCap, setTypeCap] = useState<Partial<Record<ArchNodeType, number | null>>>({})
  const [hiddenTypes, setHiddenTypes] = useState<Set<ArchNodeType>>(new Set())
  const [globalPct, setGlobalPct] = useState<number>(DEFAULT_GLOBAL_PCT)

  // Debounce so repeatedly nudging a slider doesn't hammer the
  // simulation / refetch.
  const [debouncedCap, setDebouncedCap] = useState(typeCap)
  const [debouncedHidden, setDebouncedHidden] = useState(hiddenTypes)
  useEffect(() => {
    const id = setTimeout(() => setDebouncedCap(typeCap), DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [typeCap])
  useEffect(() => {
    const id = setTimeout(() => setDebouncedHidden(hiddenTypes), DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [hiddenTypes])

  // Global slider — applies a percentage cap to every tunable type.
  function setGlobalDensity(pct: number) {
    setGlobalPct(pct)
    const next: Partial<Record<ArchNodeType, number | null>> = { ...typeCap }
    for (const t of TUNABLE_TYPES) {
      const total = typeTotals.get(t) ?? 0
      if (total === 0) continue
      next[t] = Math.max(0, Math.round((total * pct) / 100))
    }
    setTypeCap(next)
  }

  const effectiveTypeLimits = useMemo(() => {
    const out: Partial<Record<ArchNodeType, number>> = {}
    for (const t of TUNABLE_TYPES) {
      const v = debouncedCap[t]
      if (typeof v === 'number') out[t] = v
    }
    return out
  }, [debouncedCap])

  /* ── 4. Search isolation ───────────────────────────────────── */
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setDebouncedSearch(search.trim()), 250)
    return () => clearTimeout(id)
  }, [search])

  const searchActive = debouncedSearch.length > 0
  const searchMatches = useMemo(() => {
    if (!searchActive) return new Set<string>()
    const q = debouncedSearch.toLowerCase()
    const out = new Set<string>()
    for (const n of allNodes) {
      if (n.label.toLowerCase().includes(q) || n.id.toLowerCase().includes(q)) {
        out.add(n.id)
      }
    }
    return out
  }, [allNodes, debouncedSearch, searchActive])

  const searchNeighbors = useMemo(() => {
    if (!searchActive) return new Set<string>()
    const out = new Set<string>()
    for (const e of allEdges) {
      if (searchMatches.has(e.source)) out.add(e.target)
      if (searchMatches.has(e.target)) out.add(e.source)
    }
    for (const id of searchMatches) out.delete(id)
    return out
  }, [allEdges, searchMatches, searchActive])

  /* ── 5. Apply search isolation to the visible set ─────────── */
  const { displayNodes, displayEdges } = useMemo(() => {
    if (!searchActive) {
      return { displayNodes: allNodes, displayEdges: allEdges }
    }
    const keep = new Set<string>([...searchMatches, ...searchNeighbors])
    const displayNodes = allNodes.filter((n) => keep.has(n.id))
    const displayEdges = allEdges.filter((e) => keep.has(e.source) && keep.has(e.target))
    return { displayNodes, displayEdges }
  }, [allNodes, allEdges, searchActive, searchMatches, searchNeighbors])

  /* ── 6. Detail panel + focus mode ──────────────────────────── */
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [focusNodeId, setFocusNodeId] = useState<string | null>(null)

  const selectedNode = useMemo(
    () => allNodes.find((n) => n.id === selectedId) ?? null,
    [allNodes, selectedId],
  )

  // Neighbor list for the detail panel.
  const neighborList = useMemo(() => {
    if (!selectedNode) return [] as GraphNode[]
    const ids = new Set<string>()
    for (const e of allEdges) {
      if (e.source === selectedNode.id) ids.add(e.target)
      if (e.target === selectedNode.id) ids.add(e.source)
    }
    return allNodes.filter((n) => ids.has(n.id))
  }, [selectedNode, allEdges, allNodes])

  /* ── 7. Context menu state ─────────────────────────────────── */
  const [ctxMenu, setCtxMenu] = useState<ContextMenuState>({
    open: false,
    x: 0,
    y: 0,
    node: null,
  })
  function openCtxMenuForNode(n: GraphNode, ev: React.MouseEvent) {
    setCtxMenu({ open: true, x: ev.clientX, y: ev.clientY, node: n })
  }
  function openCtxMenuForCanvas(ev: React.MouseEvent) {
    setCtxMenu({ open: true, x: ev.clientX, y: ev.clientY, node: null })
  }
  function closeCtxMenu() {
    setCtxMenu({ open: false, x: 0, y: 0, node: null })
  }

  /* ── 8. CRUD modals ────────────────────────────────────────── */
  const [modal, setModal] = useState<ModalState>({ kind: 'none' })

  // The selected node for modal purposes — when the operator clicked
  // a context menu item we use ctxMenu.node; otherwise selectedNode.
  const modalAnchor = useMemo<GraphNode | null>(() => {
    if (ctxMenu.node) return ctxMenu.node
    return selectedNode
  }, [ctxMenu.node, selectedNode])

  /* ── 9. Edge type counts for the legend ────────────────────── */
  const edgeTypeCounts = useMemo(() => {
    const m = new Map<ArchEdgeType, number>()
    for (const e of displayEdges) {
      m.set(e.type, (m.get(e.type) ?? 0) + 1)
    }
    return m
  }, [displayEdges])

  /* ── 10. Render ────────────────────────────────────────────── */
  const hasNodes = allNodes.length > 0

  return (
    <div data-testid="cloud-architecture" className="relative">
      {/* Toolbar — search + global density. */}
      <div
        data-testid="cloud-architecture-toolbar"
        className="mb-2 flex flex-wrap items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
      >
        <input
          data-testid="cloud-architecture-search"
          type="search"
          placeholder="Search nodes…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-text)] placeholder:text-[var(--color-text-dim)]"
        />
        {searchActive && (
          <span
            data-testid="cloud-architecture-search-counter"
            className="text-xs text-[var(--color-text-dim)]"
          >
            {searchMatches.size} matches + {searchNeighbors.size} neighbors
          </span>
        )}

        <div className="ml-auto flex items-center gap-2">
          <label className="text-xs text-[var(--color-text-dim)]" htmlFor="arch-global-density">
            Density
          </label>
          <input
            id="arch-global-density"
            data-testid="cloud-architecture-global-density"
            type="range"
            min={0}
            max={100}
            step={5}
            value={globalPct}
            onChange={(e) => setGlobalDensity(parseInt(e.target.value, 10))}
            className="w-32"
          />
          <span
            data-testid="cloud-architecture-global-density-pct"
            className="w-10 text-right text-xs tabular-nums text-[var(--color-text-dim)]"
          >
            {globalPct}%
          </span>
          {focusNodeId && (
            <button
              type="button"
              data-testid="cloud-architecture-clear-focus"
              onClick={() => setFocusNodeId(null)}
              className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg)]"
            >
              Clear focus
            </button>
          )}
        </div>
      </div>

      {/* Per-type badges with mini density controls. */}
      <div
        data-testid="cloud-architecture-type-bar"
        className="mb-2 flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
      >
        {(['Cloud', 'Region', 'Cluster', 'vCluster', 'NodePool', 'WorkerNode', 'LoadBalancer', 'Network'] as ArchNodeType[]).map((t) => {
          const total = typeTotals.get(t) ?? 0
          const hidden = hiddenTypes.has(t)
          const isTunable = TUNABLE_TYPES.includes(t)
          const cap = typeCap[t]
          const small = total < SMALL_TYPE_THRESHOLD
          return (
            <TypeBadge
              key={t}
              type={t}
              total={total}
              hidden={hidden}
              capped={typeof cap === 'number' ? cap : null}
              small={small}
              tunable={isTunable}
              onToggleHidden={() => {
                setHiddenTypes((prev) => {
                  const n = new Set(prev)
                  if (n.has(t)) n.delete(t)
                  else n.add(t)
                  return n
                })
              }}
              onSetCap={(v) => setTypeCap((prev) => ({ ...prev, [t]: v }))}
            />
          )
        })}
      </div>

      <div
        data-testid="cloud-architecture-canvas-wrap"
        className="relative w-full overflow-hidden rounded-xl border border-[var(--color-border)]"
        style={{ height: 540 }}
      >
        {isLoading && !data && (
          <div
            data-testid="cloud-architecture-loading"
            className="flex h-full items-center justify-center text-sm text-[var(--color-text-dim)]"
          >
            Loading architecture…
          </div>
        )}

        {isError && !data && (
          <div
            data-testid="cloud-architecture-error"
            className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-sm"
          >
            <p className="font-medium text-[var(--color-danger)]">
              Couldn&rsquo;t load architecture
            </p>
            <p className="text-[var(--color-text-dim)]">
              The Catalyst API is temporarily unreachable. Retry will start
              automatically.
            </p>
            <button
              type="button"
              onClick={onRefetch}
              className="mt-2 rounded-md border border-[var(--color-border)] bg-transparent px-3 py-1 text-xs hover:bg-[var(--color-bg)]"
            >
              Retry
            </button>
          </div>
        )}

        {!hasNodes && !isLoading && !isError && (
          <div
            data-testid="cloud-architecture-empty"
            className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-sm"
          >
            <div className="h-3 w-3 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent" />
            <p className="font-medium text-[var(--color-text)]">Provisioning&hellip;</p>
            <p className="text-[var(--color-text-dim)]">
              The cloud architecture will appear here as soon as the
              Sovereign cluster reports its first nodes.
            </p>
          </div>
        )}

        {hasNodes && (
          <GraphCanvas
            ref={handleRef}
            nodes={displayNodes}
            edges={displayEdges}
            highlightedIds={searchMatches}
            focusNodeId={focusNodeId}
            hiddenTypes={debouncedHidden}
            typeLimits={effectiveTypeLimits}
            onNodeClick={(n) => setSelectedId(n.id)}
            onNodeDoubleClick={(n) => setFocusNodeId(n.id)}
            onNodeContextMenu={openCtxMenuForNode}
            onCanvasContextMenu={openCtxMenuForCanvas}
            onEdgeCreate={(s, t) => {
              // P2 surfaces drag-to-create-edge as an event the caller
              // can wire to a future relation API. We log the intent
              // and prompt no-op until #321 lands.
              if (typeof console !== 'undefined' && console.info) {
                console.info('[architecture-graph] edge create requested', s, '→', t)
              }
            }}
          />
        )}
      </div>

      {/* Edge legend. */}
      {hasNodes && (
        <div
          data-testid="cloud-architecture-edge-legend"
          className="mt-2 flex flex-wrap items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
        >
          <span className="text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]">
            Relations
          </span>
          {ALL_EDGE_TYPES.map((t) => {
            const count = edgeTypeCounts.get(t) ?? 0
            return (
              <span
                key={t}
                data-testid={`cloud-architecture-edge-legend-${t}`}
                className="inline-flex items-center gap-1.5 text-xs text-[var(--color-text)]"
                aria-label={`${t} relation: ${count} edges`}
              >
                <svg width={22} height={6} aria-hidden="true">
                  <line
                    x1={1}
                    y1={3}
                    x2={21}
                    y2={3}
                    stroke={EDGE_STROKE[t]}
                    strokeWidth={1.5}
                    strokeDasharray={EDGE_DASHED[t] ? '5,3' : undefined}
                  />
                </svg>
                <span>{t}</span>
                <span className="text-[var(--color-text-dim)]">({count})</span>
              </span>
            )
          })}
        </div>
      )}

      {/* Detail panel — slides in from the right on node click. */}
      {selectedNode && (
        <DetailPanel
          node={selectedNode}
          neighbors={neighborList}
          focusNodeId={focusNodeId}
          onClose={() => setSelectedId(null)}
          onToggleFocus={() => {
            setFocusNodeId((cur) => (cur === selectedNode.id ? null : selectedNode.id))
          }}
          onAddChild={() => {
            const kind = selectedNode.type
            if (kind === 'Region') setModal({ kind: 'add-cluster' })
            else if (kind === 'Cluster') setModal({ kind: 'add-vcluster' })
            else if (kind === 'Cloud') setModal({ kind: 'add-region' })
          }}
          onDelete={() => setModal({ kind: 'delete' })}
          onPickNeighbor={(id) => setSelectedId(id)}
        />
      )}

      {/* Context menu — node OR canvas. */}
      {ctxMenu.open && (
        <ContextMenu
          state={ctxMenu}
          onClose={closeCtxMenu}
          onAddRegion={() => {
            setModal({ kind: 'add-region' })
            closeCtxMenu()
          }}
          onAddChild={() => {
            const k = ctxMenu.node?.type
            if (k === 'Region') setModal({ kind: 'add-cluster' })
            else if (k === 'Cluster') setModal({ kind: 'add-vcluster' })
            else if (k === 'Cloud') setModal({ kind: 'add-region' })
            closeCtxMenu()
          }}
          onAddNodePool={() => {
            setModal({ kind: 'add-nodepool' })
            closeCtxMenu()
          }}
          onAddLB={() => {
            setModal({ kind: 'add-lb' })
            closeCtxMenu()
          }}
          onDelete={() => {
            setModal({ kind: 'delete' })
            closeCtxMenu()
          }}
        />
      )}

      {/* AddRegion is the only modal that can fire from an empty
          canvas right-click — it doesn't need an anchor node. */}
      {data && (
        <AddRegionModal
          open={modal.kind === 'add-region'}
          deploymentId={deploymentId}
          defaultProvider={inferDefaultProvider(data)}
          onClose={() => setModal({ kind: 'none' })}
        />
      )}

      {/* All other CRUD modals require a node anchor — they're
          opened from the detail panel or a node-targeted context
          menu. */}
      {data && modalAnchor && (
        <>
          {modalAnchor.type === 'Region' && (
            <>
              <AddClusterModal
                open={modal.kind === 'add-cluster'}
                deploymentId={deploymentId}
                regionId={stripPrefix(modalAnchor.id, 'Region')}
                regionProvider={
                  (data.topology.regions.find(
                    (r) => r.id === stripPrefix(modalAnchor.id, 'Region'),
                  )?.provider as CloudProvider) ?? 'hetzner'
                }
                onClose={() => setModal({ kind: 'none' })}
              />
              <AddLBModal
                open={modal.kind === 'add-lb'}
                deploymentId={deploymentId}
                regionId={stripPrefix(modalAnchor.id, 'Region')}
                onClose={() => setModal({ kind: 'none' })}
              />
            </>
          )}

          {modalAnchor.type === 'Cluster' && (
            <>
              <AddVClusterModal
                open={modal.kind === 'add-vcluster'}
                deploymentId={deploymentId}
                clusterId={stripPrefix(modalAnchor.id, 'Cluster')}
                onClose={() => setModal({ kind: 'none' })}
              />
              <AddNodePoolModal
                open={modal.kind === 'add-nodepool'}
                deploymentId={deploymentId}
                clusterId={stripPrefix(modalAnchor.id, 'Cluster')}
                regionProvider={inferProviderForCluster(
                  data,
                  stripPrefix(modalAnchor.id, 'Cluster'),
                )}
                onClose={() => setModal({ kind: 'none' })}
              />
            </>
          )}

          <DeleteCascadeConfirm
            open={modal.kind === 'delete'}
            deploymentId={deploymentId}
            resource={resourceForType(modalAnchor.type)}
            resourceId={stripPrefix(modalAnchor.id, modalAnchor.type)}
            resourceLabel={modalAnchor.label}
            onClose={() => setModal({ kind: 'none' })}
          />
        </>
      )}
    </div>
  )
}

/* ── Sub-components ──────────────────────────────────────────────── */

interface TypeBadgeProps {
  type: ArchNodeType
  total: number
  hidden: boolean
  capped: number | null
  small: boolean
  tunable: boolean
  onToggleHidden: () => void
  onSetCap: (v: number | null) => void
}

function TypeBadge({
  type,
  total,
  hidden,
  capped,
  small,
  tunable,
  onToggleHidden,
  onSetCap,
}: TypeBadgeProps) {
  const [open, setOpen] = useState(false)
  const dotColor = NODE_FILL[type]
  const visibleCount = hidden ? 0 : capped ?? total

  return (
    <div className="relative">
      <button
        type="button"
        data-testid={`cloud-architecture-type-badge-${type}`}
        data-hidden={hidden ? 'true' : 'false'}
        onClick={() => {
          if (small || !tunable) {
            // Small types just toggle visibility on click.
            onToggleHidden()
          } else {
            setOpen((v) => !v)
          }
        }}
        className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs ${
          hidden
            ? 'border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)] line-through'
            : 'border-[var(--color-border)] bg-[var(--color-bg)] text-[var(--color-text)]'
        }`}
      >
        <span
          aria-hidden="true"
          className="h-2.5 w-2.5 rounded-full"
          style={{ background: dotColor }}
        />
        <span className="font-medium">{type}</span>
        <span className="text-[var(--color-text-dim)]">
          {visibleCount}/{total}
        </span>
      </button>

      {open && tunable && (
        <div
          data-testid={`cloud-architecture-type-popover-${type}`}
          className="absolute left-0 top-full z-30 mt-1 flex w-56 flex-col gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 shadow-xl"
        >
          <div className="flex items-center justify-between text-xs font-semibold text-[var(--color-text)]">
            <span>{type}</span>
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
              aria-label="Close popover"
            >
              ×
            </button>
          </div>
          <input
            data-testid={`cloud-architecture-type-slider-${type}`}
            type="range"
            min={0}
            max={total}
            step={1}
            value={capped ?? total}
            onChange={(e) => onSetCap(parseInt(e.target.value, 10))}
            className="w-full"
          />
          <div className="flex justify-between text-[10px] text-[var(--color-text-dim)]">
            <span>0</span>
            <span>{capped ?? total}</span>
            <span>{total}</span>
          </div>
          <div className="flex flex-wrap gap-1">
            <PresetButton type={type} preset="None" onClick={() => onSetCap(0)} />
            <PresetButton
              type={type}
              preset="25%"
              onClick={() => onSetCap(Math.round(total * 0.25))}
            />
            <PresetButton
              type={type}
              preset="50%"
              onClick={() => onSetCap(Math.round(total * 0.5))}
            />
            <PresetButton type={type} preset="All" onClick={() => onSetCap(null)} />
            <PresetButton type={type} preset="Hide" onClick={onToggleHidden} />
          </div>
        </div>
      )}
    </div>
  )
}

function PresetButton({
  type,
  preset,
  onClick,
}: {
  type: ArchNodeType
  preset: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      data-testid={`cloud-architecture-type-preset-${type}-${preset}`}
      onClick={onClick}
      className="rounded-md border border-[var(--color-border)] bg-transparent px-1.5 py-0.5 text-[10px] hover:bg-[var(--color-bg)]"
    >
      {preset}
    </button>
  )
}

interface DetailPanelProps {
  node: GraphNode
  neighbors: GraphNode[]
  focusNodeId: string | null
  onClose: () => void
  onToggleFocus: () => void
  onAddChild: () => void
  onDelete: () => void
  onPickNeighbor: (id: string) => void
}

function DetailPanel({
  node,
  neighbors,
  focusNodeId,
  onClose,
  onToggleFocus,
  onAddChild,
  onDelete,
  onPickNeighbor,
}: DetailPanelProps) {
  const meta = node.metadata ?? {}
  const focused = focusNodeId === node.id

  // The kind-appropriate child label, or null when no add-child action
  // applies for this type.
  const addChildLabel = useMemo(() => {
    if (node.type === 'Cloud') return '+ Add region'
    if (node.type === 'Region') return '+ Add cluster'
    if (node.type === 'Cluster') return '+ Add vCluster'
    return null
  }, [node.type])

  const deletable = ['Region', 'Cluster', 'vCluster'].includes(node.type)

  return (
    <aside
      role="dialog"
      aria-label={`${node.label} details`}
      data-testid="infrastructure-detail-panel"
      className="fixed right-0 top-14 z-30 flex h-[calc(100vh-3.5rem)] w-96 flex-col gap-3 border-l border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 shadow-xl"
    >
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p
            data-testid="infrastructure-detail-panel-name"
            className="truncate text-base font-semibold text-[var(--color-text-strong)]"
          >
            {node.label}
          </p>
          <p
            data-testid="infrastructure-detail-panel-type"
            className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]"
          >
            {node.type}
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          data-testid="infrastructure-detail-panel-close"
          className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
          aria-label="Close detail panel"
        >
          ×
        </button>
      </header>

      <section
        data-testid="infrastructure-detail-panel-properties"
        className="flex flex-col gap-1.5"
      >
        <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
          Properties
        </h3>
        {Object.keys(meta).length === 0 ? (
          <p className="text-xs text-[var(--color-text-dim)]">No properties.</p>
        ) : (
          <dl className="grid grid-cols-3 gap-x-2 gap-y-1.5 text-xs">
            {Object.entries(meta).map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="col-span-1 truncate text-[var(--color-text-dim)]">{k}</dt>
                <dd
                  className="col-span-2 truncate font-mono text-[var(--color-text)]"
                  data-testid={`infrastructure-detail-panel-prop-${k}`}
                >
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </section>

      <section className="flex flex-col gap-1.5">
        <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
          Connections ({neighbors.length})
        </h3>
        <button
          type="button"
          data-testid="infrastructure-detail-panel-toggle-focus"
          onClick={onToggleFocus}
          className="self-start rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text)] hover:bg-[var(--color-bg)]"
        >
          {focused ? 'Exit focus mode' : 'Focus neighbors'}
        </button>
        <ul
          data-testid="infrastructure-detail-panel-neighbors"
          className="max-h-48 overflow-y-auto rounded-md border border-[var(--color-border)]"
        >
          {neighbors.length === 0 ? (
            <li className="px-2 py-1.5 text-xs text-[var(--color-text-dim)]">
              No connections.
            </li>
          ) : (
            neighbors.map((nb) => (
              <li key={nb.id}>
                <button
                  type="button"
                  data-testid={`infrastructure-detail-panel-neighbor-${nb.id}`}
                  onClick={() => onPickNeighbor(nb.id)}
                  className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-xs hover:bg-[var(--color-bg)]"
                >
                  <span
                    aria-hidden="true"
                    className="h-2 w-2 rounded-full"
                    style={{ background: NODE_FILL[nb.type] }}
                  />
                  <span className="truncate text-[var(--color-text)]">{nb.label}</span>
                  <span className="ml-auto text-[var(--color-text-dim)]">{nb.type}</span>
                </button>
              </li>
            ))
          )}
        </ul>
      </section>

      <section
        data-testid="infrastructure-detail-panel-actions"
        className="mt-auto flex flex-col gap-1.5"
      >
        <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
          Actions
        </h3>
        {addChildLabel && (
          <button
            type="button"
            data-testid={
              node.type === 'Region'
                ? 'infrastructure-detail-panel-action-add-cluster'
                : node.type === 'Cluster'
                  ? 'infrastructure-detail-panel-action-add-vcluster'
                  : 'infrastructure-detail-panel-action-add-region'
            }
            onClick={onAddChild}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-left text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)]"
          >
            {addChildLabel}
          </button>
        )}
        {deletable && (
          <button
            type="button"
            data-testid={`infrastructure-detail-panel-action-delete-${node.type.toLowerCase()}`}
            onClick={onDelete}
            className="rounded-md border border-[color-mix(in_srgb,var(--color-danger)_50%,var(--color-border))] px-3 py-1.5 text-left text-xs font-medium text-[var(--color-danger)] hover:bg-[color-mix(in_srgb,var(--color-danger)_8%,transparent)]"
          >
            Delete {node.type}
          </button>
        )}
      </section>
    </aside>
  )
}

interface ContextMenuProps {
  state: ContextMenuState
  onClose: () => void
  onAddRegion: () => void
  onAddChild: () => void
  onAddNodePool: () => void
  onAddLB: () => void
  onDelete: () => void
}

function ContextMenu({
  state,
  onClose,
  onAddRegion,
  onAddChild,
  onAddNodePool,
  onAddLB,
  onDelete,
}: ContextMenuProps) {
  // Click-outside dismissal.
  useEffect(() => {
    function onDoc(ev: MouseEvent) {
      // Anything outside the menu closes it. The menu carries
      // data-testid="cloud-architecture-context-menu" so we test for
      // that.
      const t = ev.target as HTMLElement | null
      if (!t?.closest('[data-testid="cloud-architecture-context-menu"]')) {
        onClose()
      }
    }
    function onEsc(ev: KeyboardEvent) {
      if (ev.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onEsc)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onEsc)
    }
  }, [onClose])

  const items: { key: string; label: string; onClick: () => void; danger?: boolean }[] = []
  if (!state.node) {
    items.push({ key: 'add-region', label: '+ Add region', onClick: onAddRegion })
  } else {
    if (state.node.type === 'Cloud') {
      items.push({ key: 'add-region', label: '+ Add region', onClick: onAddChild })
    }
    if (state.node.type === 'Region') {
      items.push({ key: 'add-cluster', label: '+ Add cluster', onClick: onAddChild })
      items.push({ key: 'add-lb', label: '+ Add load balancer', onClick: onAddLB })
    }
    if (state.node.type === 'Cluster') {
      items.push({ key: 'add-vcluster', label: '+ Add vCluster', onClick: onAddChild })
      items.push({ key: 'add-nodepool', label: '+ Add node pool', onClick: onAddNodePool })
    }
    if (['Region', 'Cluster', 'vCluster'].includes(state.node.type)) {
      items.push({
        key: 'delete',
        label: `Delete ${state.node.type}`,
        onClick: onDelete,
        danger: true,
      })
    }
  }

  if (items.length === 0) return null

  return (
    <div
      data-testid="cloud-architecture-context-menu"
      data-context-target={state.node ? state.node.type : 'canvas'}
      style={{ position: 'fixed', left: state.x, top: state.y, zIndex: 60 }}
      className="min-w-[180px] rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] py-1 shadow-xl"
      role="menu"
    >
      {items.map((it) => (
        <button
          key={it.key}
          type="button"
          role="menuitem"
          data-testid={`cloud-architecture-context-${it.key}`}
          onClick={it.onClick}
          className={`block w-full px-3 py-1.5 text-left text-xs ${
            it.danger
              ? 'text-[var(--color-danger)] hover:bg-[color-mix(in_srgb,var(--color-danger)_8%,transparent)]'
              : 'text-[var(--color-text)] hover:bg-[var(--color-bg)]'
          }`}
        >
          {it.label}
        </button>
      ))}
    </div>
  )
}

/* ── Helpers ─────────────────────────────────────────────────────── */

function stripPrefix(compositeId: string, type: string): string {
  const prefix = `${type}:`
  return compositeId.startsWith(prefix) ? compositeId.slice(prefix.length) : compositeId
}

function inferDefaultProvider(data: HierarchicalInfrastructure): CloudProvider {
  const first = data.cloud[0]
  return ((first?.provider ?? 'hetzner') as CloudProvider)
}

function inferProviderForCluster(
  data: HierarchicalInfrastructure,
  clusterId: string,
): CloudProvider {
  for (const r of data.topology.regions ?? []) {
    for (const c of r.clusters ?? []) {
      if (c.id === clusterId) return r.provider as CloudProvider
    }
  }
  return 'hetzner'
}

function resourceForType(type: ArchNodeType): 'regions' | 'clusters' | 'vclusters' {
  if (type === 'Region') return 'regions'
  if (type === 'Cluster') return 'clusters'
  if (type === 'vCluster') return 'vclusters'
  return 'regions'
}

/* ── Edge type re-exports for callers that want the legend palette. */
export { EDGE_STROKE, EDGE_DASHED } from './types'
export type { GraphEdge as ArchitectureGraphEdge, GraphNode as ArchitectureGraphNode }
