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

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useRouter, Link } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useQuery } from '@tanstack/react-query'

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
import {
  computeSquarifiedLayout,
  NESTED_HEADER_HEIGHT_PX as SQ_HEADER,
  type SquarifiedRect,
} from '@/lib/treemap-squarified'

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
  /** Layout depth of the cell within the squarified tree. 0 = top-level
   *  cell at the current drill depth, 1 = first nested layer, etc.
   *  Used (along with `drillPath.length`) to resolve which dimension
   *  the hovered/clicked cell actually represents — see the bug-B
   *  comment in `_onCellClick`. */
  depth: number
}

let _onCellHover: ((info: CellHoverInfo | null) => void) | null = null
let _onCellClick: ((item: TreemapItem, cellDepth: number) => void) | null = null

/** Neutral fill used when a cell's percentage is null (e.g. utilization
 *  requested but metrics-server is not installed on the Sovereign).
 *  Desaturated grey, visibly different from any point on the
 *  utilization/health/age gradients. */
const NULL_PERCENTAGE_FILL = 'rgba(125, 125, 125, 0.45)'

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
  const { deploymentId: resolved } = useResolvedDeploymentId()
  const deploymentId = resolved ?? ''
  const router = useRouter()

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // PR M (2026-05-17 t142 founder follow-up #1): default Layer-1 = `cluster`
  // on multi-region Sovereigns so the operator sees the 3-cluster grouping
  // immediately. Previously default was `['family', 'application']` —
  // founder opened /dashboard, saw family-grouped bubbles, concluded the
  // multi-cluster fix was broken.
  //
  // Wave 2 Family D (t10 regression): the snapshot-driven `sovereignFQDN`
  // is fetched asynchronously via SSE — on first paint it is null, so the
  // default fell back to `['family', 'application']` even on a Sovereign
  // Console. Test agent caught:
  //
  //     DOM testid `treemap-layer-0-select` value="family" on first paint
  //
  // Fix: read mode synchronously from `DETECTED_MODE` (window.location-
  // derived at module load, stable for the lifetime of the page). This
  // is the SAME source the SovereignSidebar + cloud-list routes use for
  // their mode-gated rendering, so default Layer-1 stays consistent with
  // the rest of the sidebar's Sovereign affordances.
  const defaultLayers: readonly TreemapDimension[] =
    DETECTED_MODE.mode === 'sovereign'
      ? ['cluster', 'application']
      : ['family', 'application']
  const [layers, setLayers] = useState<readonly TreemapDimension[]>(
    initialLayers ?? defaultLayers,
  )
  const [colorBy, setColorBy] = useState<TreemapColorBy>(initialColorBy ?? 'utilization')
  const [sizeBy, setSizeBy] = useState<TreemapSizeBy>(initialSizeBy ?? 'cpu_request')

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

  /* Visible items at the current drill depth.
   * treemapData.items may be null (not just undefined) when the cluster
   * has not yet reported back — the backend emits `"items": null` during
   * provisioning. Guard with ?. so the null-items path falls through to
   * the empty-state render instead of throwing. */
  const visibleItems = useMemo<TreemapItem[]>(() => {
    if (!treemapData?.items) return []
    return walkDrillPath(treemapData.items, drillPath)
  }, [treemapData, drillPath])

  /* SquarifiedSurface receives colorFn directly via prop. The
   * _onCellHover / _onCellClick mailboxes survive only because they
   * decouple the surface from the page's tooltip + drill state — the
   * page registers handlers that read its own React state, and the
   * surface invokes them without needing a closure-bound prop callback
   * that would re-render on every mouse move. */
  const colorFn = useMemo(() => colorFunctionFor(colorBy), [colorBy])
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
    _onCellClick = (item, cellDepth) => {
      // Inner-tile drill-down (issue #1927):
      //   • Parent cell (children.length > 0)  → push onto breadcrumb
      //     stack so the operator drills one layer deeper without a
      //     refetch. The breadcrumb step records the dimension of the
      //     parent — `layers[drillPath.length + cellDepth]`.
      //   • Leaf cell whose RENDERED dimension is `application` and that
      //     carries a real `id` → deep-link to /app/$componentId so the
      //     operator can inspect the underlying Helm release. Before
      //     this fix the inner tiles rendered with `cursor: default`
      //     and dropped the click silently, leaving 84/85 cells dead
      //     on the canonical Cluster→Application layer pair.
      //   • Leaf cell on any other dimension (cluster header, namespace,
      //     family, sovereign, region, vcluster) without children stays
      //     a no-op — those rows don't have a stable detail target yet.
      //
      // 2026-05-19 follow-up (issue #1927 reopen, agent aced939b walk):
      //   PR #1931 read the dimension as `layers[drillPath.length]`
      //   which always returned the OUTER layer (`cluster` at the
      //   canonical default `['cluster','application']` + drillPath=[]).
      //   Even though SquarifiedSurface rendered the inner application
      //   tiles, the leaf-branch guard `dimension === 'application'`
      //   was FALSE and the click silently dropped. Fix: include the
      //   cell's actual layout depth so an application leaf at
      //   cellDepth=1 under `['cluster','application']` resolves to
      //   dimension=`application` and triggers the deep-link.
      const layerIdx = drillPath.length + cellDepth
      const dimension = layers[layerIdx] ?? layers[layers.length - 1]
      if (item.children && item.children.length > 0) {
        setDrillPath((prev) => [
          ...prev,
          { dimension, id: item.id, name: item.name },
        ])
        return
      }
      if (dimension === 'application' && item.id) {
        // 2026-05-19 follow-up (issue #1927 reopen, bug B):
        //   the backend's treemap handler emits `id = applicationKey(pod)
        //   = pod.labels["app.kubernetes.io/instance"]` (dashboard.go
        //   line 427). For bootstrap-kit installs the upstream subchart
        //   strips the bp- prefix on its Pod labels (Harbor's helm
        //   templates the instance label as `harbor`, not `bp-harbor`),
        //   so `item.id` arrives bare. But the consoleAppDetailRoute
        //   `/app/$componentId` keys on the Application CR name which
        //   IS bp-prefixed for every bootstrap-kit install (see
        //   clusters/_template/bootstrap-kit/*.yaml releaseName/CR
        //   metadata.name). AppDetail's findApplication() also matches
        //   on `a.id === 'bp-<slug>'` (applicationCatalog.ts line 179).
        //   Without normalisation the bare id 404s at AppDetail's
        //   "App not found" fallback.
        //
        //   Apply the same bp- prefix convention AppsPage already uses
        //   (AppsPage.tsx line 719: `/app/${app.id}` where app.id is
        //   always bp-prefixed). For ids that already carry the prefix
        //   we leave them alone — defensive against any treemap source
        //   that emits the canonical id (e.g. a future dimension that
        //   buckets on Application CR name directly).
        const componentId = item.id.startsWith('bp-') ? item.id : `bp-${item.id}`
        // Inline the navigation so this effect's deps don't have to
        // carry the outer `navigateToApp` closure (whose identity
        // changes on every render via the `router` reference).
        router.navigate({
          to: '/app/$componentId' as never,
          params: { componentId } as never,
        })
      }
    }
  }, [layers, drillPath.length, router])

  useEffect(() => {
    return () => {
      if (hoverTimerRef.current !== null) {
        window.clearTimeout(hoverTimerRef.current)
      }
    }
  }, [])

  const isEmpty = !query.isLoading && !treemapData?.items?.length
  const isNested = layers.length > 1 && drillPath.length === 0

  function popDrillTo(idx: number) {
    setDrillPath((prev) => prev.slice(0, idx))
  }

  function navigateToApp(componentId: string) {
    // Same bp- prefix normalisation as `_onCellClick` (bug B): the
    // treemap emits `id` from the Pod's `app.kubernetes.io/instance`
    // label which is bare (`harbor`), but AppDetail's route + lookup
    // both key on the bp- prefixed Application CR name (`bp-harbor`).
    const id = componentId.startsWith('bp-') ? componentId : `bp-${componentId}`
    router.navigate({
      to: '/app/$componentId' as never,
      params: { componentId: id } as never,
    })
  }

  /* ── Render ────────────────────────────────────────────────────── */

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Dashboard"
      headerSlotRight={
        <div
          className="flex items-center gap-3 text-right text-[11px] text-[var(--color-text-dim)]"
          data-testid="dashboard-header-meta"
        >
          <div data-testid="dashboard-total-count">{totalCount} items</div>
          {/* Decommission link (issue #319). Routes to the self-
           *  decommission page; gated by typed-FQDN confirmation +
           *  Hetzner-token re-prompt on the destination page. The link
           *  is always visible — orphan-recovery and post-handover
           *  decommission share the same surface. */}
          <Link
            to="/decommission/$deploymentId"
            params={{ deploymentId }}
            className="rounded-md border border-[var(--color-border)] px-2 py-1 text-[11px] text-[var(--color-text-dim)] hover:border-rose-500 hover:text-rose-300 no-underline"
            data-testid="dashboard-decommission-link"
          >
            Decommission
          </Link>
        </div>
      }
    >
      <div className="mx-auto max-w-7xl" data-testid="dashboard-page">
        {/* Title moved to header centre slot (#366 item 2) — anchor a
         *  hidden testid for back-compat with existing component tests. */}
        <span data-testid="dashboard-title" className="sr-only">
          Dashboard
        </span>

        <TreemapLayerController
          layers={layers}
          setLayers={setLayers}
          colorBy={colorBy}
          setColorBy={setColorBy}
          sizeBy={sizeBy}
          setSizeBy={setSizeBy}
        />

        {/* Breadcrumbs — drill stack pop targets. Hidden at root depth
         *  (#531 item 4 — the standalone "All" chip wasted vertical
         *  space and had no functional purpose when nothing was
         *  drilled). The "All" chip reappears as the leftmost crumb as
         *  soon as the operator drills into a parent so they can pop
         *  back to root with one click. */}
        {drillPath.length > 0 ? (
          <nav
            className="mt-3 flex flex-wrap items-center gap-1 text-xs"
            aria-label="Drill path"
            data-testid="dashboard-breadcrumb"
          >
            <button
              type="button"
              onClick={() => popDrillTo(0)}
              className="rounded-md px-2 py-1 text-[var(--color-text-dim)] transition-colors hover:text-[var(--color-text)]"
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
        ) : null}

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
            <SquarifiedSurface
              items={visibleItems}
              isNested={isNested}
              colorFn={colorFn}
              onCellHover={(info) => _onCellHover?.(info)}
              onCellClick={(item, depth) => _onCellClick?.(item, depth)}
            />
          )}

          {/* Hover tooltip — absolute-positioned Paper. Viewport-clamped
           *  by the inline style logic. The hovered cell's dimension is
           *  resolved from `drillPath.length + cell.depth` so a nested
           *  application leaf under a cluster header tooltips with
           *  `Open application` even at drillPath=[] (the canonical
           *  Cluster→Application landing view). See the bug-A comment
           *  in `_onCellClick`. */}
          {hoverInfo && (
            <HoverTooltip
              info={hoverInfo}
              colorBy={colorBy}
              sizeBy={sizeBy}
              onAppClick={navigateToApp}
              currentDimension={
                layers[drillPath.length + hoverInfo.depth] ??
                layers[layers.length - 1]
              }
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
  const sizeLabel = sizeBy === 'cpu_request'
    ? 'CPU request'
    : sizeBy === 'memory_request'
      ? 'Memory request'
      : sizeBy === 'cpu_usage'
        ? 'CPU usage'
        : sizeBy === 'memory_usage'
          ? 'Memory usage'
          : sizeBy === 'cpu_limit'
            ? 'CPU limit'
            : sizeBy === 'memory_limit'
              ? 'Memory limit'
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
          {item.percentage === null
            ? colorBy === 'utilization'
              ? 'metrics-server not installed'
              : 'no data'
            : `${Math.round(item.percentage)}%`}
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
    case 'cpu_request':
    case 'cpu_usage':
    case 'cpu_limit':
      return `${(v / 1000).toFixed(2)} cores`
    case 'memory_request':
    case 'memory_usage':
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

/* ── Squarified treemap surface ─────────────────────────────────────
 *
 * Replaces the recharts <Treemap> with a pure-SVG renderer driven by
 * our squarified layout algorithm (lib/treemap-squarified.ts). The
 * cell content (fill, label, sub-label, hover/click handlers) is
 * inlined here — there's no need for the recharts module-level mailbox
 * pattern because we render the cells directly without a foreign
 * cloning step.
 *
 * Sizing: ResizeObserver tracks the container's width; height is
 * fixed at 600px to match the prior recharts layout. Re-layout fires
 * automatically when the width or items change.
 */

interface SquarifiedSurfaceProps {
  items: readonly TreemapItem[]
  isNested: boolean
  colorFn: (pct: number) => string
  onCellHover: (info: CellHoverInfo | null) => void
  onCellClick: (item: TreemapItem, depth: number) => void
}

const SURFACE_HEIGHT_PX = 600

function SquarifiedSurface({
  items,
  isNested,
  colorFn,
  onCellHover,
  onCellClick,
}: SquarifiedSurfaceProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState<number>(0)

  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    function measure() {
      if (!el) return
      setWidth(el.clientWidth)
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Always pass children when isNested — that's what triggers the
  // depth-1 cell emission in the squarified algorithm. When the
  // dashboard is in flat mode (drilled in / single layer), strip
  // children so only the visible level renders.
  const layoutItems = useMemo<TreemapItem[]>(() => {
    if (isNested) return items as TreemapItem[]
    return (items as TreemapItem[]).map((it) => ({ ...it, children: undefined }))
  }, [items, isNested])

  const rects = useMemo<SquarifiedRect[]>(() => {
    if (width <= 0) return []
    return computeSquarifiedLayout(layoutItems, width, SURFACE_HEIGHT_PX)
  }, [layoutItems, width])

  return (
    <div
      ref={containerRef}
      data-testid="dashboard-treemap-surface"
      style={{ width: '100%', height: SURFACE_HEIGHT_PX }}
    >
      {width > 0 && (
        <svg
          width={width}
          height={SURFACE_HEIGHT_PX}
          role="img"
          aria-label="Resource utilisation treemap"
          style={{ display: 'block' }}
        >
          {rects.map((r, i) => (
            <SquarifiedCell
              key={`${r.item.name}-${r.depth}-${i}`}
              rect={r}
              colorFn={colorFn}
              onHover={onCellHover}
              onClick={onCellClick}
            />
          ))}
        </svg>
      )}
    </div>
  )
}

interface SquarifiedCellProps {
  rect: SquarifiedRect
  colorFn: (pct: number) => string
  onHover: (info: CellHoverInfo | null) => void
  onClick: (item: TreemapItem, depth: number) => void
}

function SquarifiedCell({ rect, colorFn, onHover, onClick }: SquarifiedCellProps) {
  const { x0, y0, x1, y1, item, isParent, depth } = rect
  const w = x1 - x0
  const h = y1 - y0
  if (w <= 0 || h <= 0) return null

  const pct = item.percentage
  // Parent cells in nested mode get a transparent body (the children
  // tile inside) so only the header strip carries colour. Leaf cells
  // get the full gradient fill.
  const fill = isParent
    ? 'rgba(255, 255, 255, 0.04)'
    : pct === null
      ? NULL_PERCENTAGE_FILL
      : colorFn(pct)

  const showLabel = w >= LABEL_MIN_WIDTH_PX && h >= LABEL_MIN_HEIGHT_PX
  // Issue #1927: leaf cells with an `id` are clickable too (handler in
  // Dashboard decides whether to drill or deep-link to /app/$id). Only
  // truly inert tiles (synthetic rollups with no id and no children)
  // stay on the default cursor.
  const cursor = item.children?.length || item.id ? 'pointer' : 'default'

  function handleEnter(e: React.MouseEvent) {
    onHover({ item, x: e.clientX, y: e.clientY, depth })
  }
  function handleLeave() {
    onHover(null)
  }
  function handleClick() {
    // Issue #1927: forward every click on a cell that carries either
    // children (drill) or an id (deep-link). The decision of WHAT to do
    // lives in the page-level _onCellClick mailbox, which knows the
    // current layer dimension (computed from `drillPath.length + depth`).
    if (item.children?.length || item.id) onClick(item, depth)
  }

  if (isParent) {
    // Header strip + outline frame.
    return (
      <g
        onMouseEnter={handleEnter}
        onMouseMove={handleEnter}
        onMouseLeave={handleLeave}
        onClick={handleClick}
        style={{ cursor }}
      >
        <rect
          x={x0}
          y={y0}
          width={w}
          height={h}
          style={{
            fill,
            stroke: 'rgba(255, 255, 255, 0.18)',
            strokeWidth: 1,
          }}
        />
        <rect
          x={x0}
          y={y0}
          width={w}
          height={SQ_HEADER}
          style={{
            fill: pct === null ? NULL_PERCENTAGE_FILL : colorFn(pct),
            stroke: 'rgba(255, 255, 255, 0.18)',
            strokeWidth: 1,
          }}
        />
        {showLabel && (
          <text
            x={x0 + 8}
            y={y0 + 16}
            fill="rgba(255, 255, 255, 0.95)"
            fontSize={11}
            fontWeight={600}
            style={{ pointerEvents: 'none' }}
          >
            {truncateLabel(item.name, w)}
          </text>
        )}
      </g>
    )
  }

  // Leaf cell.
  return (
    <g
      onMouseEnter={handleEnter}
      onMouseMove={handleEnter}
      onMouseLeave={handleLeave}
      onClick={handleClick}
      style={{ cursor }}
    >
      <rect
        x={x0}
        y={y0}
        width={w}
        height={h}
        style={{
          fill,
          stroke: 'rgba(255, 255, 255, 0.18)',
          strokeWidth: depth > 0 ? 0.5 : 1,
        }}
      />
      {showLabel && (
        <>
          <text
            x={x0 + 8}
            y={y0 + 16}
            fill="rgba(255, 255, 255, 0.95)"
            fontSize={11}
            fontWeight={600}
            style={{ pointerEvents: 'none' }}
          >
            {truncateLabel(item.name, w)}
          </text>
          <text
            x={x0 + 8}
            y={y0 + 30}
            fill="rgba(255, 255, 255, 0.7)"
            fontSize={10}
            style={{ pointerEvents: 'none' }}
          >
            {pct === null ? '— %' : `${Math.round(pct)}%`}
          </text>
        </>
      )}
    </g>
  )
}
