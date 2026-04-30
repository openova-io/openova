/**
 * Dashboard — Sovereign-portal resource utilisation surface served at
 *   /sovereign/provision/$deploymentId/dashboard
 *
 * Founder spec (verbatim, condensed):
 *   • Treemap rectangles. Box AREA = resource limit allocated.
 *     Box COLOR = utilisation (continuous gradient blue → green → red:
 *     blue = wasted, green = optimum, red = over-utilised).
 *   • Recharts <Treemap>, NOT raw D3. Recharts handles the squarified
 *     layout; we only own the cell renderer + the toolbar + drill-down.
 *   • Up to 4 layers, picked from
 *     [sovereign | cluster | family | namespace | application]. The
 *     first layer is the outer ring; deeper layers nest inside.
 *   • Click a parent cell → drill in (push onto a breadcrumb stack).
 *     Clicking a breadcrumb pops back. NO refetch — the breadcrumb
 *     walks the in-memory tree.
 *   • When `sizeBy` is a capacity metric the colour selector locks
 *     to `utilization` — the controller component owns this rule.
 *
 * ── Why module-level callback refs (the unsexy part) ────────────────
 * Recharts clones the `content` prop into its own DOM tree; the cloned
 * tree is rendered with a static React API that does NOT preserve the
 * outer component's closures or hooks. Practically: if the cell
 * renderer reads from React state directly, every state change is
 * invisible to the cloned tree.
 *
 * The fix is a tiny module-level mailbox the page sets at render time
 * (`_onCellHover`, `_onCellClick`, `_activeColorFn`); the cloned
 * cell renderer reads from those. No hooks inside the cell renderer,
 * no closure capture, no children-rerender hacks. This pattern is
 * lifted directly from Recharts' own examples for treemap drill-down.
 *
 * ── Why a parentBoundsByName Map ────────────────────────────────────
 * Recharts doesn't tell child cells where the parent header bar is.
 * Without that information a tall, narrow child can render its label
 * UNDER the parent's 24px header strip and look broken. We track the
 * parent's measured y/x in a Map (key = parent name) and clip child
 * label y-positions to (parentY + headerHeight + padding).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the metric
 * options + dimension list live in the controller / types module, not
 * in this page. The cell padding / header height that DO live here are
 * named constants exported for tests.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useRouter } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ResponsiveContainer, Treemap } from 'recharts'

import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import {
  TreemapLayerController,
} from '@/components/TreemapLayerController'
import {
  colorFunctionFor,
  getDashboardTreemap,
  walkDrillPath,
  type TreemapColorBy,
  type TreemapData,
  type TreemapDimension,
  type TreemapItem,
  type TreemapSizeBy,
} from '@/lib/treemap.types'

/* ── Constants (named, not inline literals) ─────────────────────── */

/** Pixel height of the parent header strip in nested mode. The cell
 *  renderer reserves this band along the top of every parent cell so
 *  the parent label has a stable reading row, no matter the cell
 *  geometry. */
export const NESTED_HEADER_HEIGHT_PX = 24

/** Minimum pixel size at which a cell's label / sub-label render at
 *  all. Anything smaller looks like noise — recharts still draws the
 *  rectangle, we just suppress the text. */
export const LABEL_MIN_WIDTH_PX = 50
export const LABEL_MIN_HEIGHT_PX = 24

/** Inner padding for parent cells in nested mode — the children
 *  rectangle starts this many px below the header strip. */
export const NESTED_PADDING_PX = 2

/** Tooltip linger time — keeps the tooltip up after the operator
 *  leaves the cell so they can mouse over the link inside it. */
const TOOLTIP_KEEP_ALIVE_MS = 300

/** React Query stale time for treemap data. */
const TREEMAP_STALE_MS = 60_000

/* ── Module-level mailbox (see file header) ─────────────────────── */

interface CellHoverInfo {
  item: TreemapItem
  x: number
  y: number
}

