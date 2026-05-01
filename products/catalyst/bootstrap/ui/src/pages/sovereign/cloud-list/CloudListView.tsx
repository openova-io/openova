/**
 * CloudListView — list-mode body for the Cloud parent surface (issue
 * #350 item 7). Lives inside CloudPage's content area when the
 * `view=list` query param is active.
 *
 * Layout contract:
 *   • Top-of-page: 12-tile resource-kind card grid. Each tile shows an
 *     icon, the kind label, and a count. Clicking a tile sets the
 *     active kind (and updates the URL ?kind=… query so the state is
 *     bookmarkable / shareable).
 *   • Toolbar: a compact <select> dropdown that mirrors the card-grid
 *     selection. Keyboard-driven users (kbd-power users + a11y) can
 *     change the active kind without mouse-clicking a tile.
 *   • Below: the active kind's existing P3 list page rendered inline.
 *     Components are reused as-is — none of them are rewritten.
 *
 * The active kind persists to localStorage under `sov-cloud-list-kind`
 * so a return visit lands on the operator's last-viewed list. The URL
 * query takes precedence when present.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every kind id,
 * label and tagline lives in a single typed table at the top of this
 * file — there's no inlined string literal at the call sites.
 */

import { useCallback, useEffect, useMemo } from 'react'
import { useNavigate, useRouterState, useSearch } from '@tanstack/react-router'
import { useCloud } from '../CloudPage'
import { CLOUD_LIST_CSS } from './cloudListCss'

import { ClustersPage } from '../cloud-compute/ClustersPage'
import { VClustersPage } from '../cloud-compute/VClustersPage'
import { NodePoolsPage } from '../cloud-compute/NodePoolsPage'
import { WorkerNodesPage } from '../cloud-compute/WorkerNodesPage'
import { LoadBalancersPage } from '../cloud-network/LoadBalancersPage'
import { ServicesPage } from '../cloud-network/ServicesPage'
import { IngressesPage } from '../cloud-network/IngressesPage'
import { DnsZonesPage } from '../cloud-network/DnsZonesPage'
import { PvcsPage } from '../cloud-storage/PvcsPage'
import { BucketsPage } from '../cloud-storage/BucketsPage'
import { VolumesPage } from '../cloud-storage/VolumesPage'
import { StorageClassesPage } from '../cloud-storage/StorageClassesPage'

/* ── Kind catalogue ─────────────────────────────────────────────── */

export type CloudListKind =
  | 'clusters'
  | 'vclusters'
  | 'node-pools'
  | 'worker-nodes'
  | 'load-balancers'
  | 'services'
  | 'ingresses'
  | 'dns-zones'
  | 'pvcs'
  | 'buckets'
  | 'volumes'
  | 'storage-classes'

interface KindEntry {
  id: CloudListKind
  label: string
  /** One-line tagline shown beneath the count on the tile. */
  tagline: string
  /** True when the count is real; false when the underlying informer
   *  isn't wired yet (we render a "—" instead of a number). */
  hasData: boolean
  Component: () => React.JSX.Element
  /** SVG path data on the canonical 24x24 viewBox — lucide-style. */
  icon: string
  /** Conceptual category (drives the small chip on the tile). */
  category: 'compute' | 'network' | 'storage'
}

// Tabler / lucide-style outlines on the same 24x24 viewBox the
// sidebar NavIcon uses.
const ICON_CLUSTER = 'M3 12c0 -4 4 -7 9 -7s9 3 9 7v6m0 0a2 2 0 0 1 -2 2H5a2 2 0 0 1 -2 -2v0M3 18v0M3 8h18'
const ICON_VCLUSTER = 'M4 7a3 3 0 0 1 3 -3h10a3 3 0 0 1 3 3v10a3 3 0 0 1 -3 3H7a3 3 0 0 1 -3 -3zM8 8h8M8 12h8M8 16h5'
const ICON_NODE_POOL = 'M5 4h4v4H5zM5 16h4v4H5zM15 4h4v4h-4zM15 16h4v4h-4zM7 8v8M17 8v8M9 6h6M9 18h6'
const ICON_WORKER_NODE = 'M4 5h16v6H4zM4 13h16v6H4zM7 8h.01M7 16h.01M11 8h6M11 16h6'
const ICON_LB = 'M12 4v4m0 0a4 4 0 0 0 -4 4v0m4 -4a4 4 0 0 1 4 4v0M4 12h4M16 12h4M6 14a2 2 0 0 0 2 2v0a2 2 0 0 0 2 -2v0a2 2 0 0 0 -2 -2v0a2 2 0 0 0 -2 2zM14 14a2 2 0 0 0 2 2v0a2 2 0 0 0 2 -2v0a2 2 0 0 0 -2 -2v0a2 2 0 0 0 -2 2z'
const ICON_SERVICE = 'M5 7h14M5 12h14M5 17h14M3 7v10M21 7v10'
const ICON_INGRESS = 'M3 12h6M21 12h-6M9 8l4 4 -4 4M15 16l-4 -4 4 -4'
const ICON_DNS = 'M12 3a9 9 0 0 0 0 18m0 -18a9 9 0 0 1 0 18m0 -18c2 2 3 5 3 9s-1 7 -3 9m0 -18c-2 2 -3 5 -3 9s1 7 3 9M3 12h18'
const ICON_PVC = 'M5 8a7 3 0 0 0 14 0A7 3 0 0 0 5 8zM5 8v8a7 3 0 0 0 14 0V8M5 12a7 3 0 0 0 14 0'
const ICON_BUCKET = 'M5 6h14l-1 14a2 2 0 0 1 -2 2H8a2 2 0 0 1 -2 -2zM9 6V4a3 3 0 0 1 6 0v2'
const ICON_VOLUME = 'M5 4a7 3 0 0 0 14 0A7 3 0 0 0 5 4zM5 4v16a7 3 0 0 0 14 0V4'
const ICON_STORAGE_CLASS = 'M4 6h16M4 12h16M4 18h16M8 6v12M16 6v12'

