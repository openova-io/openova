/**
 * SovereignSidebar — left rail for the Sovereign Console.
 *
 * Analogous to Sidebar.tsx (which is tied to the Catalyst-Zero
 * /provision/$deploymentId/* route tree), but in Sovereign mode:
 *
 *   - Routes use the /console/* prefix (no deploymentId param) — the
 *     Sovereign is implicit from the hostname.
 *   - The estate label shows the Sovereign FQDN.
 *   - The footer card shows the authenticated user's identity (#4187:
 *     resolved from GET /api/v1/whoami via `useSession` — the cookie/PIN
 *     session principal — falling back to OIDC id_token claims only on
 *     legacy PKCE builds), not the generic "User"/"Operator" placeholder.
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

import { useEffect, useRef, useState } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { loadTokens, parseJWTClaims } from '@/shared/lib/oidc'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useConsoleScope } from '@/shared/lib/useConsoleScope'
import { useSession } from '@/shared/lib/useSession'
import { getSidebarEntries, type SidebarEntry } from '@/lib/console-ui.api'

// #4110 — the ONLY nav items an Org-scoped customer console may show. The
// customer sees their OWN estate (apps + catalog browse + sandbox + their
// users) and their OWN settings; every sovereign-admin surface (Dashboard
// fleet/treemap, the whole-cluster Cloud view, provisioning Jobs, the
// Sovereign Compliance dashboards, the cross-org Organizations directory)
// is hidden. Settings is handled separately (rendered after FLAT_NAV) and
// is allowed for an Org session — it shows only the Org's own settings.
const ORG_SCOPED_NAV_IDS: ReadonlySet<FlatNavItem['id']> = new Set([
  'apps',
  'catalog',
  'sandbox',
  'users',
])

interface SovereignSidebarProps {
  /** Sovereign FQDN derived from window.location.hostname. */
  sovereignFQDN: string
}

// ── Cloud icon (verbatim Tabler IconCloud — same as Sidebar.tsx) ─────────────
const CLOUD_ICON =
  'M6.657 18c-2.572 0 -4.657 -2.007 -4.657 -4.483c0 -2.475 2.085 -4.482 4.657 -4.482c.393 -1.762 1.794 -3.2 3.675 -3.773c1.88 -.572 3.956 -.193 5.444 1c1.488 1.19 2.162 3.007 1.77 4.769h.99c1.913 0 3.464 1.56 3.464 3.486c0 1.927 -1.551 3.487 -3.465 3.487h-11.878'

interface FlatNavItem {
  id: 'apps' | 'catalog' | 'sandbox' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'billing' | 'settings'
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
    label: 'Agenity',
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
  // the BSS entry that previously lived here; the legacy /bss* + retired-prefix +
  // /parent-domains URLs redirect into /organizations (router.tsx).
  //
  // Icon: an org-chart / building line-glyph (nodes + connecting edges)
  // matching the single-stroke icon family used by the other entries.
  //
  // RBAC: always visible — the sovereign-admin owns the directory; the
  // catalyst-api enforces tier-bound access server-side on every
  // /api/v1/org/* and /catalog/admin/* call.
  {
    id: 'organizations',
    label: 'Organizations',
    to: '/organizations',
    icon: 'M9 3a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V3zM3 17a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H5a2 2 0 01-2-2v-2zm12 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2zM12 7v4M12 11H6v4m6-4h6v4',
  },
  // Billing (issue #4196, founder top customer-facing item). ONE
  // first-class commercial menu — Vouchers · Orders · Revenue (Showback)
  // — each a native React section on the catalyst-api
  // /api/v1/org/billing/* bridge. Replaces the former billing surface
  // buried under Organizations (and the dead iframe BSS pages). Voucher
  // issuance is the Phase-0 sovereign-admin onboarding tool (DoD.md
  // Phase 0) and is reachable day-one in any billing mode — the showback
  // gate (#4170) never blocks it. The legacy /organizations/billing/* +
  // /bss/* URLs redirect into /billing (router.tsx).
  //
  // RBAC: sovereign-admin only — same as Organizations/Dashboard/Jobs,
  // it is NOT in ORG_SCOPED_NAV_IDS so an Org-scoped customer console
  // never sees it; the catalyst-api requireVoucherIssuer gate
  // (superadmin OR sovereign-admin) is the server-side authority.
  //
  // Icon: a receipt / credit-card line-glyph matching the single-stroke
  // icon family used by the other entries.
  {
    id: 'billing',
    label: 'Billing',
    to: '/billing',
    icon: 'M3 10h18M5 6h14a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2zm2 10h4',
  },
]

const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/settings',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

