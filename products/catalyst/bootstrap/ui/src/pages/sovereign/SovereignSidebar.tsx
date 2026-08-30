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
 * EPIC #6723 lane C — the rail is a MAPPING surface, not only a constant.
 * Founder (2026-08-31): "OpenOva is composed of applications; the left menu
 * or sub-menus of the sovereign console can be connected to the respective
 * applications, like Agenity; OpenOva should provide that flexibility in its
 * admin settings to map." Entries beyond FLAT_NAV come from the merged
 * /console-ui/sidebar-entries view (Blueprint spec.consoleUI defaults +
 * installed Applications with a user UI ⊕ the sovereign-admin's overrides
 * from Settings → Menu). An entry with `parent` renders as a sub-item under
 * that FLAT_NAV item; enabled=false entries are not rendered at all. Agenity
 * itself is the first Blueprint-sourced entry (bp-agenity consoleUI) — the
 * former static `sandbox` FLAT_NAV row is gone; the /sandbox* routes and
 * their redirect to /apps/bp-agenity/dashboard stay in router.tsx. Scope
 * follows the entry's SOURCE: Blueprint-sourced entries render for every
 * session (an Org-scoped console keeps Agenity exactly as before); only
 * Application candidates are Sovereign-level and hidden from Org sessions.
 *
 * Related: GitHub issue #607, #6723
 */

import { Fragment, useEffect, useRef, useState } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { loadTokens, parseJWTClaims } from '@/shared/lib/oidc'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useConsoleScope } from '@/shared/lib/useConsoleScope'
import { useSession } from '@/shared/lib/useSession'
import { getSidebarEntries, type SidebarEntry } from '@/lib/console-ui.api'
import {
  DYNAMIC_ENTRY_FALLBACK_ICON,
  FLAT_NAV,
  SETTINGS_ITEM,
  type FlatNavItem,
} from './sovereignNav'

// #4110 — the ONLY nav items an Org-scoped customer console may show. The
// customer sees their OWN estate (apps + catalog browse + their users) and
// their OWN settings; every sovereign-admin surface (Dashboard
// fleet/treemap, the whole-cluster Cloud view, provisioning Jobs, the
// Sovereign Compliance dashboards, the cross-org Organizations directory)
// is hidden. Settings is handled separately (rendered after FLAT_NAV) and
// is allowed for an Org session — it shows only the Org's own settings.
// (#6723: the static `sandbox` row that used to sit here is now the
// Blueprint-sourced Agenity entry, which still renders on an Org-scoped
// console — see the source-based scope rule on visibleEntries below. Only
// Application candidates (app:<name>) are Sovereign-level and hidden here.)
const ORG_SCOPED_NAV_IDS: ReadonlySet<FlatNavItem['id']> = new Set([
  'apps',
  'catalog',
  'users',
])