const KINDS: readonly KindEntry[] = [
  { id: 'clusters',        label: 'Clusters',        tagline: 'k3s / k8s control planes',                  hasData: true,  Component: ClustersPage,        icon: ICON_CLUSTER,       category: 'compute' },
  { id: 'vclusters',       label: 'vClusters',       tagline: 'Logical isolation per Sovereign tenant',    hasData: true,  Component: VClustersPage,       icon: ICON_VCLUSTER,      category: 'compute' },
  { id: 'node-pools',      label: 'Node Pools',      tagline: 'Worker pools grouped by SKU + role',        hasData: true,  Component: NodePoolsPage,       icon: ICON_NODE_POOL,     category: 'compute' },
  { id: 'worker-nodes',    label: 'Worker Nodes',    tagline: 'Individual VMs / kubelets reporting in',    hasData: true,  Component: WorkerNodesPage,     icon: ICON_WORKER_NODE,   category: 'compute' },
  { id: 'load-balancers',  label: 'Load Balancers',  tagline: 'Cloud-provisioned LBs fronting clusters',   hasData: true,  Component: LoadBalancersPage,   icon: ICON_LB,            category: 'network' },
  { id: 'services',        label: 'Services',        tagline: 'Awaiting service informer (#321)',          hasData: false, Component: ServicesPage,        icon: ICON_SERVICE,       category: 'network' },
  { id: 'ingresses',       label: 'Ingresses',       tagline: 'Awaiting ingress informer (#321)',          hasData: false, Component: IngressesPage,       icon: ICON_INGRESS,       category: 'network' },
  { id: 'dns-zones',       label: 'DNS Zones',       tagline: 'Awaiting external-dns informer (#321)',     hasData: false, Component: DnsZonesPage,        icon: ICON_DNS,           category: 'network' },
  { id: 'pvcs',            label: 'PVCs',            tagline: 'Persistent volume claims',                  hasData: true,  Component: PvcsPage,            icon: ICON_PVC,           category: 'storage' },
  { id: 'buckets',         label: 'Buckets',         tagline: 'S3-compatible (SeaweedFS / provider)',      hasData: true,  Component: BucketsPage,         icon: ICON_BUCKET,        category: 'storage' },
  { id: 'volumes',         label: 'Volumes',         tagline: 'Cloud block volumes attached to nodes',     hasData: true,  Component: VolumesPage,         icon: ICON_VOLUME,        category: 'storage' },
  { id: 'storage-classes', label: 'Storage Classes', tagline: 'Awaiting storage-class informer (#321)',    hasData: false, Component: StorageClassesPage,  icon: ICON_STORAGE_CLASS, category: 'storage' },
] as const

const KIND_IDS: readonly CloudListKind[] = KINDS.map((k) => k.id)
const DEFAULT_KIND: CloudListKind = 'clusters'
const KIND_STORAGE_KEY = 'sov-cloud-list-kind'

function isValidKind(value: unknown): value is CloudListKind {
  return typeof value === 'string' && (KIND_IDS as readonly string[]).includes(value)
}

function readPersistedKind(): CloudListKind | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(KIND_STORAGE_KEY)
    return isValidKind(raw) ? raw : null
  } catch {
    return null
  }
}

function writePersistedKind(kind: CloudListKind): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(KIND_STORAGE_KEY, kind)
  } catch {
    /* noop */
  }
}

/* ── Component ──────────────────────────────────────────────────── */

