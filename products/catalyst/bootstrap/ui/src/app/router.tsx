import { createRouter, createRoute, createRootRoute, redirect, isRedirect } from '@tanstack/react-router'
import { IS_SAAS } from '@/shared/constants/env'
import { API_BASE, isCatalystZeroURL } from '@/shared/config/urls'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { setProvisionFlashBanner } from '@/shared/lib/flashBanner'
import { currentPathRelativeToBasepath } from '@/shared/lib/basepathRelative'

/**
 * Runtime basepath detection (issue #618).
 *
 * The same catalyst-ui image is deployed in two topologies:
 *   1. Sovereign clusters — served at console.<sov-fqdn>/ (basepath '/')
 *   2. Catalyst-Zero on contabo — served at console.openova.io/sovereign/*
 *      with a Traefik strip-prefix middleware (browser URL keeps /sovereign/).
 *
 * Vite base is '/' so the nginx bundle is always rooted at '/'. But
 * TanStack Router reads window.location.pathname, which still has the
 * '/sovereign' prefix on contabo. We detect this at module-init time:
 * if the current path starts with '/sovereign', tell the router the
 * base is '/sovereign' so it strips the prefix before matching routes.
 */
// G110-followup #2706: route through isCatalystZeroURL() (host + path), not
// path-only — on a Sovereign cluster a stale-bookmark URL like
// `console.<sov-fqdn>/sovereign/login` was setting basepath='/sovereign'
// and trapping every internal navigation under `/sovereign/*` forever.
const basepath = isCatalystZeroURL() ? '/sovereign' : '/'

/**
 * isCatalystZero — true when the UI is running on Catalyst-Zero
 * (console.openova.io/sovereign/*). Used by wizardAuthGuard to decide
 * whether to enforce session-cookie auth.
 *
 * We detect by hostname rather than IS_SAAS / IS_SELFHOSTED because the
 * same selfhosted build image runs on both Catalyst-Zero (contabo-mkt)
 * and tenant Sovereign consoles (console.<sov-fqdn>). Only Catalyst-Zero
 * has Keycloak + the session middleware wired up; Sovereign clusters
 * manage their own auth separately.
 */
const isCatalystZero =
  typeof window !== 'undefined' && window.location.hostname === 'console.openova.io'

// Lazy page imports
import { RootLayout } from './layouts/RootLayout'
import { AppLayout } from './layouts/AppLayout'
import { WizardLayout } from './layouts/WizardLayout'
import { SovereignConsoleLayout } from './layouts/SovereignConsoleLayout'

import { LoginPage } from '@/pages/auth/LoginPage'
import { VerifyPinPage } from '@/pages/auth/VerifyPinPage'
import { AuthCallbackPage } from '@/pages/auth/AuthCallbackPage'
import { SignupPage } from '@/pages/auth/SignupPage'
import { ForgotPage } from '@/pages/auth/ForgotPage'
import { HandoverErrorPage } from '@/pages/auth/HandoverErrorPage'
import { DashboardPage } from '@/pages/dashboard/DashboardPage'
import { CrossSovereignView } from '@/pages/dashboard/CrossSovereignView'
import { FleetTreemap } from '@/pages/dashboard/FleetTreemap'
import { WizardPage } from '@/pages/wizard/WizardPage'
import { SuccessPage } from '@/pages/success/SuccessPage'
import { DesignShowcase } from '@/pages/designs/DesignShowcase'
import { JobsDepsVizDemo } from '@/pages/designs/JobsDepsVizDemo'
import { MarketplaceFamilyPage } from '@/pages/marketplace/MarketplaceFamilyPage'
import { MarketplaceProductPage } from '@/pages/marketplace/MarketplaceProductPage'
import { ProvisionPage } from '@/pages/provision/ProvisionPage'
import { AppsPage } from '@/pages/sovereign/AppsPage'
import { AppDetail } from '@/pages/sovereign/AppDetail'
import { CatalogDetail } from '@/pages/sovereign/CatalogDetail'
import { InstallPage } from '@/pages/sovereign/InstallPage'
import { JobsPage } from '@/pages/sovereign/JobsPage'
import { JobDetail } from '@/pages/sovereign/JobDetail'
import { JobsTimeline } from '@/pages/sovereign/JobsTimeline'
import { Dashboard } from '@/pages/sovereign/Dashboard'
import { CloudPage } from '@/pages/sovereign/CloudPage'
import { ResourceDetailRoute } from '@/pages/sovereign/cloud-list/ResourceDetailRoute'
import { SessionsRoute } from '@/pages/sovereign/sessions/SessionsRoute'
import { DecommissionPage } from '@/pages/sovereign/DecommissionPage'
import { UserAccessListPage } from '@/pages/admin/user-access/UserAccessListPage'
import { UserAccessEditPage } from '@/pages/admin/user-access/UserAccessEditPage'
import { MultiGrantEditPage } from '@/pages/admin/rbac/MultiGrantEditPage'
import { GroupBrowserPage } from '@/pages/admin/rbac/GroupBrowserPage'
import { RoleBrowserPage } from '@/pages/admin/rbac/RoleBrowserPage'
import { OrgMembersPage } from '@/pages/admin/rbac/OrgMembersPage'
import { AccessMatrixPage } from '@/pages/admin/rbac/AccessMatrixPage'
import { AuditPage } from '@/pages/admin/rbac/AuditPage'
import { ParentDomainsPage } from '@/pages/admin/parent-domains/ParentDomainsPage'
// EPIC-2 (#1097) slice P — Blueprint publishing + curate.
import { PublishPage as BlueprintPublishPage } from '@/pages/admin/blueprints/PublishPage'
import { CuratePage as BlueprintCuratePage } from '@/pages/admin/blueprints/CuratePage'
import { SREDashboardPage } from '@/pages/admin/compliance/SREDashboardPage'
import { SecLeadDashboardPage } from '@/pages/admin/compliance/SecLeadDashboardPage'
import { PolicyDrilldownPage } from '@/pages/admin/compliance/PolicyDrilldownPage'
// Wave-2 Family-E (#1583, C11-008): standalone Falco runtime-alerts page.
import { RuntimeAlertsPage } from '@/pages/admin/compliance/RuntimeAlertsPage'
import { SettingsPage } from '@/pages/sovereign/SettingsPage'
import { NotificationsPage } from '@/pages/sovereign/NotificationsPage'
// Sovereign-mode /console/* routes use the same canonical components as
// /provision/$deploymentId/* — see the SovereignConsoleRedirect helper
// near the bottom of this file. The duplicate ConsoleDashboardPage /
// ConsoleAppsPage / ConsoleJobsPage / ConsoleCloudPage / ConsoleUsersPage
// / ConsoleSettingsPage stubs have been DELETED (issue: pixel-byte-byte
// identical UI between mothership-side /provision/$id/dashboard and
// Sovereign-side post-handover console).
// Wave 5 (2026-05-17): MarketplaceSettings standalone page retired —
// the toggle moved into SettingsPage as a `<SectionCard id="marketplace">`
// anchor section. Founder UX-polish review removed the dedicated page +
// sub-nav child. Old /settings/marketplace URL now 404s; bookmarks
// resolve via the operator clicking Settings in the sidebar then
// scrolling to the Marketplace anchor.
import { DeploymentsList } from '@/pages/sovereign/DeploymentsList'
import { UsersPage as SMEUsersPage } from '@/pages/sme/UsersPage'
import { RolesPage as SMERolesPage } from '@/pages/sme/RolesPage'
import { CreateTenantPage as SMECreateTenantPage } from '@/pages/sme/CreateTenantPage'
import { SovereigntyPreviewPage } from '@/pages/sovereignty/SovereigntyPreviewPage'
// qa-loop iter-6 Cluster-A `spa-target-state-routes-missing` —
// stub pages mounted under /app/$deploymentId/* for routes whose
// full implementations are owned by other slices. See
// pages/sovereign/stubs/README pattern in each file.
import { NetworkingPage } from '@/pages/sovereign/networking/NetworkingPage'
import { ContinuumPage } from '@/pages/sovereign/stubs/ContinuumPage'
// qa-loop iter-12 Fix #50: Resources family — moved out of `stubs/` into
// the wired `pages/sovereign/resources/` package. Each component now
// subscribes to a real catalyst-api endpoint via TanStack Query (no
// "(pending live data)" placeholders). The legacy `stubs/Resources*`
// + `stubs/PodLogsPage` files have been deleted to prevent future
// imports from routing back to a stub. See `feedback_no_mvp_no_workarounds.md`.
import { ResourcesApplyPage } from '@/pages/sovereign/resources/ResourcesApplyPage'
import { ResourcesSearchPage } from '@/pages/sovereign/resources/ResourcesSearchPage'
import { ResourcesListPage } from '@/pages/sovereign/resources/ResourcesListPage'
import { ResourceDetailNoTabPage } from '@/pages/sovereign/stubs/ResourceDetailNoTabPage'
import { PodLogsPage } from '@/pages/sovereign/resources/PodLogsPage'
// Family F (Wave 3, t10 C6-003/004/005) — BSS-in-console.
// Founder #1 requirement: "the backed of the the mark place mutst be
// just aotnerh menu under console like https://console.<sov>/bss".
//
// Wave 6 PR 1 (2026-05-17): /bss is a NATIVE React landing
// (BssLandingPage) using the PortalShell chrome shared with Dashboard /
// Apps / Jobs / Settings. The 5 sub-sections wrap themselves in
// PortalShell via BssSectionShell — the prior BssLayout tab strip is
// retired in favor of the sidebar's existing BSS group + the landing's
// section-nav grid. Iframe content is preserved in the section pages
// until Wave 6 PRs 2-6 native-port each one.
import { BssLandingPage } from '@/pages/sovereign/bss/BssLandingPage'
import { BillingPage as BssBillingPage } from '@/pages/sovereign/bss/BillingPage'
import { OrdersPage as BssOrdersPage } from '@/pages/sovereign/bss/OrdersPage'
import { RevenuePage as BssRevenuePage } from '@/pages/sovereign/bss/RevenuePage'
import { VouchersPage as BssVouchersPage } from '@/pages/sovereign/bss/VouchersPage'
import { TenantsPage as BssTenantsPage } from '@/pages/sovereign/bss/TenantsPage'
// Wave 3 — Sandbox UI scaffold (branch: sandbox-wave3-ui-scaffold).
// Per-Org agent-coding workspace mounted under /sandbox/* in the chroot
// Sovereign Console. SandboxLanding is the 6-agent picker;
// SandboxSession hosts xterm.js for /sandbox/$id; SandboxSettings is
// the BYOS Claude Max OAuth surface.
import { SandboxLanding } from '@/pages/sovereign/sandbox/SandboxLanding'
import { SandboxSession } from '@/pages/sovereign/sandbox/SandboxSession'
import { SandboxSettings } from '@/pages/sovereign/sandbox/SandboxSettings'
import {
  canonicalisePath,
  hasCatalystSession,
  isPublicPath,
  probeWhoamiAndCacheMarker,
  sanitizeNextParam,
} from './auth-gate'

