/**
 * CloudPage — Sovereign-portal Cloud surface (issue #350).
 *
 * Single parent page served at `/sovereign/provision/$id/cloud`. The
 * legacy `/cloud/<category>` and `/cloud/<category>/<resource>` paths
 * (#309 P3) are now redirect-only — they 301 into this page with a
 * `view=list&kind=…` query.
 *
 * Layout:
 *   • Header: H1 "Cloud" + tagline + per-Sovereign switcher.
 *   • Toolbar: a segmented View toggle (Graph | List) and a Fullscreen
 *     button on the right.
 *   • Content area: when `view=graph` (default) — renders the
 *     ArchitectureGraphPage. When `view=list` — renders CloudListView
 *     (12-tile card grid + dropdown switcher + active P3 list table).
 *
 * The view toggle persists to localStorage under `sov-cloud-view`. The
 * URL query takes precedence on every navigation, so deep links are
 * always explicit. Operators arriving at `/cloud` without a `view`
 * query are redirected to either the persisted view or `view=graph`
 * (default).
 *
 * Fullscreen behaves like the canonical pattern: `requestFullscreen()`
 * on the main content container, smooth scale + fade transition,
 * native Esc to exit, plus an "Exit fullscreen" button rendered inside
 * the overlay (top-right) for discoverability.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — graph view, list view, fullscreen all ship at
 *      once.
 *   #4 (never hardcode) — every label / route / token comes from a
 *      typed constant or a CSS variable.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import {
  Outlet,
  useNavigate,
  useParams,
  useRouterState,
  useSearch,
} from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PortalShell } from './PortalShell'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useDeploymentEvents } from './useDeploymentEvents'
import { Architecture } from './Architecture'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { CloudListView } from './cloud-list/CloudListView'
import { CloudKindChips } from './cloud-list/CloudKindChips'
import {
  CLOUD_PAGE_K8S_KINDS,
  DEFAULT_KIND,
  isValidKind,
  readPersistedKind,
  type CloudListKind,
} from './cloud-list/kinds'
import { deriveKindCounts } from './cloud-list/kindCounts'
import { useK8sCacheStream } from '@/widgets/architecture-graph/useK8sCacheStream'
import { CloudLensProvider } from '@/widgets/architecture-graph/useCloudLens'
import type { LensId } from '@/widgets/architecture-graph/presets'
import {
  getHierarchicalInfrastructure,
  listDeployments,
  type CloudSpec,
  type DeploymentSummary,
  type HierarchicalInfrastructure,
  type TopologyTree,
} from '@/lib/infrastructure.types'

/* ── View mode ──────────────────────────────────────────────────── */

export type CloudView = 'graph' | 'list'
const DEFAULT_VIEW: CloudView = 'graph'
const VIEW_STORAGE_KEY = 'sov-cloud-view'

function isValidView(value: unknown): value is CloudView {
  return value === 'graph' || value === 'list'
}

function writePersistedView(view: CloudView): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(VIEW_STORAGE_KEY, view)
  } catch {
    /* noop */
  }
}

/**
 * Synthesise a `cloud` block from the regions list when the backend
 * doesn't return one. Every distinct provider becomes a single
 * cloud-tenant anchor.
 */
function inferCloudFromTopology(topology?: TopologyTree): CloudSpec[] {
  const regions = topology?.regions ?? []
  const byProvider = new Map<string, CloudSpec>()
  for (const r of regions) {
    const key = r.provider ?? 'unknown'
    const existing = byProvider.get(key)
    if (existing) {
      existing.regionCount += 1
    } else {
      byProvider.set(key, {
        id: `cloud-${key}`,
        name: key,
        provider: key,
        regionCount: 1,
        quotaUsed: 0,
        quotaLimit: 0,
      })
    }
  }
  return Array.from(byProvider.values())
}

/* ── Shared infrastructure query context ───────────────────────── */

export interface CloudContextValue {
  deploymentId: string
  data: HierarchicalInfrastructure | null
  isLoading: boolean
  isError: boolean
  refetch: () => void
  /** Live K8s snapshot from useK8sCacheStream — single subscription
   *  shared across the whole page (chip counts + every list page +
   *  the architecture graph). K8sListPage reads from this instead of
   *  opening a duplicate SSE connection. */
  k8sSnapshot: ReadonlyMap<string, unknown> | null
  k8sStatus: 'connecting' | 'open' | 'closed' | 'error'
  k8sRevision: number
}

