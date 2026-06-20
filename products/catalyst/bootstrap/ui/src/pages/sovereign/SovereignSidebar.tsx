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
 * Nav items follow the operator mental model: overview → infra →
 * workloads → operations → access → commerce → config:
 *   Dashboard | Cloud | Apps | Jobs | Users | BSS | Settings
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), all labels /
 * icons / route paths live in named constants, not in inline literals.
 *
 * Related: GitHub issue #607
 */

import { useState } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { loadTokens, parseJWTClaims } from '@/shared/lib/oidc'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { getSidebarEntries, type SidebarEntry } from '@/lib/console-ui.api'

interface SovereignSidebarProps {
  /** Sovereign FQDN derived from window.location.hostname. */
  sovereignFQDN: string
}

// ── Cloud icon (verbatim Tabler IconCloud — same as Sidebar.tsx) ─────────────
const CLOUD_ICON =
  'M6.657 18c-2.572 0 -4.657 -2.007 -4.657 -4.483c0 -2.475 2.085 -4.482 4.657 -4.482c.393 -1.762 1.794 -3.2 3.675 -3.773c1.88 -.572 3.956 -.193 5.444 1c1.488 1.19 2.162 3.007 1.77 4.769h.99c1.913 0 3.464 1.56 3.464 3.486c0 1.927 -1.551 3.487 -3.465 3.487h-11.878'

interface FlatNavItem {
  id: 'apps' | 'catalog' | 'sandbox' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'settings'
  label: string
  to: string
  icon: string
}

// Wave 5 (2026-05-17, founder UX-polish review): order follows the
// operator mental model — overview first, then descend through the
// stack from infrastructure to operations to access to commerce.
// Settings stays pinned at the bottom (defined separately, rendered
// after the FLAT_NAV map below).
const FLAT_NAV: FlatNavItem[] = [
  {
    id: 'dashboard',
    label: 'Dashboard',
    to: '/dashboard',
    icon: 'M3 3h7v9H3V3zm11 0h7v5h-7V3zM14 10h7v11h-7V10zM3 14h7v7H3v-7z',
  },
  {
    id: 'cloud',
    label: 'Cloud',
    to: '/cloud',
    icon: CLOUD_ICON,
  },
  {
    id: 'apps',
    label: 'Apps',
    to: '/apps',
    icon: 'M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z',
  },
  // Catalog (#3601, EPIC #3597). Founder point 2 (2026-06-15): the
  // catalog is its OWN left-nav page now, no longer a tab on /apps.
  // `/catalog` → CatalogPage (the catalog grid). Sits right after Apps:
  // Apps is "what's installed", Catalog is "what you can install".
  //
  // Icon: a stacked-cards / catalog glyph (single-stroke, matching the
  // icon family used by the other entries).
  {
    id: 'catalog',
    label: 'Catalog',
    to: '/catalog',
    icon: 'M4 7a2 2 0 012-2h8a2 2 0 012 2v10a2 2 0 01-2 2H6a2 2 0 01-2-2V7zm14 1a2 2 0 012 2v7a2 2 0 01-2 2M8 9h6M8 13h6',
  },
  // Sandbox (Wave 3, 2026-05-18 — issue: sandbox-wave3-ui-scaffold).
  // Per-Org agent-coding workspace — each session runs a chosen agent
  // CLI (claude/cursor-agent/aider/qwen/opencode/little-coder) in a pod
  // inside the user's vcluster; the in-pod pty-server pipes ANSI to
  // xterm.js in the browser. See products/sandbox/docs/architecture.md
  // §1. Sits between Apps (workloads catalogue) and Jobs (operations)
  // because it shares the "what's running" mental model.
  //
  // Icon: terminal/monitor single-stroke SVG matching the icon family
  // used by Cloud (Tabler cloud) / Apps (grid) / Jobs (clipboard).
  {
    id: 'sandbox',
    label: 'Sandbox',
    to: '/sandbox',
    icon: 'M3 4a1 1 0 011-1h16a1 1 0 011 1v12a1 1 0 01-1 1H4a1 1 0 01-1-1V4zm5 5l3 3-3 3m5 0h4M8 21h8',
  },
  {
    id: 'jobs',
    label: 'Jobs',
    to: '/jobs',
    icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4',
  },
  // #3958 — the standalone Reconciliation entry is GONE; reconcilers now
  // merge into the unified Cloud graph (the Cloud entry).
  // Compliance (Wave 5.62a, Refs #2318 / #1096): expose the existing
  // SRE / SecLead compliance dashboards (mounted at /sre/compliance +
  // /sec/compliance in router.tsx) via a single Sidebar entry. Lands
  // on the SRE dashboard by default; the page links to the SecLead
  // dashboard + policy drill-downs internally. Backend pipeline is
  // already shipped (20 baseline Kyverno policies in bp-kyverno-
  // policies, 6 in Enforce post-Wave-5.53, SSE stream from catalyst-
  // api at /api/v1/compliance/stream).
  //
  // Icon: shield with checkmark — fits the single-stroke family.
  {
    id: 'compliance',
    label: 'Compliance',
    to: '/sre/compliance',
    icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z',
  },
  {
    id: 'users',
    label: 'Users',
    to: '/users',
    icon: 'M9 7a4 4 0 100 8 4 4 0 000-8zM3 21v-2a4 4 0 014-4h4a4 4 0 014 4v2M16 3.13a4 4 0 010 7.75M21 21v-2a4 4 0 00-3-3.87',
  },
  // Organizations (issue #3378, founder-agreed model 2026-06-13). ONE
  // menu replacing BSS and the never-built OSS: the parent org's complete
  // view of everything beneath it — the org directory (parent first),
  // entering any sub-org for support (audited impersonation), the
  // commerce catalog, mode-aware billing, and the domain pools. Replaces
  // the BSS entry that previously lived here; the legacy /bss* + /sme/* +
  // /parent-domains URLs redirect into /organizations (router.tsx).
  //
  // Icon: an org-chart / building line-glyph (nodes + connecting edges)
  // matching the single-stroke icon family used by the other entries.
  //
  // RBAC: always visible — the sovereign-admin owns the directory; the
  // catalyst-api enforces tier-bound access server-side on every
  // /api/v1/sme/* and /catalog/admin/* call.
  {
    id: 'organizations',
    label: 'Organizations',
    to: '/organizations',
    icon: 'M9 3a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V3zM3 17a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H5a2 2 0 01-2-2v-2zm12 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2zM12 7v4M12 11H6v4m6-4h6v4',
  },
]