/**
 * rootBeforeLoad — universal auth gate (#1090 cluster A2,
 * extended for qa-loop iter-2 cluster `spa-route-guard-rejects-pin-session`).
 *
 * Runs before EVERY route's beforeLoad in the tree. Three responsibilities:
 *
 *   1. Path canonicalisation — redirect malformed paths (//x, /x/, /X)
 *      to canonical /x so deep-link variants don't slip past the gate.
 *
 *   2. Sovereign-mode auth fast-path — when a `catalyst:authed` marker
 *      is present in sessionStorage (set by VerifyPinPage on PIN verify,
 *      /auth/handover beforeLoad, or SovereignConsoleLayout on whoami
 *      200), allow the route through without a network round-trip.
 *
 *   3. Sovereign-mode auth authoritative-check — when no marker is
 *      present, fall through to GET /api/v1/whoami to authoritatively
 *      detect the HttpOnly `catalyst_session` cookie. On 200, cache the
 *      marker and allow through. On 401, redirect to /login with the
 *      original deep-link preserved as ?next=. On 5xx/network error,
 *      fail open (let the route render so the layout's own probe can
 *      surface the failure with proper context).
 *
 *  Why responsibility 3 was added (2026-05-09): the previous gate
 *  bounced any deep-link load that lacked the `catalyst:authed` marker,
 *  even when a valid HttpOnly catalyst_session cookie was present. The
 *  cookie is set by /auth/pin/verify and /auth/handover but is invisible
 *  to JS — opening a new tab, refreshing after sessionStorage cleared,
 *  or pasting a deep-link URL into a fresh window all left the cookie
 *  intact while losing the JS-side marker, so operators with valid
 *  sessions were redirected to /login on every fresh entry. Caught on
 *  console.omantel.biz when the founder could not deep-link to
 *  /dashboard from a sibling tab after a successful PIN verify.
 *
 * Mothership (catalyst-zero) mode handles its own auth via
 * provisionAuthGuard / wizardAuthGuard for its routes — the gate is a
 * no-op in non-sovereign mode (other than canonicalisation).
 */
async function rootBeforeLoad({ location }: { location: { pathname: string } }) {
  if (typeof window === 'undefined') return
  const pathname = location.pathname
  const canonical = canonicalisePath(pathname)
  if (canonical !== pathname) {
    // TanStack Router's `location.pathname` is POST-basepath — i.e. on
    // contabo (basepath='/sovereign') a visit to
    // `/sovereign/provision/$id/jobs/install-X%3AY` arrives here with
    // pathname=`/provision/$id/jobs/install-X%3AY`. `canonicalisePath`
    // lowercases, so `%3A` → `%3a` and the comparison triggers a
    // hard-nav to `canonical`. But `window.location.replace` operates
    // on the FULL URL — calling replace(canonical) navigates to a
    // bare `/provision/...` URL without the `/sovereign/` prefix,
    // which nginx (which only serves the SPA under `/sovereign/*`)
    // 404s. Re-add the basepath here for the hard-nav target.
    //
    // Caught live on prov #82 + #84 (omani.works, 2026-05-14): the
    // canvas-row-click + open-link patterns produced `install-X%3AY`
    // URLs that the operator opened, canonicalisation lowercased the
    // hex, and the resulting hard-nav landed on a bare `/provision/`
    // path → nginx 404 "page not found". The `<Link to>` and
    // `navigate({to})` paths are fine because the router re-adds
    // basepath on internal navigation; only this hard-nav escape was
    // broken.
    // G110-followup #2706: host + path check (not path-only).
    const basepath = isCatalystZeroURL() ? '/sovereign' : ''
    const newURL = basepath + canonical + window.location.search + window.location.hash
    window.location.replace(newURL)
    throw redirect({ to: canonical as never, replace: true })
  }
  if (DETECTED_MODE.mode !== 'sovereign') return
  if (isPublicPath(canonical)) return
  if (hasCatalystSession()) return
  // No JS-side marker — authoritatively probe /whoami to detect the
  // HttpOnly catalyst_session cookie before redirecting to /login.
  const whoami = await probeWhoamiAndCacheMarker(API_BASE)
  if (whoami === true) return
  if (whoami === null) return // 5xx / network error — fail open
  // 401 — genuinely unauthenticated. Redirect with deep-link, but
  // sanitize the `next` so we can never construct an open-redirect
  // payload (CWE-601). qa-loop iter-4 cluster
  // `users-page-null-map-and-open-redirect`.
  const rawNext = pathname + window.location.search
  const safeNext = sanitizeNextParam(rawNext)
  throw redirect({
    to: '/login',
    search: safeNext ? { next: safeNext } : {},
    replace: true,
  })
}

// Root
const rootRoute = createRootRoute({ component: RootLayout, beforeLoad: rootBeforeLoad })

/**
 * Index redirect — mode-aware.
 *
 * catalyst-zero (console.openova.io): redirect to /wizard (Provisioning Wizard)
 * sovereign (console.<sov-fqdn>):     redirect to /console/dashboard (Sovereign Console)
 *
 * IS_SAAS is preserved as a higher-priority override for the Catalyst-Zero
 * SaaS variant of the platform where local auth is used.
 *
 * Per INVIOLABLE-PRINCIPLES.md #4, mode detection is runtime-derived from
 * the hostname via DETECTED_MODE — never hardcoded here.
 *
 * Related: GitHub issue #607
 */
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  beforeLoad: () => {
    if (IS_SAAS) throw redirect({ to: '/login' })
    if (DETECTED_MODE.mode === 'sovereign') throw redirect({ to: '/dashboard' as never })
    throw redirect({ to: '/wizard' })
  },
})

// Auth routes
// /login — search params: `next` (deep-link target preserved across the
// PIN flow, issue #1090 cluster B), `error` (post-redirect error code
// from VerifyPinPage's expired/attempts-exceeded branch). validateSearch
// is required so TanStack Router preserves both keys on
// `redirect({ to: '/login', search: { next, error } })` calls — without
// it the search type defaults to `{}` and the params are stripped.
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
  validateSearch: (raw: Record<string, unknown>): { next?: string; error?: string } => {
    const out: { next?: string; error?: string } = {}
    // Sanitize `next` to prevent open-redirect (CWE-601): an attacker
    // could craft /login?next=//evil.com so post-login navigation
    // sends the operator off-origin. qa-loop iter-4 cluster
    // `users-page-null-map-and-open-redirect`.
    const safeNext = sanitizeNextParam(raw.next)
    if (safeNext) out.next = safeNext
    if (typeof raw.error === 'string' && raw.error.length > 0) out.error = raw.error
    return out
  },
})
// /login/verify — PIN-entry step of the 6-digit auth flow (issue #688).
// Mounted at sibling depth (not nested under /login) so a refresh on the
// verify URL doesn't lose its email/requestId search params to a parent
// loader.
const loginVerifyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login/verify',
  component: VerifyPinPage,
  // Search params: `email` + `requestId` (issued by /login PIN POST),
  // and `next` (deep-link target, issue #1090 cluster B) preserved
  // across the PIN flow so VerifyPinPage can navigate back to the
  // requested deployment surface after a successful 6-digit verify
  // instead of dropping the operator on /wizard.
  validateSearch: (raw: Record<string, unknown>): {
    email?: string
    requestId?: string
    next?: string
  } => {
    const out: { email?: string; requestId?: string; next?: string } = {}
    if (typeof raw.email === 'string' && raw.email.length > 0) out.email = raw.email
    if (typeof raw.requestId === 'string' && raw.requestId.length > 0) {
      out.requestId = raw.requestId
    }
    // Same open-redirect sanitization as /login (CWE-601). qa-loop
    // iter-4 cluster `users-page-null-map-and-open-redirect`.
    const safeNext = sanitizeNextParam(raw.next)
    if (safeNext) out.next = safeNext
    return out
  },
})
const signupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/signup', component: SignupPage })
const forgotRoute = createRoute({ getParentRoute: () => rootRoute, path: '/forgot', component: ForgotPage })

/**
 * OIDC authorization_code callback route (issue #607).
 *
 * Keycloak redirects here after the user authenticates:
 *   GET /auth/callback?code=<code>&state=<state>
 *
 * AuthCallbackPage exchanges the code for tokens, then navigates to
 * /console/dashboard. The route is intentionally outside the
 * SovereignConsoleLayout so it runs before auth state is resolved.
 */
const authCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/callback',
  component: AuthCallbackPage,
})

/**
 * Handover-token reception route (issue #607).
 *
 * The server-side (Agent C / catalyst-api on the Sovereign) handles
 * POST /auth/handover — it validates the JWT, creates a Keycloak session,
 * and returns 302 with session cookies to /console/dashboard.
 *
 * The client does NOT intercept this URL at the fetch level. If the
 * browser lands here (unlikely — the server redirect should carry the
 * browser directly to /console/dashboard), redirect immediately.
 *
 * This route exists purely as a safety net to prevent a TanStack Router
 * 404 in case the server 302 for some reason resolves to a client-side
 * navigation rather than a full HTTP redirect.
 */
const authHandoverRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/handover',
  beforeLoad: () => {
    // The server-side AuthHandover handler has already set the
    // HttpOnly catalyst_session cookie before redirecting here. Mark
    // the rootRoute auth gate (#1090 cluster A2) as satisfied so the
    // next navigation to /dashboard isn't bounced to /login.
    if (typeof window !== 'undefined') {
      try { sessionStorage.setItem('catalyst:authed', '1') } catch { /* private */ }
    }
    throw redirect({ to: '/dashboard' as never, replace: true })
  },
  component: () => null,
})

// HandoverErrorPage moved to `@/pages/auth/HandoverErrorPage` 2026-05-09
// (qa-loop iter-1 cluster `auth-handover-flow-text`) so it can be
// unit-tested without booting the router and so the matrix-asserted
// "missing" token in document.body.innerText is owned by a single file.
const authHandoverErrorRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/handover-error',
  validateSearch: (raw: Record<string, unknown>): { reason?: string } => {
    if (typeof raw.reason === 'string' && raw.reason.length > 0) {
      return { reason: raw.reason }
    }
    return {}
  },
  component: HandoverErrorPage,
})