let _onCellHover: ((info: CellHoverInfo | null) => void) | null = null
let _onCellClick: ((item: TreemapItem) => void) | null = null
let _activeColorFn: (pct: number) => string = colorFunctionFor('utilization')
const _parentBoundsByName: Map<string, { x: number; y: number; width: number; height: number }> =
  new Map()
/** Lookup table keyed by cell `name` so the cell renderer can recover
 *  the original TreemapItem (with its `children[]` and full
 *  `percentage`) from whatever shape Recharts hands the renderer.
 *  The page repopulates this at every render. */
const _itemsByName: Map<string, TreemapItem> = new Map()

/* ── Page ────────────────────────────────────────────────────────── */

export interface DashboardProps {
  /** Test seam — disables the live SSE attach (the dashboard doesn't
   *  consume events itself, but the PortalShell's parent does). */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialDataOverride?: TreemapData
  /** Test seam — initial state of the layer / colour / size selects. */
  initialLayers?: readonly TreemapDimension[]
  initialColorBy?: TreemapColorBy
  initialSizeBy?: TreemapSizeBy
}

export function Dashboard({
  disableStream = false,
  initialDataOverride,
  initialLayers,
  initialColorBy,
  initialSizeBy,
}: DashboardProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/dashboard' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId
  const router = useRouter()

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const [layers, setLayers] = useState<readonly TreemapDimension[]>(
    initialLayers ?? ['family', 'application'],
  )
  const [colorBy, setColorBy] = useState<TreemapColorBy>(initialColorBy ?? 'utilization')
  const [sizeBy, setSizeBy] = useState<TreemapSizeBy>(initialSizeBy ?? 'cpu_limit')

  /** Drill stack — each entry is a (dimension, id, name) triple. The
   *  visible items are derived by walking the in-memory tree. The
   *  React key for the drill state is derived from layers/colorBy/
   *  sizeBy so changing any of those triggers a remount of the inner
   *  surface and naturally resets the drill path — no setState in an
   *  effect. */
  const drillKey = `${layers.join(',')}|${colorBy}|${sizeBy}`
  const [drillState, setDrillState] = useState<{
    key: string
    path: Array<{ dimension: TreemapDimension; id: string | null; name: string }>
  }>({ key: drillKey, path: [] })
  // If the controls changed, drop the drill path on the next render.
  // This is a derived-state-from-prop pattern, not a side-effect.
  const drillPath = drillState.key === drillKey ? drillState.path : []
  function setDrillPath(
    next:
      | Array<{ dimension: TreemapDimension; id: string | null; name: string }>
      | ((prev: Array<{ dimension: TreemapDimension; id: string | null; name: string }>) => Array<{
          dimension: TreemapDimension
          id: string | null
          name: string
        }>),
  ) {
    setDrillState((prev) => ({
      key: drillKey,
      path: typeof next === 'function' ? next(prev.key === drillKey ? prev.path : []) : next,
    }))
  }

  /** Hover state. The actual rendering uses a Paper-style absolute
   *  div positioned near the cursor; the data lives here. */
  const [hoverInfo, setHoverInfo] = useState<CellHoverInfo | null>(null)
  const hoverTimerRef = useRef<number | null>(null)

  const query = useQuery<TreemapData>({
    queryKey: ['treemap', layers.join(','), colorBy, sizeBy, deploymentId],
    queryFn: () => getDashboardTreemap(layers, colorBy, sizeBy, deploymentId),
    staleTime: TREEMAP_STALE_MS,
    enabled: !initialDataOverride,
    placeholderData: (prev) => prev,
  })

  const treemapData: TreemapData | undefined = initialDataOverride ?? query.data
  const totalCount = treemapData?.total_count ?? 0

  /* Visible items at the current drill depth. */
  const visibleItems = useMemo<TreemapItem[]>(() => {
    if (!treemapData) return []
    return walkDrillPath(treemapData.items, drillPath)
  }, [treemapData, drillPath])

  /* Wire module-level callbacks. The cell renderer reads from these
   * synchronously, no React closure capture. */
  const colorFn = useMemo(() => colorFunctionFor(colorBy), [colorBy])
  useEffect(() => {
    _activeColorFn = colorFn
  }, [colorFn])
  useEffect(() => {
    _onCellHover = (info) => {
      if (hoverTimerRef.current !== null) {
        window.clearTimeout(hoverTimerRef.current)
        hoverTimerRef.current = null
      }
      if (info === null) {
        // Linger — give the operator time to traverse to the tooltip's
        // own link affordance before hiding.
        hoverTimerRef.current = window.setTimeout(() => {
          setHoverInfo(null)
        }, TOOLTIP_KEEP_ALIVE_MS)
        return
      }
      setHoverInfo(info)
    }
  }, [])

  useEffect(() => {
    _onCellClick = (item) => {
      if (!item.children || item.children.length === 0) return
      const dimension = layers[drillPath.length] ?? layers[layers.length - 1]
      setDrillPath((prev) => [
        ...prev,
        { dimension, id: item.id, name: item.name },
      ])
    }
  }, [layers, drillPath.length])

  useEffect(() => {
    return () => {
      if (hoverTimerRef.current !== null) {
        window.clearTimeout(hoverTimerRef.current)
      }
    }
  }, [])

  /* Reset parent bounds map every render. The cell renderer fills it
   * during the same paint, so children seeing an outdated map is
   * acceptable on the first paint and self-corrects on the next. */
  _parentBoundsByName.clear()
  // Repopulate the name→item lookup so the cell renderer can
  // recover the original TreemapItem (with children and a full
  // percentage) regardless of how recharts mangles the props.
  _itemsByName.clear()
  for (const it of visibleItems) {
    _itemsByName.set(it.name, it)
    if (it.children) {
      for (const c of it.children) _itemsByName.set(c.name, c)
    }
  }

  const isEmpty = !query.isLoading && (!treemapData || treemapData.items.length === 0)
  const isNested = layers.length > 1 && drillPath.length === 0

  function popDrillTo(idx: number) {
    setDrillPath((prev) => prev.slice(0, idx))
  }

  function navigateToApp(componentId: string) {
    router.navigate({
      to: '/provision/$deploymentId/app/$componentId',
      params: { deploymentId, componentId },
    })
  }

  /* ── Render ────────────────────────────────────────────────────── */

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <div className="mx-auto max-w-7xl" data-testid="dashboard-page">
        <header className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h1
              className="text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="dashboard-title"
            >
              Dashboard
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Resource utilisation across this Sovereign — box size shows allocated capacity, colour shows how it&rsquo;s being used.
            </p>
          </div>
          <div className="text-right text-xs text-[var(--color-text-dim)]">
            <div data-testid="dashboard-total-count">{totalCount} items</div>
            <div className="font-mono">{deploymentId.slice(0, 8)}</div>
          </div>
        </header>

        <TreemapLayerController
          layers={layers}
          setLayers={setLayers}
          colorBy={colorBy}
          setColorBy={setColorBy}
          sizeBy={sizeBy}
          setSizeBy={setSizeBy}
        />

        {/* Breadcrumbs — drill stack pop targets. Always visible so the
         *  operator can see the depth even when at root (root chip is
         *  shown as the active item). */}
        <nav
          className="mt-3 flex flex-wrap items-center gap-1 text-xs"
          aria-label="Drill path"
          data-testid="dashboard-breadcrumb"
        >
          <button
            type="button"
            onClick={() => popDrillTo(0)}
            className={`rounded-md px-2 py-1 transition-colors ${
              drillPath.length === 0
                ? 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]'
                : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
            }`}
            data-testid="dashboard-breadcrumb-root"
          >
            All
          </button>
          {drillPath.map((step, i) => (
            <span key={`${step.id}-${i}`} className="flex items-center gap-1">
              <span className="text-[var(--color-text-dimmer)]">/</span>
              <button
                type="button"
                onClick={() => popDrillTo(i + 1)}
                className={`rounded-md px-2 py-1 transition-colors ${
                  i === drillPath.length - 1
                    ? 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]'
                    : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
                }`}
                data-testid={`dashboard-breadcrumb-${i}`}
              >
                {step.name}
              </button>
            </span>
          ))}
        </nav>

        {/* Treemap surface */}
        <div
          className="relative mt-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
          data-testid="dashboard-treemap-frame"
        >
          {query.isLoading && !treemapData && (
            <div
              className="flex h-[600px] items-center justify-center text-sm text-[var(--color-text-dim)]"
              data-testid="dashboard-loading"
            >
              Loading utilisation data…
            </div>
          )}

          {query.isError && (
            <div
              className="rounded-md border border-[color:rgba(239,68,68,0.4)] bg-[color:rgba(239,68,68,0.08)] p-3 text-sm text-[#fca5a5]"
              data-testid="dashboard-error"
            >
              Failed to load resource utilisation data. Retrying…
            </div>
          )}

          {isEmpty && !query.isError && (
            <div
              className="flex h-[600px] flex-col items-center justify-center gap-2 text-center text-sm text-[var(--color-text-dim)]"
              data-testid="dashboard-empty"
            >
              <p className="font-medium text-[var(--color-text)]">
                No utilisation data yet.
              </p>
              <p>
                Once the Sovereign cluster reports back, this dashboard will
                show resource allocation and consumption per application.
              </p>
            </div>
          )}

          {!isEmpty && treemapData && visibleItems.length > 0 && (
            <ResponsiveContainer width="100%" height={600}>
              <Treemap
                data={visibleItems as unknown as Array<Record<string, unknown>>}
                dataKey="size_value"
                aspectRatio={4 / 3}
                isAnimationActive={false}
                content={
                  isNested
                    ? (NestedTreemapContent as unknown as React.ReactElement)
                    : (TreemapContent as unknown as React.ReactElement)
                }
              />
            </ResponsiveContainer>
          )}

          {/* Hover tooltip — absolute-positioned Paper. Viewport-clamped
           *  by the inline style logic. */}
          {hoverInfo && (
            <HoverTooltip
              info={hoverInfo}
              colorBy={colorBy}
              sizeBy={sizeBy}
              onAppClick={navigateToApp}
              currentDimension={layers[drillPath.length] ?? layers[layers.length - 1]}
            />
          )}
        </div>

        {/* Legend */}
        <Legend colorBy={colorBy} />
      </div>
    </PortalShell>
  )
}