const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/settings',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

// ── Settings sub-nav ──────────────────────────────────────────────────────────
//
// Wave 5 (2026-05-17, founder UX-polish review): Marketplace moved off
// the sub-nav and INTO SettingsPage as a `<SectionCard id="marketplace">`
// anchor section (same pattern as #dns, #sovereign, #notifications).
// Founder: *"if market place is just a toggle etting under setting it
// dosnt need tohave a sdicated page and it doesnt need to have child
// left pane menu item"*. The dedicated /settings/marketplace route was
// retired in the same PR.
//
// Parent Domains remains a sub-nav child for now — it's a substantial
// admin surface (registrar tokens, DNS propagation panels, "+ Add
// another parent domain" modal), not a single toggle, so the sub-page
// model still fits.
interface SubNavItem {
  id: string
  label: string
  to: string
}

const SETTINGS_SUB_NAV: SubNavItem[] = [
  // Parent Domains — admin "Add another parent domain" + DNS propagation
  // status panel (issue #829). Lives under Settings so the sidebar
  // surface stays compact for the typical SME tenant who never sees
  // this surface; operator-admins reach it via /console/parent-domains
  // directly from the welcome email or by clicking through Settings.
  { id: 'parent-domains', label: 'Parent Domains', to: '/parent-domains' },
]

// ── Active-state derivation ───────────────────────────────────────────────────

type ActiveSection = 'apps' | 'catalog' | 'sandbox' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'settings'

const CLOUD_PATH_RE = /^\/(cloud|infrastructure)(\/|$)/

