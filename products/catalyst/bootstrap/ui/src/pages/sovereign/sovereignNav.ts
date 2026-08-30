/**
 * sovereignNav.ts — the Sovereign Console left-rail catalogue (issue #607,
 * EPIC #6723 lane C).
 *
 * Lives outside SovereignSidebar.tsx so non-component modules (Settings →
 * Menu's parent dropdown, tests) can import the FLAT_NAV catalogue without
 * tripping react-refresh's components-only export rule. Per
 * docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label / icon /
 * route path is a named constant here, not an inline literal in the JSX.
 */

// ── Cloud icon (verbatim Tabler IconCloud — same as Sidebar.tsx) ─────────────
const CLOUD_ICON =
  'M6.657 18c-2.572 0 -4.657 -2.007 -4.657 -4.483c0 -2.475 2.085 -4.482 4.657 -4.482c.393 -1.762 1.794 -3.2 3.675 -3.773c1.88 -.572 3.956 -.193 5.444 1c1.488 1.19 2.162 3.007 1.77 4.769h.99c1.913 0 3.464 1.56 3.464 3.486c0 1.927 -1.551 3.487 -3.465 3.487h-11.878'

export interface FlatNavItem {
  id: 'apps' | 'catalog' | 'jobs' | 'compliance' | 'dashboard' | 'cloud' | 'users' | 'organizations' | 'billing' | 'sovereignty' | 'settings'
  label: string
  to: string
  /** Optional fragment appended to `to`. An entry that targets an anchor
   *  SECTION of a page rather than a page carries it here so the rendered
   *  href is `/page#anchor` and the operator lands ON the panel — landing at
   *  the top of an eleven-section page is the state row 160 recorded as
   *  failing, so the anchor is the substance of the entry, not decoration. */
  hash?: string
  icon: string
}

// Wave 5 (2026-05-17, founder UX-polish review): order follows the
// operator mental model — overview first, then descend through the
// stack from infrastructure to operations to access to commerce.
// Settings stays pinned at the bottom (defined separately, rendered
// after the FLAT_NAV map below).
export const FLAT_NAV: FlatNavItem[] = [
  {
    id: 'dashboard',
    label: 'Dashboard',
    to: '/dashboard',
    icon: 'M3 3h7v9H3V3zm11 0h7v5h-7V3zM14 10h7v11h-7V10zM3 14h7v7H3v-7z',
  },
  {
    id: 'cloud',
    label: 'Resources',
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
  // Agenity (#6723 lane C) is NOT a static row any more. The former
  // `sandbox` entry (Wave 3 scaffold, relabelled "Agenity" at the lift) is
  // now delivered by bp-agenity's Blueprint spec.consoleUI
  // (products/agenity/blueprint.yaml: sidebarEntry=true, order 40, route
  // /apps/bp-agenity/dashboard, the same terminal glyph) and arrives here
  // through the merged /console-ui/sidebar-entries view, where the
  // sovereign-admin can rename, re-route, re-order, nest or hide it from
  // Settings → Menu. The /sandbox* routes + redirect remain in router.tsx.
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
  // Sovereignty (UAT row 160, #3379). The cutover is Pillar 5 — the single
  // act that severs the eight mothership tethers — and until now its only
  // reachable trigger was the LAST section of an eleven-section /settings
  // page. The hw293-2026-08-10 walk enumerated this nav and got exactly
  // eleven entries, none of them Sovereignty, while /sovereignty rendered
  // "Not Found": the trigger existed but was not a surface.
  //
  // It is an ANCHOR entry, not a page. #793 deliberately mounted the cutover
  // card as `<div id="sovereignty">` inside SettingsPage rather than giving it
  // a route, so that the operator sees it in the context of the Sovereign's
  // own configuration; duplicating it behind /sovereignty would give the
  // cutover two front doors and two places for its state to disagree. The
  // `hash` field carries the anchor so the rendered href is
  // /settings#sovereignty and the entry lands ON the panel.
  //
  // RBAC: NOT in ORG_SCOPED_NAV_IDS. The cutover severs the whole Sovereign;
  // exposing its trigger to an Org-scoped customer session would be a worse
  // defect than the one this entry fixes.
  //
  // Icon: a shield-with-severed-link glyph in the single-stroke family — the
  // same shield the Compliance entry uses, with the tether broken.
  {
    id: 'sovereignty',
    label: 'Sovereignty',
    to: '/settings',
    hash: 'sovereignty',
    icon: 'M12 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016A11.955 11.955 0 0112 2.944zM10 9.5l-1 1a2.121 2.121 0 003 3l1-1m1-1.5l1-1a2.121 2.121 0 00-3-3l-1 1',
  },
]

export const SETTINGS_ITEM: FlatNavItem = {
  id: 'settings',
  label: 'Settings',
  to: '/settings',
  icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
}

// ── Mappable parents (#6723) ──────────────────────────────────────────────────
//
// The FLAT_NAV items a mapped entry may nest under, exported for the
// Settings → Menu parent dropdown. Anchor entries (Sovereignty → /settings#…)
// are not pages and are excluded; Settings is not in FLAT_NAV at all and
// must never gain children (rulings in SovereignSidebar.tsx). Kept in lockstep with the Go
// `sidebarParentIDs` list (console_ui.go) — the API also returns `parents`
// and the dropdown intersects the two, so a drift narrows the choice rather
// than offering a parent the server would refuse.
export interface SidebarParentOption {
  id: string
  label: string
}
export const SIDEBAR_PARENT_OPTIONS: readonly SidebarParentOption[] = FLAT_NAV.filter(
  (item) => !item.hash,
).map(({ id, label }) => ({ id, label }))

/** Default glyph for a mapped entry whose Blueprint declares no sidebarIcon. */
export const DYNAMIC_ENTRY_FALLBACK_ICON = 'M4 6h16M4 12h16M4 18h16'