/* ── Hover tooltip ──────────────────────────────────────────────── */

interface HoverTooltipProps {
  info: CellHoverInfo
  colorBy: TreemapColorBy
  sizeBy: TreemapSizeBy
  onAppClick: (componentId: string) => void
  currentDimension: TreemapDimension
}

function HoverTooltip({
  info,
  colorBy,
  sizeBy,
  onAppClick,
  currentDimension,
}: HoverTooltipProps) {
  const { item, x, y } = info
  // Viewport-clamp so the tooltip never escapes off-screen.
  const TOOLTIP_W = 240
  const TOOLTIP_H = 130
  const viewportW = typeof window !== 'undefined' ? window.innerWidth : 1440
  const viewportH = typeof window !== 'undefined' ? window.innerHeight : 900
  const clampedX = Math.max(8, Math.min(x + 12, viewportW - TOOLTIP_W - 8))
  const clampedY = Math.max(8, Math.min(y + 12, viewportH - TOOLTIP_H - 8))

  const colorLabel = colorBy === 'utilization'
    ? 'Utilisation'
    : colorBy === 'health' ? 'Health' : 'Age'
  const sizeLabel = sizeBy === 'cpu_limit'
    ? 'CPU limit'
    : sizeBy === 'memory_limit'
      ? 'Memory'
      : sizeBy === 'storage_limit'
        ? 'Storage'
        : 'Replicas'

  const isApp = currentDimension === 'application'
  const componentId = isApp ? (item.id ?? '') : ''

  return (
    <div
      role="tooltip"
      data-testid="dashboard-tooltip"
      style={{
        position: 'fixed',
        left: clampedX,
        top: clampedY,
        width: TOOLTIP_W,
        zIndex: 50,
        pointerEvents: 'auto',
      }}
      className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs shadow-lg"
    >
      <div className="font-semibold text-[var(--color-text-strong)]" data-testid="dashboard-tooltip-name">
        {item.name}
      </div>
      <div className="mt-1 flex justify-between text-[var(--color-text-dim)]">
        <span>{colorLabel}</span>
        <span className="font-mono" data-testid="dashboard-tooltip-percentage">
          {Math.round(item.percentage)}%
        </span>
      </div>
      <div className="mt-1 flex justify-between text-[var(--color-text-dim)]">
        <span>{sizeLabel}</span>
        <span className="font-mono">{formatSizeValue(item.size_value, sizeBy)}</span>
      </div>
      <div className="mt-1 flex justify-between text-[var(--color-text-dim)]">
        <span>Items</span>
        <span className="font-mono">{item.count}</span>
      </div>
      {isApp && componentId && (
        <button
          type="button"
          onClick={() => onAppClick(componentId)}
          className="mt-2 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-accent)] hover:bg-[var(--color-surface-hover)]"
          data-testid="dashboard-tooltip-link"
        >
          Open application →
        </button>
      )}
    </div>
  )
}

