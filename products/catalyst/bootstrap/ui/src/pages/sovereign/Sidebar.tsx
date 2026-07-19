/**
 * Sidebar — Sovereign-portal left rail. Pixel-port of
 * core/console/src/components/Sidebar.svelte.
 *
 * Layout contract (issue #350 — drop accordion):
 *   • Fixed left rail, w-56, full height
 *   • Logo + product wordmark in the 56px header
 *   • Single-Sovereign label in place of the canonical Org
 *     switcher (the wizard is single-Sovereign by definition).
 *   • Nav list — flat single-level destinations only:
 *
 *       — apps                    → /sovereign/provision/$deploymentId
 *       — jobs                    → /sovereign/provision/$deploymentId/jobs
 *       — dashboard               → /sovereign/provision/$deploymentId/dashboard
 *       — cloud                   → /sovereign/provision/$deploymentId/cloud
 *       — users                   → /sovereign/provision/$deploymentId/users
 *       — settings                → /sovereign/provision/$deploymentId/settings
 *
 *     The Cloud entry is a single flat <Link> (no accordion, no
 *     chevron, no sub-items). The parent CloudPage owns the in-page
 *     graph/list view toggle, the resource-kind switcher, and the
 *     fullscreen affordance — see CloudPage.tsx.
 *
 *     Cloud is highlighted as active whenever the operator is on any
 *     /cloud/* route OR any legacy /infrastructure/* route (covered
 *     by the redirect-only routes in app/router.tsx).
 *
 * Issue #750: the bespoke bottom-left "Operator / Provisioning session"
 * card was removed. Identity placement is now consistent with the rest
 * of the application — the signed-in user's email-initial avatar +
 * sign-out menu live in the top-right of the PortalShell header via
 * <ProfileMenu />, mirroring the wizard + Sovereign-console surfaces.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * href and color comes from runtime data + canonical token names —
 * there's no inline hex value or hard-coded path.
 */

import { Link, useRouterState } from '@tanstack/react-router'

interface SidebarProps {
  /** Current deployment id — surfaced as the "Sovereign" label. */
  deploymentId: string
  /** Resolved Sovereign FQDN (from snapshot or wizard store). */
  sovereignFQDN?: string | null
}

/* ── Top-level (flat) nav items ─────────────────────────────────── */

interface FlatNavItem {
  id: 'apps' | 'catalog' | 'jobs' | 'dashboard' | 'cloud' | 'users' | 'settings'
  label: string
  to:
    | '/provision/$deploymentId'
    | '/provision/$deploymentId/catalog'
    | '/provision/$deploymentId/jobs'
    | '/provision/$deploymentId/dashboard'
    | '/provision/$deploymentId/cloud'
    | '/provision/$deploymentId/users'
    | '/provision/$deploymentId/settings'
  /** SVG path data — same `d` strings as core/console for visual parity. */
  icon: string
}

/**
 * Cloud icon — verbatim Tabler `IconCloud` path data (issue #350
 * item 8). Lifted from @tabler/icons-react v3.41.1 IconCloud.mjs so
 * the Sidebar's lightweight inline-SVG NavIcon renders the same shape
 * the rest of the catalyst-ui uses for cloud affordances. Renders at
 * the same 24x24 viewBox the other NavIcon paths use.
 */
const CLOUD_ICON =
  'M6.657 18c-2.572 0 -4.657 -2.007 -4.657 -4.483c0 -2.475 2.085 -4.482 4.657 -4.482c.393 -1.762 1.794 -3.2 3.675 -3.773c1.88 -.572 3.956 -.193 5.444 1c1.488 1.19 2.162 3.007 1.77 4.769h.99c1.913 0 3.464 1.56 3.464 3.486c0 1.927 -1.551 3.487 -3.465 3.487h-11.878'

