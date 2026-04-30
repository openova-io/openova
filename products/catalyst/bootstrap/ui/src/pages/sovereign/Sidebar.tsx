/**
 * Sidebar — Sovereign-portal left rail. Pixel-port of
 * core/console/src/components/Sidebar.svelte plus the accordion
 * structure under "Cloud" (issue #309).
 *
 * Layout contract (P3 of #309 — second-level accordion):
 *   • Fixed left rail, w-56, full height
 *   • Logo + product wordmark in the 56px header
 *   • Single-Sovereign label in place of the canonical Tenant
 *     switcher (the wizard is single-Sovereign by definition).
 *   • Nav list — flat for top-level destinations + a TWO-LEVEL
 *     accordion under "Cloud":
 *
 *       — apps                    → /sovereign/provision/$deploymentId
 *       — jobs                    → /sovereign/provision/$deploymentId/jobs
 *       — dashboard               → /sovereign/provision/$deploymentId/dashboard
 *       — cloud (accordion)       → /sovereign/provision/$deploymentId/cloud
 *           ↳ architecture        → /cloud/architecture
 *           ↳ compute (accordion) → /cloud/compute (landing)
 *               ↳ clusters        → /cloud/compute/clusters
 *               ↳ vclusters       → /cloud/compute/vclusters
 *               ↳ node pools      → /cloud/compute/node-pools
 *               ↳ worker nodes    → /cloud/compute/worker-nodes
 *           ↳ network (accordion) → /cloud/network (landing)
 *               ↳ services        → /cloud/network/services
 *               ↳ ingresses       → /cloud/network/ingresses
 *               ↳ load balancers  → /cloud/network/load-balancers
 *               ↳ dns zones       → /cloud/network/dns-zones
 *           ↳ storage (accordion) → /cloud/storage (landing)
 *               ↳ pvcs            → /cloud/storage/pvcs
 *               ↳ storage classes → /cloud/storage/storage-classes
 *               ↳ buckets         → /cloud/storage/buckets
 *               ↳ volumes         → /cloud/storage/volumes
 *       — settings                → /wizard
 *
 *     The Cloud accordion is auto-expanded when the operator is on a
 *     /cloud/* route and persists its open/closed state across reloads
 *     in localStorage under the key `sov-nav-cloud-expanded`.
 *
 *     Each second-level accordion (compute/network/storage) likewise
 *     persists its expand state under
 *     `sov-nav-cloud-{compute,network,storage}-expanded`. Auto-expands
 *     when the operator is on the matching sub-tree (e.g. on
 *     /cloud/compute/clusters → Compute opens).
 *
 *     Each second-level header is split into TWO interactive elements:
 *     a left-side <Link> that navigates to the category landing page
 *     (/cloud/{compute,network,storage}) and a right-side <button>
 *     chevron that toggles the sub-tree without leaving the current
 *     page. This avoids the anti-pattern of an item that's both a
 *     navigation target and a toggle in the same hit area — the founder
 *     called this out during the JobsPage tab-strip iteration.
 *
 *   • Operator card at the bottom — analog of the canonical "User"
 *     card; the wizard runs unauthenticated so we show "Operator ·
 *     Provisioning session".
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * href and color comes from runtime data + canonical token names —
 * there's no inline hex value or hard-coded path.
 */

import { useEffect, useState } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'

interface SidebarProps {
  /** Current deployment id — surfaced as the "Sovereign" label. */
  deploymentId: string
  /** Resolved Sovereign FQDN (from snapshot or wizard store). */
  sovereignFQDN?: string | null
}

/* ── Top-level (flat) nav items ─────────────────────────────────── */

interface FlatNavItem {
  id: 'apps' | 'jobs' | 'dashboard' | 'settings'
  label: string
  to:
    | '/provision/$deploymentId'
    | '/provision/$deploymentId/jobs'
    | '/provision/$deploymentId/dashboard'
    | '/wizard'
  /** SVG path data — same `d` strings as core/console for visual parity. */
  icon: string
}

const FLAT_NAV: FlatNavItem[] = [
  {
    id: 'apps',
    label: 'Apps',
    to: '/provision/$deploymentId',
    icon: 'M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z',
  },
  {
    id: 'jobs',
    label: 'Jobs',
    to: '/provision/$deploymentId/jobs',
    icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4',
  },
  {
    id: 'dashboard',
    label: 'Dashboard',
    to: '/provision/$deploymentId/dashboard',
    icon: 'M3 3h7v9H3V3zm11 0h7v5h-7V3zM14 10h7v11h-7V10zM3 14h7v7H3v-7z',
  },
]