function formatSizeValue(v: number | undefined, sizeBy: TreemapSizeBy): string {
  if (v === undefined || v === null) return '—'
  switch (sizeBy) {
    case 'cpu_limit':
      return `${(v / 1000).toFixed(2)} cores`
    case 'memory_limit':
    case 'storage_limit':
      return formatBytes(v)
    case 'replica_count':
      return String(v)
    default:
      return String(v)
  }
}

function formatBytes(bytes: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i += 1
  }
  return `${v.toFixed(1)} ${units[i]}`
}

/* ── Legend ─────────────────────────────────────────────────────── */

function Legend({ colorBy }: { colorBy: TreemapColorBy }) {
  const fn = colorFunctionFor(colorBy)
  const stops = [0, 25, 50, 75, 100]
  const leftLabel = colorBy === 'health' ? 'Unhealthy' : colorBy === 'age' ? 'New' : 'Wasted'
  const midLabel = colorBy === 'health' ? 'Warning' : 'Optimum'
  const rightLabel = colorBy === 'health' ? 'Healthy' : colorBy === 'age' ? 'Old' : 'Hot'
  return (
    <div
      className="mt-4 flex items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs"
      data-testid="dashboard-legend"
    >
      <span className="font-medium text-[var(--color-text-dim)]">{leftLabel}</span>
      <div className="flex h-4 flex-1 overflow-hidden rounded-sm">
        {stops.slice(0, -1).map((s, i) => (
          <div
            key={s}
            className="flex-1"
            style={{
              background: `linear-gradient(90deg, ${fn(s)}, ${fn(stops[i + 1]!)})`,
            }}
          />
        ))}
      </div>
      <span className="font-medium text-[var(--color-text-dim)]">{midLabel}</span>
      <div className="w-2" />
      <span className="font-medium text-[var(--color-text-dim)]">{rightLabel}</span>
    </div>
  )
}

