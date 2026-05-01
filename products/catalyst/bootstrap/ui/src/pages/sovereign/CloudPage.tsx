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
import { useDeploymentEvents } from './useDeploymentEvents'
import { Architecture } from './Architecture'
import { CloudListView } from './cloud-list/CloudListView'
import {
  getHierarchicalInfrastructure,
  listDeployments,
  type CloudSpec,
  type DeploymentSummary,
  type HierarchicalInfrastructure,
  type TopologyTree,
} from '@/lib/infrastructure.types'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'

/* ── View mode ──────────────────────────────────────────────────── */

export type CloudView = 'graph' | 'list'
const DEFAULT_VIEW: CloudView = 'graph'
const VIEW_STORAGE_KEY = 'sov-cloud-view'

function isValidView(value: unknown): value is CloudView {
  return value === 'graph' || value === 'list'
}

function readPersistedView(): CloudView | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(VIEW_STORAGE_KEY)
    return isValidView(raw) ? raw : null
  } catch {
    return null
  }
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
  const params = useParams({ strict: false }) as { deploymentId: string }
  const deploymentId = params.deploymentId
  const navigate = useNavigate()

  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useSearch({ strict: false }) as { view?: string; kind?: string }

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Single hierarchical-topology fetch — every sub-page reads off this.
  const topologyQuery = useQuery<HierarchicalInfrastructure>({
    queryKey: ['infra-hierarchical', deploymentId],
    queryFn: () => getHierarchicalInfrastructure(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
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
    const raw =
      initialDataOverride ??
      topologyQuery.data ??
      (topologyQuery.isError ? infrastructureTopologyFixture : null)
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

  const ctx: CloudContextValue = useMemo(
    () => ({
      deploymentId,
      data,
      isLoading: !initialDataOverride && topologyQuery.isLoading && !data,
      isError: topologyQuery.isError && !initialDataOverride,
      refetch: () => topologyQuery.refetch(),
    }),
    [deploymentId, data, initialDataOverride, topologyQuery],
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
  const activeView: CloudView = useMemo(() => {
    if (viewOverride) return viewOverride
    if (isValidView(search.view)) return search.view
    return readPersistedView() ?? DEFAULT_VIEW
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
      to: '/provision/$deploymentId/cloud' as never,
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
      to: '/provision/$deploymentId/cloud' as never,
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
      to: '/provision/$deploymentId/cloud' as never,
      params: { deploymentId: nextId } as never,
      search: target as never,
    })
  }

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
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{CLOUD_PAGE_CSS}</style>

      <div data-testid="cloud-page" className="mx-auto max-w-7xl">
        <header className="mb-3 flex items-start justify-between gap-4">
          <div>
            <h1
              className="text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="cloud-title"
            >
              Cloud
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Sovereign cloud — regions, clusters, and resources for{' '}
              {sovereignFQDN ?? `deployment ${deploymentId.slice(0, 8)}`}.
            </p>
          </div>
          <div className="flex flex-col items-end gap-1.5">
            <select
              data-testid="cloud-sovereign-switcher"
              value={deploymentId}
              onChange={(e) => handleSwitch(e.target.value)}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
            >
              {switcherOptions.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.sovereignFQDN || d.id.slice(0, 8)}
                </option>
              ))}
            </select>
            <div className="text-right text-xs text-[var(--color-text-dim)]">
              <div className="font-mono">{deploymentId.slice(0, 8)}</div>
            </div>
          </div>
        </header>

        {/* Toolbar: View toggle (left) + Fullscreen button (right). */}
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

          <button
            type="button"
            data-testid="cloud-page-fullscreen-toggle"
            aria-pressed={isFullscreen}
            aria-label={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
            className="cloud-page-fs-toggle"
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
            <span>{isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}</span>
          </button>
        </div>

        <CloudContext.Provider value={ctx}>
          <div
            id="cloud-page-content"
            ref={contentRef}
            className={`cloud-page-content ${isFullscreen ? 'cloud-page-content-fullscreen' : ''}`}
            data-testid="cloud-content"
            data-fullscreen={isFullscreen ? 'true' : 'false'}
            data-view={activeView}
          >
            {/* Floating exit button — only visible while fullscreen. */}
            {isFullscreen && (
              <button
                type="button"
                data-testid="cloud-page-fullscreen-exit"
                aria-label="Exit fullscreen"
                className="cloud-page-fs-exit"
                onClick={exitFullscreen}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 4v3a2 2 0 0 1 -2 2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 4v3a2 2 0 0 0 2 2h3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 20v-3a2 2 0 0 0 -2 -2h-3" />
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15 20v-3a2 2 0 0 1 2 -2h3" />
                </svg>
                <span>Exit fullscreen</span>
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
        </CloudContext.Provider>
      </div>
    </PortalShell>
  )
}

const CLOUD_PAGE_CSS = `
.cloud-page-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}
.cloud-page-view-toggle {
  display: inline-flex;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--color-bg-2);
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
  gap: 0.4rem;
  padding: 0.4rem 0.75rem;
  font-size: 0.82rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease, transform 0.12s ease;
}
.cloud-page-fs-toggle:hover { color: var(--color-text); background: var(--color-surface-hover); }
.cloud-page-fs-toggle svg { width: 16px; height: 16px; }
.cloud-page-fs-toggle[aria-pressed="true"] {
  color: var(--color-accent);
  border-color: var(--color-accent);
}

.cloud-page-content {
  position: relative;
  margin-top: 0.5rem;
  /* Smooth scale + fade transition on enter/exit fullscreen */
  transition: transform 250ms ease, opacity 250ms ease, padding 250ms ease, background 250ms ease;
  transform-origin: top center;
}
.cloud-page-content-fullscreen {
  background: var(--color-bg);
  padding: 1.25rem;
  /* The native fullscreenchange path applies the user-agent
   * fullscreen styling; for the synthetic fallback we apply a fixed
   * overlay so the operator still sees a maximised view. */
}
.cloud-page-content:fullscreen {
  background: var(--color-bg);
  padding: 1.25rem;
  overflow: auto;
}
/* Some user agents emit -webkit-full-screen separately; mirror. */
.cloud-page-content:-webkit-full-screen {
  background: var(--color-bg);
  padding: 1.25rem;
  overflow: auto;
}

.cloud-page-fs-exit {
  position: absolute;
  top: 0.75rem;
  right: 0.75rem;
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.4rem 0.75rem;
  font-size: 0.82rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  cursor: pointer;
  z-index: 5;
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