// App routes
//
// DoD D17b (caught on t131 2026-05-16): the mothership `appRoute`
// claims the `/app` path PREFIX with AppLayout chrome + dozens of
// child routes (/app/$deploymentId/applications, /app/$deploymentId/
// settings, etc.). The chroot Sovereign Console ALSO has a clean
// `/app/$componentId` route (consoleAppDetailRoute → AppDetail).
//
// When the operator clicks an app card on the Sovereign Console
// `/apps`, the AppCard generates HREF `/app/<name>` (line ~720 in
// AppsPage.tsx). On chroot the URL resolves to the MOTHERSHIP
// appRoute because:
//   1. appRoute is registered first under rootRoute (line 1640+)
//   2. The mothership's children `/app/$deploymentId/...` accept
//      `bp-cnpg` as $deploymentId
//   3. AppLayout renders with the mothership Sidebar
//
// Result: clicking ANY app card on the Sovereign Console renders
// the mothership-context AppsPage with 44 cards INSTEAD of the
// AppDetail page for the clicked component.
//
// Fix: appRoute's beforeLoad redirects to the canonical Sovereign
// AppDetail route (`/app/$componentId` under consoleLayoutRoute)
// whenever DETECTED_MODE.mode === 'sovereign'. The `/app` path is
// then exclusively the mothership's territory on Catalyst-Zero, and
// the Sovereign Console resolves cleanly to consoleAppDetailRoute.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/app',
  component: AppLayout,
  beforeLoad: async ({ location }) => {
    // #3086 — catalyst-zero (console.openova.io) short-circuits the
    // central rootBeforeLoad gate (router.tsx:230 returns when
    // DETECTED_MODE.mode !== 'sovereign'), so the `/app/*` fleet
    // surfaces (dashboard / install / sre / sec compliance) rendered
    // to anonymous deep-links instead of bouncing to /login?next=.
    // Run the canonical provisionAuthGuard at the SHARED `/app` parent
    // so every child inherits the gate. The guard is a no-op on
    // Sovereign clusters (they manage their own OIDC auth), so the
    // mode-aware canonicalisation redirects below still run unchanged.
    // The already-guarded `/app/$deploymentId/*` children re-run the
    // guard redundantly — harmless (a second 401 probe just re-throws
    // the same redirect).
    await provisionAuthGuard()
    if (DETECTED_MODE.mode === 'sovereign') {
      // D17/D17b walkthrough on t132 2026-05-17: the previous
      // strip-`/app`-prefix-and-redirect logic broke /app/<componentId>
      // because no chroot route matches `/<componentId>` directly —
      // consoleAppDetailRoute is registered at `/app/$componentId` under
      // consoleLayoutRoute, so we must NOT strip the prefix. Instead,
      // only redirect when the sub-path is a mothership-only surface
      // (Fleet view dashboard, install wizard, SRE/SEC consoles,
      // blueprint catalog) — those have no Sovereign Console equivalent
      // and would 404. For any other sub-path (component name like
      // `/app/bp-cnpg`, or bare `/app`) leave routing alone so the
      // child consoleAppDetailRoute / consoleAppsRoute can claim it.
      const remainder = location.pathname.replace(/^\/app/, '')
      const motherOnly = ['/dashboard', '/install', '/sre', '/sec', '/blueprints']
      if (motherOnly.some(p => remainder.startsWith(p))) {
        throw redirect({ to: '/dashboard' as never })
      }
      // bare `/app` with nothing after → canonical apps grid
      if (remainder === '' || remainder === '/') {
        throw redirect({ to: '/apps' as never })
      }
      // Otherwise: let TanStack match the most-specific child route
      // (consoleAppDetailRoute at `/app/$componentId`). No redirect.
    }
  },
})
// /app/dashboard renders the mothership multi-Sovereign Fleet view
// (DashboardPage). On a Sovereign Console (chroot, console.<sov-fqdn>)
// this surface MUST NOT be reachable — the Sovereign owns a single
// deployment, the "fleet" concept belongs to the mothership only.
// Caught live on t129.omani.works (2026-05-16, BUG-016 / D24): the
// fleet view rendered "7 Sovereigns" with duplicate t129.omani.works
// rows and "APPS 0, ORGS 0" despite 44 apps installed.
//
// Beforeload-redirect to the canonical Sovereign dashboard at
// `/dashboard` (consoleDashboardRoute, the per-Sovereign landing).
// Mothership-side (`catalyst-zero`) keeps the fleet view as today.
const dashboardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/dashboard',
  component: DashboardPage,
  beforeLoad: () => {
    if (DETECTED_MODE.mode === 'sovereign') {
      throw redirect({ to: '/dashboard' as never, replace: true })
    }
  },
})

// EPIC-6 (#1101) slice U-Fleet-3 — cross-Sovereign Applications view.
// Pivot from the Sovereign-card grid to the Application × Sovereign
// table. Filters: org / topology / DR posture. Each row links to the
// per-Sovereign chroot console's AppDetail.
const crossSovApplicationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/dashboard/applications',
  component: CrossSovereignView,
})

// TBD-E14 — fleet-wide treemap surface (mothership only).
// Single-layer Sovereigns map; each cell deep-links to that Sov's
// chroot /dashboard treemap. See pages/dashboard/FleetTreemap.tsx.
const fleetTreemapRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/dashboard/treemap',
  component: FleetTreemap,
})

/**
 * provisionAuthGuard — beforeLoad for /provision/$deploymentId routes
 * (issue #689).
 *
 * The wizard surface is anonymous-first: a visitor can run the entire
 * 7-step flow without ever signing in.  But the post-launch
 * `/provision/<id>` surface is per-deployment state owned by a real
 * operator, and therefore requires a session — anonymous visitors get
 * redirected back to the wizard with an inline banner explaining why.
 *
 * Catalyst-Zero only.  Sovereign clusters handle their own auth in the
 * SovereignConsoleLayout's OIDC gate.
 *
 * Per #689 DoD: a signed-in operator who is NOT the owner of the
 * deployment id sees the canonical 404 surface (the catalyst-api
 * returns 404 for cross-tenant access — the UI does not need to
 * implement a separate "you don't have access" branch).
 */
async function provisionAuthGuard() {
  if (!isCatalystZero) return // Sovereign clusters manage their own auth
  try {
    const res = await fetch(`${API_BASE}/v1/whoami`, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
    if (res.status === 401) {
      // Anonymous — flash the banner AND preserve the deep-link target as
      // ?next= so the PIN flow lands the operator BACK on the requested
      // page after verify. The flash banner is kept for the case where
      // the operator dismisses /login and clicks "Wizard" — they still
      // see a contextual hint.
      //
      // Issue #1090 (cluster B): /sovereign/provision/<id>/{jobs/timeline,
      // cloud,users,settings} previously bounced to /wizard step-1 with
      // a banner but lost the deep link entirely — no sessionStorage of
      // the path, no next= param, so post-PIN the operator landed on
      // /wizard, never on the requested deployment surface.
      //
      // The `next` value is the post-basepath route path so
      // VerifyPinPage's `navigate({ to: next })` resolves cleanly through
      // the router (basepath is added back automatically). On contabo
      // the browser URL is `/sovereign/provision/...`; we strip
      // `/sovereign` here so the round-trip doesn't double up.
      setProvisionFlashBanner('Sign in to view your deployments')
      const here = currentPathRelativeToBasepath()
      throw redirect({
        to: '/login',
        search: { next: here } as never,
        replace: true,
      })
    }
    // Any other status (200, 5xx) — allow through. A 5xx that locks the
    // user out of `/provision` would erase their post-launch progress
    // visibility, which is worse than rendering a stale page.
  } catch (err) {
    if (isRedirect(err)) throw err
    // Network errors / unreachable backend — fall through, render the
    // page; AppsPage's own 404 / 503 branch will surface the failure.
  }
}

// Wizard — guest-mode (issue #689). The wizard route renders for
// anonymous visitors; auth fires only when they click Launch on
// StepReview.  This is the SME-marketplace pattern — the visitor can
// see the entire product surface before being asked to sign in.
const wizardLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/wizard',
  component: WizardLayout,
})
const wizardRoute = createRoute({ getParentRoute: () => wizardLayoutRoute, path: '/', component: WizardPage })

// Success (full-screen)
const successRoute = createRoute({ getParentRoute: () => rootRoute, path: '/success', component: SuccessPage })

// Deployments list (issue #747) — operator's history surface. Reachable
// via /sovereign/deployments (Catalyst-Zero) or /deployments (Sovereign
// build, though that build never exposes the wizard so this route is
// effectively Catalyst-Zero only).
//
// Issue #3086: this route is NOT under appRoute, so the #3088 deep-link
// guard never covered it. Accessed logged-out, the component-level
// `window.location.replace('/login?next=...')` dropped the `/sovereign`
// basepath and 404'd on nginx (which only serves the SPA under
// `/sovereign/*`). Gate the route with provisionAuthGuard — the SAME
// basepath-aware router redirect the sibling /provision routes use:
// `throw redirect({ to: '/login', search: { next } })` resolves through
// the configured basepath to `/sovereign/login?next=<post-basepath path>`
// on Catalyst-Zero. The component's useSession() anonymous branch stays
// as defense-in-depth for the session-expired-while-mounted case.
const deploymentsListRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/deployments',
  component: DeploymentsList,
  beforeLoad: provisionAuthGuard,
})

/**
 * Post-handover redirect (issue #319).
 *
 * After the handover finalisation flow (#317) stamps `adoptedAt` on the
 * deployment record, the customer's Sovereign is operationally self-
 * sufficient: they administer it through their own
 * `console.<sovereign-fqdn>`, not through Catalyst-Zero anymore. The
 * shell URL `console.openova.io/sovereign/<id>` therefore needs to
 * redirect to the customer-side console once that flag is set.
 *
 * We only redirect on the bare `/provision/$deploymentId` URL — the
 * deep-links (`/jobs`, `/cloud`, `/app/...`) keep rendering on
 * Catalyst-Zero so the operator retains a post-mortem audit trail for
 * the original provisioning run.
 *
 * Failure modes: a 404 from catalyst-api means the deployment was
 * wiped — fall through to the page (which renders its own "deployment
 * not found" surface). A network error means we render the page;
 * better to surface stale info than block the operator.
 */