/* ── Cell renderers (cloned by Recharts — NO HOOKS) ─────────────── */

interface RechartsCellProps {
  x?: number
  y?: number
  width?: number
  height?: number
  index?: number
  depth?: number
  name?: string
  size_value?: number
  percentage?: number
  count?: number
  id?: string | null
  children?: TreemapItem[]
  root?: { children?: TreemapItem[] }
  payload?: TreemapItem
}

/**
 * Resolve the underlying TreemapItem from whatever shape Recharts
 * passes through. Recharts may flatten the node onto props directly OR
 * pass it as `payload`; we accept either so the renderer is robust to
 * a recharts version bump.
 */
function resolveItem(props: RechartsCellProps): TreemapItem | null {
  // Prefer the name→item lookup so we recover the full TreemapItem
  // (with its children[]) regardless of what Recharts hands the
  // cloned renderer.
  if (props.name) {
    const fromLookup = _itemsByName.get(props.name)
    if (fromLookup) return fromLookup
  }
  if (props.payload && typeof props.payload === 'object' && 'name' in props.payload) {
    return props.payload as TreemapItem
  }
  if (props.name) {
    return {
      id: (props.id as string | null | undefined) ?? null,
      name: props.name,
      count: props.count ?? 0,
      percentage: props.percentage ?? 0,
      size_value: props.size_value,
    }
  }
  return null
}

