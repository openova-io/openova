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
  AddBucketModal,
  AddClusterModal,
  AddLBModal,
  AddNetworkModal,
  AddNodePoolModal,
  AddPVCModal,
  AddRegionModal,
  AddVClusterModal,
  AddVolumeModal,
  AddWorkerNodeModal,
  DeleteCascadeConfirm,
  EditClusterModal,
  EditLBModal,
  EditNetworkModal,
  EditNodePoolModal,
  EditRegionModal,
  EditVClusterModal,
  EditWorkerNodeModal,
  SimpleDeleteConfirm,
  WipeDeploymentModal,
} from '@/components/CrudModals'
import type { CloudProvider } from '@/entities/deployment/model'
import type {
  BucketItem,
  ClusterSpec,
  HierarchicalInfrastructure,
  LoadBalancerSpec,
  NetworkSpec,
  NodePoolSpec,
  NodeSpec,
  PVCItem,
  RegionSpec,
  VClusterSpec,
  VolumeItem,
} from '@/lib/infrastructure.types'
import { GraphCanvas, type GraphCanvasHandle } from './GraphCanvas'
import { ReconcilerDrillPanel } from './ReconcilerDrillPanel'
import { isReconcilerNode } from './reconcilerDrill'
import { buildCloudGraph } from './cloudGraphData'
import { useK8sCacheStream, type K8sSnapshot } from './useK8sCacheStream'
import type { ReconciliationNode } from '@/lib/reconciliation.api'
import {
  ALL_EDGE_TYPES,
  ALL_NODE_TYPES,
  EDGE_STROKE,
  NODE_FILL,
  SMALL_TYPE_THRESHOLD,
  type ArchEdgeType,
  type ArchNodeType,
  type GraphEdge,
  type GraphNode,
} from './types'
import { LENSES, LENS_ORDER, type LensId } from './presets'
import { useCloudLensOptional, useCloudLensState } from './useCloudLens'
import { NODE_ICON } from './icons'

/* ── Constants ───────────────────────────────────────────────────── */

/**
 * Types that participate in the per-type density slider when their
 * total exceeds SMALL_TYPE_THRESHOLD. Anything not in this list (or
 * with total < threshold) renders fully and the popover only carries
 * the visibility toggle.
 */
const TUNABLE_TYPES: ArchNodeType[] = [
  'WorkerNode',
  'NodePool',
  'LoadBalancer',
  'Network',
  'PVC',
  'Bucket',
  'Volume',
  'Service',
  'Ingress',
  // K8s-side types — high-cardinality kinds whose density slider
  // matters most. The density slider (default 100%, #3970) governs how
  // many of each render once the lens has the chip ON.
  'Pod',
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'ConfigMap',
]

const DEBOUNCE_MS = 400
/**
 * Default global density (#3970). Opens at 100% so EVERY active type
 * renders fully on first paint — no high-cardinality node is silently
 * hidden before the operator touches the slider. (#3958 opened at 50,
 * which the operator rejected: "half my pods are missing and I never
 * moved the slider".)
 */
const DEFAULT_GLOBAL_PCT = 100

/* ── Public props ────────────────────────────────────────────────── */

export interface ArchitectureGraphPageProps {
  deploymentId: string
  data: HierarchicalInfrastructure | null
  isLoading: boolean
  isError: boolean
  onRefetch: () => void
  /**
   * Live K8s cache snapshot fed by the page-level useK8sCacheStream
   * subscription on CloudPage. When provided, this widget reads from
   * the shared snapshot instead of opening its own EventSource —
   * ONE SSE connection per page is the canonical pattern (see
   * useK8sCacheStream's design notes about HTTP/1.1 connection-budget
   * starvation when the same origin opens duplicate streams).
   *
   * When omitted (e.g. in standalone storybook usage or tests), the
   * widget falls back to opening its own subscription.
   */
  k8sSnapshot?: K8sSnapshot | null
  /** Companion revision counter from useK8sCacheStream — bumps on every
   *  applied delta so memoised adapters re-derive when the in-place
   *  Map mutates. */
  k8sRevision?: number
  /**
   * Reconciler nodes from the #3925/#3958 /reconciliation endpoint —
   * Flux HelmReleases + Kustomizations AND the declarative non-Flux
   * reconcilers (cert-manager / CNPG / External-Secrets / Catalyst CRs).
   * Merged into the SAME canvas as typed nodes (shape=category,
   * border=family, fill=state). When omitted (tests / standalone) the
   * canvas is just the cloud + k8s view.
   */
  reconcilers?: ReconciliationNode[] | null
}

/* ── Component ───────────────────────────────────────────────────── */

interface ContextMenuState {
  open: boolean
  x: number
  y: number
  /** When non-null the menu acts on a node; otherwise it's the empty-canvas menu. */
  node: GraphNode | null
}

/**
 * One entry in the detail panel's grouped neighbor list (#348 item 3).
 * The relation field carries the edge type so the panel can group
 * neighbors under per-relation subheaders; direction distinguishes
 * outgoing vs incoming edges (informational — the graph itself is
 * undirected for layout purposes).
 */
interface NeighborEntry {
  node: GraphNode
  relation: ArchEdgeType
  /** "out" = selected → neighbor; "in" = neighbor → selected. */
  direction: 'out' | 'in'
}

