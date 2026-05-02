import { createRouter, createRoute, createRootRoute, redirect } from '@tanstack/react-router'
import { IS_SAAS } from '@/shared/constants/env'
import { API_BASE } from '@/shared/config/urls'

// Lazy page imports
import { RootLayout } from './layouts/RootLayout'
import { AppLayout } from './layouts/AppLayout'
import { WizardLayout } from './layouts/WizardLayout'

import { LoginPage } from '@/pages/auth/LoginPage'
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

// Root
const rootRoute = createRootRoute({ component: RootLayout })

// Index redirect
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  beforeLoad: () => {
    if (IS_SAAS) throw redirect({ to: '/login' })
    throw redirect({ to: '/wizard' })
  },
})

// Auth routes
const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: LoginPage })
const signupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/signup', component: SignupPage })
const forgotRoute = createRoute({ getParentRoute: () => rootRoute, path: '/forgot', component: ForgotPage })

// App routes
const appRoute = createRoute({ getParentRoute: () => rootRoute, path: '/app', component: AppLayout })
const dashboardRoute = createRoute({ getParentRoute: () => appRoute, path: '/dashboard', component: DashboardPage })

// Wizard
const wizardLayoutRoute = createRoute({ getParentRoute: () => rootRoute, path: '/wizard', component: WizardLayout })
const wizardRoute = createRoute({ getParentRoute: () => wizardLayoutRoute, path: '/', component: WizardPage })

// Success (full-screen)
const successRoute = createRoute({ getParentRoute: () => rootRoute, path: '/success', component: SuccessPage })

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
})

// Global jobs list — table view (issue #204 founder spec). Each row is
// a clickable link that navigates to the per-job detail page.
const provisionJobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs',
  component: JobsPage,
})

// Jobs timeline (Gantt-style retrospective). Static segment, MUST be
// registered BEFORE the dynamic $jobId route below so TanStack Router
// resolves `/jobs/timeline` to this surface, not to JobDetail with
// jobId="timeline".
const provisionJobsTimelineRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/timeline',
  component: JobsTimeline,
})

// Per-Job detail page (epic #204).
const provisionJobDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/$jobId',
  component: JobDetail,
})

// Sovereign Dashboard — resource-utilisation treemap (founder spec).
const provisionDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/dashboard',
  component: Dashboard,
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
})
const provisionUsersNewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/users/new',
  component: UserAccessEditPage,
})
const provisionUsersEditRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/users/$name',
  component: UserAccessEditPage,
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

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  signupRoute,
  forgotRoute,
  appRoute.addChildren([dashboardRoute]),
  wizardLayoutRoute.addChildren([wizardRoute]),
  successRoute,
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
  legacyProvisionRoute,
  designsRoute,
  designsJobsDepsVizRoute,
  marketplaceFamilyRoute,
  marketplaceProductRoute,
])

// basepath mirrors Vite's `base: '/sovereign/'` so internal <Link> and
// router.navigate calls emit URLs prefixed with /sovereign/.
export const router = createRouter({ routeTree, basepath: '/sovereign' })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