/**
 * Flat cell renderer used when the tree has only one layer (or when
 * the operator has drilled into a leaf parent so the visible items
 * are flat). NO React hooks — Recharts clones this and the cloned
 * tree's render path doesn't preserve hook order.
 */
function TreemapContent(props: RechartsCellProps) {
  const {
    x = 0,
    y = 0,
    width = 0,
    height = 0,
    name = '',
  } = props

  if (width <= 0 || height <= 0) return null

  const item = resolveItem(props)
  const percentage = item?.percentage ?? props.percentage ?? 0
  const fill = _activeColorFn(percentage)
  const showLabel = width >= LABEL_MIN_WIDTH_PX && height >= LABEL_MIN_HEIGHT_PX

  function handleEnter(e: React.MouseEvent) {
    if (!_onCellHover || !item) return
    _onCellHover({ item, x: e.clientX, y: e.clientY })
  }
  function handleLeave() {
    if (!_onCellHover) return
    _onCellHover(null)
  }
  function handleClick() {
    if (!_onCellClick || !item) return
    _onCellClick(item)
  }

  return (
    <g onMouseEnter={handleEnter} onMouseMove={handleEnter} onMouseLeave={handleLeave} onClick={handleClick} style={{ cursor: item?.children?.length ? 'pointer' : 'default' }}>
      <rect
        x={x}
        y={y}
        width={width}
        height={height}
        style={{
          fill,
          stroke: 'rgba(255, 255, 255, 0.18)',
          strokeWidth: 1,
        }}
      />
      {showLabel && (
        <>
          <text
            x={x + 8}
            y={y + 16}
            fill="rgba(255, 255, 255, 0.95)"
            fontSize={11}
            fontWeight={600}
            style={{ pointerEvents: 'none' }}
          >
            {truncateLabel(name, width)}
          </text>
          <text
            x={x + 8}
            y={y + 30}
            fill="rgba(255, 255, 255, 0.7)"
            fontSize={10}
            style={{ pointerEvents: 'none' }}
          >
            {Math.round(percentage)}%
          </text>
        </>
      )}
    </g>
  )
}

/**
 * Two-level nested cell renderer. Recharts emits cells at depth=1
 * (parent rectangle) and depth=2 (children); the renderer gates the
 * label band + parent-bounds bookkeeping on `depth`.
 *
 * Children's labels are clipped vertically against the parent's
 * stored bounds via `_parentBoundsByName` so a label can't escape
 * under the parent's header.
 */