async function maybeRedirectToCustomerConsole(deploymentId: string): Promise<void> {
  try {
    const res = await fetch(`${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}`, {
      headers: { Accept: 'application/json' },
    })
    if (!res.ok) return
    const body = (await res.json()) as { adoptedAt?: string; sovereignFQDN?: string }
    if (!body.adoptedAt || !body.sovereignFQDN) return
    // Only redirect when both the finalisation flag AND a real FQDN are
    // present. Per docs/INVIOLABLE-PRINCIPLES.md #4 the redirect target
    // is derived from runtime data, never hardcoded.
    const target = `https://console.${body.sovereignFQDN}/`
    // Hard navigation — TanStack `redirect()` only handles in-app routes.
    if (typeof window !== 'undefined') {
      window.location.replace(target)
      // Throw to abort the route resolution so AppsPage doesn't paint
      // before the browser navigates.
      throw new Error('redirecting to customer console')
    }
  } catch (err) {
    // Re-throw the redirect-abort sentinel; swallow everything else
    // (network errors fall through to render the local page).
    if (err instanceof Error && err.message === 'redirecting to customer console') {
      throw err
    }
  }
}

// Provision — Sovereign Admin landing surface, pixel-ported from
// core/console/src/components/AppsPage.svelte (Deployments + Catalog
// tabs + auto-fit card grid). Replaces the legacy DAG view + the
// invented "AdminPage" surface with the canonical console shell.
// StepReview redirects here on submit, so the URL shape stays stable.
const provisionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId',
  component: AppsPage,
  beforeLoad: async ({ params }) => {
    // Issue #689 — anonymous visitors get redirected to the wizard
    // with a flash banner; signed-in but cross-tenant gets 404 from
    // the API (no UI-side branch needed).
    await provisionAuthGuard()
    await maybeRedirectToCustomerConsole(params.deploymentId)
  },
})

// Per-Application detail page — pixel-ported from core/console
// AppDetail.svelte. SECTIONS, NOT TABS: hero / About / Connection /
// Bundled deps / Tenant / Configuration / Jobs (Jobs section appended
// for the wizard provision context).
const provisionAppRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/app/$componentId',
  component: AppDetail,
  // Issue #689 — anonymous redirected to wizard with banner; cross-tenant 404 from API.
  beforeLoad: provisionAuthGuard,
})

// Global jobs list — table view (issue #204 founder spec). Each row is
// a clickable link that navigates to the per-job detail page.
const provisionJobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs',
  component: JobsPage,
  beforeLoad: provisionAuthGuard,
})

// Jobs timeline (Gantt-style retrospective). Static segment, MUST be
// registered BEFORE the dynamic $jobId route below so TanStack Router
// resolves `/jobs/timeline` to this surface, not to JobDetail with
// jobId="timeline".
const provisionJobsTimelineRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/timeline',
  component: JobsTimeline,
  beforeLoad: provisionAuthGuard,
})

// Per-Job detail page (epic #204).
const provisionJobDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/$jobId',
  component: JobDetail,
  beforeLoad: provisionAuthGuard,
})

// Sovereign Dashboard — resource-utilisation treemap (founder spec).
const provisionDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/dashboard',
  component: Dashboard,
  beforeLoad: provisionAuthGuard,
})

// Sovereign self-decommission (issue #319). Reachable from the Sovereign
// Admin Dashboard's Decommission link AND directly via deep-link from
// the customer's own console.<sovereign-fqdn> after handover. POSTs to
// the existing /api/v1/deployments/{id}/wipe endpoint with an optional
// backup destination — the same canonical seam shared with the wizard's
// pre-handover Cancel & Wipe flow.
const provisionDecommissionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/decommission/$deploymentId',
  component: DecommissionPage,
  // #3086 — this surface renders a DESTRUCTIVE wipe form. It must never
  // be reachable by an anonymous deep-link on catalyst-zero; gate it with
  // the canonical provisionAuthGuard (401 → /login?next=, no-op on
  // Sovereign clusters which run their own OIDC auth).
  beforeLoad: provisionAuthGuard,
})

/* ── Cloud (issue #350) ─────────────────────────────────────────
 *
 * Single parent route. The graph/list dispatch is owned by
 * CloudPage.tsx via the `view` query param; the resource kind for
 * list view is the `kind` query param. The legacy P3 sub-routes
 * `/cloud/<category>/<resource>` are preserved as 301 redirects so
 * deep links and bookmarks keep working without rendering the
 * renamed surface twice.
 */

interface CloudSearch {
  view?: 'graph' | 'list'
  kind?: string
}

/**
 * D17 Wave-1 Fix-Author Family A (2026-05-17 t10.omantel.biz):
 *
 * Test agents (E, C2) reported every deep-link `/cloud?view=list&kind=<X>`
 * was "redirected to /dashboard or /cloud/resource/.../overview". Several
 * of the failing kinds in the agent matrix are NOT in `KIND_IDS`
 * (kinds.ts) but ARE the natural plural / no-hyphen / kubectl form an
 * operator types:
 *
 *   loadbalancers              → canonical `load-balancers`
 *   nodepools / node-pool      → canonical `node-pools`
 *   workernodes / worker-node  → canonical `worker-nodes`
 *   storageclasses             → canonical `storage-classes`
 *   dnszones                   → canonical `dns-zones`
 *   httproutes                 → fall back to `services` (closest kind)
 *   networkpolicies            → not in registry — fall back to default
 *   ciliumnetworkpolicies      → not in registry — fall back to default
 *   ciliumclusterwidenetworkpolicies
 *                              → not in registry — fall back to default
 *   policyreports / clusterpolicyreports
 *                              → not in registry — fall back to default
 *   pvc / pv                   → canonical `pvcs` / `persistentvolumes`
 *
 * Without normalisation, `CloudListView`'s URL-canonicalising useEffect
 * sees `search.kind !== activeKind` and fires a `navigate({replace:true})`
 * to overwrite the URL. The downstream re-mount + concurrent SSE
 * connection churn produces the "drifts to /dashboard" symptom the test
 * agents saw. Normalising AT validateSearch fixes it at the lowest
 * possible layer so the URL the React tree observes is already canonical
 * on the very first render — no nav-replace storm, no /dashboard drift.
 *
 * Per CLAUDE.md "architect-first": `KIND_IDS` (`kinds.ts`) is the single
 * source of truth for valid kinds; this map only lives in router.tsx
 * because the alias normalisation must happen at route-parse time before
 * any component mounts. The map is closed (no fall-through) — anything
 * not in `KIND_IDS` and not in the alias set is left as-is so the
 * CloudListView's existing `isValidKind` fallback to DEFAULT_KIND still
 * applies (no behavioural regression for valid kinds).
 */
const CLOUD_KIND_ALIASES: Record<string, string> = {
  // Hyphen vs no-hyphen (kubectl natural form)
  loadbalancers: 'load-balancers',
  loadbalancer: 'load-balancers',
  nodepools: 'node-pools',
  nodepool: 'node-pools',
  workernodes: 'worker-nodes',
  workernode: 'worker-nodes',
  storageclasses: 'storage-classes',
  storageclass: 'storage-classes',
  dnszones: 'dns-zones',
  dnszone: 'dns-zones',
  // Singular forms of valid plural kinds
  pvc: 'pvcs',
  pv: 'persistentvolumes',
  persistentvolume: 'persistentvolumes',
  cluster: 'clusters',
  vcluster: 'vclusters',
  service: 'services',
  ingress: 'ingresses',
  bucket: 'buckets',
  volume: 'volumes',
  pod: 'pods',
  deployment: 'deployments',
  statefulset: 'statefulsets',
  daemonset: 'daemonsets',
  replicaset: 'replicasets',
  configmap: 'configmaps',
  secret: 'secrets',
  namespace: 'namespaces',
  node: 'nodes',
  endpointslice: 'endpointslices',
  // Kinds the test matrix mentions but the registry doesn't surface yet
  // — alias to the nearest valid kind so the URL doesn't bounce.
  // HTTPRoutes are Gateway-API objects that ride on top of Services;
  // operator intent of "look at HTTP routing" is best served by the
  // Services list until a dedicated kind ships.
  httproutes: 'services',
  httproute: 'services',
  // Network-policy kinds are not in the K8s list registry; fall back to
  // services (the closest networking surface) so the operator lands on a
  // populated table instead of drifting.
  networkpolicies: 'services',
  networkpolicy: 'services',
  ciliumnetworkpolicies: 'services',
  ciliumnetworkpolicy: 'services',
  ciliumclusterwidenetworkpolicies: 'services',
  ciliumclusterwidenetworkpolicy: 'services',
  // Policy reports — Wave-2 Family-E (#1583/C11-005/C11-006): both
  // kinds now have first-class CloudListKind registrations + pages; the
  // alias collapses kubectl-natural singular/plural to the canonical
  // plural form. The old `→ configmaps` rewrite was a silent fallback
  // that hid an architecture gap (UI didn't surface Kyverno reports).
  policyreport: 'policyreports',
  clusterpolicyreport: 'clusterpolicyreports',
}

function normaliseCloudKind(raw: string): string {
  const lower = raw.toLowerCase()
  return CLOUD_KIND_ALIASES[lower] ?? raw
}

const provisionCloudRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/cloud',
  component: CloudPage,
  beforeLoad: provisionAuthGuard,
  validateSearch: (raw: Record<string, unknown>): CloudSearch => {
    const out: CloudSearch = {}
    if (raw.view === 'graph' || raw.view === 'list') out.view = raw.view
    if (typeof raw.kind === 'string' && raw.kind.length > 0) {
      out.kind = normaliseCloudKind(raw.kind)
    }
    return out
  },
})

// EPIC-4 Slice R1 (#1099) — drill-down detail page (mothership tree).
// URL shape: /provision/$deploymentId/cloud/resource/$kind/$ns/$name/$tab
// `$ns` is `_` for cluster-scoped resources (matches server-side path).
const provisionResourceDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/cloud/resource/$kind/$ns/$name/$tab',
  component: ResourceDetailRoute,
  beforeLoad: provisionAuthGuard,
})

// EPIC-4 Slice E3 (#1099) — Guacamole session list + replay.
const provisionSessionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/sessions',
  component: SessionsRoute,
  beforeLoad: provisionAuthGuard,
})

// 301 redirects for the legacy P3 sub-routes. Each one redirects to
// the consolidated `/cloud?view=…&kind=…` shape and renders nothing
// itself — tanstack-router runs `beforeLoad` before the component
// resolves, so the throw happens before paint.
const NoopRedirectComponent = () => null

interface LegacyRedirect {
  /** Path appended to /provision/$deploymentId/cloud. */
  path: string
  /** Search params to apply on the redirect target. */
  search: CloudSearch
}

