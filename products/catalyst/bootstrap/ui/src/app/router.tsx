import { createRouter, createRoute, createRootRoute, redirect, isRedirect } from '@tanstack/react-router'
import { IS_SAAS } from '@/shared/constants/env'
import { API_BASE } from '@/shared/config/urls'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { setProvisionFlashBanner } from '@/shared/lib/flashBanner'

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
const basepath =
  typeof window !== 'undefined' && window.location.pathname.startsWith('/sovereign')
    ? '/sovereign'
    : '/'

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
import { DashboardPage } from '@/pages/dashboard/DashboardPage'
import { WizardPage } from '@/pages/wizard/WizardPage'
import { SuccessPage } from '@/pages/success/SuccessPage'
import { DesignShowcase } from '@/pages/designs/DesignShowcase'
import { JobsDepsVizDemo } from '@/pages/designs/JobsDepsVizDemo'
import { MarketplaceFamilyPage } from '@/pages/marketplace/MarketplaceFamilyPage'
import { MarketplaceProductPage } from '@/pages/marketplace/MarketplaceProductPage'
import { ProvisionPage } from '@/pages/provision/ProvisionPage'
import { AppsPage } from '@/pages/sovereign/AppsPage'
import { AppDetail } from '@/pages/sovereign/AppDetail'
import { JobsPage } from '@/pages/sovereign/JobsPage'
import { JobDetail } from '@/pages/sovereign/JobDetail'
import { JobsTimeline } from '@/pages/sovereign/JobsTimeline'
import { Dashboard } from '@/pages/sovereign/Dashboard'
import { CloudPage } from '@/pages/sovereign/CloudPage'
import { DecommissionPage } from '@/pages/sovereign/DecommissionPage'
import { UserAccessListPage } from '@/pages/admin/user-access/UserAccessListPage'
import { UserAccessEditPage } from '@/pages/admin/user-access/UserAccessEditPage'
import { SettingsPage } from '@/pages/sovereign/SettingsPage'
import { NotificationsPage } from '@/pages/sovereign/NotificationsPage'
import { ConsoleDashboardPage } from '@/pages/sovereign/console/ConsoleDashboardPage'
import { ConsoleAppsPage } from '@/pages/sovereign/console/ConsoleAppsPage'
import { ConsoleJobsPage } from '@/pages/sovereign/console/ConsoleJobsPage'
import { ConsoleCloudPage } from '@/pages/sovereign/console/ConsoleCloudPage'
import { ConsoleUsersPage } from '@/pages/sovereign/console/ConsoleUsersPage'
import { ConsoleSettingsPage } from '@/pages/sovereign/console/ConsoleSettingsPage'
import { MarketplaceSettings } from '@/pages/sovereign/settings/MarketplaceSettings'
import { CatalogAdminPage } from '@/pages/sovereign/CatalogAdminPage'
import { DeploymentsList } from '@/pages/sovereign/DeploymentsList'
import { SovereigntyPreviewPage } from '@/pages/sovereignty/SovereigntyPreviewPage'

// Root
const rootRoute = createRootRoute({ component: RootLayout })

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
    if (DETECTED_MODE.mode === 'sovereign') throw redirect({ to: '/console/dashboard' as never })
    throw redirect({ to: '/wizard' })
  },
})

// Auth routes
const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: LoginPage })
// /login/verify — PIN-entry step of the 6-digit auth flow (issue #688).
// Mounted at sibling depth (not nested under /login) so a refresh on the
// verify URL doesn't lose its email/requestId search params to a parent
// loader.
const loginVerifyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login/verify',
  component: VerifyPinPage,
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
    throw redirect({ to: '/console/dashboard' as never, replace: true })
  },
  component: () => null,
})

// App routes
const appRoute = createRoute({ getParentRoute: () => rootRoute, path: '/app', component: AppLayout })
const dashboardRoute = createRoute({ getParentRoute: () => appRoute, path: '/dashboard', component: DashboardPage })

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
      // Anonymous — flash a banner the wizard can render, then redirect.
      setProvisionFlashBanner('Sign in to view your deployments')
      throw redirect({ to: '/wizard', replace: true })
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
// effectively Catalyst-Zero only). The page itself reads useSession()
// and renders an anonymous-only "Sign in" prompt when no cookie is
// present, so we don't gate the route — keeps it consistent with the
// wizard's guest-mode pattern.
const deploymentsListRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/deployments',
  component: DeploymentsList,
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

const provisionCloudRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/cloud',
  component: CloudPage,
  beforeLoad: provisionAuthGuard,
  validateSearch: (raw: Record<string, unknown>): CloudSearch => {
    const out: CloudSearch = {}
    if (raw.view === 'graph' || raw.view === 'list') out.view = raw.view
    if (typeof raw.kind === 'string' && raw.kind.length > 0) out.kind = raw.kind
    return out
  },
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
        to: '/provision/$deploymentId/cloud' as never,
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
      to: '/provision/$deploymentId/cloud' as never,
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
        to: '/provision/$deploymentId/cloud' as never,
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

// Legacy DAG provision view — preserved at a sub-path so existing
// links and CI smoke tests (which still curl `/provision/legacy/...`)
// don't 404 mid-rollout.
const legacyProvisionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/legacy/$deploymentId',
  component: ProvisionPage,
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

const consoleLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/console',
  component: SovereignConsoleLayout,
})

// /console → redirect to /console/dashboard
const consoleIndexRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/console/dashboard' as never, replace: true })
  },
  component: () => null,
})

const consoleDashboardRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/dashboard',
  component: ConsoleDashboardPage,
})

const consoleAppsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/apps',
  component: ConsoleAppsPage,
})

const consoleJobsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/jobs',
  component: ConsoleJobsPage,
})

const consoleCloudRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/cloud',
  component: ConsoleCloudPage,
})

const consoleUsersRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/users',
  component: ConsoleUsersPage,
})

const consoleSettingsRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/settings',
  component: ConsoleSettingsPage,
})

// /console/settings/marketplace — operator toggles marketplace mode on a
// live Sovereign (issue #710 wave 3b). The page POSTs to
// /api/v1/sovereigns/{id}/marketplace which commits the per-Sovereign
// overlay change to the GitOps repo so Flux reconciles the chart.
const consoleSettingsMarketplaceRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/settings/marketplace',
  component: MarketplaceSettings,
})

// /console/catalog — Sovereign-console operator's per-row marketplace
// publishing toggle (issue #710 wave 2.5). Backend support shipped in
// PR #724: GET /catalog/apps + PATCH /catalog/admin/apps/{slug}/publish.
const consoleCatalogRoute = createRoute({
  getParentRoute: () => consoleLayoutRoute,
  path: '/catalog',
  component: CatalogAdminPage,
})

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  loginVerifyRoute,
  authCallbackRoute,
  signupRoute,
  forgotRoute,
  authHandoverRoute,
  appRoute.addChildren([dashboardRoute]),
  wizardLayoutRoute.addChildren([wizardRoute]),
  successRoute,
  deploymentsListRoute,
  provisionRoute,
  provisionAppRoute,
  provisionJobsRoute,
  provisionJobsTimelineRoute,
  provisionJobDetailRoute,
  provisionDashboardRoute,
  provisionDecommissionRoute,
  provisionCloudRoute.addChildren(legacyCloudRedirectRoutes),
  provisionInfrastructureRoute.addChildren([
    provisionInfrastructureIndexRoute,
    ...infraLegacyRedirectRoutes,
  ]),
  provisionUsersListRoute,
  provisionUsersNewRoute,
  provisionUsersEditRoute,
  provisionSettingsRoute,
  provisionNotificationsRoute,
  legacyProvisionRoute,
  designsRoute,
  designsJobsDepsVizRoute,
  sovereigntyPreviewRoute,
  marketplaceFamilyRoute,
  marketplaceProductRoute,
  consoleLayoutRoute.addChildren([
    consoleIndexRoute,
    consoleDashboardRoute,
    consoleAppsRoute,
    consoleJobsRoute,
    consoleCloudRoute,
    consoleUsersRoute,
    consoleCatalogRoute,
    consoleSettingsRoute,
    consoleSettingsMarketplaceRoute,
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