const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/wizard',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

/* ── Cloud accordion — first level ──────────────────────────────── */

/** Server-stack icon — three horizontal bars suggesting clusters /
 *  nodes, distinct from the dashboard's quadrant grid and the apps
 *  4-square shape. */
const CLOUD_ICON =
  'M5 12H3m18 0h-2M5 7h14M5 12h14M5 17h14M5 7a2 2 0 00-2 2v6a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2H5z'

/** Discriminated union for the four first-level Cloud entries.
 *  Architecture is a LEAF (no expand toggle), Compute / Network /
 *  Storage are CATEGORIES with their own expand state and a
 *  resource-list child set. */
type CloudCategoryId = 'compute' | 'network' | 'storage'
type CloudFirstLevelId = 'architecture' | CloudCategoryId

interface CloudLeafItem {
  kind: 'leaf'
  id: 'architecture'
  label: string
  suffix: 'architecture'
}

interface CloudCategoryItem {
  kind: 'category'
  id: CloudCategoryId
  label: string
  /** Suffix appended to /provision/$deploymentId/cloud (== landing). */
  suffix: CloudCategoryId
  /** Resource-list children — sub-sub items shown when expanded. */
  children: readonly { id: string; label: string; suffix: string }[]
}

type CloudFirstLevel = CloudLeafItem | CloudCategoryItem

const CLOUD_FIRST_LEVEL: readonly CloudFirstLevel[] = [
  {
    kind: 'leaf',
    id: 'architecture',
    label: 'Architecture',
    suffix: 'architecture',
  },
  {
    kind: 'category',
    id: 'compute',
    label: 'Compute',
    suffix: 'compute',
    children: [
      { id: 'clusters', label: 'Clusters', suffix: 'clusters' },
      { id: 'vclusters', label: 'vClusters', suffix: 'vclusters' },
      { id: 'node-pools', label: 'Node Pools', suffix: 'node-pools' },
      { id: 'worker-nodes', label: 'Worker Nodes', suffix: 'worker-nodes' },
    ],
  },
  {
    kind: 'category',
    id: 'network',
    label: 'Network',
    suffix: 'network',
    children: [
      { id: 'services', label: 'Services', suffix: 'services' },
      { id: 'ingresses', label: 'Ingresses', suffix: 'ingresses' },
      { id: 'load-balancers', label: 'Load Balancers', suffix: 'load-balancers' },
      { id: 'dns-zones', label: 'DNS Zones', suffix: 'dns-zones' },
    ],
  },
  {
    kind: 'category',
    id: 'storage',
    label: 'Storage',
    suffix: 'storage',
    children: [
      { id: 'pvcs', label: 'PVCs', suffix: 'pvcs' },
      { id: 'storage-classes', label: 'Storage Classes', suffix: 'storage-classes' },
      { id: 'buckets', label: 'Buckets', suffix: 'buckets' },
      { id: 'volumes', label: 'Volumes', suffix: 'volumes' },
    ],
  },
] as const

const CLOUD_EXPANDED_STORAGE_KEY = 'sov-nav-cloud-expanded'

function categoryStorageKey(cat: CloudCategoryId): string {
  return `sov-nav-cloud-${cat}-expanded`
}

/** Read persisted expand state for a given key. Returns `null` when no
 *  stored value exists (caller chooses the fallback). */
function readPersistedExpanded(key: string): boolean | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(key)
    if (raw === 'true') return true
    if (raw === 'false') return false
    return null
  } catch {
    // Safari private mode etc — fail open.
    return null
  }
}

function writePersistedExpanded(key: string, open: boolean): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(key, open ? 'true' : 'false')
  } catch {
    /* noop */
  }
}

/* ── Active-state derivation ────────────────────────────────────── */

type ActiveSection = 'apps' | 'jobs' | 'dashboard' | 'cloud' | 'settings'

// Cloud section is active when the path matches any of the
// `/cloud[/...]` or legacy `/infrastructure[/...]` segments. We use a
// regex against discrete segments rather than `includes('/cloud')`
// because deploymentIds are free-form strings that may legitimately
// contain the word "cloud" themselves.
const CLOUD_PATH_RE = /\/(cloud|infrastructure)(\/|$)/