interface SovereignSidebarProps {
  /** Sovereign FQDN derived from window.location.hostname. */
  sovereignFQDN: string
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

type ActiveSection = 'apps' | 'catalog' | 'sandbox' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'billing' | 'sovereignty' | 'settings'

const CLOUD_PATH_RE = /^\/(cloud|infrastructure)(\/|$)/

/**
 * @param pathname router location pathname
 * @param hash     router location hash, with or without its leading `#`.
 *   Two nav entries now share `/settings` — Settings and Sovereignty — so the
 *   pathname alone can no longer decide which is active, and an entry that can
 *   never light is not a first-class surface. The hash is the discriminator,
 *   and it is normalised here because TanStack Router reports it without the
 *   `#` while `location.hash` in the DOM includes it.
 */
function deriveActiveSection(pathname: string, hash = ''): ActiveSection {
  const fragment = hash.replace(/^#/, '')
  // Checked BEFORE the /settings rule below, which would otherwise swallow it.
  if (/^\/settings(\/|$)/.test(pathname) && fragment === 'sovereignty') return 'sovereignty'
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
  const hash = useRouterState({ select: (s) => s.location.hash })
  const activeSection = deriveActiveSection(pathname, hash)

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

  // Wave 5.69c (#2396) + #6723 — mapped sidebar entries from the MERGED
  // /console-ui/sidebar-entries view (Blueprint spec.consoleUI defaults +
  // installed Applications with a user UI ⊕ the sovereign-admin's
  // overrides). Top-level entries splice in between FLAT_NAV and the pinned
  // Settings entry; entries carrying `parent` render as sub-items under
  // that FLAT_NAV item; enabled=false entries are hidden. Graceful
  // degradation: empty list on 404/502/network-error leaves the hardcoded
  // nav untouched. Settings → Menu invalidates this key on save.
  // The Sovereign id the merged view is read under: the console-internal
  // deployment id when the session resolves one, else the hostname-derived
  // FQDN (an Org-scoped console may not resolve a deployment record; the
  // chroot catalyst-api maps either form onto its single cluster).
  const { deploymentId: resolvedDeploymentId } = useResolvedDeploymentId()
  const sidebarSovereignId = resolvedDeploymentId ?? DETECTED_MODE.sovereignFQDN ?? ''
  const dynamicEntriesQuery = useQuery<SidebarEntry[]>({
    queryKey: ['console-ui-sidebar-entries', sidebarSovereignId],
    queryFn: () => getSidebarEntries(sidebarSovereignId),
    enabled: sidebarSovereignId !== '',
    staleTime: 60_000,
    placeholderData: (prev) => prev ?? [],
  })
  const dynamicEntries: SidebarEntry[] = dynamicEntriesQuery.data ?? []
  // #4110 + #6723 scope rule, by SOURCE not by session: a Blueprint-sourced
  // entry (bp-agenity's consoleUI) is not Organization-specific and renders
  // on an Org-scoped console exactly as the static Agenity row did; an
  // Application candidate (app:<name>) is a Sovereign-level mapping — the
  // merged view names Applications from every Organization — and stays
  // suppressed for an Org session. catalyst-api applies the same rule
  // server-side (HandleConsoleUISidebarEntries confines an Org-scoped
  // session to source=blueprint), so this filter is belt-and-braces.
  const visibleEntries: SidebarEntry[] = dynamicEntries.filter(
    (entry) => entry.enabled !== false && (!orgScoped || entry.source === 'blueprint'),
  )
  const flatNavIds = new Set<string>(navItems.map((item) => item.id))
  // A parent the rail does not render (stale mapping, or a FLAT_NAV id this
  // build no longer carries) degrades to top-level rather than vanishing.
  const topLevelEntries = visibleEntries.filter(
    (entry) => !entry.parent || !flatNavIds.has(entry.parent),
  )
  const childrenOf = (parentId: string): SidebarEntry[] =>
    visibleEntries.filter((entry) => entry.parent === parentId)

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
          const children = childrenOf(item.id)
          return (
            <Fragment key={item.id}>
              <Link
                to={item.to as never}
                hash={item.hash as never}
                // Two entries now share the `/settings` pathname, and TanStack's
                // Link owns `aria-current` — it sets `page` from its OWN match,
                // which by default ignores the fragment. Without this, Sovereignty
                // reads as the current page while the operator is on plain
                // /settings, and Settings reads as current while they are on the
                // cutover panel: both entries announce themselves as the location
                // at once. Scoped to entries that carry a hash (plus the pinned
                // Settings entry below) rather than applied blanket, so no other
                // nav entry changes its match semantics.
                activeOptions={item.hash ? { includeHash: true } : undefined}
                className={`mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm no-underline transition-colors ${cls}`}
                data-testid={`sov-console-nav-${item.id}`}
                aria-current={isActive ? 'page' : undefined}
              >
                <NavIcon d={item.icon} />
                {item.label}
              </Link>
              {/* #6723 — mapped sub-menu: entries the sovereign-admin nested
                  under THIS item from Settings → Menu. */}
              {children.length > 0 ? (
                <div
                  role="group"
                  aria-label={`${item.label} sub-menu`}
                  data-testid={`sov-console-nav-children-${item.id}`}
                >
                  {children.map((entry) => (
                    <DynamicNavLink key={`bp-${entry.id}`} entry={entry} pathname={pathname} nested />
                  ))}
                </div>
              ) : null}
            </Fragment>
          )
        })}

        {/* Wave 5.69c (#2396) + #6723 — top-level mapped entries (Blueprint
            spec.consoleUI defaults such as Agenity, plus any Application the
            sovereign-admin enabled without a parent). Rendered between
            hardcoded FLAT_NAV and pinned Settings; disabled entries never
            reach this list; an Org-scoped console sees the Blueprint-sourced
            ones only (#4110, source rule above). */}
        {topLevelEntries.map((entry) => (
          <DynamicNavLink key={`bp-${entry.id}`} entry={entry} pathname={pathname} />
        ))}

        {/* Settings at the bottom of the nav list */}
        {(() => {
          const isActive = activeSection === SETTINGS_ITEM.id
          const cls = isActive
            ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
            : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
          return (
            <Link
              to={SETTINGS_ITEM.to as never}
              // The other half of the pair above: on /settings#sovereignty this
              // entry must NOT claim to be the current page.
              activeOptions={{ includeHash: true }}
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

/**
 * DynamicNavLink — one mapped entry (#6723). A console path renders as a
 * router Link (basepath-aware, client-side); an https:// route — the
 * sovereign-admin pointing the entry at an app's own front door on one of
 * the Sovereign's parent domains — renders as a plain anchor, because it
 * leaves the SPA. The `sov-console-nav-bp-<id>` test id is the Wave 5.69c
 * contract and is unchanged; `data-nav-parent` / `data-nav-source` expose
 * the mapping for walks.
 */
function DynamicNavLink({
  entry,
  pathname,
  nested = false,
}: {
  entry: SidebarEntry
  pathname: string
  nested?: boolean
}) {
  const external = /^https:\/\//i.test(entry.route)
  const isActive = !external && entry.route !== '' && pathname.startsWith(entry.route)
  const tone = isActive
    ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
    : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
  const shape = nested
    ? 'mx-2 flex items-center gap-3 rounded-lg py-1.5 pl-9 pr-3 text-[13px]'
    : 'mx-2 flex items-center gap-3 rounded-lg px-3 py-2 text-sm'
  const className = `${shape} no-underline transition-colors ${tone}`
  const body = (
    <>
      <NavIcon d={entry.icon || DYNAMIC_ENTRY_FALLBACK_ICON} />
      {entry.label}
    </>
  )
  if (external) {
    return (
      <a
        href={entry.route}
        className={className}
        data-testid={`sov-console-nav-bp-${entry.id}`}
        data-nav-parent={entry.parent || undefined}
        data-nav-source={entry.source}
        data-nav-nested={nested ? 'true' : undefined}
      >
        {body}
      </a>
    )
  }
  return (
    <Link
      to={entry.route as never}
      className={className}
      data-testid={`sov-console-nav-bp-${entry.id}`}
      data-nav-parent={entry.parent || undefined}
      data-nav-source={entry.source}
      data-nav-nested={nested ? 'true' : undefined}
      aria-current={isActive ? 'page' : undefined}
    >
      {body}
    </Link>
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