type ModalKind =
  | 'none'
  // Add affordances ------------------------------------------------
  | 'add-region'
  | 'add-cluster'
  | 'add-vcluster'
  | 'add-nodepool'
  | 'add-worker-node'
  | 'add-lb'
  | 'add-network'
  | 'add-pvc'
  | 'add-bucket'
  | 'add-volume'
  // Edit affordances -----------------------------------------------
  | 'edit-region'
  | 'edit-cluster'
  | 'edit-vcluster'
  | 'edit-nodepool'
  | 'edit-worker-node'
  | 'edit-lb'
  | 'edit-network'
  | 'edit-pvc'
  | 'edit-bucket'
  | 'edit-volume'
  // Delete affordances ---------------------------------------------
  | 'delete'
  | 'delete-simple'
  | 'wipe-deployment'

interface ModalState {
  kind: ModalKind
}

export function ArchitectureGraphPage({
  deploymentId,
  data,
  isLoading,
  isError,
  onRefetch,
  k8sSnapshot: k8sSnapshotProp,
  k8sRevision: k8sRevisionProp,
  reconcilers,
}: ArchitectureGraphPageProps) {
  const handleRef = useRef<GraphCanvasHandle | null>(null)

  /* ── 0. Live K8s cache stream — feeds the K8s-side adapter
   *     alongside the cloud-side hierarchy fetch.
   *
   *     PRIMARY: read from the page-level shared snapshot passed in
   *     via props. CloudPage holds ONE EventSource subscribing to all
   *     kinds; the snapshot is broadcast to (a) the chip counts in
   *     the toolbar, (b) every K8sListPage in list view, and (c) this
   *     graph view. Sharing one subscription avoids the HTTP/1.1
   *     6-connections-per-origin starvation that otherwise leaves
   *     consumers stuck on the "connecting" placeholder forever.
   *
   *     FALLBACK: when no snapshot is supplied (storybook / tests /
   *     direct embed), open an internal stream so the widget remains
   *     self-sufficient. */
  const fallback = useK8sCacheStream(deploymentId, {
    enabled: k8sSnapshotProp == null,
  })
  const k8sSnapshot = k8sSnapshotProp ?? fallback.snapshot
  const k8sRevision = k8sRevisionProp ?? fallback.revision

  /* ── 1. Adapter — the ONE dataset: tree → nodes/edges, merged with the
   *     K8s side AND the reconciler set (#3958/#3970). Shared with the
   *     list view via buildCloudGraph so graph + list are provably the
   *     same dataset incl. reconcilers. */
  const { nodes: allNodes, edges: allEdges } = useMemo(
    () => buildCloudGraph(data, k8sSnapshot, reconcilers),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data, k8sRevision, reconcilers],
  )

  /* ── 2. Type totals for slider sizing ──────────────────────── */
  const typeTotals = useMemo(() => {
    const m = new Map<ArchNodeType, number>()
    for (const n of allNodes) m.set(n.type, (m.get(n.type) ?? 0) + 1)
    return m
  }, [allNodes])

  /* ── 3. Lens = active chip-set + density state (#3970) ────────
   * THE MODEL: a lens IS a named chip-set. Selecting a lens REPLACES the
   * active chip-set with exactly that lens's chips; the strip renders
   * ONLY the active chips; `+ Add` grows the active set (active = lens ∪
   * additions); removing a chip shrinks it. There is NO separate hidden
   * filter layer — `hiddenTypes` is just the complement of `activeTypes`.
   * Default lens = Cloud (#3970).
   *
   * The lens state is SHARED with the list view via CloudLensProvider so
   * the view=graph↔list toggle preserves the lens + chips. When rendered
   * standalone (tests/storybook with no provider) we fall back to a
   * private instance. */
  const sharedLens = useCloudLensOptional()
  const privateLens = useCloudLensState()
  const { lensId, activeTypes, isCustom, selectLens, addChip, removeChip } =
    sharedLens ?? privateLens

  // The set of types HIDDEN from the canvas — purely the complement of
  // the active chip-set. The strip IS the lens (made editable); nothing
  // is hidden "underneath" a fuller strip (#3970 — the filter-layer is
  // deleted).
  const hiddenTypes = useMemo(() => {
    const h = new Set<ArchNodeType>()
    for (const t of ALL_NODE_TYPES) {
      if (!activeTypes.has(t)) h.add(t)
    }
    return h
  }, [activeTypes])

  // Per-type explicit cap; null = "all" (no cap).
  const [typeCap, setTypeCap] = useState<Partial<Record<ArchNodeType, number | null>>>({})
  const [globalPct, setGlobalPct] = useState<number>(DEFAULT_GLOBAL_PCT)

  // Debounce so repeatedly nudging a slider doesn't hammer the
  // simulation / refetch.
  const [debouncedCap, setDebouncedCap] = useState(typeCap)
  useEffect(() => {
    const id = setTimeout(() => setDebouncedCap(typeCap), DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [typeCap])

  /**
   * Derive whether a type is "small" (auto-100%). Small types skip
   * the global slider entirely and render fully whenever their chip
   * is active. The threshold (#348 item 1) is shared with the
   * test suite via SMALL_TYPE_THRESHOLD.
   */
  function isSmallType(t: ArchNodeType): boolean {
    const total = typeTotals.get(t) ?? 0
    return total < SMALL_TYPE_THRESHOLD
  }

  // Global slider — applies a percentage cap to every tunable type
  // with total >= SMALL_TYPE_THRESHOLD. Small types are always
  // rendered at 100% (#348 item 1).
  function setGlobalDensity(pct: number) {
    setGlobalPct(pct)
    const next: Partial<Record<ArchNodeType, number | null>> = { ...typeCap }
    for (const t of TUNABLE_TYPES) {
      const total = typeTotals.get(t) ?? 0
      if (total === 0) continue
      if (isSmallType(t)) {
        // Small types: clear any cap so they render fully.
        next[t] = null
        continue
      }
      next[t] = Math.max(0, Math.round((total * pct) / 100))
    }
    setTypeCap(next)
  }

  const effectiveTypeLimits = useMemo(() => {
    const out: Partial<Record<ArchNodeType, number>> = {}
    for (const t of TUNABLE_TYPES) {
      if (isSmallType(t)) continue // small types always render at 100%
      const v = debouncedCap[t]
      if (typeof v === 'number') out[t] = v
    }
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedCap, typeTotals])

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

  // Neighbor list for the detail panel — annotated with the relation
  // type (and direction marker) for grouping (#348 item 3).
  const neighborList = useMemo<NeighborEntry[]>(() => {
    if (!selectedNode) return []
    const byId = new Map(allNodes.map((n) => [n.id, n]))
    const out: NeighborEntry[] = []
    for (const e of allEdges) {
      if (e.source === selectedNode.id) {
        const target = byId.get(e.target)
        if (target) out.push({ node: target, relation: e.type, direction: 'out' })
      } else if (e.target === selectedNode.id) {
        const source = byId.get(e.source)
        if (source) out.push({ node: source, relation: e.type, direction: 'in' })
      }
    }
    return out
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
    <div
      data-testid="cloud-architecture"
      className="relative flex h-full min-h-[540px] flex-col"
    >
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

        {/* Lens dropdown (#3970) — a lens IS the chip-set below. Picking
            a lens REPLACES the active chips with exactly that lens's
            chips (the strip shows ONLY those). `Custom` shows once the
            operator has +Added or removed a chip. */}
        <label className="flex items-center gap-1.5 text-xs text-[var(--color-text-dim)]" htmlFor="arch-lens">
          Lens
          <select
            id="arch-lens"
            data-testid="cloud-architecture-lens"
            value={lensId}
            onChange={(e) => selectLens(e.target.value as LensId)}
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-text)]"
          >
            <optgroup label="Domain">
              {LENS_ORDER.filter((id) => LENSES[id].group === 'domain').map((id) => (
                <option key={id} value={id}>
                  {LENSES[id].label}
                </option>
              ))}
            </optgroup>
            <optgroup label="Family">
              {LENS_ORDER.filter((id) => LENSES[id].group === 'family').map((id) => (
                <option key={id} value={id}>
                  {LENSES[id].label}
                </option>
              ))}
            </optgroup>
          </select>
          {isCustom && (
            <span
              data-testid="cloud-architecture-lens-custom"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-1.5 py-0.5 text-[10px] font-semibold text-[var(--color-text-dim)]"
            >
              Custom
            </span>
          )}
        </label>

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

      {/* Per-type chips with mini density controls. */}
      <div
        data-testid="cloud-architecture-type-bar"
        className="mb-2 flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
      >
        {ALL_NODE_TYPES.filter((t) => activeTypes.has(t)).map((t) => {
          const total = typeTotals.get(t) ?? 0
          const isTunable = TUNABLE_TYPES.includes(t)
          const cap = typeCap[t]
          const small = isSmallType(t)
          return (
            <TypeBadge
              key={t}
              type={t}
              total={total}
              capped={typeof cap === 'number' ? cap : null}
              small={small}
              tunable={isTunable}
              onRemove={() => removeChip(t)}
              onSetCap={(v) => setTypeCap((prev) => ({ ...prev, [t]: v }))}
            />
          )
        })}
        <AddChipPopover
          inactiveTypes={ALL_NODE_TYPES.filter((t) => !activeTypes.has(t))}
          typeTotals={typeTotals}
          onAdd={addChip}
        />
      </div>

      <div
        data-testid="cloud-architecture-canvas-wrap"
        className="relative w-full flex-1 overflow-hidden rounded-xl border border-[var(--color-border)]"
        style={{ minHeight: 540 }}
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
            hiddenTypes={hiddenTypes}
            typeLimits={effectiveTypeLimits}
            showLegend
            edgeTypeCounts={edgeTypeCounts}
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

      {/* #3980 fix 3 — the standalone bottom "ArchiMate connections (N)"
       *  button is retired. The relations list now lives INSIDE the
       *  collapsible Legend panel (GraphLegend, bottom-right of the
       *  canvas) which the canvas renders via showLegend + edgeTypeCounts.
       *  No separate bottom button. */}

      {/* Reconciler drill-in (UAT row 193 / #5223) — a reconciler-side
          node opens the #3996 drill (status + owning-controller logs +
          reconcile/suspend/resume) instead of the generic infrastructure
          panel, re-wiring the drill that lived on the deleted
          /reconciliation page onto the unified Cloud graph. */}
      {selectedNode && isReconcilerNode(selectedNode) && (
        <ReconcilerDrillPanel
          deploymentId={deploymentId}
          node={selectedNode}
          onClose={() => setSelectedId(null)}
        />
      )}

      {/* Detail panel — slides in from the right on node click. */}
      {selectedNode && !isReconcilerNode(selectedNode) && (
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
          onEdit={() => {
            const k = selectedNode.type
            if (k === 'Region') setModal({ kind: 'edit-region' })
            else if (k === 'Cluster') setModal({ kind: 'edit-cluster' })
            else if (k === 'vCluster') setModal({ kind: 'edit-vcluster' })
            else if (k === 'NodePool') setModal({ kind: 'edit-nodepool' })
            else if (k === 'WorkerNode') setModal({ kind: 'edit-worker-node' })
            else if (k === 'LoadBalancer') setModal({ kind: 'edit-lb' })
            else if (k === 'Network') setModal({ kind: 'edit-network' })
          }}
          onDelete={() => {
            // Cloud root → deployment-level wipe (Phase-0 orphan purge,
            // tofu destroy + Hetzner orphan force-purge + PDM release +
            // local cleanup). Per-resource Crossplane XRC delete is the
            // right path for Region / Cluster / vCluster (day-2 cascade).
            // Standalone resources (NodePool / WorkerNode / LoadBalancer /
            // Network) go through the SimpleDeleteConfirm. See
            // docs/INVIOLABLE-PRINCIPLES.md #3.
            if (selectedNode.type === 'Cloud') {
              setModal({ kind: 'wipe-deployment' })
            } else if (
              selectedNode.type === 'Region' ||
              selectedNode.type === 'Cluster' ||
              selectedNode.type === 'vCluster'
            ) {
              setModal({ kind: 'delete' })
            } else {
              setModal({ kind: 'delete-simple' })
            }
          }}
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
          onAddWorkerNode={() => {
            setModal({ kind: 'add-worker-node' })
            closeCtxMenu()
          }}
          onAddLB={() => {
            setModal({ kind: 'add-lb' })
            closeCtxMenu()
          }}
          onAddNetwork={() => {
            setModal({ kind: 'add-network' })
            closeCtxMenu()
          }}
          onAddPVC={() => {
            setModal({ kind: 'add-pvc' })
            closeCtxMenu()
          }}
          onAddBucket={() => {
            setModal({ kind: 'add-bucket' })
            closeCtxMenu()
          }}
          onAddVolume={() => {
            setModal({ kind: 'add-volume' })
            closeCtxMenu()
          }}
          onEdit={() => {
            const k = ctxMenu.node?.type
            if (k === 'Region') setModal({ kind: 'edit-region' })
            else if (k === 'Cluster') setModal({ kind: 'edit-cluster' })
            else if (k === 'vCluster') setModal({ kind: 'edit-vcluster' })
            else if (k === 'NodePool') setModal({ kind: 'edit-nodepool' })
            else if (k === 'WorkerNode') setModal({ kind: 'edit-worker-node' })
            else if (k === 'LoadBalancer') setModal({ kind: 'edit-lb' })
            else if (k === 'Network') setModal({ kind: 'edit-network' })
            closeCtxMenu()
          }}
          onDelete={() => {
            // Same Cloud-root → wipe rule as the detail panel.
            const k = ctxMenu.node?.type
            if (k === 'Cloud') {
              setModal({ kind: 'wipe-deployment' })
            } else if (k === 'Region' || k === 'Cluster' || k === 'vCluster') {
              setModal({ kind: 'delete' })
            } else {
              setModal({ kind: 'delete-simple' })
            }
            closeCtxMenu()
          }}
        />
      )}

      {/* AddRegion + storage-class adds can fire from an empty canvas
          right-click — they don't need a node anchor. */}
      {data && (
        <>
          <AddRegionModal
            open={modal.kind === 'add-region'}
            deploymentId={deploymentId}
            defaultProvider={inferDefaultProvider(data)}
            onClose={() => setModal({ kind: 'none' })}
          />
          <AddPVCModal
            open={modal.kind === 'add-pvc'}
            deploymentId={deploymentId}
            storageClasses={inferStorageClasses(data)}
            onClose={() => setModal({ kind: 'none' })}
          />
          <AddBucketModal
            open={modal.kind === 'add-bucket'}
            deploymentId={deploymentId}
            onClose={() => setModal({ kind: 'none' })}
          />
          <AddVolumeModal
            open={modal.kind === 'add-volume' && !modalAnchor}
            deploymentId={deploymentId}
            regionIds={allRegionIds(data)}
            nodeIds={allNodeIds(data)}
            onClose={() => setModal({ kind: 'none' })}
          />
          <AddNetworkModal
            open={modal.kind === 'add-network' && !modalAnchor}
            deploymentId={deploymentId}
            regionIds={allRegionIds(data)}
            onClose={() => setModal({ kind: 'none' })}
          />
        </>
      )}

      {/* All other CRUD modals require a node anchor — they're
          opened from the detail panel or a node-targeted context
          menu. */}
      {data && modalAnchor && (() => {
        const lookup = lookupSpecForGraphNode(data, modalAnchor)
        const closeModal = () => setModal({ kind: 'none' })
        const stripped = stripPrefix(modalAnchor.id, modalAnchor.type)
        return (
          <>
            {modalAnchor.type === 'Region' && (
              <>
                <AddClusterModal
                  open={modal.kind === 'add-cluster'}
                  deploymentId={deploymentId}
                  regionId={stripped}
                  regionProvider={lookup.provider}
                  onClose={closeModal}
                />
                <AddLBModal
                  open={modal.kind === 'add-lb'}
                  deploymentId={deploymentId}
                  regionId={stripped}
                  onClose={closeModal}
                />
                <AddNetworkModal
                  open={modal.kind === 'add-network'}
                  deploymentId={deploymentId}
                  regionIds={allRegionIds(data)}
                  defaultRegionId={stripped}
                  onClose={closeModal}
                />
                <AddVolumeModal
                  open={modal.kind === 'add-volume'}
                  deploymentId={deploymentId}
                  regionIds={allRegionIds(data)}
                  nodeIds={nodeIdsForRegion(lookup.parentRegion)}
                  defaultRegionId={stripped}
                  onClose={closeModal}
                />
                {lookup.region && (
                  <EditRegionModal
                    open={modal.kind === 'edit-region'}
                    deploymentId={deploymentId}
                    region={lookup.region}
                    onClose={closeModal}
                  />
                )}
              </>
            )}

            {modalAnchor.type === 'Cluster' && (
              <>
                <AddVClusterModal
                  open={modal.kind === 'add-vcluster'}
                  deploymentId={deploymentId}
                  clusterId={stripped}
                  onClose={closeModal}
                />
                <AddNodePoolModal
                  open={modal.kind === 'add-nodepool'}
                  deploymentId={deploymentId}
                  clusterId={stripped}
                  regionProvider={inferProviderForCluster(data, stripped)}
                  onClose={closeModal}
                />
                <AddWorkerNodeModal
                  open={modal.kind === 'add-worker-node'}
                  deploymentId={deploymentId}
                  clusterId={stripped}
                  regionProvider={inferProviderForCluster(data, stripped)}
                  onClose={closeModal}
                />
                {lookup.cluster && (
                  <EditClusterModal
                    open={modal.kind === 'edit-cluster'}
                    deploymentId={deploymentId}
                    cluster={lookup.cluster}
                    regionProvider={lookup.provider}
                    onClose={closeModal}
                  />
                )}
              </>
            )}

            {modalAnchor.type === 'vCluster' && lookup.vcluster && (
              <EditVClusterModal
                open={modal.kind === 'edit-vcluster'}
                deploymentId={deploymentId}
                vcluster={lookup.vcluster}
                onClose={closeModal}
              />
            )}

            {modalAnchor.type === 'NodePool' && lookup.pool && (
              <EditNodePoolModal
                open={modal.kind === 'edit-nodepool'}
                deploymentId={deploymentId}
                pool={lookup.pool}
                regionProvider={lookup.provider}
                onClose={closeModal}
              />
            )}

            {modalAnchor.type === 'WorkerNode' && lookup.node && (
              <EditWorkerNodeModal
                open={modal.kind === 'edit-worker-node'}
                deploymentId={deploymentId}
                node={lookup.node}
                regionProvider={lookup.provider}
                onClose={closeModal}
              />
            )}

            {modalAnchor.type === 'LoadBalancer' && lookup.lb && (
              <EditLBModal
                open={modal.kind === 'edit-lb'}
                deploymentId={deploymentId}
                lb={lookup.lb}
                onClose={closeModal}
              />
            )}

            {modalAnchor.type === 'Network' && lookup.network && (
              <EditNetworkModal
                open={modal.kind === 'edit-network'}
                deploymentId={deploymentId}
                network={lookup.network}
                onClose={closeModal}
              />
            )}

            {/* Cascade-aware delete (Region/Cluster/vCluster). */}
            <DeleteCascadeConfirm
              open={modal.kind === 'delete'}
              deploymentId={deploymentId}
              resource={resourceForType(modalAnchor.type)}
              resourceId={stripped}
              resourceLabel={modalAnchor.label}
              onClose={closeModal}
            />

            {/* Standalone delete (NodePool / WorkerNode / LoadBalancer / Network). */}
            {(() => {
              const r = simpleDeleteResource(modalAnchor.type)
              if (!r) return null
              return (
                <SimpleDeleteConfirm
                  open={modal.kind === 'delete-simple'}
                  deploymentId={deploymentId}
                  resource={r}
                  resourceId={stripped}
                  resourceLabel={modalAnchor.label}
                  resourceKind={modalAnchor.type}
                  onClose={closeModal}
                />
              )
            })()}
          </>
        )
      })()}

      {/* Deployment-level Phase-0 wipe (issue #318). Reachable from the
          Cloud-root node's Delete action OR the canvas context menu's
          Delete on a Cloud node. Distinct from DeleteCascadeConfirm
          which targets Crossplane XRCs in the day-2 path. */}
      <WipeDeploymentModal
        open={modal.kind === 'wipe-deployment'}
        deploymentId={deploymentId}
        sovereignFQDN={inferSovereignFQDNFromGraph(data)}
        onClose={() => setModal({ kind: 'none' })}
        onWiped={() => {
          setModal({ kind: 'none' })
          // Hard-navigate to /sovereign so the wizard re-mounts fresh
          // (no stale provisioning store state).
          window.location.href = '/sovereign'
        }}
      />
    </div>
  )
}