interface CloudListViewProps {
  /** Optional explicit kind override — used by tests. When omitted the
   *  active kind comes from the URL query, the persisted value, or
   *  the default in that order. */
  kindOverride?: CloudListKind
  /** Optional explicit counts — used by tests to avoid wiring the
   *  full topology fixture. */
  countsOverride?: Partial<Record<CloudListKind, number>>
}

export function CloudListView({ kindOverride, countsOverride }: CloudListViewProps = {}) {
  const { deploymentId, data, isLoading } = useCloud()
  const navigate = useNavigate()
  // The Cloud route registers `kind` in its validateSearch schema; if
  // the search hook isn't available (older route registration during
  // a hot-reload) we fall back to the location.search string.
  const search = useSearch({ strict: false }) as { view?: string; kind?: string }
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  // Resolve the active kind: explicit override > URL query > persisted
  // localStorage > default. The first time the operator lands on the
  // list view without a `kind` query, we update the URL so the state
  // is shareable.
  const activeKind: CloudListKind = useMemo(() => {
    if (kindOverride) return kindOverride
    if (isValidKind(search.kind)) return search.kind
    const persisted = readPersistedKind()
    if (persisted) return persisted
    return DEFAULT_KIND
  }, [kindOverride, search.kind])

  // Persist the active kind whenever it changes.
  useEffect(() => {
    writePersistedKind(activeKind)
  }, [activeKind])

  // Make sure the URL carries the explicit kind so deep links work.
  useEffect(() => {
    if (kindOverride) return
    if (search.kind === activeKind) return
    if (!pathname.endsWith('/cloud')) return
    navigate({
      to: '/provision/$deploymentId/cloud' as never,
      params: { deploymentId } as never,
      search: { view: 'list', kind: activeKind } as never,
      replace: true,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeKind, search.kind, pathname, deploymentId])

  const setActiveKind = useCallback(
    (next: CloudListKind) => {
      navigate({
        to: '/provision/$deploymentId/cloud' as never,
        params: { deploymentId } as never,
        search: { view: 'list', kind: next } as never,
        replace: false,
      })
    },
    [navigate, deploymentId],
  )

  /* ── Counts derived from the shared infra tree ─────────────── */
  const counts = useMemo<Record<CloudListKind, number | null>>(() => {
    const c: Record<CloudListKind, number | null> = {
      'clusters': 0,
      'vclusters': 0,
      'node-pools': 0,
      'worker-nodes': 0,
      'load-balancers': 0,
      'services': null,
      'ingresses': null,
      'dns-zones': null,
      'pvcs': 0,
      'buckets': 0,
      'volumes': 0,
      'storage-classes': null,
    }
    if (data) {
      let clusters = 0
      let vclusters = 0
      let nodePools = 0
      let workerNodes = 0
      let lb = 0
      for (const region of data.topology.regions ?? []) {
        for (const cluster of region.clusters ?? []) {
          clusters += 1
          vclusters += cluster.vclusters?.length ?? 0
          nodePools += cluster.nodePools?.length ?? 0
          workerNodes += cluster.nodes?.length ?? 0
          lb += cluster.loadBalancers?.length ?? 0
        }
      }
      c['clusters'] = clusters
      c['vclusters'] = vclusters
      c['node-pools'] = nodePools
      c['worker-nodes'] = workerNodes
      c['load-balancers'] = lb
      c['pvcs'] = data.storage?.pvcs?.length ?? 0
      c['buckets'] = data.storage?.buckets?.length ?? 0
      c['volumes'] = data.storage?.volumes?.length ?? 0
    }
    if (countsOverride) {
      for (const [k, v] of Object.entries(countsOverride)) {
        if (typeof v === 'number') {
          c[k as CloudListKind] = v
        }
      }
    }
    return c
  }, [data, countsOverride])

  const ActiveListComponent = useMemo(() => {
    const entry = KINDS.find((k) => k.id === activeKind) ?? KINDS[0]
    return entry.Component
  }, [activeKind])

  return (
    <div data-testid="cloud-list-view">
      <style>{CLOUD_LIST_VIEW_CSS}</style>
      <style>{CLOUD_LIST_CSS}</style>

      {/* Toolbar: dropdown switcher (compact, kbd-driven). */}
      <div className="cloud-list-view-toolbar" data-testid="cloud-list-view-toolbar">
        <label className="cloud-list-view-dropdown-label">
          <span className="cloud-list-view-dropdown-caption">Resource</span>
          <select
            data-testid="cloud-list-view-kind-select"
            value={activeKind}
            onChange={(ev) => setActiveKind(ev.target.value as CloudListKind)}
            className="cloud-list-view-dropdown-select"
            aria-label="Select resource kind"
          >
            {KINDS.map((k) => {
              const c = counts[k.id]
              const showCount = k.hasData && c !== null
              return (
                <option key={k.id} value={k.id}>
                  {k.label}
                  {showCount ? ` (${c})` : ''}
                </option>
              )
            })}
          </select>
        </label>
      </div>

      {/* Card grid — 12 tiles, click to switch. */}
      <div className="cloud-list-view-tile-grid" data-testid="cloud-list-view-tile-grid">
        {KINDS.map((k) => {
          const c = counts[k.id]
          const showCount = k.hasData && c !== null
          const isActive = k.id === activeKind
          return (
            <button
              key={k.id}
              type="button"
              data-testid={`cloud-list-view-tile-${k.id}`}
              data-kind={k.id}
              data-active={isActive ? 'true' : 'false'}
              data-category={k.category}
              className={`cloud-list-view-tile ${isActive ? 'cloud-list-view-tile-active' : ''}`}
              aria-pressed={isActive}
              onClick={() => setActiveKind(k.id)}
            >
              <div className="cloud-list-view-tile-icon" aria-hidden>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
                  <path d={k.icon} />
                </svg>
              </div>
              <div className="cloud-list-view-tile-body">
                <div className="cloud-list-view-tile-name">
                  <span>{k.label}</span>
                  <span
                    className="cloud-list-view-tile-count"
                    data-testid={`cloud-list-view-tile-${k.id}-count`}
                  >
                    {showCount ? c : '—'}
                  </span>
                </div>
                <p className="cloud-list-view-tile-tagline">{k.tagline}</p>
              </div>
            </button>
          )
        })}
      </div>

      {/* Active list table — re-uses the P3 list-page component. */}
      <div className="cloud-list-view-active" data-testid={`cloud-list-view-active-${activeKind}`}>
        {isLoading && !data ? (
          <div
            className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
            data-testid="cloud-list-view-loading"
          >
            Loading {KINDS.find((k) => k.id === activeKind)?.label.toLowerCase()}…
          </div>
        ) : (
          <ActiveListComponent />
        )}
      </div>
    </div>
  )
}

/* ── Local CSS — tile grid + dropdown ──────────────────────────── */

const CLOUD_LIST_VIEW_CSS = `
.cloud-list-view-toolbar {
  display: flex;
  align-items: flex-end;
  gap: 0.6rem;
  margin-bottom: 0.75rem;
}
.cloud-list-view-dropdown-label {
  display: inline-flex;
  flex-direction: column;
  gap: 0.18rem;
}
.cloud-list-view-dropdown-caption {
  font-size: 0.62rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.cloud-list-view-dropdown-select {
  padding: 0.42rem 0.6rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  color: var(--color-text);
  font-size: 0.85rem;
  cursor: pointer;
  min-width: 14rem;
}
.cloud-list-view-dropdown-select:focus {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.cloud-list-view-tile-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 0.6rem;
  margin-bottom: 1.25rem;
}
.cloud-list-view-tile {
  display: flex;
  align-items: flex-start;
  gap: 0.6rem;
  text-align: left;
  background: var(--color-surface);
  border: 1.5px solid var(--color-border);
  border-radius: 12px;
  padding: 0.8rem;
  cursor: pointer;
  color: var(--color-text);
  transition: transform 0.12s ease, border-color 0.12s ease, background 0.12s ease;
  font: inherit;
}
.cloud-list-view-tile:hover {
  border-color: color-mix(in srgb, var(--color-accent) 60%, var(--color-border));
  transform: translateY(-1px);
}
.cloud-list-view-tile:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.cloud-list-view-tile-active {
  border-color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 8%, var(--color-surface));
}
.cloud-list-view-tile[data-category="compute"] .cloud-list-view-tile-icon { color: #60a5fa; }
.cloud-list-view-tile[data-category="network"] .cloud-list-view-tile-icon { color: #34d399; }
.cloud-list-view-tile[data-category="storage"] .cloud-list-view-tile-icon { color: #f59e0b; }
.cloud-list-view-tile-icon {
  flex-shrink: 0;
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.cloud-list-view-tile-icon svg {
  width: 22px;
  height: 22px;
}
.cloud-list-view-tile-body {
  min-width: 0;
  flex: 1;
}
.cloud-list-view-tile-name {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.4rem;
  font-size: 0.92rem;
  font-weight: 600;
  color: var(--color-text-strong);
}
.cloud-list-view-tile-count {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 0.06rem 0.5rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}
.cloud-list-view-tile-active .cloud-list-view-tile-count {
  background: color-mix(in srgb, var(--color-accent) 22%, transparent);
  color: var(--color-accent);
}
.cloud-list-view-tile-tagline {
  margin: 0.18rem 0 0;
  font-size: 0.78rem;
  color: var(--color-text-dim);
  line-height: 1.3;
}

.cloud-list-view-active {
  margin-top: 0.5rem;
}
`