function deriveActiveSection(pathname: string): ActiveSection {
  if (CLOUD_PATH_RE.test(pathname)) return 'cloud'
  if (/^\/dashboard(\/|$)/.test(pathname)) return 'dashboard'
  // /catalog(/*) → 'catalog' (#3601) so the Catalog nav item highlights
  // for the catalog grid AND the per-Blueprint class page
  // (/catalog/$blueprintName → CatalogDetail).
  if (/^\/catalog(\/|$)/.test(pathname)) return 'catalog'
  // /sandbox(/*) → 'sandbox' so the nav highlight covers the landing,
  // /sandbox/$id, and /sandbox/settings (Wave 3).
  if (/^\/sandbox(\/|$)/.test(pathname)) return 'sandbox'
  if (/^\/jobs(\/|$)/.test(pathname)) return 'jobs'
  // /sre/compliance + /sec/compliance + /compliance/* all highlight
  // the Compliance nav entry (Wave 5.62a, Refs #2318 / #1096). The
  // entry's to: '/sre/compliance' is the SRE dashboard landing; the
  // page links onward to SecLead and PolicyDrilldown.
  if (/^\/(sre|sec)\/compliance(\/|$)/.test(pathname)) return 'compliance'
  if (/^\/compliance(\/|$)/.test(pathname)) return 'compliance'
  if (/^\/users(\/|$)/.test(pathname)) return 'users'
  // /organizations(/*) → 'organizations' so the Organizations nav item
  // highlights for the directory, the internal door, and every moved
  // sub-surface (billing/orders/revenue/vouchers/domains). Issue #3378.
  if (/^\/organizations(\/|$)/.test(pathname)) return 'organizations'
  // /settings/* OR /parent-domains → 'settings' so the Settings nav
  // item highlights and the sub-nav (Marketplace + Parent Domains)
  // expands. Per inviolable principle #4, the path list is pulled
  // from SETTINGS_SUB_NAV rather than re-typed here.
  if (/^\/settings(\/|$)/.test(pathname)) return 'settings'
  if (SETTINGS_SUB_NAV.some((s) => pathname.startsWith(s.to))) return 'settings'
  return 'apps'
}