/** Derive the sovereign FQDN from the Architecture data so the
 *  WipeDeploymentModal confirm input matches what the operator sees on
 *  the cluster row. First cluster's name is the FQDN by convention. */
function inferSovereignFQDNFromGraph(
  data: HierarchicalInfrastructure | null,
): string | null {
  if (!data || !data.topology || !data.topology.regions) return null
  for (const r of data.topology.regions) {
    if (r.clusters && r.clusters.length > 0) return r.clusters[0].name
  }
  return null
}

/* ── Sub-components ──────────────────────────────────────────────── */

interface TypeBadgeProps {
  type: ArchNodeType
  total: number
  capped: number | null
  small: boolean
  tunable: boolean
  onRemove: () => void
  onSetCap: (v: number | null) => void
}

function TypeBadge({
  type,
  total,
  capped,
  small,
  tunable,
  onRemove,
  onSetCap,
}: TypeBadgeProps) {
  const [open, setOpen] = useState(false)
  const dotColor = NODE_FILL[type]
  const visibleCount = capped ?? total

  // Small types: chip click does nothing (visibility toggle off — the
  // "×" handles removal). Tunable + non-small types: click opens the
  // density popover.
  const clickable = tunable && !small

  return (
    <div className="relative">
      <div
        data-testid={`cloud-architecture-type-badge-${type}`}
        data-small={small ? 'true' : 'false'}
        className="inline-flex items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] text-[var(--color-text)]"
      >
        <button
          type="button"
          data-testid={`cloud-architecture-type-badge-${type}-label`}
          onClick={() => clickable && setOpen((v) => !v)}
          className={`flex items-center gap-1.5 rounded-l-md px-2 py-1 text-xs ${
            clickable ? 'hover:bg-[var(--color-bg-2)]' : 'cursor-default'
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
        <button
          type="button"
          data-testid={`cloud-architecture-type-badge-${type}-remove`}
          onClick={onRemove}
          aria-label={`Remove ${type} chip`}
          className="rounded-r-md px-1.5 py-1 text-xs text-[var(--color-text-dim)] hover:bg-[color-mix(in_srgb,var(--color-danger)_8%,transparent)] hover:text-[var(--color-danger)]"
        >
          ×
        </button>
      </div>

      {open && tunable && !small && (
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
          </div>
        </div>
      )}
    </div>
  )
}

/* ── Add chip popover ────────────────────────────────────────────── */

interface AddChipPopoverProps {
  inactiveTypes: ArchNodeType[]
  typeTotals: Map<ArchNodeType, number>
  onAdd: (t: ArchNodeType) => void
}

function AddChipPopover({ inactiveTypes, typeTotals, onAdd }: AddChipPopoverProps) {
  const [open, setOpen] = useState(false)

  // Click-outside dismissal.
  useEffect(() => {
    if (!open) return
    function onDoc(ev: MouseEvent) {
      const t = ev.target as HTMLElement | null
      if (!t?.closest('[data-testid="cloud-architecture-add-chip-popover"]')) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  const disabled = inactiveTypes.length === 0
  return (
    <div className="relative">
      <button
        type="button"
        data-testid="cloud-architecture-add-chip-button"
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        aria-label="Add type chip"
        className={`inline-flex items-center gap-1 rounded-md border border-dashed border-[var(--color-border)] px-2 py-1 text-xs ${
          disabled
            ? 'cursor-not-allowed text-[var(--color-text-dim)] opacity-50'
            : 'text-[var(--color-text)] hover:bg-[var(--color-bg)]'
        }`}
      >
        <span aria-hidden="true">+</span>
        <span>Add</span>
      </button>
      {open && !disabled && (
        <div
          data-testid="cloud-architecture-add-chip-popover"
          className="absolute left-0 top-full z-30 mt-1 flex w-56 flex-col gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-2 shadow-xl"
        >
          {inactiveTypes.map((t) => {
            const total = typeTotals.get(t) ?? 0
            return (
              <button
                key={t}
                type="button"
                data-testid={`cloud-architecture-add-chip-item-${t}`}
                onClick={() => {
                  onAdd(t)
                  setOpen(false)
                }}
                className="flex items-center gap-2 rounded-md px-2 py-1 text-left text-xs text-[var(--color-text)] hover:bg-[var(--color-bg)]"
              >
                <span
                  aria-hidden="true"
                  className="h-2.5 w-2.5 rounded-full"
                  style={{ background: NODE_FILL[t] }}
                />
                <span className="font-medium">{t}</span>
                <span className="ml-auto text-[var(--color-text-dim)]">{total}</span>
              </button>
            )
          })}
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
  neighbors: NeighborEntry[]
  focusNodeId: string | null
  onClose: () => void
  onToggleFocus: () => void
  onAddChild: () => void
  onEdit: () => void
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
  onEdit,
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

  // Group neighbors by relation (#348 item 3). Stable order: follow
  // ALL_EDGE_TYPES so the panel reads consistently between renders.
  const groupedNeighbors = useMemo(() => {
    const groups = new Map<ArchEdgeType, NeighborEntry[]>()
    for (const e of neighbors) {
      const arr = groups.get(e.relation) ?? []
      arr.push(e)
      groups.set(e.relation, arr)
    }
    return ALL_EDGE_TYPES.filter((t) => groups.has(t)).map((t) => ({
      relation: t,
      entries: groups.get(t)!,
    }))
  }, [neighbors])

  // Cloud-root carries a destructive action too — but with different
  // semantics than Region/Cluster/vCluster. Per issue #318, the Cloud
  // node's Delete is the deployment-level Cancel & Wipe (Phase-0 orphan
  // purge: tofu destroy + Hetzner direct-API force-purge + PDM release).
  // Region/Cluster/vCluster Delete goes through Crossplane XRC
  // deletionPolicy (day-2 cascade path, INVIOLABLE-PRINCIPLES.md #3).
  // NodePool / WorkerNode / LoadBalancer / Network Delete goes through
  // the SimpleDeleteConfirm path. Both share the onDelete callback —
  // the parent component branches on node.type to pick the right modal.
  const deletable = [
    'Cloud',
    'Region',
    'Cluster',
    'vCluster',
    'NodePool',
    'WorkerNode',
    'LoadBalancer',
    'Network',
  ].includes(node.type)
  const deleteLabel = node.type === 'Cloud' ? 'Cancel & Wipe deployment' : `Delete ${node.type}`

  // Edit affordance — every node type with a backing spec is editable.
  // Cloud is excluded (no per-cloud config beyond the wipe action).
  const editable = [
    'Region',
    'Cluster',
    'vCluster',
    'NodePool',
    'WorkerNode',
    'LoadBalancer',
    'Network',
  ].includes(node.type)

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
        <div
          data-testid="infrastructure-detail-panel-neighbors"
          className="max-h-[40vh] overflow-y-auto rounded-md border border-[var(--color-border)]"
        >
          {neighbors.length === 0 ? (
            <p className="px-2 py-1.5 text-xs text-[var(--color-text-dim)]">
              No connections.
            </p>
          ) : (
            groupedNeighbors.map(({ relation, entries }) => (
              <div
                key={relation}
                data-testid={`arch-detail-panel-relation-group-${relation}`}
              >
                <h4
                  data-testid={`arch-detail-panel-relation-header-${relation}`}
                  className="sticky top-0 flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]"
                >
                  <span style={{ color: EDGE_STROKE[relation] }}>{relation}</span>
                  <span className="ml-auto text-[var(--color-text-dim)]">{entries.length}</span>
                </h4>
                <ul>
                  {entries.map((entry) => {
                    const Icon = NODE_ICON[entry.node.type]
                    return (
                      <li key={`${entry.relation}:${entry.node.id}`}>
                        <button
                          type="button"
                          data-testid={`arch-detail-panel-neighbor-${entry.relation}-${entry.node.id}`}
                          data-direction={entry.direction}
                          onClick={() => onPickNeighbor(entry.node.id)}
                          className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-xs hover:bg-[var(--color-bg)]"
                        >
                          {Icon ? (
                            <Icon
                              size={14}
                              stroke={2}
                              color={NODE_FILL[entry.node.type]}
                              aria-hidden
                            />
                          ) : (
                            <span
                              aria-hidden="true"
                              className="h-2 w-2 rounded-full"
                              style={{ background: NODE_FILL[entry.node.type] }}
                            />
                          )}
                          <span className="truncate text-[var(--color-text)]">
                            {entry.node.label}
                          </span>
                          <span className="ml-auto text-[var(--color-text-dim)]">
                            {entry.node.type}
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </div>
            ))
          )}
        </div>
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
        {editable && (
          <button
            type="button"
            data-testid={`infrastructure-detail-panel-action-edit-${node.type.toLowerCase()}`}
            onClick={onEdit}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-left text-xs font-medium text-[var(--color-text)] hover:bg-[var(--color-bg)]"
          >
            {`Edit ${node.type}`}
          </button>
        )}
        {deletable && (
          <button
            type="button"
            data-testid={
              node.type === 'Cloud'
                ? 'infrastructure-detail-panel-action-wipe-deployment'
                : `infrastructure-detail-panel-action-delete-${node.type.toLowerCase()}`
            }
            onClick={onDelete}
            className="rounded-md border border-[color-mix(in_srgb,var(--color-danger)_50%,var(--color-border))] px-3 py-1.5 text-left text-xs font-medium text-[var(--color-danger)] hover:bg-[color-mix(in_srgb,var(--color-danger)_8%,transparent)]"
          >
            {deleteLabel}
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
  onAddWorkerNode: () => void
  onAddLB: () => void
  onAddNetwork: () => void
  onAddPVC: () => void
  onAddBucket: () => void
  onAddVolume: () => void
  onEdit: () => void
  onDelete: () => void
}

function ContextMenu({
  state,
  onClose,
  onAddRegion,
  onAddChild,
  onAddNodePool,
  onAddWorkerNode,
  onAddLB,
  onAddNetwork,
  onAddPVC,
  onAddBucket,
  onAddVolume,
  onEdit,
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
    items.push({ key: 'add-pvc', label: '+ Add PVC', onClick: onAddPVC })
    items.push({ key: 'add-bucket', label: '+ Add bucket', onClick: onAddBucket })
    items.push({ key: 'add-volume', label: '+ Add volume', onClick: onAddVolume })
  } else {
    const t = state.node.type
    if (t === 'Cloud') {
      items.push({ key: 'add-region', label: '+ Add region', onClick: onAddChild })
    }
    if (t === 'Region') {
      items.push({ key: 'add-cluster', label: '+ Add cluster', onClick: onAddChild })
      items.push({ key: 'add-lb', label: '+ Add load balancer', onClick: onAddLB })
      items.push({ key: 'add-network', label: '+ Add network', onClick: onAddNetwork })
      items.push({ key: 'add-volume', label: '+ Add volume', onClick: onAddVolume })
    }
    if (t === 'Cluster') {
      items.push({ key: 'add-vcluster', label: '+ Add vCluster', onClick: onAddChild })
      items.push({ key: 'add-nodepool', label: '+ Add node pool', onClick: onAddNodePool })
      items.push({
        key: 'add-worker-node',
        label: '+ Add worker node',
        onClick: onAddWorkerNode,
      })
      items.push({ key: 'add-pvc', label: '+ Add PVC', onClick: onAddPVC })
    }
    // Edit affordance — every node type with a backing spec is
    // editable. Cloud has its own wipe-deployment flow.
    if (['Region', 'Cluster', 'vCluster', 'NodePool', 'WorkerNode', 'LoadBalancer', 'Network'].includes(t)) {
      items.push({
        key: 'edit',
        label: `Edit ${t}`,
        onClick: onEdit,
      })
    }
    // Delete affordance — every kind except Cloud (which becomes
    // wipe-deployment in the parent).
    if (t !== 'Cloud') {
      items.push({
        key: 'delete',
        label: `Delete ${t}`,
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

/* ── Lookup helpers for Edit modals ──────────────────────────────── */

interface SpecLookups {
  region?: RegionSpec
  cluster?: ClusterSpec
  vcluster?: VClusterSpec
  pool?: NodePoolSpec
  node?: NodeSpec
  lb?: LoadBalancerSpec
  network?: NetworkSpec
  pvc?: PVCItem
  bucket?: BucketItem
  volume?: VolumeItem
  /** Provider used by the parent region — drives SKU pickers. */
  provider: CloudProvider
  /** Region of the matched element (when applicable). */
  parentRegion?: RegionSpec
}

function lookupSpecForGraphNode(
  data: HierarchicalInfrastructure,
  node: GraphNode,
): SpecLookups {
  const id = stripPrefix(node.id, node.type)
  const out: SpecLookups = { provider: 'hetzner' }
  for (const region of data.topology.regions ?? []) {
    if (node.type === 'Region' && region.id === id) {
      out.region = region
      out.provider = (region.provider as CloudProvider) ?? 'hetzner'
      out.parentRegion = region
      return out
    }
    for (const cluster of region.clusters ?? []) {
      if (node.type === 'Cluster' && cluster.id === id) {
        out.cluster = cluster
        out.provider = (region.provider as CloudProvider) ?? 'hetzner'
        out.parentRegion = region
        return out
      }
      for (const vc of cluster.vclusters ?? []) {
        if (node.type === 'vCluster' && vc.id === id) {
          out.vcluster = vc
          out.provider = (region.provider as CloudProvider) ?? 'hetzner'
          out.parentRegion = region
          return out
        }
      }
      for (const pool of cluster.nodePools ?? []) {
        if (node.type === 'NodePool' && pool.id === id) {
          out.pool = pool
          out.provider = (region.provider as CloudProvider) ?? 'hetzner'
          out.parentRegion = region
          return out
        }
      }
      for (const wn of cluster.nodes ?? []) {
        if (node.type === 'WorkerNode' && wn.id === id) {
          out.node = wn
          out.provider = (region.provider as CloudProvider) ?? 'hetzner'
          out.parentRegion = region
          return out
        }
      }
      for (const lb of cluster.loadBalancers ?? []) {
        if (node.type === 'LoadBalancer' && lb.id === id) {
          out.lb = lb
          out.provider = (region.provider as CloudProvider) ?? 'hetzner'
          out.parentRegion = region
          return out
        }
      }
    }
    for (const net of region.networks ?? []) {
      if (node.type === 'Network' && net.id === id) {
        out.network = net
        out.provider = (region.provider as CloudProvider) ?? 'hetzner'
        out.parentRegion = region
        return out
      }
    }
  }
  return out
}

/** Map a graph node type to the DELETE resource segment. Used for the
 *  SimpleDeleteConfirm path (non-cascade kinds). */
function simpleDeleteResource(type: ArchNodeType):
  | 'pools'
  | 'nodes'
  | 'loadbalancers'
  | 'networks'
  | null {
  switch (type) {
    case 'NodePool':
      return 'pools'
    case 'WorkerNode':
      return 'nodes'
    case 'LoadBalancer':
      return 'loadbalancers'
    case 'Network':
      return 'networks'
    default:
      return null
  }
}

/** Worker-node ids in the same parent region as a given lookup. */
function nodeIdsForRegion(region: RegionSpec | undefined): string[] {
  if (!region) return []
  const ids: string[] = []
  for (const c of region.clusters ?? []) {
    for (const n of c.nodes ?? []) ids.push(n.id)
  }
  return ids
}

/** All region ids in the deployment. */
function allRegionIds(data: HierarchicalInfrastructure): string[] {
  return (data.topology.regions ?? []).map((r) => r.id)
}

/** Distinct storage classes seen in the topology — feeds the PVC
 *  Add modal. Falls back to ["default"] when the topology hasn't
 *  surfaced any. */
function inferStorageClasses(data: HierarchicalInfrastructure): string[] {
  const set = new Set<string>()
  for (const pvc of data.storage?.pvcs ?? []) set.add(pvc.storageClass)
  return set.size > 0 ? Array.from(set).sort() : ['default']
}

/** All worker-node ids (across all regions). */
function allNodeIds(data: HierarchicalInfrastructure): string[] {
  const ids: string[] = []
  for (const r of data.topology.regions ?? []) {
    for (const c of r.clusters ?? []) {
      for (const n of c.nodes ?? []) ids.push(n.id)
    }
  }
  return ids
}

/* ── Edge type re-exports for callers that want the legend palette. */
export { EDGE_STROKE, EDGE_DASHED } from './types'
export type { GraphEdge as ArchitectureGraphEdge, GraphNode as ArchitectureGraphNode }