function NestedTreemapContent(props: RechartsCellProps) {
  const {
    x = 0,
    y = 0,
    width = 0,
    height = 0,
    depth = 1,
    name = '',
  } = props

  if (width <= 0 || height <= 0) return null

  const item = resolveItem(props)
  const percentage = item?.percentage ?? props.percentage ?? 0

  // Recharts depths: 0 = root, 1 = first-level cells (the parents),
  // 2 = second-level cells (the children). Treat depth >= 2 as leaf.
  const isParent = depth === 1
  const isLeaf = depth >= 2

  if (isParent) {
    // Record bounds so leaves know where the header band ends.
    _parentBoundsByName.set(name, { x, y, width, height })

    function handleParentEnter(e: React.MouseEvent) {
      if (!_onCellHover || !item) return
      _onCellHover({ item, x: e.clientX, y: e.clientY })
    }
    function handleParentLeave() {
      if (!_onCellHover) return
      _onCellHover(null)
    }
    function handleParentClick() {
      if (!_onCellClick || !item) return
      _onCellClick(item)
    }

    return (
      <g
        onMouseEnter={handleParentEnter}
        onMouseMove={handleParentEnter}
        onMouseLeave={handleParentLeave}
        onClick={handleParentClick}
        style={{ cursor: item?.children?.length ? 'pointer' : 'default' }}
      >
        {/* Outer parent frame — no fill, just a subtle outline. */}
        <rect
          x={x}
          y={y}
          width={width}
          height={height}
          style={{
            fill: 'rgba(255, 255, 255, 0.02)',
            stroke: 'rgba(255, 255, 255, 0.20)',
            strokeWidth: 1,
          }}
        />
        {/* Parent header strip with the parent's own name + count. */}
        <rect
          x={x}
          y={y}
          width={width}
          height={NESTED_HEADER_HEIGHT_PX}
          style={{ fill: 'rgba(0, 0, 0, 0.35)' }}
        />
        {width >= LABEL_MIN_WIDTH_PX && (
          <text
            x={x + 8}
            y={y + 16}
            fill="rgba(255, 255, 255, 0.92)"
            fontSize={11}
            fontWeight={700}
            style={{ pointerEvents: 'none' }}
          >
            {truncateLabel(name, width)} · {item?.count ?? 0}
          </text>
        )}
      </g>
    )
  }

  if (isLeaf) {
    const fill = _activeColorFn(percentage)

    // Clip leaf y against parent header — leaf cells whose top edge is
    // inside the header strip get pushed down so labels don't render
    // under the header.
    let renderY = y
    let renderHeight = height
    // Find the parent containing this leaf by spatial test (leaf is
    // contained when its centre is inside the parent rect).
    const cx = x + width / 2
    const cy = y + height / 2
    let parent: { x: number; y: number; width: number; height: number } | null = null
    for (const [, bounds] of _parentBoundsByName) {
      if (
        cx >= bounds.x &&
        cx <= bounds.x + bounds.width &&
        cy >= bounds.y &&
        cy <= bounds.y + bounds.height
      ) {
        parent = bounds
        break
      }
    }
    if (parent) {
      const minY = parent.y + NESTED_HEADER_HEIGHT_PX + NESTED_PADDING_PX
      if (renderY < minY) {
        const delta = minY - renderY
        renderY = minY
        renderHeight = Math.max(0, renderHeight - delta)
      }
    }
    if (renderHeight <= 0) return null

    const showLabel =
      width >= LABEL_MIN_WIDTH_PX && renderHeight >= LABEL_MIN_HEIGHT_PX

    function handleEnter(e: React.MouseEvent) {
      if (!_onCellHover || !item) return
      _onCellHover({ item, x: e.clientX, y: e.clientY })
    }
    function handleLeave() {
      if (!_onCellHover) return
      _onCellHover(null)
    }
    function handleClick() {
      if (!_onCellClick || !item) return
      _onCellClick(item)
    }

    return (
      <g
        onMouseEnter={handleEnter}
        onMouseMove={handleEnter}
        onMouseLeave={handleLeave}
        onClick={handleClick}
        style={{ cursor: item?.children?.length ? 'pointer' : 'default' }}
      >
        <rect
          x={x}
          y={renderY}
          width={width}
          height={renderHeight}
          style={{
            fill,
            stroke: 'rgba(255, 255, 255, 0.15)',
            strokeWidth: 1,
          }}
        />
        {showLabel && (
          <>
            <text
              x={x + 6}
              y={renderY + 14}
              fill="rgba(255, 255, 255, 0.95)"
              fontSize={10}
              fontWeight={600}
              style={{ pointerEvents: 'none' }}
            >
              {truncateLabel(name, width)}
            </text>
            <text
              x={x + 6}
              y={renderY + 26}
              fill="rgba(255, 255, 255, 0.65)"
              fontSize={9}
              style={{ pointerEvents: 'none' }}
            >
              {Math.round(percentage)}%
            </text>
          </>
        )}
      </g>
    )
  }

  return null
}

/**
 * Truncate the label so it fits the cell width — recharts doesn't
 * clip text and a full label can overrun the cell. Rough char-width
 * estimate of 6.5px @ 11px font.
 */
function truncateLabel(name: string, width: number): string {
  const maxChars = Math.max(3, Math.floor((width - 12) / 6.5))
  if (name.length <= maxChars) return name
  return name.slice(0, Math.max(1, maxChars - 1)) + '…'
}