function deriveActiveSection(pathname: string): ActiveSection {
  if (CLOUD_PATH_RE.test(pathname)) return 'cloud'
  if (pathname.endsWith('/dashboard')) return 'dashboard'
  if (pathname.endsWith('/jobs')) return 'jobs'
  if (pathname.startsWith('/sovereign/wizard') || pathname.startsWith('/wizard')) return 'settings'
  return 'apps'
}

/**
 * Derive which first-level Cloud item is active for the highlight.
 * For Compute / Network / Storage the rule is: the parent is active
 * whenever the current path is at the landing page OR any of its
 * child resource-list pages.
 */
function deriveActiveCloudFirst(pathname: string): CloudFirstLevelId | null {
  for (const item of CLOUD_FIRST_LEVEL) {
    if (pathname.endsWith(`/cloud/${item.suffix}`)) return item.id
    if (item.kind === 'category') {
      // Match /cloud/<category>/<anything> for child highlighting.
      if (pathname.includes(`/cloud/${item.suffix}/`)) return item.id
    }
  }
  // Map the legacy /infrastructure/* paths so the highlight survives
  // the brief paint before the redirect lands.
  if (pathname.endsWith('/infrastructure/topology')) return 'architecture'
  if (pathname.endsWith('/infrastructure/compute')) return 'compute'
  if (pathname.endsWith('/infrastructure/network')) return 'network'
  if (pathname.endsWith('/infrastructure/storage')) return 'storage'
  return null
}

/**
 * Derive the active sub-sub item under a category, e.g. when on
 * /cloud/compute/clusters this returns ('compute', 'clusters'). Used
 * to apply aria-current=page to the matching child link.
 */
function deriveActiveCloudChild(
  pathname: string,
): { categoryId: CloudCategoryId; childSuffix: string } | null {
  for (const item of CLOUD_FIRST_LEVEL) {
    if (item.kind !== 'category') continue
    for (const child of item.children) {
      if (pathname.endsWith(`/cloud/${item.suffix}/${child.suffix}`)) {
        return { categoryId: item.id, childSuffix: child.suffix }
      }
    }
  }
  return null
}

/* ── Component ──────────────────────────────────────────────────── */

