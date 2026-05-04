/**
 * SovereignSidebar — left rail for the Sovereign Console.
 *
 * Analogous to Sidebar.tsx (which is tied to the Catalyst-Zero
 * /provision/$deploymentId/* route tree), but in Sovereign mode:
 *
 *   - Routes use the /console/* prefix (no deploymentId param) — the
 *     Sovereign is implicit from the hostname.
 *   - The tenant label shows the Sovereign FQDN.
 *   - The footer card shows the authenticated user's name (from
 *     OIDC tokens), not the generic "Operator" placeholder.
 *
 * Nav items mirror Sidebar.tsx exactly — same icons, same order:
 *   Apps | Jobs | Dashboard | Cloud | Users | Settings
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), all labels /
 * icons / route paths live in named constants, not in inline literals.
 *
 * Related: GitHub issue #607
 */

import { Link, useRouterState } from '@tanstack/react-router'
import { loadTokens, parseJWTClaims } from '@/shared/lib/oidc'

interface SovereignSidebarProps {
  /** Sovereign FQDN derived from window.location.hostname. */
  sovereignFQDN: string
}

// ── Cloud icon (verbatim Tabler IconCloud — same as Sidebar.tsx) ─────────────
const CLOUD_ICON =
  'M6.657 18c-2.572 0 -4.657 -2.007 -4.657 -4.483c0 -2.475 2.085 -4.482 4.657 -4.482c.393 -1.762 1.794 -3.2 3.675 -3.773c1.88 -.572 3.956 -.193 5.444 1c1.488 1.19 2.162 3.007 1.77 4.769h.99c1.913 0 3.464 1.56 3.464 3.486c0 1.927 -1.551 3.487 -3.465 3.487h-11.878'

interface FlatNavItem {
  id: 'apps' | 'jobs' | 'dashboard' | 'cloud' | 'users' | 'catalog' | 'settings'
  label: string
  to: string
  icon: string
}

const FLAT_NAV: FlatNavItem[] = [
  {
    id: 'apps',
    label: 'Apps',
    to: '/console/apps',
    icon: 'M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z',
  },
  {
    id: 'jobs',
    label: 'Jobs',
    to: '/console/jobs',
    icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4',
  },
  {
    id: 'dashboard',
    label: 'Dashboard',
    to: '/console/dashboard',
    icon: 'M3 3h7v9H3V3zm11 0h7v5h-7V3zM14 10h7v11h-7V10zM3 14h7v7H3v-7z',
  },
  {
    id: 'cloud',
    label: 'Cloud',
    to: '/console/cloud',
    icon: CLOUD_ICON,
  },
  {
    id: 'users',
    label: 'Users',
    to: '/console/users',
    icon: 'M9 7a4 4 0 100 8 4 4 0 000-8zM3 21v-2a4 4 0 014-4h4a4 4 0 014 4v2M16 3.13a4 4 0 010 7.75M21 21v-2a4 4 0 00-3-3.87',
  },
  {
    // Catalog — Sovereign-console operator surface for marketplace
    // publishing toggles (issue #710 wave 2.5).
    id: 'catalog',
    label: 'Catalog',
    to: '/console/catalog',
    icon: 'M3 7v13h18V7M3 7l9-4 9 4M3 7h18M9 11v6M15 11v6',
  },
]

const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/console/settings',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

// ── Active-state derivation ───────────────────────────────────────────────────

type ActiveSection = 'apps' | 'jobs' | 'dashboard' | 'cloud' | 'users' | 'catalog' | 'settings'

const CLOUD_PATH_RE = /\/console\/(cloud|infrastructure)(\/|$)/

function deriveActiveSection(pathname: string): ActiveSection {
  if (CLOUD_PATH_RE.test(pathname)) return 'cloud'
  if (/\/console\/dashboard(\/|$)/.test(pathname)) return 'dashboard'
  if (/\/console\/jobs(\/|$)/.test(pathname)) return 'jobs'
  if (/\/console\/users(\/|$)/.test(pathname)) return 'users'
  if (/\/console\/catalog(\/|$)/.test(pathname)) return 'catalog'
  if (/\/console\/settings(\/|$)/.test(pathname)) return 'settings'
  return 'apps'
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SovereignSidebar({ sovereignFQDN }: SovereignSidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeSection = deriveActiveSection(pathname)

  // Read user info from the OIDC session for the footer card.
  const tokens = loadTokens()
  const claims = tokens ? parseJWTClaims(tokens.idToken) : {}
  const userName =
    (claims.name as string | undefined) ??
    (claims.preferred_username as string | undefined) ??
    (claims.email as string | undefined) ??
    'Tenant'
  const userInitials = userName
    .split(/\s+/)
    .map((w) => w[0]?.toUpperCase() ?? '')
    .slice(0, 2)
    .join('')

  return (
    <aside
      className="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]"
      data-testid="sov-console-sidebar"
    >
      {/* Logo + Sovereign label */}
      <div className="border-b border-[var(--color-border)]">
        <div className="flex h-14 items-center gap-2 px-4">
          <svg viewBox="0 0 700 400" width={36} height={20} className="flex-shrink-0" fill="none" aria-hidden>
            <defs>
              <linearGradient id="console-sidebar-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
                <stop offset="0%" stopColor="#3B82F6" />
                <stop offset="100%" stopColor="#818CF8" />
              </linearGradient>
            </defs>
            <path
              d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
              fill="none"
              stroke="url(#console-sidebar-logo-grad)"
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
            data-testid="sov-console-tenant-label"
          >
            <span className="min-w-0 flex-1 truncate text-[var(--color-text-strong)]">
              {sovereignFQDN}
            </span>
          </div>
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-3" data-testid="sov-console-nav">
        {FLAT_NAV.map((item) => {
          const isActive = activeSection === item.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              key={item.id}
              to={item.to as never}
              className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-console-nav-${item.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={item.icon} />
              {item.label}
            </Link>
          )
        })}

        {/* Settings at the bottom of the nav list */}
        {(() => {
          const isActive = activeSection === SETTINGS_ITEM.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              to={SETTINGS_ITEM.to as never}
              className={`mx-2 mt-0.5 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-console-nav-${SETTINGS_ITEM.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={SETTINGS_ITEM.icon} />
              {SETTINGS_ITEM.label}
            </Link>
          )
        })()}
      </nav>

      {/* User card at the bottom */}
      <div className="border-t border-[var(--color-border)] p-3">
        <div className="flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-xs font-bold text-[var(--color-accent)]">
            {userInitials || 'T'}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium text-[var(--color-text)]">{userName}</p>
            <p className="truncate text-[10px] text-[var(--color-text-dimmer)]">{sovereignFQDN}</p>
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