const CloudContext = createContext<CloudContextValue | null>(null)

export function useCloud(): CloudContextValue {
  const ctx = useContext(CloudContext)
  if (!ctx) {
    throw new Error('useCloud must be used inside a CloudPage subtree')
  }
  return ctx
}

const STALE_MS = 30_000

/* ── Page shell ────────────────────────────────────────────────── */

export interface CloudPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /**
   * Test seam — render a content slot directly instead of dispatching
   * on the URL `view` query. Allows component tests to mount the
   * shell without requiring a full TanStack-Router child tree.
   */
  contentOverride?: ReactNode
  /**
   * Test seam — bypass the React Query fetcher with synthetic data.
   * The data flows through CloudContext to children so every sub-page
   * sees the same response.
   */
  initialDataOverride?: HierarchicalInfrastructure
  /** Test seam — bypass the deployments-list fetch. */
  deploymentsOverride?: DeploymentSummary[]
  /** Test seam — force the view irrespective of URL/storage. */
  viewOverride?: CloudView
}

export function CloudPage({
  disableStream = false,
  contentOverride,
  initialDataOverride,
  deploymentsOverride,
  viewOverride,
}: CloudPageProps = {}) {
  // tanstack-router resolves the matched route's params at runtime;
  // both the new `/cloud` parent and the legacy `/infrastructure`
  // parent expose the same `deploymentId` param, and the strict:false
  // option lets us share this component across both during the
  // rename window.
  const params = useParams({ strict: false }) as { deploymentId?: string }
  // Chroot fallback: on the Sovereign's adult hostname the URL carries
  // no deploymentId — resolve it from the JWT cookie via /sovereign/self
  // (same pattern as JobDetail). Without this the topology query fires
  // against /deployments/undefined/infrastructure/topology and the page
  // shows "Couldn't load architecture".
  const { deploymentId: resolvedDepId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedDepId ?? ''
  // Chroot-aware nav target: when the operator is monitoring an in-flight
  // Sovereign from the mothership wizard, every link MUST stay scoped under
  // /provision/<id>/cloud. On the Sovereign's adult hostname (DETECTED_MODE
  // === 'sovereign') the deploymentId is implicit so we use clean /cloud.
  const cloudPath = (id: string) =>
    DETECTED_MODE.mode === 'sovereign' || !id
      ? '/cloud'
      : `/provision/${id}/cloud`
  const navigate = useNavigate()

  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useSearch({ strict: false }) as {
    view?: string
    kind?: string
    // #3996 follow-up — the reconciliation deep-link opens the surface on
    // the Reconciliation lens; the route's validateSearch already
    // closed-set-validated this against LENSES, so we thread it straight
    // into CloudLensProvider as the initial chip-set.
    lens?: LensId
  }

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Single hierarchical-topology fetch — every sub-page reads off this.
  // Don't fire with an empty deploymentId — the URL would resolve to
  // /deployments//infrastructure/topology and surface as "Couldn't
  // load architecture". On the chroot the cookie-resolution may take a
  // tick; the query auto-runs once deploymentId lands.
  const topologyQuery = useQuery<HierarchicalInfrastructure>({
    queryKey: ['infra-hierarchical', deploymentId],
    queryFn: () => getHierarchicalInfrastructure(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride && !!deploymentId,
    retry: 1,
  })

  // Deployments list — feeds the per-Sovereign header switcher.
  const deploymentsQuery = useQuery<DeploymentSummary[]>({
    queryKey: ['deployments-list'],
    queryFn: listDeployments,
    staleTime: 60_000,
    enabled: !deploymentsOverride,
    retry: 1,
  })

  const data = useMemo<HierarchicalInfrastructure | null>(() => {
    const raw = initialDataOverride ?? topologyQuery.data ?? null
    if (!raw) return null
    return {
      cloud: raw.cloud ?? inferCloudFromTopology(raw.topology),
      topology: {
        pattern: raw.topology?.pattern ?? 'solo',
        regions: (raw.topology?.regions ?? []).map((r) => ({
          ...r,
          clusters: (r.clusters ?? []).map((c) => ({
            ...c,
            vclusters: c.vclusters ?? [],
            loadBalancers: (c.loadBalancers ?? []).map((lb) => ({
              ...lb,
              listeners:
                lb.listeners ??
                (typeof (lb as unknown as { ports?: string }).ports === 'string'
                  ? ((lb as unknown as { ports: string }).ports || '')
                      .split(',')
                      .map((p) => p.trim())
                      .filter(Boolean)
                      .map((p) => ({ port: parseInt(p, 10), protocol: 'tcp' }))
                  : []),
              targets: lb.targets ?? [],
            })),
            nodePools: c.nodePools ?? [],
            nodes: c.nodes ?? [],
          })),
          networks: (r.networks ?? []).map((n) => ({
            ...n,
            peerings: n.peerings ?? [],
            firewalls: n.firewalls ?? [],
          })),
        })),
      },
      storage: {
        pvcs: raw.storage?.pvcs ?? [],
        buckets: raw.storage?.buckets ?? [],
        volumes: raw.storage?.volumes ?? [],
      },
    }
  }, [initialDataOverride, topologyQuery.data, topologyQuery.isError])

  // Live K8s snapshot — feeds the chip-count badges AND every list
  // page via CloudContext. Single subscription per page (subscribing
  // to all kinds) avoids the HTTP/1.1 connection-budget starvation
  // that hangs duplicate streams at "connecting" indefinitely.
  // PR #1062 wired the backend; PR #1065 hoisted this above the ctx
  // build so list pages can read from the same snapshot.
  //
  // #3987: the subscription MUST cover every per-kind list page's
  // registry kind, not just the architecture graph's structural kinds.
  // The default (GRAPH_K8S_KINDS) omitted secret / policyreport /
  // clusterpolicyreport and ALL the reconciler + Catalyst-CR kinds, so
  // the snapshot never held a `helmrelease:` (etc.) key and
  // `/cloud?view=list&kind=helmreleases` rendered "No helmrelease
  // objects" despite 49+ live (the graph showed them via the separate
  // /reconciliation endpoint). CLOUD_PAGE_K8S_KINDS is the de-duplicated
  // union of GRAPH_K8S_KINDS + every KIND_TO_REGISTRY registry Name, so
  // every list page AND every chip count reads its real live objects.
  const k8sStream = useK8sCacheStream(deploymentId, {
    enabled: !!deploymentId && !disableStream,
    kinds: CLOUD_PAGE_K8S_KINDS,
  })

  const ctx: CloudContextValue = useMemo(
    () => ({
      deploymentId,
      data,
      isLoading: !initialDataOverride && topologyQuery.isLoading && !data,
      isError: topologyQuery.isError && !initialDataOverride,
      refetch: () => topologyQuery.refetch(),
      k8sSnapshot: k8sStream.snapshot,
      k8sStatus: k8sStream.status,
      k8sRevision: k8sStream.revision,
    }),
    [deploymentId, data, initialDataOverride, topologyQuery, k8sStream.snapshot, k8sStream.status, k8sStream.revision],
  )

  const deployments = deploymentsOverride ?? deploymentsQuery.data ?? []
  const switcherOptions: DeploymentSummary[] = useMemo(() => {
    const list = [...deployments]
    if (!list.find((d) => d.id === deploymentId)) {
      list.unshift({
        id: deploymentId,
        sovereignFQDN: sovereignFQDN ?? deploymentId,
        status: 'unknown',
      })
    }
    return list
  }, [deployments, deploymentId, sovereignFQDN])

  /* ── View mode resolution ───────────────────────────────────── */
  // Graph is the ALWAYS-default when no explicit `?view=` is present
  // (#3980 fix 1). A persisted preference is still written on every
  // change (so an operator who explicitly toggled List sees their
  // choice reflected in the URL during that session), but a FRESH load
  // of `/cloud` with no `view` query must land on Graph — never silently
  // re-open to a previously-used List view. The persisted value is
  // therefore NOT consulted here; DEFAULT_VIEW ('graph') wins.
  const activeView: CloudView = useMemo(() => {
    if (viewOverride) return viewOverride
    if (isValidView(search.view)) return search.view
    return DEFAULT_VIEW
  }, [viewOverride, search.view])

  // Persist + URL-canonicalise on every change.
  useEffect(() => {
    writePersistedView(activeView)
  }, [activeView])

  useEffect(() => {
    if (viewOverride) return
    if (search.view === activeView) return
    if (!pathname.endsWith('/cloud')) return
    // For graph view we don't carry kind; for list view we let the
    // list view itself populate kind on mount (avoids a double-nav
    // when the persisted kind is read client-side).
    const next: { view: CloudView; kind?: string } = { view: activeView }
    if (activeView === 'list' && typeof search.kind === 'string') {
      next.kind = search.kind
    }
    navigate({
      to: cloudPath(deploymentId) as never,
      params: { deploymentId } as never,
      search: next as never,
      replace: true,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeView, search.view, search.kind, pathname, deploymentId, viewOverride])

  function setView(next: CloudView) {
    if (next === activeView) return
    const target: { view: CloudView; kind?: string } = { view: next }
    if (next === 'list' && typeof search.kind === 'string') {
      target.kind = search.kind
    }
    navigate({
      to: cloudPath(deploymentId) as never,
      params: { deploymentId } as never,
      search: target as never,
      replace: false,
    })
  }

  function handleSwitch(nextId: string) {
    if (nextId === deploymentId) return
    // Preserve the active view (and active kind if list) when the
    // operator switches Sovereigns.
    const target: { view: CloudView; kind?: string } = { view: activeView }
    if (activeView === 'list' && typeof search.kind === 'string') {
      target.kind = search.kind
    }
    navigate({
      to: cloudPath(nextId) as never,
      params: { deploymentId: nextId } as never,
      search: target as never,
    })
  }

  /* ── Resource-kind chip strip (issue #366 item 1 · #3978 restore) ──
   * #3970 retired this strip from the list path when it flattened the
   * list into CloudUnifiedList. #3978 restores it: the strip drives the
   * per-kind CloudListView dispatch (each kind → its own column set),
   * EXACTLY as before, now including the reconciler kinds. */
  const activeKind: CloudListKind = useMemo(() => {
    if (isValidKind(search.kind)) return search.kind
    return readPersistedKind() ?? DEFAULT_KIND
  }, [search.kind])

  // Per-kind count map for the chip badges. Built dynamically from the
  // KIND_IDS catalogue (never a hardcoded seed — that maintenance trap
  // is what stale-counted new kinds before). Topology-projected kinds
  // (clusters / vclusters / node-pools / worker-nodes / load-balancers /
  // pvcs / buckets / volumes) seed from the topology snapshot; every
  // K8s-backed kind (incl. the reconcilers) is overridden from the live
  // SSE snapshot once initialState arrives. `null` = data unavailable
  // (renders "—"); 0 = genuinely empty.
  // Body extracted to cloud-list/kindCounts.ts (#5611) so the guard can
  // assert on the VALUE this produces without hand-feeding the chips a
  // `counts` prop production never generates.
  const kindCounts = useMemo<Record<CloudListKind, number | null>>(
    () => deriveKindCounts(data, k8sStream.snapshot),
    [data, k8sStream.snapshot, k8sStream.revision],
  )

  const setKind = useCallback(
    (next: CloudListKind) => {
      navigate({
        to: cloudPath(deploymentId) as never,
        params: { deploymentId } as never,
        search: { view: 'list', kind: next } as never,
        replace: false,
      })
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [navigate, deploymentId],
  )

  /* ── Fullscreen ─────────────────────────────────────────────── */
  const contentRef = useRef<HTMLDivElement | null>(null)
  const [isFullscreen, setIsFullscreen] = useState(false)

  useEffect(() => {
    function handleChange() {
      const fs = document.fullscreenElement
      setIsFullscreen(fs === contentRef.current)
    }
    document.addEventListener('fullscreenchange', handleChange)
    // Defensive: also listen to the prefixed events for older Safari.
    document.addEventListener('webkitfullscreenchange' as never, handleChange)
    return () => {
      document.removeEventListener('fullscreenchange', handleChange)
      document.removeEventListener('webkitfullscreenchange' as never, handleChange)
    }
  }, [])

  const enterFullscreen = useCallback(() => {
    const el = contentRef.current
    if (!el) return
    const req =
      (el.requestFullscreen as unknown as undefined | (() => Promise<void>)) ??
      ((el as unknown as { webkitRequestFullscreen?: () => Promise<void> })
        .webkitRequestFullscreen as undefined | (() => Promise<void>))
    if (typeof req === 'function') {
      req.call(el)?.catch(() => {
        /* User-agent denied fullscreen — render synthetic-fullscreen
         *  state via CSS instead, see contentRef class below. */
        setIsFullscreen(true)
      })
    } else {
      // Synthetic fallback for environments without the API (jsdom,
      // older browsers): toggle a CSS-only "fullscreen" affordance so
      // the operator still sees a maximised view.
      setIsFullscreen(true)
    }
  }, [])

  const exitFullscreen = useCallback(() => {
    if (document.fullscreenElement === contentRef.current) {
      const exit =
        (document.exitFullscreen as unknown as undefined | (() => Promise<void>)) ??
        ((document as unknown as { webkitExitFullscreen?: () => Promise<void> })
          .webkitExitFullscreen as undefined | (() => Promise<void>))
      if (typeof exit === 'function') {
        exit.call(document)?.catch(() => {
          setIsFullscreen(false)
        })
      } else {
        setIsFullscreen(false)
      }
    } else {
      setIsFullscreen(false)
    }
  }, [])

  const toggleFullscreen = useCallback(() => {
    if (isFullscreen) exitFullscreen()
    else enterFullscreen()
  }, [isFullscreen, enterFullscreen, exitFullscreen])

  // Pick the body component: contentOverride > Outlet (legacy redirect
  // routes still point children here) > view-driven dispatch.
  let body: ReactNode
  if (contentOverride) {
    body = contentOverride
  } else if (activeView === 'list') {
    body = <CloudListView />
  } else {
    body = <Architecture />
  }

  // Outlet support: when the router has a child route matched (e.g.
  // legacy /cloud/architecture before the redirect fires), render it
  // through the Outlet. Otherwise render the dispatched body. The
  // legacy routes are no-op redirect components, so the Outlet path
  // collapses to null in steady state.
  const outletSlot = <Outlet />

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Cloud"
      headerSlotRight={
        <select
          data-testid="cloud-sovereign-switcher"
          value={deploymentId}
          onChange={(e) => handleSwitch(e.target.value)}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
        >
          {switcherOptions.map((d) => (
            <option key={d.id ?? ''} value={d.id ?? ''}>
              {d.sovereignFQDN || (d.id ?? '').slice(0, 8) || '(unknown)'}
            </option>
          ))}
        </select>
      }
    >
      <style>{CLOUD_PAGE_CSS}</style>

      <div data-testid="cloud-page" className="cloud-page mx-auto flex w-full max-w-7xl flex-col">
        {/* The page title now lives in the PortalShell header centre
         *  slot (issue #366 item 2). The body opens directly with the
         *  toolbar. A hidden testid anchor preserves the legacy
         *  cloud-title selector used by component tests. */}
        <span data-testid="cloud-title" className="sr-only">
          Cloud
        </span>

        {/* Toolbar: View toggle (left) + chip strip (centre, list view
         *  only) + Fullscreen icon button (right). */}
        <div
          className="cloud-page-toolbar"
          data-testid="cloud-page-toolbar"
        >
          <div
            role="tablist"
            aria-label="Cloud view"
            className="cloud-page-view-toggle"
            data-testid="cloud-page-view-toggle"
          >
            <button
              type="button"
              role="tab"
              data-testid="cloud-page-view-graph"
              data-active={activeView === 'graph' ? 'true' : 'false'}
              aria-selected={activeView === 'graph'}
              aria-controls="cloud-page-content"
              onClick={() => setView('graph')}
              className={`cloud-page-view-tab ${activeView === 'graph' ? 'cloud-page-view-tab-active' : ''}`}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
                <circle cx="6" cy="6" r="2" />
                <circle cx="18" cy="6" r="2" />
                <circle cx="12" cy="18" r="2" />
                <line x1="7.5" y1="7" x2="11" y2="16.5" strokeLinecap="round" />
                <line x1="16.5" y1="7" x2="13" y2="16.5" strokeLinecap="round" />
                <line x1="8" y1="6" x2="16" y2="6" strokeLinecap="round" />
              </svg>
              <span>Graph</span>
            </button>
            <button
              type="button"
              role="tab"
              data-testid="cloud-page-view-list"
              data-active={activeView === 'list' ? 'true' : 'false'}
              aria-selected={activeView === 'list'}
              aria-controls="cloud-page-content"
              onClick={() => setView('list')}
              className={`cloud-page-view-tab ${activeView === 'list' ? 'cloud-page-view-tab-active' : ''}`}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
                <line x1="4" y1="6" x2="20" y2="6" strokeLinecap="round" />
                <line x1="4" y1="12" x2="20" y2="12" strokeLinecap="round" />
                <line x1="4" y1="18" x2="20" y2="18" strokeLinecap="round" />
              </svg>
              <span>List</span>
            </button>
          </div>

          {/* #3978 restore — in list view the resource-kind chip strip
              drives the per-kind CloudListView dispatch (each kind → its
              own column set). #3970 had wrongly removed it. In graph view
              a spacer keeps the fullscreen button right-aligned (the graph
              owns its own lens chip-set via CloudLensProvider). */}
          {activeView === 'list' ? (
            <CloudKindChips
              activeKind={activeKind}
              counts={kindCounts}
              onChange={setKind}
            />
          ) : (
            <span aria-hidden className="cloud-page-toolbar-spacer" />
          )}

          <button
            type="button"
            data-testid="cloud-page-fullscreen-toggle"
            aria-pressed={isFullscreen}
            aria-label="Toggle fullscreen"
            title={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
            className="cloud-page-fs-toggle cloud-page-fs-toggle-icon"
            onClick={toggleFullscreen}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
              {isFullscreen ? (
                <>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 4v3a2 2 0 0 1 -2 2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 4v3a2 2 0 0 0 2 2h3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 20v-3a2 2 0 0 0 -2 -2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 20v-3a2 2 0 0 1 2 -2h3" />
                </>
              ) : (
                <>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M4 9V6a2 2 0 0 1 2 -2h3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M20 9V6a2 2 0 0 0 -2 -2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M4 15v3a2 2 0 0 0 2 2h3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M20 15v3a2 2 0 0 1 -2 2h-3" />
                </>
              )}
            </svg>
          </button>
        </div>

        <CloudContext.Provider value={ctx}>
          <CloudLensProvider initialLensId={search.lens}>
          <div
            id="cloud-page-content"
            ref={contentRef}
            className={`cloud-page-content ${isFullscreen ? 'cloud-page-content-fullscreen' : ''}`}
            data-testid="cloud-content"
            data-fullscreen={isFullscreen ? 'true' : 'false'}
            data-view={activeView}
          >
            {/* Floating icon-only exit button — only visible while
             *  fullscreen (issue #366 item 4). Aria-label kept for a11y. */}
            {isFullscreen && (
              <button
                type="button"
                data-testid="cloud-page-fullscreen-exit"
                aria-label="Exit fullscreen"
                title="Exit fullscreen"
                className="cloud-page-fs-exit cloud-page-fs-exit-icon"
                onClick={exitFullscreen}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 4v3a2 2 0 0 1 -2 2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 4v3a2 2 0 0 0 2 2h3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 20v-3a2 2 0 0 0 -2 -2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 20v-3a2 2 0 0 1 2 -2h3" />
                </svg>
              </button>
            )}
            {/* Body — outlet shows up first in case a child redirect
             *  route is still resolving; otherwise the dispatched body. */}
            <div className="cloud-page-body" data-testid={`cloud-page-body-${activeView}`}>
              {body}
            </div>
            {/* Hidden outlet — used by the redirect-only legacy paths
             *  that mount under this route. They render `null` so the
             *  Outlet stays invisible in steady state. */}
            <div className="cloud-page-outlet" aria-hidden>
              {outletSlot}
            </div>
          </div>
          </CloudLensProvider>
        </CloudContext.Provider>
      </div>
    </PortalShell>
  )
}

const CLOUD_PAGE_CSS = `
.cloud-page-toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
.cloud-page-toolbar-spacer { flex: 1; }
.cloud-page-toolbar > [data-testid="cloud-kind-chips"] {
  flex: 1;
  overflow-x: auto;
}
.cloud-page-toolbar > [data-testid="cloud-page-fullscreen-toggle"] {
  margin-left: auto;
}
.cloud-page-view-toggle {
  display: inline-flex;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--color-bg-2);
  flex-shrink: 0;
}
.cloud-page-view-tab {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.4rem 0.85rem;
  font-size: 0.82rem;
  color: var(--color-text-dim);
  background: transparent;
  border: 0;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}
.cloud-page-view-tab:hover { color: var(--color-text); background: var(--color-surface-hover); }
.cloud-page-view-tab svg { width: 16px; height: 16px; }
.cloud-page-view-tab + .cloud-page-view-tab { border-left: 1px solid var(--color-border); }
.cloud-page-view-tab-active {
  background: color-mix(in srgb, var(--color-accent) 16%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}

.cloud-page-fs-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.4rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, transform 0.12s ease;
  flex-shrink: 0;
}
.cloud-page-fs-toggle-icon {
  width: 32px;
  height: 32px;
  padding: 0;
}
.cloud-page-fs-toggle:hover { color: var(--color-text); background: var(--color-surface-hover); }
.cloud-page-fs-toggle svg { width: 16px; height: 16px; }
.cloud-page-fs-toggle[aria-pressed="true"] {
  color: var(--color-accent);
  border-color: var(--color-accent);
}

/* 100%-height graph (#3980 fix 4): the page is a flex column that fills
 * the PortalShell <main> (flex-1, so it stretches to the viewport bottom).
 * The graph content + body grab the remaining vertical space so the canvas
 * reaches the bottom edge instead of leaving ~20% dead space below a fixed
 * 540px box. List view is height-driven by its own content, so we scope the
 * grow to the graph view only via [data-view="graph"]. */
.cloud-page {
  flex: 1;
  min-height: 0;
}
.cloud-page-content {
  position: relative;
  margin-top: 0.5rem;
  /* Smooth scale + fade transition on enter/exit fullscreen */
  transition: transform 250ms ease, opacity 250ms ease, padding 250ms ease, background 250ms ease;
  transform-origin: top center;
}
.cloud-page-content[data-view="graph"]:not(.cloud-page-content-fullscreen) {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.cloud-page-content[data-view="graph"]:not(.cloud-page-content-fullscreen) .cloud-page-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.cloud-page-content[data-view="graph"]:not(.cloud-page-content-fullscreen) .cloud-page-body > * {
  flex: 1;
  min-height: 0;
}
.cloud-page-content-fullscreen,
.cloud-page-content:fullscreen,
.cloud-page-content:-webkit-full-screen {
  /* Fullscreen 100% height (issue #366 item 4): override any inherited
   * min-height / margin so the canvas reaches both top and bottom of
   * the viewport. The body is a flex column where .cloud-page-body
   * grabs flex: 1. */
  width: 100vw;
  height: 100vh;
  margin: 0;
  padding: 1.25rem;
  background: var(--color-bg);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.cloud-page-content-fullscreen .cloud-page-body,
.cloud-page-content:fullscreen .cloud-page-body,
.cloud-page-content:-webkit-full-screen .cloud-page-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.cloud-page-content-fullscreen .cloud-page-body > *,
.cloud-page-content:fullscreen .cloud-page-body > *,
.cloud-page-content:-webkit-full-screen .cloud-page-body > * {
  flex: 1;
  min-height: 0;
  height: 100%;
}

.cloud-page-fs-exit {
  position: absolute;
  top: 0.75rem;
  right: 0.75rem;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  cursor: pointer;
  z-index: 5;
}
.cloud-page-fs-exit-icon {
  width: 32px;
  height: 32px;
  padding: 0;
}
.cloud-page-fs-exit:hover { color: var(--color-text); background: var(--color-surface-hover); }
.cloud-page-fs-exit svg { width: 16px; height: 16px; }

.cloud-page-outlet { display: none; }

/* ── Legacy P3 list-page primitives (kept so list pages still
 *    render their own scaffolding correctly). ───────────────── */

.infra-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 0.75rem;
}
.infra-section { margin-top: 1.25rem; }
.infra-section h2 {
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--color-text-strong);
  margin: 0 0 0.5rem 0;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.infra-section h2 .count {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 0.08rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
}
.infra-empty {
  margin-top: 2rem;
  text-align: center;
  color: var(--color-text-dim);
  padding: 2rem 1rem;
  border: 1px dashed var(--color-border);
  border-radius: 12px;
  background: var(--color-bg-2);
}
.infra-empty .title {
  font-size: 0.95rem;
  color: var(--color-text-strong);
  font-weight: 600;
  margin: 0 0 0.3rem;
}
.infra-empty .sub { font-size: 0.82rem; margin: 0; }
`