const LEGACY_CLOUD_REDIRECTS: readonly LegacyRedirect[] = [
  // Architecture → graph view
  { path: '/architecture', search: { view: 'graph' } },
  // Compute landing + per-resource → list view with the right kind
  { path: '/compute', search: { view: 'list', kind: 'clusters' } },
  { path: '/compute/clusters', search: { view: 'list', kind: 'clusters' } },
  { path: '/compute/vclusters', search: { view: 'list', kind: 'vclusters' } },
  { path: '/compute/node-pools', search: { view: 'list', kind: 'node-pools' } },
  { path: '/compute/worker-nodes', search: { view: 'list', kind: 'worker-nodes' } },
  // Network landing + per-resource
  { path: '/network', search: { view: 'list', kind: 'load-balancers' } },
  { path: '/network/services', search: { view: 'list', kind: 'services' } },
  { path: '/network/ingresses', search: { view: 'list', kind: 'ingresses' } },
  { path: '/network/load-balancers', search: { view: 'list', kind: 'load-balancers' } },
  { path: '/network/dns-zones', search: { view: 'list', kind: 'dns-zones' } },
  // Storage landing + per-resource
  { path: '/storage', search: { view: 'list', kind: 'pvcs' } },
  { path: '/storage/pvcs', search: { view: 'list', kind: 'pvcs' } },
  { path: '/storage/storage-classes', search: { view: 'list', kind: 'storage-classes' } },
  { path: '/storage/buckets', search: { view: 'list', kind: 'buckets' } },
  { path: '/storage/volumes', search: { view: 'list', kind: 'volumes' } },
] as const

const legacyCloudRedirectRoutes = LEGACY_CLOUD_REDIRECTS.map((r) =>
  createRoute({
    getParentRoute: () => provisionCloudRoute,
    path: r.path,
    component: NoopRedirectComponent,
    beforeLoad: ({ params }) => {
      throw redirect({
        to: '/cloud' as never,
        params: params as never,
        search: r.search as never,
        replace: true,
      })
    },
  }),
)

// Legacy /infrastructure/* — every legacy path now redirects to the
// /cloud query shape. Preserves back-compat with the original P1 of
// #309 redirect set.
const provisionInfrastructureRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/infrastructure',
  component: NoopRedirectComponent,
})

const provisionInfrastructureIndexRoute = createRoute({
  getParentRoute: () => provisionInfrastructureRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/cloud' as never,
      params: params as never,
      search: { view: 'graph' } as never,
      replace: true,
    })
  },
  component: NoopRedirectComponent,
})

interface InfraLegacyRedirect {
  path: string
  search: CloudSearch
}

const INFRA_LEGACY_REDIRECTS: readonly InfraLegacyRedirect[] = [
  { path: '/topology', search: { view: 'graph' } },
  { path: '/compute', search: { view: 'list', kind: 'clusters' } },
  { path: '/storage', search: { view: 'list', kind: 'pvcs' } },
  { path: '/network', search: { view: 'list', kind: 'load-balancers' } },
] as const

const infraLegacyRedirectRoutes = INFRA_LEGACY_REDIRECTS.map((r) =>
  createRoute({
    getParentRoute: () => provisionInfrastructureRoute,
    path: r.path,
    component: NoopRedirectComponent,
    beforeLoad: ({ params }) => {
      throw redirect({
        to: '/cloud' as never,
        params: params as never,
        search: r.search as never,
        replace: true,
      })
    },
  }),
)

/* ── Sovereign IAM — User Access editor (issue #323) ─────────────
 *
 * Three routes under /provision/$deploymentId/users:
 *   • list     → /users           (UserAccessListPage)
 *   • new      → /users/new       (UserAccessEditPage with no name)
 *   • edit     → /users/$name     (UserAccessEditPage with name)
 */
const provisionUsersListRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/users',
  component: UserAccessListPage,
  beforeLoad: provisionAuthGuard,
})
const provisionUsersNewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/users/new',
  component: UserAccessEditPage,
  beforeLoad: provisionAuthGuard,
})
const provisionUsersEditRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/users/$name',
  component: UserAccessEditPage,
  beforeLoad: provisionAuthGuard,
})

/* ── Sovereign IAM — multi-grant editor + group/role browser
 *      (EPIC-3 #1098 slice U1+U3+U4) ───────────────────────────────
 *
 * Mounted under /provision/$deploymentId/rbac/* (mothership) and
 * /rbac/* (chroot Sovereign Console — see consoleLayoutRoute below).
 * The legacy /users routes (UserAccessEditPage) stay in place during
 * the deprecation grace period; the multi-grant editor lives on a
 * dedicated path so the UI surfaces both side-by-side.
 */
const provisionRBACMultiGrantRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/rbac/grant',
  component: MultiGrantEditPage,
  beforeLoad: provisionAuthGuard,
})
const provisionRBACGroupsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/rbac/groups',
  component: GroupBrowserPage,
  beforeLoad: provisionAuthGuard,
})
const provisionRBACRolesRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/rbac/roles',
  component: RoleBrowserPage,
  beforeLoad: provisionAuthGuard,
})

// EPIC-2 (#1097) slice P — Blueprint publishing + Curate routes.
// Mounted under /provision/$deploymentId/blueprints/* (mothership) and
// /blueprints/* (chroot Sovereign Console — see consoleLayoutRoute
// children below). Publish is per-Org owner; Curate is sovereign-admin
// only.
const provisionBlueprintsPublishRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/blueprints/publish',
  component: BlueprintPublishPage,
  beforeLoad: provisionAuthGuard,
})
const provisionBlueprintsCurateRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/blueprints/curate',
  component: BlueprintCuratePage,
  beforeLoad: provisionAuthGuard,
})

// EPIC-3 (#1098) slice U5-U8 — RBAC member views (per-org Members,
// access matrix, audit trail). Per-Application Members tab lives
// inside AppDetail and so doesn't need its own route.
const provisionRBACMatrixRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/rbac/matrix',
  component: AccessMatrixPage,
  beforeLoad: provisionAuthGuard,
})
const provisionRBACAuditRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/rbac/audit',
  component: AuditPage,
  beforeLoad: provisionAuthGuard,
})
const provisionOrgMembersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/organizations/$orgId/members',
  component: OrgMembersPage,
  beforeLoad: provisionAuthGuard,
})

/* ── Sovereign Settings (issue #516) ─────────────────────────────
 *
 * Deployment-scoped Settings surface. Replaces the legacy sidebar
 * Settings link → /wizard divert; the Settings sidebar entry now
 * targets this route instead.
 */
const provisionSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/settings',
  component: SettingsPage,
  beforeLoad: provisionAuthGuard,
})

// Standalone notifications surface (#531 item 1) — same in-memory list
// the bell renders, but with room to scroll long error traces.
const provisionNotificationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/notifications',
  component: NotificationsPage,
  beforeLoad: provisionAuthGuard,
})

/* ── Compliance dashboards (slice U, #1096) ──────────────────────────
 *
 * Mother-side admin surface. Mounted at root under `/admin/compliance/*`
 * (per the slice brief) — sibling to the /provision/$id tree because
 * compliance is fleet-scoped, not per-deployment. The auth gate is the
 * same provisionAuthGuard used by every other admin surface.
 *
 *   /admin/compliance/sre                  — SRE Lead dashboard (U1)
 *   /admin/compliance/security             — Security Lead dashboard (U2)
 *   /admin/compliance/policy/$policyName   — per-policy drill-down (U4)
 *
 * Chroot Sovereign Console mirrors live under `/sre/compliance`,
 * `/sec/compliance`, `/compliance/policy/$policyName` (added below in
 * the consoleLayoutRoute children).
 */
const adminComplianceSREDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/compliance/sre',
  component: SREDashboardPage,
  beforeLoad: provisionAuthGuard,
})
const adminComplianceSecurityDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/compliance/security',
  component: SecLeadDashboardPage,
  beforeLoad: provisionAuthGuard,
})
const adminCompliancePolicyDrilldownRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/compliance/policy/$policyName',
  component: PolicyDrilldownPage,
  beforeLoad: provisionAuthGuard,
})
// Wave-2 Family-E (#1583, C11-008): /admin/compliance/runtime — Falco
// runtime-security alerts feed. Chroot mirror lives below as
// `consoleComplianceRuntimeRoute`.
const adminComplianceRuntimeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/compliance/runtime',
  component: RuntimeAlertsPage,
  beforeLoad: provisionAuthGuard,
})

// Legacy DAG provision view — preserved at a sub-path so existing
// links and CI smoke tests (which still curl `/provision/legacy/...`)
// don't 404 mid-rollout.
const legacyProvisionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/legacy/$deploymentId',
  component: ProvisionPage,
  // #3086 — the legacy DAG provision view is per-deployment operator
  // state; gate it like every other /provision/* surface so an
  // anonymous catalyst-zero deep-link bounces to /login?next= instead
  // of rendering. No-op on Sovereign clusters.
  beforeLoad: provisionAuthGuard,
})

// Design showcase
const designsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/designs', component: DesignShowcase })

/**
 * Layout-free preview surface for the SovereigntyCard widget (#793).
 * Mounted outside the SovereignConsoleLayout so the Playwright spec
 * can render the card deterministically without reproducing the
 * full OIDC + sovereign-hostname auth shell. The PRODUCTION mount is
 * inside ConsoleDashboardPage; this route is the test harness.
 */
const sovereigntyPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/sovereignty/preview',
  component: SovereigntyPreviewPage,
})
const designsJobsDepsVizRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/designs/jobs-deps-viz',
  component: JobsDepsVizDemo,
})

// Marketplace — long-form family portfolio + product detail surfaces
// reachable from the wizard's component-card chips (family) and card body
// (product). Wizard state lives in zustand+persist (localStorage) so
// navigation across these routes never drops the operator's selection.
const marketplaceFamilyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/marketplace/family/$familyId',
  component: MarketplaceFamilyPage,
})
const marketplaceProductRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/marketplace/product/$componentId',
  component: MarketplaceProductPage,
})

/* ── Sovereign Console routes (issue #607) ────────────────────────────
 *
 * Route tree for Sovereign mode (console.<sov-fqdn>):
 *
 *   /console                  → redirect to /console/dashboard
 *   /console/dashboard        → ConsoleDashboardPage
 *   /console/apps             → ConsoleAppsPage
 *   /console/jobs             → ConsoleJobsPage
 *   /console/cloud            → ConsoleCloudPage
 *   /console/users            → ConsoleUsersPage
 *   /console/settings         → ConsoleSettingsPage
 *
 * All /console/* routes are children of consoleLayoutRoute, which
 * mounts the SovereignConsoleLayout (OIDC auth gate + sidebar + header).
 *
 * Auth routes (outside the layout — must be accessible before auth):
 *   /auth/callback            → AuthCallbackPage (PKCE code exchange)
 *   /auth/handover            → redirect to /console/dashboard (safety net)
 */

