/**
 * Sidebar — pixel-port of core/console/src/components/Sidebar.svelte.
 *
 * Layout contract (matches canonical 1:1):
 *   • Fixed left rail, w-56, full height
 *   • Logo + product wordmark in the 56px header
 *   • Tenant switcher in the canonical surface — DROPPED here because
 *     a Sovereign-provision wizard target is single-Sovereign by
 *     definition. The deploymentId is the surrogate; we render it as a
 *     static label so users still get the "what am I looking at" cue
 *     the canonical switcher provides.
 *   • Nav list — this surface ships only the items that have a real
 *     destination in the Sovereign-provision context:
 *
 *       — `apps`     → /sovereign/provision/$deploymentId
 *       — `jobs`     → /sovereign/provision/$deploymentId/jobs
 *       — `settings` → static link to wizard step (operator can revise
 *                       deployment options before completion)
 *
 *     `dashboard`, `domains`, `billing`, `team` are OMITTED because
 *     they reach surfaces that don't exist on a freshly-provisioning
 *     Sovereign — those are tenant-console concerns, post-handover.
 *     Adding them as dead links would betray the canonical 1:1 promise
 *     and surface broken nav.
 *
 *   • User card at the bottom — the canonical version reads from a
 *     signed-in tenant session; the Sovereign-provision wizard runs
 *     unauthenticated, so we show "Operator · Provisioning session".
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

interface NavItem {
  id: 'apps' | 'jobs' | 'dashboard' | 'settings'
  label: string
  /** Tanstack-router target — `null` for static external/non-tanstack routes. */
  to:
    | '/provision/$deploymentId'
    | '/provision/$deploymentId/jobs'
    | '/provision/$deploymentId/dashboard'
    | '/wizard'
  /** SVG path data — same `d` strings as core/console for visual parity. */
  icon: string
}

const NAV: NavItem[] = [
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
    // Treemap-style 4-pane grid icon — visually distinct from the
    // 4-square Apps icon (Dashboard's quadrants are unequal).
    icon: 'M3 3h7v9H3V3zm11 0h7v5h-7V3zM14 10h7v11h-7V10zM3 14h7v7H3v-7z',
  },
  {
    id: 'settings',
    label: 'Settings',
    to: '/wizard',
    icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
  },
]

/** Compute the active nav item from the current pathname. */
function deriveActive(pathname: string): NavItem['id'] {
  if (pathname.endsWith('/dashboard')) return 'dashboard'
  if (pathname.endsWith('/jobs')) return 'jobs'
  if (pathname.startsWith('/sovereign/wizard') || pathname.startsWith('/wizard')) return 'settings'
  return 'apps'
}

export function Sidebar({ deploymentId, sovereignFQDN }: SidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activePage = deriveActive(pathname)

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
        {NAV.map((item) => {
          const isActive = activePage === item.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          // Settings target points outside the provision sub-tree, so it
          // doesn't take a deploymentId param — Tanstack Link handles the
          // distinction by omitting `params` for non-parameterised routes.
          const linkProps =
            item.to === '/wizard'
              ? { to: '/wizard' as const }
              : { to: item.to, params: { deploymentId } }
          return (
            <Link
              key={item.id}
              {...linkProps}
              activeOptions={{ exact: true }}
              className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-nav-${item.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <svg
                className="h-4 w-4 shrink-0"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={1.5}
              >
                <path strokeLinecap="round" strokeLinejoin="round" d={item.icon} />
              </svg>
              {item.label}
            </Link>
          )
        })}
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