// ── Settings: no sub-nav children ─────────────────────────────────────────────
//
// Settings has NO sub-left-pane children — every settings surface is a
// granular anchor section of the unified /settings page (#organization,
// #sovereign, #dns, #domain-mode, #parent-domains, #marketplace, …).
//
// History:
//   • Wave 5 (2026-05-17): Marketplace moved off the sub-nav INTO
//     SettingsPage as `<SectionCard id="marketplace">`. Founder: *"if
//     market place is just a toggle etting under setting it dosnt need
//     tohave a sdicated page and it doesnt need to have child left pane
//     menu item"*.
//   • #4089 (2026-06-22): Parent Domains was the LAST sub-nav child —
//     the only odd-one-out sub-left-pane item. Founder: *"currently the
//     parent domains are showing under settings as a sub left-pane menu
//     which is the only weird/odd-one-out … It needs to be granular as
//     other settings such as …/settings#dns"*. Re-homed to
//     `<SectionCard id="parent-domains">` in SettingsPage.tsx. The
//     standalone surface lives on at /organizations/domains (the legacy
//     /parent-domains route redirects there — router.tsx).
//
// With the sub-nav now empty, the Settings entry is a single flat link
// like every other top-level nav item.

// ── Active-state derivation ───────────────────────────────────────────────────