const FLAT_NAV: FlatNavItem[] = [
  {
    id: 'apps',
    label: 'Apps',
    to: '/provision/$deploymentId',
    icon: 'M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z',
  },
  // Catalog (#3601) — the catalog grid as its own page in the provision
  // tree (parity with the chroot SovereignSidebar Catalog entry).
  {
    id: 'catalog',
    label: 'Catalog',
    to: '/provision/$deploymentId/catalog',
    icon: 'M4 7a2 2 0 012-2h8a2 2 0 012 2v10a2 2 0 01-2 2H6a2 2 0 01-2-2V7zm14 1a2 2 0 012 2v7a2 2 0 01-2 2M8 9h6M8 13h6',
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
  {
    id: 'cloud',
    label: 'Cloud',
    to: '/provision/$deploymentId/cloud',
    icon: CLOUD_ICON,
  },
  // #3958 — the standalone Reconciliation entry is GONE; reconcilers now
  // merge into the unified Cloud graph (the Cloud entry above).
  {
    id: 'users',
    label: 'Users',
    to: '/provision/$deploymentId/users',
    // Tabler IconUsers — verbatim path data, viewBox 24x24.
    icon: 'M9 7a4 4 0 100 8 4 4 0 000-8zM3 21v-2a4 4 0 014-4h4a4 4 0 014 4v2M16 3.13a4 4 0 010 7.75M21 21v-2a4 4 0 00-3-3.87',
  },
]

const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/provision/$deploymentId/settings',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

/* ── Active-state derivation ────────────────────────────────────── */

type ActiveSection = 'apps' | 'catalog' | 'jobs' | 'dashboard' | 'cloud' | 'users' | 'settings'

// Cloud section is active when the path matches any of the
// `/cloud[/...]` or legacy `/infrastructure[/...]` segments. We use a
// regex against discrete segments rather than `includes('/cloud')`
// because deploymentIds are free-form strings that may legitimately
// contain the word "cloud" themselves.
const CLOUD_PATH_RE = /\/(cloud|infrastructure)(\/|$)/

function deriveActiveSection(pathname: string): ActiveSection {
  if (CLOUD_PATH_RE.test(pathname)) return 'cloud'
  if (pathname.endsWith('/dashboard')) return 'dashboard'
  // Catalog (#3601) — /catalog and /catalog/<blueprint> both count.
  if (/\/catalog(\/|$)/.test(pathname)) return 'catalog'
  if (pathname.endsWith('/jobs')) return 'jobs'
  // Users surface — list, /new, and /<name> all count.
  if (/\/users(\/|$)/.test(pathname)) return 'users'
  // Settings — deployment-scoped settings page (issue #516). The
  // legacy `/wizard` divert was the bug being fixed.
  if (/\/settings(\/|$)/.test(pathname)) return 'settings'
  return 'apps'
}

/* ── Component ──────────────────────────────────────────────────── */

export function Sidebar({ deploymentId, sovereignFQDN }: SidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeSection = deriveActiveSection(pathname)

  const sovereignLabel = sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`

  return (
    <aside
      className="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]"
      data-testid="admin-sidebar"
    >
      {/* Logo + Sovereign label (replaces canonical Org switcher) */}
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
            data-testid="sov-org-label"
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
          // For "Cloud" we want the highlight to apply on any /cloud/*
          // path, not just exact match — so we omit `activeOptions`
          // (default behavior matches by prefix). For the others the
          // exact-match guard prevents visual drift when the operator
          // is on a deeper sub-page.
          return (
            <Link
              key={item.id}
              to={item.to as never}
              params={{ deploymentId } as never}
              activeOptions={item.id === 'cloud' ? undefined : { exact: true }}
              className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-nav-${item.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={item.icon} />
              {item.label}
            </Link>
          )
        })}

        {/* Settings stays at the bottom of the nav list */}
        {(() => {
          const isActive = activeSection === SETTINGS_ITEM.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              to={SETTINGS_ITEM.to as never}
              params={{ deploymentId } as never}
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

      {/*
        Issue #750: the bespoke "Operator / Provisioning session" card
        used to live here. It was removed because:
          1. Identity belongs top-right (matches WizardLayout +
             SovereignConsoleLayout + marketplace header — this surface
             was the lone outlier).
          2. The label "Operator" was hard-coded and never reflected the
             actual signed-in user's email; the canonical <ProfileMenu />
             reads useSession() and renders the email-initial avatar.
        The replacement now lives in PortalShell.tsx's top-right slot.
      */}
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