/**
 * Sovereign-mode /console/* — REDIRECT-ONLY shell.
 *
 * The duplicate ConsoleDashboardPage / ConsoleAppsPage / ConsoleJobsPage /
 * ConsoleCloudPage / ConsoleUsersPage / ConsoleSettingsPage stubs from
 * PR #937 have been DELETED. There is now exactly ONE canonical implementation
 * of every operator surface — Dashboard, AppsPage, JobsPage, CloudPage,
 * UserAccessListPage, SettingsPage — under the `/provision/$deploymentId/*`
 * route tree (the same the wizard renders at console.openova.io).
 *
 * For Sovereign-mode operators landing on `console.<sov-fqdn>/console/*`
 * (the URL the SovereignSidebar links to), the routes below redirect to
 * the canonical `/provision/$selfDeploymentId/*` after fetching the self
 * deployment id from `/api/v1/sovereign/self`. Same components, same
 * styling, same data — pixel-byte-byte identical to the mothership view.
 *
 * Iteration 2 will drop the `/sovereign/provision/$id/` URL prefix on
 * Sovereigns by refactoring the canonical components to read deploymentId
 * from a route-aware hook (useResolvedDeploymentId, already added).
 */
/**
 * Sovereign Console layout — mounted at the root path on Sovereign clusters
 * so operator pages live at clean URLs (`/dashboard`, `/apps`, `/jobs`,
 * `/cloud`, `/users`, `/settings`, `/parent-domains`, `/catalog`). On
 * contabo the same component renders at `/sovereign/<page>` — but the
 * mothership wizard tracks per-deployment state at `/sovereign/provision/$id/*`
 * (the transient URL pattern that's only meaningful while monitoring
 * a specific provisioning run from the wizard shell).
 */
/**
 * Pathless layout route — inherits the parent URL (root) and only adds
 * the SovereignConsoleLayout chrome. Children live at clean root paths.
 */
const consoleLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_sovereign_console',
  component: SovereignConsoleLayout,
})
const consoleDashboardRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/dashboard',
  component: Dashboard,
})
const consoleAppsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/apps',
  component: AppsPage,
})
const consoleJobsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/jobs',
  component: JobsPage,
})
// G117.2 #2741 — Catalog class drill-down (chroot Sovereign Console).
// `/catalog/$blueprintName` renders the CLASS page: header + topology
// list + per-Blueprint instance table + "+ New instance" affordance for
// multi-instance Blueprints. Distinct from `/apps` (instance grid) and
// `/app/$componentId` (single-instance detail). Ports the abandoned
// Astro+Svelte scaffold at products/catalyst/console/src/routes/catalog
// /[name]/+page.svelte to the production React UI.
const consoleCatalogDetailRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/catalog/$blueprintName',
  component: CatalogDetail,
})
// Jobs timeline (Gantt-style retrospective) — chroot Sovereign Console.
// MUST be registered BEFORE the dynamic $jobId route below so TanStack
// Router resolves `/jobs/timeline` to this surface, not to JobDetail with
// jobId="timeline" (which would render the canonical "Job not found"
// branch). Sibling parity with provisionJobsTimelineRoute on the
// /provision/$deploymentId/* tree. Caught on console.omantel.biz QA pass
// 2026-05-07 (TC-050).
const consoleJobsTimelineRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/jobs/timeline',
  component: JobsTimeline,
})
const consoleJobDetailRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/jobs/$jobId',
  component: JobDetail,
})
const consoleCloudRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/cloud',
  component: CloudPage,
  // Mirrors provisionCloudRoute.validateSearch so child legacy-redirect
  // routes (TC-090..092) can pass `view` and `kind` through cleanly and
  // CloudPage's useSearch reads typed values.
  //
  // D17 Wave-1 Fix-Author Family A (2026-05-17): normalise `kind` via
  // `normaliseCloudKind` so kubectl-natural / no-hyphen / singular forms
  // (loadbalancers, services-vs-service, dnszones, httproutes, …) map
  // to canonical `KIND_IDS` BEFORE the React tree mounts. Without this,
  // CloudListView's URL-replace useEffect storms on the kind mismatch,
  // which (combined with concurrent SSE re-connect) was producing the
  // "drifts to /dashboard" symptom test agents E + C2 saw on t10.
  validateSearch: (raw: Record<string, unknown>): CloudSearch => {
    const out: CloudSearch = {}
    if (raw.view === 'graph' || raw.view === 'list') out.view = raw.view
    if (typeof raw.kind === 'string' && raw.kind.length > 0) {
      out.kind = normaliseCloudKind(raw.kind)
    }
    return out
  },
})

// EPIC-4 Slice R1 (#1099) — drill-down detail page (chroot tree).
const consoleResourceDetailRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/cloud/resource/$kind/$ns/$name/$tab',
  component: ResourceDetailRoute,
})

// EPIC-4 Slice E3 (#1099) — Guacamole session list + replay (chroot).
const consoleSessionsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sessions',
  component: SessionsRoute,
})
const consoleUsersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/users',
  component: UserAccessListPage,
})
const consoleUsersNewRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/users/new',
  component: UserAccessEditPage,
})
const consoleUsersEditRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/users/$name',
  component: UserAccessEditPage,
})

// EPIC-3 (#1098) slice U1+U3+U4 — RBAC management chroot routes.
const consoleRBACMultiGrantRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/rbac/grant',
  component: MultiGrantEditPage,
})
const consoleRBACGroupsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/rbac/groups',
  component: GroupBrowserPage,
})
const consoleRBACRolesRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/rbac/roles',
  component: RoleBrowserPage,
})

// EPIC-2 (#1097) slice P — Blueprint publishing + Curate chroot routes.
const consoleBlueprintsPublishRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/blueprints/publish',
  component: BlueprintPublishPage,
})
const consoleBlueprintsCurateRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/blueprints/curate',
  component: BlueprintCuratePage,
})

// EPIC-3 (#1098) slice U5-U8 — RBAC member views chroot routes.
const consoleRBACMatrixRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/rbac/matrix',
  component: AccessMatrixPage,
})
const consoleRBACAuditRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/rbac/audit',
  component: AuditPage,
})
const consoleOrgMembersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/organizations/$orgId/members',
  component: OrgMembersPage,
})

const consoleSettingsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/settings',
  component: SettingsPage,
})
const consoleAppDetailRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/app/$componentId',
  component: AppDetail,
})

// EPIC-2 Slice I (#1097) — live install flow.
//
// Two sibling URL trees per the same pattern as compliance dashboards
// (slice U):
//
//   Mothership tenant operator (provision tree):
//     /provision/$deploymentId/install                — catalog landing
//     /provision/$deploymentId/install/$blueprintName — Blueprint pre-selected
//
//   Chroot Sovereign Console (consoleLayoutRoute children):
//     /install                                        — same surface, deploymentId resolved via /api/v1/sovereign/self
//     /install/$blueprintName                         — Blueprint pre-selected
//
// Per the brief the install page reads catalyst-catalog via the
// catalyst-api proxy; the InstallForm widget auto-generates the form
// from spec.configSchema (RJSF + Ajv). Submit creates the Application
// CR, status modal subscribes to the SSE stream, "Open Apps" navigates
// back to the canonical AppsPage when the operator dismisses.
const provisionInstallRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/install',
  component: InstallPage,
  beforeLoad: provisionAuthGuard,
})
const provisionInstallBlueprintRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/install/$blueprintName',
  component: () => {
    const { blueprintName } = provisionInstallBlueprintRoute.useParams() as { blueprintName: string }
    return <InstallPage preselectedBlueprint={blueprintName} />
  },
  beforeLoad: provisionAuthGuard,
})
const consoleInstallRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/install',
  component: InstallPage,
})
const consoleInstallBlueprintRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/install/$blueprintName',
  component: () => {
    const { blueprintName } = consoleInstallBlueprintRoute.useParams() as { blueprintName: string }
    return <InstallPage preselectedBlueprint={blueprintName} />
  },
})

/* ── SME-tier console routes (issue #802) ────────────────────────────
 *
 * Mounted under the same /console/* tree as the otech-tier routes —
 * the same SovereignConsoleLayout owns the auth gate + chrome — but
 * the page components target the SME unified-rbac surface.
 *
 *   /console/sme/users   → SMEUsersPage (POST + GET + DELETE
 *                          /api/v1/sme/users; the create form fires
 *                          the ADR-0003 3-step hook).
 *   /console/sme/roles   → SMERolesPage (read-only canonical
 *                          group → app-role map).
 *
 * Whether these routes are exposed in the sidebar is decided at
 * runtime by the SME-tenant-aware nav (see SovereignSidebar.tsx),
 * which reads the discovery payload from `getTenantContext()`.
 * Because TanStack Router resolves on URL match (not on sidebar
 * visibility), the routes themselves are always registered — that
 * keeps the bundle a single SPA per [Q-mine-1]/#795 and lets the
 * SME admin deep-link into either page from the welcome email.
 */
const consoleSMEUsersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sme/users',
  component: SMEUsersPage,
})

const consoleSMERolesRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sme/roles',
  component: SMERolesPage,
})

/* ── Multi-domain Sovereign — Parent Domains admin (issue #829) ────────
 *
 * Operator-admin "Add another parent domain" surface + DNS propagation
 * status panel. Mounted under /console/* so it sits behind the same
 * RequireSession + Sovereign-tier auth gate every other admin page uses.
 *
 *   /console/parent-domains    → ParentDomainsPage
 *
 * Visibility in the sidebar is decided in SovereignSidebar.tsx by
 * checking the operator-admin role; the route registration here is
 * always present so a deep-link from the welcome email still resolves.
 */
const consoleParentDomainsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/parent-domains',
  component: ParentDomainsPage,
})

// /console/sme/tenants/new — multi-domain SME tenant onboarding form
// (issue #828, parent epic #825). The page renders the parent-domain
// dropdown on free-subdomain mode and a CNAME-validation hint on BYO
// mode. Mounted under the same SovereignConsoleLayout so it's
// reachable from the operator-tier sidebar (decided at runtime by the
// SME-tenant-aware nav, see SovereignSidebar.tsx).
const consoleSMECreateTenantRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sme/tenants/new',
  component: SMECreateTenantPage,
})

/* ── Compliance dashboards — chroot Sovereign Console (slice U, #1096) ──
 *
 * Chroot mounts: `/sre/compliance`, `/sec/compliance`,
 * `/compliance/policy/$policyName`. Pages are the same components as
 * the mother-side `/admin/compliance/*` routes — `useResolvedDeploymentId`
 * resolves the active sovereign id from `/api/v1/sovereign/self` so the
 * components stay component-id agnostic.
 */
const consoleSREComplianceRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sre/compliance',
  component: SREDashboardPage,
})
const consoleSecComplianceRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sec/compliance',
  component: SecLeadDashboardPage,
})
const consoleCompliancePolicyDrilldownRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/compliance/policy/$policyName',
  component: PolicyDrilldownPage,
})
// Wave-2 Family-E (#1583, C11-008): /compliance/runtime — chroot
// mirror of /admin/compliance/runtime. Standalone Falco runtime-
// security alerts page so the operator can deep-link directly.
const consoleComplianceRuntimeRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/compliance/runtime',
  component: RuntimeAlertsPage,
})

/**
 * Standalone notifications surface for sovereign mode (TC-160 / 2026-05-07).
 *
 * Sister to `provisionNotificationsRoute` (mothership-side at
 * `/provision/$id/notifications`). On a Sovereign console the operator
 * has no `:deploymentId` in the URL — `NotificationsPage` resolves the
 * id via `useResolvedDeploymentId` (URL param, then /sovereign/self).
 *
 * Mounted under `consoleLayoutRoute` so it inherits the sidebar + header
 * + auth gate the rest of the Sovereign console pages share.
 */
const consoleNotificationsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/notifications',
  component: NotificationsPage,
})

/* ── Family F (Wave 3 → Wave 6) — BSS-in-console routes ─────────────────
 *
 * Founder #1 requirement (2026-05-17 family-F brief):
 *   "the backed of the the mark place mutst be just aotnerh menu under
 *    console like https://console.<sov>/bss"
 *
 * Wave 6 PR 1 (2026-05-17 UX follow-up) — founder rejected the iframe
 * BssLayout's bespoke tab strip as visually clashing with the rest of
 * the Sovereign Console. The new shape:
 *
 *   /bss                → BssLandingPage (native KPI dashboard +
 *                         section-nav grid, PortalShell chrome)
 *   /bss/billing        → BillingPage (PortalShell + iframe via
 *                         BssSectionShell; native port lands in Wave 6 PR 2)
 *   /bss/orders         → OrdersPage  (PortalShell + iframe; Wave 6 PR 3)
 *   /bss/revenue        → RevenuePage (native React, Wave 6 PR 4 —
 *                         drops iframe; KPI strip + line chart +
 *                         breakdown table)
 *   /bss/vouchers       → VouchersPage(PortalShell + iframe; Wave 6 PR 5)
 *   /bss/tenants        → TenantsPage (PortalShell + iframe; Wave 6 PR 6)
 *
 * Each section page is a sibling of the landing, not a child of a
 * shared layout — no more BssLayout wrapper. The sidebar's BSS group
 * (SovereignSidebar.tsx) is the canonical navigation; the landing's
 * inline section-nav grid is a secondary affordance.
 *
 * RBAC: still gated at two layers — the SovereignSidebar's BSS group
 * is admin-visible (unconditional for v1) and the SME gateway enforces
 * /back-office/* tier checks server-side for the iframe content.
 */
const consoleBssIndexRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss',
  component: BssLandingPage,
})
const consoleBssBillingRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss/billing',
  component: BssBillingPage,
})
const consoleBssOrdersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss/orders',
  component: BssOrdersPage,
})
const consoleBssRevenueRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss/revenue',
  component: BssRevenuePage,
})
const consoleBssVouchersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss/vouchers',
  component: BssVouchersPage,
})
const consoleBssTenantsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/bss/tenants',
  component: BssTenantsPage,
})

/* ── Wave 3 — Sandbox UI scaffold (sandbox-wave3-ui-scaffold) ──────────
 *
 * Per-Org agent-coding workspace under the chroot Sovereign Console.
 * See products/sandbox/docs/architecture.md §1 for the surface contract
 * (xterm.js host in browser ↔ in-pod pty-server ↔ agent CLI).
 *
 *   /sandbox            → SandboxLanding (6-agent picker grid + recent
 *                         sessions rail, PortalShell chrome)
 *   /sandbox/$id        → SandboxSession (xterm.js terminal host; Wave 2
 *                         wires the WebSocket attach to pty-server)
 *   /sandbox/settings   → SandboxSettings (BYOS Claude Max OAuth
 *                         Connect / Disconnect; wires to Wave 1b
 *                         /api/v1/sandbox/byos/claude-code/* stubs)
 *
 * SandboxSession's path-param is `$id` — TanStack Router's $-syntax —
 * matched against the Sandbox CR name (sandbox-<slug>). Static children
 * (`/sandbox/settings`) MUST be declared BEFORE the dynamic `$id`
 * sibling so the literal segment wins on /sandbox/settings — TanStack
 * matches in registration order. The route array below preserves that
 * ordering.
 */
const consoleSandboxIndexRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sandbox',
  component: SandboxLanding,
})
const consoleSandboxSettingsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sandbox/settings',
  component: SandboxSettings,
})
const consoleSandboxSessionRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/sandbox/$id',
  component: SandboxSession,
})

/* ── Sovereign-mode cloud legacy redirects (TC-090..092 / 2026-05-07) ─
 *
 * Sister set to LEGACY_CLOUD_REDIRECTS (which is mounted under the
 * mothership `/provision/$id/cloud` subtree). These are the SAME
 * redirects but rooted at sovereign-mode `/cloud/<legacy-path>`, so
 * deep-links / bookmarks / external links into a Sovereign console at
 * `console.<sov>/cloud/architecture`, `/cloud/compute`, etc. resolve
 * cleanly to the canonical `/cloud?view=...&kind=...` query shape
 * instead of TanStack Router's bare 404 page.
 *
 * Reuses LEGACY_CLOUD_REDIRECTS verbatim so the two redirect sets
 * cannot drift.
 */
const consoleLegacyCloudRedirectRoutes = LEGACY_CLOUD_REDIRECTS.map((r) =>
  createRoute({
    getParentRoute: () => consoleCloudRoute,
    path: r.path,
    component: NoopRedirectComponent,
    beforeLoad: () => {
      throw redirect({
        to: '/cloud' as never,
        search: r.search as never,
        replace: true,
      })
    },
  }),
)

/* ── Target-state /app/$deploymentId/* tree (qa-loop iter-6 Cluster-A) ──
 *
 * Per founder rule (`feedback_no_mvp_no_workarounds.md`): the iter-6
 * test matrix is the contract. Operator URLs must live under
 * `/app/$deploymentId/<feature>/<sub>` — `applications`, `resources`,
 * `rbac`, `users`, `blueprints`, `install`, `networking`, `continuum`,
 * `shells`, `organizations`, `settings`, plus mothership-level
 * `/app/dashboard`, `/app/install/*`, `/app/sre/compliance`, and
 * `/app/sec/compliance`.
 *
 * These routes are mounted as ALIASES that re-use the canonical page
 * components from /provision/$deploymentId/* and /admin/* — there is
 * NO duplicated content. Pages whose feature isn't yet implemented
 * (Networking, Continuum, Resources Apply / Search / Pod logs) get
 * minimal stub pages under `pages/sovereign/stubs/` that mount the
 * canonical chrome + a section-title token; other Fix Authors will
 * grow them into full surfaces.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (no compromise) — the routes
 * use the same provisionAuthGuard the /provision/* tree uses, so the
 * auth contract is identical across both URL trees.
 */

// /app/dashboard already mounted under appRoute; nothing extra needed.

// /app/install + /app/install/$blueprintName — mothership marketplace
// install entry point (no deploymentId in URL — InstallPage falls back
// to useResolvedDeploymentId via /sovereign/self).
const appInstallRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/install',
  component: InstallPage,
})
const appInstallBlueprintRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/install/$blueprintName',
  component: () => {
    const { blueprintName } = appInstallBlueprintRoute.useParams() as { blueprintName: string }
    return <InstallPage preselectedBlueprint={blueprintName} />
  },
})

// /app/sre/compliance + /app/sec/compliance — mother-side compliance
// dashboards (sister to /admin/compliance/{sre,security}).
const appSREComplianceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/sre/compliance',
  component: SREDashboardPage,
})
const appSecComplianceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/sec/compliance',
  component: SecLeadDashboardPage,
})

// /app/$deploymentId — landing (re-uses AppsPage like /provision/$deploymentId).
const appDeploymentRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId',
  component: AppsPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/applications — alias of AppsPage.
const appAppsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/applications',
  component: AppsPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/applications/$componentId — alias of AppDetail.
const appAppDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/applications/$componentId',
  component: AppDetail,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/applications/$componentId/$tab — AppDetail with
// the matrix-asserted /compliance sub-path. AppDetail reads the active
// tab from useParams (already strict:false), so adding the $tab segment
// just lands on the right tab without a separate component.
const appAppDetailTabRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/applications/$componentId/$tab',
  component: AppDetail,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/install + /app/$deploymentId/install/$blueprintName.
const appDeploymentInstallRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/install',
  component: InstallPage,
  beforeLoad: provisionAuthGuard,
})
const appDeploymentInstallBlueprintRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/install/$blueprintName',
  component: () => {
    const { blueprintName } = appDeploymentInstallBlueprintRoute.useParams() as {
      blueprintName: string
    }
    return <InstallPage preselectedBlueprint={blueprintName} />
  },
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/blueprints/{publish,curate}.
const appBlueprintsPublishRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/blueprints/publish',
  component: BlueprintPublishPage,
  beforeLoad: provisionAuthGuard,
})
const appBlueprintsCurateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/blueprints/curate',
  component: BlueprintCuratePage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/users/{,new,$name}.
const appUsersListRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/users',
  component: UserAccessListPage,
  beforeLoad: provisionAuthGuard,
})
const appUsersNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/users/new',
  component: UserAccessEditPage,
  beforeLoad: provisionAuthGuard,
})
const appUsersEditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/users/$name',
  component: UserAccessEditPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/rbac/{grant,groups,roles,matrix,audit}.
const appRBACMultiGrantRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/rbac/grant',
  component: MultiGrantEditPage,
  beforeLoad: provisionAuthGuard,
})
const appRBACGroupsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/rbac/groups',
  component: GroupBrowserPage,
  beforeLoad: provisionAuthGuard,
})
const appRBACRolesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/rbac/roles',
  component: RoleBrowserPage,
  beforeLoad: provisionAuthGuard,
})
const appRBACMatrixRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/rbac/matrix',
  component: AccessMatrixPage,
  beforeLoad: provisionAuthGuard,
})
const appRBACAuditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/rbac/audit',
  component: AuditPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/organizations/$orgId/members.
const appOrgMembersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/organizations/$orgId/members',
  component: OrgMembersPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/settings.
const appSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/settings',
  component: SettingsPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/shells/sessions{,/$sessionId}.
const appShellsSessionsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/shells/sessions',
  component: SessionsRoute,
  beforeLoad: provisionAuthGuard,
})
const appShellsSessionDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/shells/sessions/$sessionId',
  component: SessionsRoute,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/networking{,/$slug} — qa-loop iter-11 Fix #48.
// The index route mounts at the bare `/networking` URL and the
// sub-route at `/networking/$slug` (policies | clustermesh | netbird |
// dmz | hubble). Both render the same NetworkingPage which dispatches
// on the URL slug.
const appNetworkingIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/networking',
  component: NetworkingPage,
  beforeLoad: provisionAuthGuard,
})
const appNetworkingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/networking/$slug',
  component: NetworkingPage,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/continuum{,/$continuumId{,/audit,/settings}}.
const appContinuumListRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/continuum',
  component: () => <ContinuumPage mode="list" />,
  beforeLoad: provisionAuthGuard,
})
const appContinuumOverviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/continuum/$continuumId',
  component: () => <ContinuumPage mode="overview" />,
  beforeLoad: provisionAuthGuard,
})
const appContinuumAuditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/continuum/$continuumId/audit',
  component: () => <ContinuumPage mode="audit" />,
  beforeLoad: provisionAuthGuard,
})
const appContinuumSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/continuum/$continuumId/settings',
  component: () => <ContinuumPage mode="settings" />,
  beforeLoad: provisionAuthGuard,
})

// /app/$deploymentId/resources/* — order matters: STATIC sub-paths
// (/apply, /search) must be registered BEFORE the dynamic $kind so
// TanStack resolves them first.
const appResourcesIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources',
  component: ResourcesListPage,
  beforeLoad: provisionAuthGuard,
})
const appResourcesApplyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/apply',
  component: ResourcesApplyPage,
  beforeLoad: provisionAuthGuard,
})
const appResourcesSearchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/search',
  component: ResourcesSearchPage,
  beforeLoad: provisionAuthGuard,
})
const appResourcesKindRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/$kind',
  component: ResourcesListPage,
  beforeLoad: provisionAuthGuard,
})
const appResourcesKindNsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/$kind/$ns',
  component: ResourcesListPage,
  beforeLoad: provisionAuthGuard,
})
const appResourceDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/$kind/$ns/$name',
  component: ResourceDetailNoTabPage,
  beforeLoad: provisionAuthGuard,
})
// Pod-specific /logs sub-route (no $tab segment in target-state shape).
const appPodLogsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/$deploymentId/resources/pods/$ns/$name/logs',
  component: PodLogsPage,
  beforeLoad: provisionAuthGuard,
})

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  loginVerifyRoute,
  authCallbackRoute,
  signupRoute,
  forgotRoute,
  authHandoverRoute,
  authHandoverErrorRoute,
  appRoute.addChildren(
    // D17 PR G (2026-05-17 t136 bug fix): on Sovereign Console
    // (chroot, console.<sov-fqdn>), the `/app/$deploymentId` dynamic
    // route under appRoute catches `/app/bp-alloy` BEFORE the chroot's
    // `consoleAppDetailRoute` at `/app/$componentId` (under
    // consoleLayoutRoute), because appRoute.addChildren registers
    // earlier in the rootRoute children. TanStack matches by
    // declaration order on equally-specific dynamic routes, so the
    // Sovereign side rendered AppsPage (catalog grid) instead of
    // AppDetail. Founder caught on t136: "/app/bp-alloy still shows
    // catalog like view, individual pages are not opening".
    //
    // Fix: filter the children list to exclude the mother-only
    // `/$deploymentId` catch-alls when running on Sovereign mode. The
    // routes are defined at module load and DETECTED_MODE.mode never
    // flips during a page lifetime, so this is safe to evaluate once
    // at routeTree build time.
    DETECTED_MODE.mode === 'sovereign'
      ? [
          // Sovereign-mode appRoute children — EXCLUDES every
          // mother-only `/$deploymentId/*` route so the chroot's
          // consoleAppDetailRoute at `/app/$componentId` can claim
          // `/app/bp-alloy` etc. The few mother-only static paths
          // still listed here are no-ops on Sovereign (the beforeLoad
          // on each redirects to the per-Sovereign equivalent).
          dashboardRoute,
        ]
      : [
          dashboardRoute,
          crossSovApplicationsRoute,
          fleetTreemapRoute,
          // qa-loop iter-6 Cluster-A — target-state /app/* routes.
          // STATIC paths first so TanStack resolves them before the
          // dynamic $deploymentId catch-all.
          appInstallRoute,
          appInstallBlueprintRoute,
          appSREComplianceRoute,
          appSecComplianceRoute,
          // /app/$deploymentId tree.
          appDeploymentRoute,
          appAppsRoute,
          appAppDetailRoute,
          appAppDetailTabRoute,
          appDeploymentInstallRoute,
          appDeploymentInstallBlueprintRoute,
          appBlueprintsPublishRoute,
          appBlueprintsCurateRoute,
          appUsersListRoute,
          appUsersNewRoute,
          appUsersEditRoute,
          appRBACMultiGrantRoute,
          appRBACGroupsRoute,
          appRBACRolesRoute,
          appRBACMatrixRoute,
          appRBACAuditRoute,
          appOrgMembersRoute,
          appSettingsRoute,
          appShellsSessionsRoute,
          appShellsSessionDetailRoute,
          appNetworkingIndexRoute,
          appNetworkingRoute,
          appContinuumListRoute,
          appContinuumOverviewRoute,
          appContinuumAuditRoute,
          appContinuumSettingsRoute,
          // Resources — static sub-paths first.
          appResourcesApplyRoute,
          appResourcesSearchRoute,
          appResourcesIndexRoute,
          appResourcesKindRoute,
          appResourcesKindNsRoute,
          appPodLogsRoute,
          appResourceDetailRoute,
        ],
  ),
  wizardLayoutRoute.addChildren([wizardRoute]),
  successRoute,
  deploymentsListRoute,
  provisionRoute,
  provisionAppRoute,
  provisionInstallRoute,
  provisionInstallBlueprintRoute,
  provisionJobsRoute,
  provisionJobsTimelineRoute,
  provisionJobDetailRoute,
  provisionDashboardRoute,
  provisionDecommissionRoute,
  provisionCloudRoute.addChildren(legacyCloudRedirectRoutes),
  provisionResourceDetailRoute,
  provisionSessionsRoute,
  provisionInfrastructureRoute.addChildren([
    provisionInfrastructureIndexRoute,
    ...infraLegacyRedirectRoutes,
  ]),
  provisionUsersListRoute,
  provisionUsersNewRoute,
  provisionUsersEditRoute,
  // EPIC-3 (#1098) slice U1+U3+U4 — multi-grant editor + group/role browsers.
  provisionRBACMultiGrantRoute,
  provisionRBACGroupsRoute,
  provisionRBACRolesRoute,
  // EPIC-3 (#1098) slice U5-U8 — member views (per-org members,
  // access matrix, audit trail). U5 (per-app Members tab) is mounted
  // inside AppDetail and so doesn't appear here.
  provisionRBACMatrixRoute,
  provisionRBACAuditRoute,
  provisionOrgMembersRoute,
  // EPIC-2 (#1097) slice P — Blueprint publishing + Curate routes.
  provisionBlueprintsPublishRoute,
  provisionBlueprintsCurateRoute,
  provisionSettingsRoute,
  provisionNotificationsRoute,
  // Compliance — slice U (#1096). Mother-side admin routes.
  adminComplianceSREDashboardRoute,
  adminComplianceSecurityDashboardRoute,
  adminCompliancePolicyDrilldownRoute,
  // Wave-2 Family-E (#1583, C11-008): standalone Falco runtime alerts.
  adminComplianceRuntimeRoute,
  legacyProvisionRoute,
  designsRoute,
  designsJobsDepsVizRoute,
  sovereigntyPreviewRoute,
  marketplaceFamilyRoute,
  marketplaceProductRoute,
  consoleLayoutRoute.addChildren([
    consoleDashboardRoute,
    consoleAppsRoute,
    consoleAppDetailRoute,
    consoleCatalogDetailRoute,
    consoleInstallRoute,
    consoleInstallBlueprintRoute,
    consoleJobsRoute,
    consoleJobsTimelineRoute,
    consoleJobDetailRoute,
    consoleCloudRoute.addChildren(consoleLegacyCloudRedirectRoutes),
    consoleResourceDetailRoute,
    consoleSessionsRoute,
    consoleUsersRoute,
    consoleUsersNewRoute,
    consoleUsersEditRoute,
    // EPIC-3 (#1098) slice U1+U3+U4 — RBAC management chroot routes.
    consoleRBACMultiGrantRoute,
    consoleRBACGroupsRoute,
    consoleRBACRolesRoute,
    // EPIC-3 (#1098) slice U5-U8 — RBAC member views chroot routes.
    consoleRBACMatrixRoute,
    consoleRBACAuditRoute,
    consoleOrgMembersRoute,
    // EPIC-2 (#1097) slice P — Blueprint publishing + Curate chroot routes.
    consoleBlueprintsPublishRoute,
    consoleBlueprintsCurateRoute,
    consoleSettingsRoute,
    consoleSMEUsersRoute,
    consoleSMERolesRoute,
    consoleParentDomainsRoute,
    consoleSMECreateTenantRoute,
    // Compliance dashboards — chroot routes (slice U, #1096).
    consoleSREComplianceRoute,
    consoleSecComplianceRoute,
    consoleCompliancePolicyDrilldownRoute,
    // Wave-2 Family-E (#1583, C11-008): chroot Falco runtime alerts.
    consoleComplianceRuntimeRoute,
    consoleNotificationsRoute,
    // Family F (Wave 3 → Wave 6) — BSS-in-console.
    // /bss is a native landing (BssLandingPage); each section is a
    // sibling that wraps in PortalShell via BssSectionShell.
    consoleBssIndexRoute,
    consoleBssBillingRoute,
    consoleBssOrdersRoute,
    consoleBssRevenueRoute,
    consoleBssVouchersRoute,
    consoleBssTenantsRoute,
    // Wave 3 — Sandbox UI scaffold. Static /sandbox/settings registered
    // before /sandbox/$id so the literal segment wins on path match.
    consoleSandboxIndexRoute,
    consoleSandboxSettingsRoute,
    consoleSandboxSessionRoute,
  ]),
])

// basepath is resolved at runtime (see top of file).
// Catalyst-Zero (contabo): '/sovereign' — browser URL has /sovereign prefix.
// Sovereign clusters: '/' — served at the domain root.
export const router = createRouter({ routeTree, basepath })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