type ActiveSection = 'apps' | 'catalog' | 'sandbox' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'billing' | 'settings'

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
  // /billing(/*) → 'billing' so the Billing nav item highlights for the
  // landing (defaults to Vouchers) and every section
  // (/billing/{vouchers,orders,revenue}). Issue #4196.
  if (/^\/billing(\/|$)/.test(pathname)) return 'billing'
  // /settings/* → 'settings' so the Settings nav item highlights. There
  // is no longer a settings sub-nav (#4089): Parent Domains — the last
  // child — became the `#parent-domains` anchor section of /settings, so
  // the legacy /parent-domains highlight is gone (it redirects to
  // /organizations/domains, which lights up Organizations).
  if (/^\/settings(\/|$)/.test(pathname)) return 'settings'
  return 'apps'
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SovereignSidebar({ sovereignFQDN }: SovereignSidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeSection = deriveActiveSection(pathname)

  // #4110 — Org-console scoping. When the backend reports an Org-scoped
  // session (whoami.orgScoped), filter the nav to the Org-safe items only.
  // Fail safe: while the scope is still loading we render the FULL nav for a
  // Sovereign-admin (the common case) but treat an explicit orgScoped=true
  // as the authoritative trigger to hide. The catalyst-api 403s the hidden
  // surfaces regardless, so a momentary nav flash can never be acted on.
  const { orgScoped } = useConsoleScope()
  const navItems = orgScoped
    ? FLAT_NAV.filter((item) => ORG_SCOPED_NAV_IDS.has(item.id))
    : FLAT_NAV

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

  // Estate-label expanded state — clicking the pill opens a small inline
  // panel listing the full Sovereign FQDN. The pill itself only has room
  // for a truncated label; the expanded panel guarantees the FQDN is
  // surfaced into the viewport DOM regardless of width. Closes on a
  // second click. (Issue #607 — TC-133 contract: clicking the sidebar
  // estate label surfaces the FQDN.)
  const [estateOpen, setEstateOpen] = useState(false)

  // User-menu open state (UAT row 27 / #5000). The footer identity card is
  // the ONLY place the signed-in owner can sign out of the Sovereign Console.
  // Clicking (or Enter/Space on) the card toggles a small dropdown that opens
  // UPWARD (the card is pinned to the bottom of the rail) exposing a
  // "Signed in as <owner>" label + a Sign out action. Closes on a second
  // click, Escape, or a click outside the card — mirroring the click-outside/
  // ESC dismissal pattern already used by the wizard-header ProfileMenu.
  const [userMenuOpen, setUserMenuOpen] = useState(false)
  const userMenuRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!userMenuOpen) return
    function onClick(e: MouseEvent) {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target as Node)) {
        setUserMenuOpen(false)
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setUserMenuOpen(false)
    }
    document.addEventListener('mousedown', onClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [userMenuOpen])

  // Resolve the FQDN to display. The prop is the canonical source when
  // present; on the chroot Sovereign Console the deploymentId-bound
  // snapshot may not yet be loaded for newly-mounted pages, in which
  // case we fall back to the hostname-derived FQDN exposed by
  // `DETECTED_MODE`. This avoids ever rendering an empty estate label
  // on `console.<sov-fqdn>` regardless of network timing.
  const resolvedFQDN =
    sovereignFQDN && sovereignFQDN.length > 0
      ? sovereignFQDN
      : DETECTED_MODE.sovereignFQDN ?? ''

  // Resolve the signed-in principal for the footer card.
  //
  // #4187: the Sovereign Console authenticates via the server-minted
  // `catalyst_session` cookie (the 6-digit PIN / handover flow), NOT the
  // legacy OIDC PKCE token set. On a cookie session `loadTokens()` returns
  // null, so reading identity exclusively from the id_token left the footer
  // showing the generic `User` placeholder + the bare Sovereign FQDN even
  // though the signed-in owner's email was available all along. The
  // authoritative principal is GET /api/v1/whoami (email + tier + roles),
  // surfaced here via the shared `useSession` hook. We fall back to OIDC
  // id_token claims only when no cookie session resolved (legacy PKCE
  // builds) — preserving the prior behaviour for those callers.
  const session = useSession()
  const tokens = loadTokens()
  const claims = tokens ? parseJWTClaims(tokens.idToken) : {}
  const userName =
    (session.email ?? undefined) ??
    (claims.name as string | undefined) ??
    (claims.preferred_username as string | undefined) ??
    (claims.email as string | undefined) ??
    'User'
  const userInitials = userName
    .split(/[\s@.]+/)
    .filter(Boolean)
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
            onClick={() => setEstateOpen((v) => !v)}
            aria-expanded={estateOpen}
            aria-controls="sov-console-org-details"
            className="flex w-full items-center justify-between gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-left text-xs transition-colors hover:border-[var(--color-accent)] hover:bg-[var(--color-surface-hover)]"
            data-testid="sov-console-org-label"
            title={resolvedFQDN}
          >
            <span
              className="min-w-0 flex-1 truncate text-[var(--color-text-strong)]"
              data-testid="sov-console-org-fqdn"
            >
              {resolvedFQDN}
            </span>
            <svg
              className={`h-3 w-3 shrink-0 text-[var(--color-text-dim)] transition-transform ${
                estateOpen ? 'rotate-180' : ''
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
          {estateOpen ? (
            <div
              id="sov-console-org-details"
              data-testid="sov-console-org-details"
              className="mt-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-[11px]"
            >
              <p className="text-[var(--color-text-dimmer)]">Sovereign FQDN</p>
              <p
                className="mt-0.5 break-all font-mono text-[var(--color-text-strong)]"
                data-testid="sov-console-org-fqdn-full"
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
        {navItems.map((item) => {
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
            between hardcoded FLAT_NAV and pinned Settings.
            #4110: suppressed on an Org-scoped console (these are
            Sovereign-level Blueprint nav entries). */}
        {(orgScoped ? [] : dynamicEntries).map((entry) => {
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

        {/* #4089: Settings has no sub-nav children — every settings
            surface (including Parent Domains) is a granular anchor
            section of the unified /settings page. */}
      </nav>

      {/* User card at the bottom — #4187: identity comes from /whoami
          (session.email), not the OIDC id_token, so the cookie/PIN-
          authenticated owner renders by email instead of `User`.

          UAT row 27 / #5000: the card is now the account MENU — the only
          Sign-out affordance in the console. The avatar/name/FQDN layout is
          unchanged (MUST-PRESERVE); we only wrap it in a menu trigger and add
          an upward-opening dropdown with a "Signed in as <owner>" label + a
          Sign out item wired to the shared two-hop `session.signOut()`. */}
      <div
        ref={userMenuRef}
        className="relative border-t border-[var(--color-border)] p-3"
        data-testid="sov-console-user-card"
      >
        {/* Dropdown opens UPWARD — the card is pinned to the bottom of the
            rail, so a downward menu would clip below the viewport. */}
        {userMenuOpen ? (
          <div
            role="menu"
            aria-label="Account"
            id="sov-console-user-menu"
            data-testid="sov-console-user-menu"
            className="absolute bottom-full left-3 right-3 mb-2 overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] py-1 shadow-lg shadow-black/30"
          >
            <div className="px-3 py-2">
              <p className="text-[10px] uppercase tracking-wide text-[var(--color-text-dimmer)]">
                Signed in as
              </p>
              <p
                className="mt-0.5 truncate text-xs font-medium text-[var(--color-text)]"
                data-testid="sov-console-user-menu-owner"
                title={userName}
              >
                {userName}
              </p>
            </div>
            <div className="border-t border-[var(--color-border)]" />
            <button
              type="button"
              role="menuitem"
              data-testid="sov-console-user-signout"
              onClick={() => {
                setUserMenuOpen(false)
                void session.signOut()
              }}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)] focus-visible:bg-[var(--color-surface-hover)] focus-visible:outline-none"
            >
              <svg
                className="h-4 w-4 shrink-0"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={1.5}
                aria-hidden
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"
                />
              </svg>
              Sign out
            </button>
          </div>
        ) : null}

        <button
          type="button"
          aria-haspopup="menu"
          aria-expanded={userMenuOpen}
          aria-controls="sov-console-user-menu"
          onClick={() => setUserMenuOpen((v) => !v)}
          data-testid="sov-console-user-trigger"
          title={`${userName} — account menu`}
          className="flex w-full items-center gap-2 rounded-lg text-left transition-colors hover:bg-[var(--color-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]"
        >
          <div
            className="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-xs font-bold text-[var(--color-accent)]"
            data-testid="sov-console-user-avatar"
          >
            {userInitials || 'T'}
          </div>
          <div className="min-w-0 flex-1">
            <p
              className="truncate text-xs font-medium text-[var(--color-text)]"
              data-testid="sov-console-user-name"
            >
              {userName}
            </p>
            <p className="truncate text-[10px] text-[var(--color-text-dimmer)]">{resolvedFQDN}</p>
          </div>
          <svg
            className={`h-3 w-3 shrink-0 text-[var(--color-text-dim)] transition-transform ${
              userMenuOpen ? 'rotate-180' : ''
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
