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
 *     [organization | sovereign | region | cluster | vcluster |
 *     family | namespace | application]. The first layer is the outer
 *     ring; deeper layers nest inside.
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
 * ── #4731 — Progress + Kind as first-class layers of THIS treemap ──
 * The provisioning view is NOT a separate component (the ad-hoc
 * ProvisioningTreemap of PR #4726 is deleted): it is this same
 * editable treemap with two extra Layer dimensions and one Color mode.
 *
 *   • `progress` layer — lifecycle-stage buckets derived from the Job
 *     tree's `type=group` parents in dependency order, falling back to
 *     kind-derived stages when the group set is sparse.
 *   • `kind` layer — the typed JobKind discriminator (#3646).
 *   • `status` colour — categorical statusColors mapping (green=
 *     succeeded/healthy, blue=running pulsing, amber=degraded,
 *     red=failed/failing, grey=pending).
 *
 * Data-source rule (the only conditional): a layer stack containing
 * progress/kind builds its TreemapItem[] client-side from
 * GET /api/v1/deployments/{id}/jobs (buildJobsTreemapData in the
 * sibling ./dashboardJobsTreemap.ts pure module); every other stack
 * calls getDashboardTreemap exactly as before.
 * While `status != ready` the DEFAULTS are layers=['progress','kind'],
 * colorBy='status', sizeBy='uniform'; on ready they flip to the
 * resource defaults — same pane, same component, a defaults change,
 * not a component swap. Job-leaf clicks deep-link to JobDetail logs
 * via the same convention as JobsTable's useJobLinkBuilder.
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
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import { useLiveJobsBackfill } from './useLiveJobsBackfill'
import { resolveApplications } from './applicationCatalog'
// #4731 — pure job-tree → treemap derivation (sibling non-component
// module, same co-location pattern as ./jobs.ts).
import {
  buildJobsTreemapData,
  cellDashedFor,
  cellFillFor,
  cellPulseFor,
  jobTileHref,
  PROVISIONING_DEFAULT_LAYERS,
} from './dashboardJobsTreemap'
import { useWizardStore } from '@/entities/deployment/store'
import { STATUS_KIND_COLOR, type StatusKind } from '@/shared/lib/statusColors'
import {
  TreemapLayerController,
} from '@/components/TreemapLayerController'
import {
  colorFunctionFor,
  getDashboardTreemap,
  isJobSourcedStack,
  statusColor,
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

/* ── Page ────────────────────────────────────────────────────────── */

export interface DashboardProps {
  /** Test seam — disables the live SSE attach (the dashboard doesn't
   *  consume events itself, but the PortalShell's parent does). */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialDataOverride?: TreemapData
  /** Test seam — initial state of the layer / colour / size selects.
   *  Treated as operator overrides: they suppress the status-derived
   *  defaults (progress/kind + status while converging, resource
   *  defaults on ready). */
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

  // The resolved application set (bootstrap kit + mandatory + wizard
  // selection) — the SAME derivation JobsPage uses. Seeds the
  // deployment-event reducer AND maps job appIds onto catalog families
  // for the job-sourced `family` dimension (#4731).
  const store = useWizardStore()
  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const isReady = (snapshot?.status ?? '').trim().toLowerCase() === 'ready'

  // #4731 amend (founder escalation 2026-07-05): the default layer stack is
  // [progress, kind] UNCONDITIONALLY — on a provisioning env AND on a
  // converged (ready) env. The prior code flipped the ready default to
  // [organization, application] + utilization; the founder rejected that
  // ("why the second layer is not kind by default"). The one treemap now
  // reads the FULL platform inventory in BOTH moments: converging fills
  // Running/Pending, converged is a big green Done block sub-divided by
  // kind, with the parked cutover tether in Dormant. organization /
  // application / utilization stay AVAILABLE — one click away in the layer
  // controller — they are just no longer the default.
  //
  // Implementation (same pattern as the old userView): the `user*` states
  // stay null until the operator touches a control; the effective value is
  // the user's choice if set, else the [progress, kind] default. No
  // status-gated flip, no setState-in-effect storm; the user keeps control
  // once they touch the toolbar.
  const [userLayers, setUserLayers] = useState<readonly TreemapDimension[] | null>(
    initialLayers ?? null,
  )
  const [userColorBy, setUserColorBy] = useState<TreemapColorBy | null>(
    initialColorBy ?? null,
  )
  const [userSizeBy, setUserSizeBy] = useState<TreemapSizeBy | null>(
    initialSizeBy ?? null,
  )
  const layers: readonly TreemapDimension[] = userLayers ?? PROVISIONING_DEFAULT_LAYERS
  // The data-source rule (#4731, the only conditional): progress/kind
  // in the stack → Job tree; anything else → getDashboardTreemap.
  const jobSourced = isJobSourcedStack(layers)
  // Colour/size defaults follow the STACK's nature, not readiness — a
  // post-ready operator re-adding the Progress layer gets status/uniform
  // back, and a converging operator pivoting to a resource stack gets
  // utilisation/cpu_request.
  const colorBy: TreemapColorBy = userColorBy ?? (jobSourced ? 'status' : 'utilization')
  const sizeBy: TreemapSizeBy = userSizeBy ?? (jobSourced ? 'uniform' : 'cpu_request')
  const setColorBy = (next: TreemapColorBy) => setUserColorBy(next)
  const setSizeBy = (next: TreemapSizeBy) => setUserSizeBy(next)
  /** Layer edits that flip the stack between job-sourced and
   *  resource-sourced reset the colour/size overrides so the selects
   *  re-derive a value that EXISTS in the new mode's option list (the
   *  controller swaps option sets — status/uniform are job-only). */
  const setLayers = (next: readonly TreemapDimension[]) => {
    if (isJobSourcedStack(next) !== jobSourced) {
      setUserColorBy(null)
      setUserSizeBy(null)
    }
    setUserLayers(next)
  }

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
    // #4731 — a job-sourced stack never touches the pods/utilisation
    // backend (status/uniform are not backend vocabulary); the resource
    // path stays byte-identical for every other stack.
    enabled: !initialDataOverride && !jobSourced,
    placeholderData: (prev) => prev,
  })

  // #4731 — job-sourced data: the SAME recursive Job tree the Jobs
  // surface polls (5s cadence while converging via useLiveJobsBackfill;
  // polling stops once ready — a manual refetch lands on the next
  // interactivation like the Jobs page).
  const {
    liveJobs,
    isLoading: jobsLoading,
    isError: jobsError,
  } = useLiveJobsBackfill({
    deploymentId,
    enabled: jobSourced && !initialDataOverride && !!deploymentId,
    disablePolling: disableStream || isReady,
    // #4731 — the treemap always wants the FULL platform inventory so a
    // converged Sovereign shows every component, not the ~14 finite rows.
    fullInventory: true,
  })
  const jobsTreemap = useMemo<TreemapData | undefined>(
    () => (jobSourced ? buildJobsTreemapData(liveJobs, layers, applications) : undefined),
    [jobSourced, liveJobs, layers, applications],
  )

  const treemapData: TreemapData | undefined =
    initialDataOverride ?? (jobSourced ? jobsTreemap : query.data)
  const totalCount = treemapData?.total_count ?? 0

  // Loading/error track whichever source is active (test overrides are
  // never loading/erroring).
  const sourceLoading = initialDataOverride
    ? false
    : jobSourced
      ? jobsLoading
      : query.isLoading
  const sourceError =
    !initialDataOverride && (jobSourced ? jobsError : query.isError)

  /* Visible items at the current drill depth.
   * treemapData.items may be null (not just undefined) when the cluster
   * has not yet reported back — the backend emits `"items": null` during
   * provisioning. Guard with ?. so the null-items path falls through to
   * the empty-state render instead of throwing. */
  const visibleItems = useMemo<TreemapItem[]>(() => {
    if (!treemapData?.items) return []
    return walkDrillPath(treemapData.items, drillPath)
  }, [treemapData, drillPath])

  /* SquarifiedSurface receives the fill resolver directly via prop. The
   * _onCellHover / _onCellClick mailboxes survive only because they
   * decouple the surface from the page's tooltip + drill state — the
   * page registers handlers that read its own React state, and the
   * surface invokes them without needing a closure-bound prop callback
   * that would re-render on every mouse move.
   *
   * #4731 — `status` colour is CATEGORICAL: the fill comes from the
   * cell's typed `statusKind` channel (statusColor), never from the
   * 0..100 percentage, and running cells pulse. Gradient modes keep the
   * exact pre-#4731 behaviour (percentage → gradient, null → neutral
   * grey). The resolution logic is the pure cellFillFor/cellPulseFor
   * (dashboardJobsTreemap.ts); these thin closures just bind the
   * page's active colorBy. */
  const fillFor = (item: TreemapItem) => cellFillFor(colorBy, item)
  const pulseFor = (item: TreemapItem) => cellPulseFor(colorBy, item)
  // #4731 — dormant cells (the parked cutover tether) render with a dashed
  // outline so they are unmistakably distinct from solid pending cells.
  const dashedFor = (item: TreemapItem) => cellDashedFor(colorBy, item)
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
      // #4731 — job leaf on a job-sourced tree: deep-link to that job's
      // JobDetail logs, same convention as JobsTable's useJobLinkBuilder.
      // `jobId` is ONLY ever stamped on job leaves by
      // buildJobsTreemapData, so this cannot misfire on a bucket
      // rendered flat after a drill (flat rendering strips `children`
      // but not `jobId`).
      if (item.jobId) {
        router.navigate({ to: jobTileHref(item.jobId, deploymentId) as never })
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
        //
        // #4704 follow-up — MODE-AWARE target. `/app/$componentId` is the
        // chroot Sovereign Console's AppDetail route; on the MOTHERSHIP
        // `/app/bp-harbor` instead matches `/app/$deploymentId` and lands
        // on the malformed-deployment-id banner (the same reserved-word
        // class as Task B). The mothership AppDetail (hero + Jobs section
        // + logs) lives at /provision/$deploymentId/app/$componentId, so
        // the treemap drill-down reaches the log-bearing detail surface
        // on both hosts.
        if (DETECTED_MODE.mode === 'sovereign') {
          router.navigate({
            to: '/app/$componentId' as never,
            params: { componentId } as never,
          })
        } else {
          router.navigate({
            to: '/provision/$deploymentId/app/$componentId' as never,
            params: { deploymentId, componentId } as never,
          })
        }
      }
    }
  }, [layers, drillPath.length, router, deploymentId])

  useEffect(() => {
    return () => {
      if (hoverTimerRef.current !== null) {
        window.clearTimeout(hoverTimerRef.current)
      }
    }
  }, [])

  const isEmpty = !sourceLoading && !treemapData?.items?.length
  const isNested = layers.length > 1 && drillPath.length === 0

  function popDrillTo(idx: number) {
    setDrillPath((prev) => prev.slice(0, idx))
  }

  function navigateToApp(componentId: string) {
    // Same bp- prefix normalisation as `_onCellClick` (bug B): the
    // treemap emits `id` from the Pod's `app.kubernetes.io/instance`
    // label which is bare (`harbor`), but AppDetail's route + lookup
    // both key on the bp- prefixed Application CR name (`bp-harbor`).
    // Mode-aware target — see the #4704 comment in `_onCellClick`.
    const id = componentId.startsWith('bp-') ? componentId : `bp-${componentId}`
    if (DETECTED_MODE.mode === 'sovereign') {
      router.navigate({
        to: '/app/$componentId' as never,
        params: { componentId: id } as never,
      })
    } else {
      router.navigate({
        to: '/provision/$deploymentId/app/$componentId' as never,
        params: { deploymentId, componentId: id } as never,
      })
    }
  }

  /** #4731 — tooltip affordance twin of the job-leaf cell click. */
  function navigateToJob(jobId: string) {
    router.navigate({ to: jobTileHref(jobId, deploymentId) as never })
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
          {/* Reconciliation link — UAT row 192 / #3925 / #5831.
           *
           *  The deep-link TARGET has always worked: /cloud?view=graph&
           *  lens=reconciliation opens the RECON lens on arrival (the cloud
           *  route's validateSearch carries `lens` through and seeds
           *  CloudLensProvider). What had no host was the AFFORDANCE.
           *  ConvergenceWizard.tsx owned it, be5477c43 replaced the 5-phase
           *  wizard with this treemap Dashboard without re-homing the link,
           *  and the component has been orphaned from the router ever since
           *  — so the only ways in were the /cloud Lens dropdown or a
           *  hand-typed URL, and row 192 sat split across three walks.
           *
           *  This Dashboard IS the convergence surface the wizard became, so
           *  the link belongs here. Mode-aware via sovereignPathOrDeployments
           *  (#4704 Task B): on the mothership a per-deployment link without
           *  an id must route to /deployments rather than collapse the literal
           *  word into the $deploymentId slot. */}
          <Link
            to={sovereignPathOrDeployments('cloud', { deploymentId }) as never}
            search={{ view: 'graph', lens: 'reconciliation' } as never}
            className="rounded-md border border-[var(--color-border)] px-2 py-1 text-[11px] text-[var(--color-text-dim)] no-underline hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
            data-testid="dashboard-link-reconciliation"
          >
            Reconciliation
          </Link>
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

        {/* #4731 — ONE treemap in both lifecycle states. While the
            deployment converges the defaults are the job-sourced
            Progress→Kind stack coloured by status; on ready they morph
            to the resource stack. The operator can re-stack either way
            at any time via the same layer controller. */}
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
          {sourceLoading && !treemapData && (
            <div
              className="flex h-[600px] items-center justify-center text-sm text-[var(--color-text-dim)]"
              data-testid="dashboard-loading"
            >
              {jobSourced ? 'Loading provisioning jobs…' : 'Loading utilisation data…'}
            </div>
          )}

          {sourceError && (
            <div
              className="rounded-md border border-[color:rgba(239,68,68,0.4)] bg-[color:rgba(239,68,68,0.08)] p-3 text-sm text-[#fca5a5]"
              data-testid="dashboard-error"
            >
              {jobSourced
                ? 'Failed to load the deployment job tree. Retrying…'
                : 'Failed to load resource utilisation data. Retrying…'}
            </div>
          )}

          {isEmpty && !sourceError && (
            <div
              className="flex h-[600px] flex-col items-center justify-center gap-2 text-center text-sm text-[var(--color-text-dim)]"
              data-testid="dashboard-empty"
            >
              {jobSourced ? (
                <>
                  <p className="font-medium text-[var(--color-text)]">
                    No jobs reported yet.
                  </p>
                  <p>
                    Provisioning jobs appear here the moment the deployment
                    starts reporting; each tile drills into that job&apos;s
                    logs.
                  </p>
                </>
              ) : (
                <>
                  <p className="font-medium text-[var(--color-text)]">
                    No utilisation data yet.
                  </p>
                  <p>
                    Once the Sovereign cluster reports back, this dashboard will
                    show resource allocation and consumption per application.
                  </p>
                </>
              )}
            </div>
          )}

          {!isEmpty && treemapData && visibleItems.length > 0 && (
            <SquarifiedSurface
              items={visibleItems}
              isNested={isNested}
              fillFor={fillFor}
              pulseFor={pulseFor}
              dashedFor={dashedFor}
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
              onJobClick={navigateToJob}
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
  /** #4731 — job-leaf twin of onAppClick (JobDetail logs). */
  onJobClick: (jobId: string) => void
  currentDimension: TreemapDimension
}

function HoverTooltip({
  info,
  colorBy,
  sizeBy,
  onAppClick,
  onJobClick,
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
    : colorBy === 'health'
      ? 'Health'
      : colorBy === 'status'
        ? 'Status'
        : 'Age'
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
                : sizeBy === 'uniform'
                  ? 'Jobs'
                  : 'Replicas'

  const isApp = currentDimension === 'application' && !item.jobId
  const componentId = isApp ? (item.id ?? '') : ''
  // #4731 — categorical status readout: raw JobStatus word on leaves
  // ("succeeded", "degraded", "healthy"…), rolled-up kind on buckets.
  const statusText = item.statusLabel ?? item.statusKind ?? 'no data'

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
          {colorBy === 'status'
            ? statusText
            : item.percentage === null
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
      {item.jobId && (
        <button
          type="button"
          onClick={() => onJobClick(item.jobId!)}
          className="mt-2 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-accent)] hover:bg-[var(--color-surface-hover)]"
          data-testid="dashboard-tooltip-job-link"
        >
          Open job logs →
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
    case 'uniform':
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

/** #4731 — discrete legend entries for the categorical status colour
 *  mode. Labels carry BOTH status axes (#3646): the one-shot lifecycle
 *  vocabulary and the health-axis vocabulary that maps onto the same
 *  semantic kind. Swatches come from the same statusColor() tint the
 *  cells use. */
const STATUS_LEGEND_ENTRIES: { kind: StatusKind; label: string }[] = [
  { kind: 'success', label: 'Succeeded / healthy' },
  { kind: 'in-progress', label: 'Running' },
  { kind: 'warning', label: 'Degraded' },
  { kind: 'failed', label: 'Failed / failing' },
  { kind: 'pending', label: 'Pending' },
  // #4731 — the parked/dormant tether (installed-but-never-fired cutover),
  // grey + dashed. Distinct from pending so the operator never reads the
  // dormant cutover as queued work.
  { kind: 'dormant', label: 'Dormant' },
]

function Legend({ colorBy }: { colorBy: TreemapColorBy }) {
  if (colorBy === 'status') {
    // Categorical legend — five discrete semantic swatches, not a
    // gradient bar (status is not a 0..100 scale).
    return (
      <div
        className="mt-4 flex flex-wrap items-center gap-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs"
        data-testid="dashboard-legend"
      >
        {STATUS_LEGEND_ENTRIES.map(({ kind, label }) => (
          <span
            key={kind}
            className="inline-flex items-center gap-1.5"
            data-testid={`dashboard-legend-status-${kind}`}
          >
            <span
              aria-hidden
              className="inline-block h-3 w-3 rounded-sm border border-[var(--color-border)]"
              style={{ background: statusColor(kind) }}
            />
            <span
              className="font-medium"
              style={{ color: STATUS_KIND_COLOR[kind] }}
            >
              {label}
            </span>
          </span>
        ))}
      </div>
    )
  }
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
  /** Resolves a cell's fill from the item itself — gradient modes read
   *  `percentage` (null → neutral grey), status mode reads the typed
   *  `statusKind` channel (#4731). */
  fillFor: (item: TreemapItem) => string
  /** True when the cell should pulse (status mode, in-progress). */
  pulseFor: (item: TreemapItem) => boolean
  /** True when the cell should render a dashed outline (status mode,
   *  dormant — the parked cutover tether, #4731). */
  dashedFor: (item: TreemapItem) => boolean
  onCellHover: (info: CellHoverInfo | null) => void
  onCellClick: (item: TreemapItem, depth: number) => void
}

const SURFACE_HEIGHT_PX = 600

/** #4731 — running cells pulse in status colour mode so an in-flight
 *  job is unmistakably distinct from pending (grey) and success
 *  (green). Same 2s ease curve the Jobs surface uses. */
const SURFACE_CSS = `
.dash-cell-pulse { animation: dash-cell-pulse 2s ease-in-out infinite; }
@keyframes dash-cell-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.45; } }
`

function SquarifiedSurface({
  items,
  isNested,
  fillFor,
  pulseFor,
  dashedFor,
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
      <style>{SURFACE_CSS}</style>
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
              fillFor={fillFor}
              pulseFor={pulseFor}
              dashedFor={dashedFor}
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
  fillFor: (item: TreemapItem) => string
  pulseFor: (item: TreemapItem) => boolean
  dashedFor: (item: TreemapItem) => boolean
  onHover: (info: CellHoverInfo | null) => void
  onClick: (item: TreemapItem, depth: number) => void
}

function SquarifiedCell({ rect, fillFor, pulseFor, dashedFor, onHover, onClick }: SquarifiedCellProps) {
  const { x0, y0, x1, y1, item, isParent, depth } = rect
  const w = x1 - x0
  const h = y1 - y0
  if (w <= 0 || h <= 0) return null

  const pct = item.percentage
  // Parent cells in nested mode get a transparent body (the children
  // tile inside) so only the header strip carries colour. Leaf cells
  // get the full semantic fill (gradient or categorical status).
  const semanticFill = fillFor(item)
  const pulse = pulseFor(item)
  // #4731 — a dormant cell (parked cutover tether) gets a dashed outline so
  // it reads as "asleep", unmistakably distinct from a solid pending cell.
  const dashArray = dashedFor(item) ? '4 3' : undefined
  const fill = isParent ? 'rgba(255, 255, 255, 0.04)' : semanticFill

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
    // Header strip + outline frame. The strip carries the semantic
    // fill (gradient percentage or #4731 rolled-up statusKind tint).
    return (
      <g
        onMouseEnter={handleEnter}
        onMouseMove={handleEnter}
        onMouseLeave={handleLeave}
        onClick={handleClick}
        style={{ cursor }}
        data-status-kind={item.statusKind}
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
          className={pulse ? 'dash-cell-pulse' : undefined}
          x={x0}
          y={y0}
          width={w}
          height={SQ_HEADER}
          style={{
            fill: semanticFill,
            stroke: 'rgba(255, 255, 255, 0.18)',
            strokeWidth: 1,
            strokeDasharray: dashArray,
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
      data-status-kind={item.statusKind}
      data-job-id={item.jobId}
    >
      <rect
        className={pulse ? 'dash-cell-pulse' : undefined}
        x={x0}
        y={y0}
        width={w}
        height={h}
        style={{
          fill,
          stroke: 'rgba(255, 255, 255, 0.18)',
          strokeWidth: depth > 0 ? 0.5 : 1,
          strokeDasharray: dashArray,
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
            {/* #4731 — job leaves read their raw status word; gradient
                cells keep the percentage read-out. */}
            {item.statusLabel ?? (pct === null ? '— %' : `${Math.round(pct)}%`)}
          </text>
        </>
      )}
    </g>
  )
}