// deriveActiveSettingsSubItem returns the id of the active settings
// sub-nav entry, or null when no sub-page is active. Drives the
// expanded sub-list rendering under the Settings nav item.
function deriveActiveSettingsSubItem(pathname: string): string | null {
  for (const sub of SETTINGS_SUB_NAV) {
    if (pathname.startsWith(sub.to)) return sub.id
  }
  return null
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SovereignSidebar({ sovereignFQDN }: SovereignSidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeSection = deriveActiveSection(pathname)
  const activeSettingsSub = deriveActiveSettingsSubItem(pathname)
  const settingsExpanded = activeSection === 'settings'

  // Wave 5.69c (#2396) — dynamic sidebar entries from installed
  // Blueprints' spec.consoleUI.sidebarEntry (Wave 5.69 CRD + Wave
  // 5.69b /console-ui/sidebar-entries handler). Splices into the
  // FLAT_NAV render path between hardcoded BSS (order=60) and the
  // pinned Settings entry. Graceful degradation: empty list on
  // 404/502/network-error leaves the hardcoded nav untouched.
  const { deploymentId: resolvedDeploymentId } = useResolvedDeploymentId()
  const dynamicEntriesQuery = useQuery<SidebarEntry[]>({
    queryKey: ['console-ui-sidebar-entries', resolvedDeploymentId ?? ''],
    queryFn: () => getSidebarEntries(resolvedDeploymentId ?? ''),
    enabled: !!resolvedDeploymentId,
    staleTime: 60_000,
    placeholderData: (prev) => prev ?? [],
  })
  const dynamicEntries: SidebarEntry[] = dynamicEntriesQuery.data ?? []

  // Tenant-label expanded state — clicking the pill opens a small inline
  // panel listing the full Sovereign FQDN. The pill itself only has room
  // for a truncated label; the expanded panel guarantees the FQDN is
  // surfaced into the viewport DOM regardless of width. Closes on a
  // second click. (Issue #607 — TC-133 contract: clicking the sidebar
  // tenant label surfaces the FQDN.)
  const [tenantOpen, setTenantOpen] = useState(false)

  // Resolve the FQDN to display. The prop is the canonical source when
  // present; on the chroot Sovereign Console the deploymentId-bound
  // snapshot may not yet be loaded for newly-mounted pages, in which
  // case we fall back to the hostname-derived FQDN exposed by
  // `DETECTED_MODE`. This avoids ever rendering an empty tenant label
  // on `console.<sov-fqdn>` regardless of network timing.
  const resolvedFQDN =
    sovereignFQDN && sovereignFQDN.length > 0
      ? sovereignFQDN
      : DETECTED_MODE.sovereignFQDN ?? ''

  // Read user info from the OIDC session for the footer card.
  const tokens = loadTokens()
  const claims = tokens ? parseJWTClaims(tokens.idToken) : {}
  const userName =
    (claims.name as string | undefined) ??
    (claims.preferred_username as string | undefined) ??
    (claims.email as string | undefined) ??
    'User'
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
          <button
            type="button"
            onClick={() => setTenantOpen((v) => !v)}
            aria-expanded={tenantOpen}
            aria-controls="sov-console-tenant-details"
            className="flex w-full items-center justify-between gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-left text-xs transition-colors hover:border-[var(--color-accent)] hover:bg-[var(--color-surface-hover)]"
            data-testid="sov-console-tenant-label"
            title={resolvedFQDN}
          >
            <span
              className="min-w-0 flex-1 truncate text-[var(--color-text-strong)]"
              data-testid="sov-console-tenant-fqdn"
            >
              {resolvedFQDN}
            </span>
            <svg
              className={`h-3 w-3 shrink-0 text-[var(--color-text-dim)] transition-transform ${
                tenantOpen ? 'rotate-180' : ''
              }`}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth={2}
              aria-hidden
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M19 9l-7 7-7-7" />
            </svg>
          </button>
          {tenantOpen ? (
            <div
              id="sov-console-tenant-details"
              data-testid="sov-console-tenant-details"
              className="mt-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-[11px]"
            >
              <p className="text-[var(--color-text-dimmer)]">Sovereign FQDN</p>
              <p
                className="mt-0.5 break-all font-mono text-[var(--color-text-strong)]"
                data-testid="sov-console-tenant-fqdn-full"
              >
                {resolvedFQDN}
              </p>
            </div>
          ) : null}
        </div>
      </div>

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-3" data-testid="sov-console-nav">
        {/* Family F (Wave 3, 2026-05-17): the external "Marketplace Admin ↗"
            link added by PR M (t142 follow-up #2) was deleted here per
            founder #1 ruling — "this url is rubbish, the backed of the
            the mark place mutst be just aotnerh menu under console
            like https://console.<sov>/bss". The BSS group below is the
            new canonical surface (in-SPA, RBAC-gated, no external tab). */}
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

        {/* Wave 5.69c (#2396) dynamic sidebar entries — Blueprints
            opted in via spec.consoleUI.sidebarEntry=true. Rendered
            between hardcoded FLAT_NAV and pinned Settings. */}
        {dynamicEntries.map((entry) => {
          const isActive = pathname.startsWith(entry.route)
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              key={`bp-${entry.id}`}
              to={entry.route as never}
              className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
              data-testid={`sov-console-nav-bp-${entry.id}`}
              aria-current={isActive ? 'page' : undefined}
            >
              <NavIcon d={entry.icon || 'M4 6h16M4 12h16M4 18h16'} />
              {entry.label}
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

        {/* Settings sub-nav — visible only when the operator is inside
            /console/settings/*. Keeps the sidebar compact by default and
            mirrors the GitLab-style "category > sub-page" expansion. */}
        {settingsExpanded ? (
          <ul
            className="mx-2 mt-0.5 flex flex-col gap-0.5"
            data-testid="sov-console-settings-sub-nav"
          >
            {SETTINGS_SUB_NAV.map((sub) => {
              const isActive = activeSettingsSub === sub.id
              const cls = isActive
                ? 'text-[var(--color-accent)]'
                : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
              return (
                <li key={sub.id}>
                  <Link
                    to={sub.to as never}
                    className={`block rounded-md py-1.5 pl-10 pr-3 text-xs no-underline transition-colors ${cls}`}
                    data-testid={`sov-console-nav-settings-${sub.id}`}
                    aria-current={isActive ? 'page' : undefined}
                  >
                    {sub.label}
                  </Link>
                </li>
              )
            })}
          </ul>
        ) : null}
      </nav>

      {/* User card at the bottom */}
      <div className="border-t border-[var(--color-border)] p-3">
        <div className="flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-xs font-bold text-[var(--color-accent)]">
            {userInitials || 'T'}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium text-[var(--color-text)]">{userName}</p>
            <p className="truncate text-[10px] text-[var(--color-text-dimmer)]">{resolvedFQDN}</p>
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