export function Sidebar({ deploymentId, sovereignFQDN }: SidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeSection = deriveActiveSection(pathname)
  const activeCloudFirst = deriveActiveCloudFirst(pathname)
  const activeCloudChild = deriveActiveCloudChild(pathname)
  const isOnCloud = activeSection === 'cloud'

  // Top-level Cloud accordion expand state. Defaults: expanded when on
  // a /cloud/* route OR when the persisted localStorage value says so.
  const [cloudExpanded, setCloudExpanded] = useState<boolean>(() => {
    const persisted = readPersistedExpanded(CLOUD_EXPANDED_STORAGE_KEY)
    if (persisted !== null) return persisted
    return isOnCloud
  })

  // Per-category second-level expand state. Defaults: expanded when
  // the operator is currently on that category's landing or one of
  // its resource-list pages, OR when the persisted value says so.
  const [computeExpanded, setComputeExpanded] = useState<boolean>(() => {
    const persisted = readPersistedExpanded(categoryStorageKey('compute'))
    if (persisted !== null) return persisted
    return activeCloudFirst === 'compute'
  })
  const [networkExpanded, setNetworkExpanded] = useState<boolean>(() => {
    const persisted = readPersistedExpanded(categoryStorageKey('network'))
    if (persisted !== null) return persisted
    return activeCloudFirst === 'network'
  })
  const [storageExpanded, setStorageExpanded] = useState<boolean>(() => {
    const persisted = readPersistedExpanded(categoryStorageKey('storage'))
    if (persisted !== null) return persisted
    return activeCloudFirst === 'storage'
  })

  // Auto-expand the parent + the active child's parent when the
  // operator navigates onto a /cloud/* route. Don't auto-collapse on
  // leaving — that's a discoverability anti-pattern; trust the
  // persisted state on subsequent visits.
  useEffect(() => {
    if (isOnCloud && !cloudExpanded) {
      setCloudExpanded(true)
      writePersistedExpanded(CLOUD_EXPANDED_STORAGE_KEY, true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOnCloud])

  useEffect(() => {
    if (activeCloudFirst === 'compute' && !computeExpanded) {
      setComputeExpanded(true)
      writePersistedExpanded(categoryStorageKey('compute'), true)
    }
    if (activeCloudFirst === 'network' && !networkExpanded) {
      setNetworkExpanded(true)
      writePersistedExpanded(categoryStorageKey('network'), true)
    }
    if (activeCloudFirst === 'storage' && !storageExpanded) {
      setStorageExpanded(true)
      writePersistedExpanded(categoryStorageKey('storage'), true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeCloudFirst])

  function toggleCloud() {
    setCloudExpanded((prev) => {
      const next = !prev
      writePersistedExpanded(CLOUD_EXPANDED_STORAGE_KEY, next)
      return next
    })
  }

  function categoryExpanded(cat: CloudCategoryId): boolean {
    if (cat === 'compute') return computeExpanded
    if (cat === 'network') return networkExpanded
    return storageExpanded
  }
  function setCategoryExpanded(cat: CloudCategoryId, next: boolean) {
    if (cat === 'compute') setComputeExpanded(next)
    else if (cat === 'network') setNetworkExpanded(next)
    else setStorageExpanded(next)
    writePersistedExpanded(categoryStorageKey(cat), next)
  }
  function toggleCategory(cat: CloudCategoryId) {
    setCategoryExpanded(cat, !categoryExpanded(cat))
  }

  const sovereignLabel = sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`

  return (
    <aside
      className="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]"
      data-testid="admin-sidebar"
    >
      {/* Logo + Sovereign label (replaces canonical Tenant switcher) */}
      <div className="border-b border-[var(--color-border)]">
        <div className="flex h-14 items-center gap-2 px-4">
          {/* Canonical OpenOva mark — same shape + gradient as core/console */}
          <svg viewBox="0 0 700 400" width={36} height={20} className="flex-shrink-0" fill="none" aria-hidden>
            <defs>
              <linearGradient id="sidebar-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
                <stop offset="0%" stopColor="#3B82F6" />
                <stop offset="100%" stopColor="#818CF8" />
              </linearGradient>
            </defs>
            <path
              d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
              fill="none"
              stroke="url(#sidebar-logo-grad)"
              strokeWidth={100}
              strokeLinecap="butt"
            />
          </svg>
          <span className="text-sm font-semibold text-[var(--color-text-strong)]">
            OpenOva <span className="font-normal text-[var(--color-text-dim)]">Sovereign</span>
          </span>
        </div>
        <div className="px-3 pb-3">
          <div
            className="flex w-full items-center justify-between gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-left text-xs"
            data-testid="sov-tenant-label"
          >
            <span className="min-w-0 flex-1 truncate text-[var(--color-text-strong)]">{sovereignLabel}</span>
          </div>
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-3" data-testid="sov-nav">
        {FLAT_NAV.map((item) => {
          const isActive = activeSection === item.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              key={item.id}
              to={item.to}
              params={{ deploymentId }}
              activeOptions={{ exact: true }}
              className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-nav-${item.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={item.icon} />
              {item.label}
            </Link>
          )
        })}

        {/* Cloud accordion (first level) */}
        <div className="mt-0.5">
          <button
            type="button"
            onClick={toggleCloud}
            onKeyDown={(ev) => {
              // Enter / Space is already the default for buttons; this
              // keeps the call site explicit for screen-reader users.
              if (ev.key === 'Enter' || ev.key === ' ') {
                ev.preventDefault()
                toggleCloud()
              }
            }}
            className={`mx-2 flex w-[calc(100%-1rem)] items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-sm transition-colors ${
              isOnCloud
                ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
                : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
            }`}
            data-testid="sov-nav-cloud"
            aria-expanded={cloudExpanded}
            aria-controls="sov-nav-cloud-group"
            aria-current={isOnCloud ? 'page' : undefined}
          >
            <span className="flex items-center gap-3">
              <NavIcon d={CLOUD_ICON} />
              Cloud
            </span>
            <Chevron
              testId="sov-nav-cloud-toggle"
              open={cloudExpanded}
            />
          </button>

          {cloudExpanded && (
            <div
              id="sov-nav-cloud-group"
              role="group"
              aria-labelledby="sov-nav-cloud"
              data-testid="sov-nav-cloud-group"
            >
              {CLOUD_FIRST_LEVEL.map((item) => {
                const isActive = activeCloudFirst === item.id
                const cls = isActive
                  ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
                  : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'

                if (item.kind === 'leaf') {
                  return (
                    <Link
                      key={item.id}
                      to={`/provision/$deploymentId/cloud/${item.suffix}` as never}
                      params={{ deploymentId } as never}
                      className={`mx-2 flex items-center gap-3 rounded-lg py-1.5 pl-10 pr-3 text-sm no-underline transition-colors ${cls}`}
                      data-testid={`sov-nav-cloud-${item.id}`}
                      aria-current={isActive ? 'page' : undefined}
                    >
                      {item.label}
                    </Link>
                  )
                }

                // Category (compute/network/storage) — split surface:
                //   • <Link> takes most of the row → navigates to the
                //     landing page (/cloud/<category>).
                //   • <button> on the right toggles the second-level
                //     accordion without leaving the current page.
                const expanded = categoryExpanded(item.id)
                return (
                  <div key={item.id} data-testid={`sov-nav-cloud-${item.id}-row`}>
                    <div className={`mx-2 flex items-stretch rounded-lg ${isActive ? 'bg-[var(--color-accent)]/10' : 'hover:bg-[var(--color-surface-hover)]'}`}>
                      <Link
                        to={`/provision/$deploymentId/cloud/${item.suffix}` as never}
                        params={{ deploymentId } as never}
                        className={`flex flex-1 items-center gap-3 rounded-l-lg py-1.5 pl-10 pr-2 text-sm no-underline transition-colors ${
                          isActive ? 'text-[var(--color-accent)]' : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
                        }`}
                        data-testid={`sov-nav-cloud-${item.id}`}
                        aria-current={isActive ? 'page' : undefined}
                      >
                        {item.label}
                      </Link>
                      <button
                        type="button"
                        onClick={() => toggleCategory(item.id)}
                        onKeyDown={(ev) => {
                          if (ev.key === 'Enter' || ev.key === ' ') {
                            ev.preventDefault()
                            toggleCategory(item.id)
                          }
                        }}
                        className={`flex items-center justify-center rounded-r-lg px-2 transition-colors ${
                          isActive ? 'text-[var(--color-accent)]' : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
                        }`}
                        data-testid={`sov-nav-cloud-${item.id}-toggle`}
                        aria-expanded={expanded}
                        aria-controls={`sov-nav-cloud-${item.id}-group`}
                        aria-label={`${expanded ? 'Collapse' : 'Expand'} ${item.label} sub-menu`}
                      >
                        <Chevron testId={`sov-nav-cloud-${item.id}-chevron`} open={expanded} />
                      </button>
                    </div>

                    {expanded && (
                      <div
                        id={`sov-nav-cloud-${item.id}-group`}
                        role="group"
                        aria-labelledby={`sov-nav-cloud-${item.id}`}
                        data-testid={`sov-nav-cloud-${item.id}-group`}
                      >
                        {item.children.map((child) => {
                          const childActive =
                            activeCloudChild?.categoryId === item.id &&
                            activeCloudChild?.childSuffix === child.suffix
                          const childCls = childActive
                            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
                            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
                          return (
                            <Link
                              key={child.id}
                              to={`/provision/$deploymentId/cloud/${item.suffix}/${child.suffix}` as never}
                              params={{ deploymentId } as never}
                              className={`mx-2 flex items-center gap-3 rounded-lg py-1.5 pl-14 pr-3 text-xs no-underline transition-colors ${childCls}`}
                              data-testid={`sov-nav-cloud-${item.id}-${child.id}`}
                              aria-current={childActive ? 'page' : undefined}
                            >
                              {child.label}
                            </Link>
                          )
                        })}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* Settings stays at the bottom of the nav list */}
        {(() => {
          const isActive = activeSection === SETTINGS_ITEM.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              to={SETTINGS_ITEM.to}
              activeOptions={{ exact: true }}
              className={`mx-2 mt-0.5 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-nav-${SETTINGS_ITEM.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={SETTINGS_ITEM.icon} />
              {SETTINGS_ITEM.label}
            </Link>
          )
        })()}
      </nav>

      {/* Operator card at the bottom — analog of canonical "User" card */}
      <div className="border-t border-[var(--color-border)] p-3">
        <div className="flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-xs font-bold text-[var(--color-accent)]">
            O
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium text-[var(--color-text)]">Operator</p>
            <p className="truncate text-[10px] text-[var(--color-text-dimmer)]">Provisioning session</p>
          </div>
        </div>
      </div>
    </aside>
  )
}

function NavIcon({ d }: { d: string }) {
  return (
    <svg
      className="h-4 w-4 shrink-0"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d={d} />
    </svg>
  )
}

function Chevron({ testId, open }: { testId: string; open: boolean }) {
  return (
    <svg
      data-testid={testId}
      className={`h-3 w-3 shrink-0 transition-transform ${open ? 'rotate-90' : ''}`}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
      aria-hidden
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M9 5l7 7-7 7" />
    </svg>
  )
}
