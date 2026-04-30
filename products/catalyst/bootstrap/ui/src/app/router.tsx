import { createRouter, createRoute, createRootRoute, redirect } from '@tanstack/react-router'
import { IS_SAAS } from '@/shared/constants/env'

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
import { FlowPage } from '@/pages/sovereign/FlowPage'
import { Dashboard } from '@/pages/sovereign/Dashboard'
import { BatchDetail } from '@/pages/sovereign/BatchDetail'
import { CloudPage } from '@/pages/sovereign/CloudPage'
import { InfrastructureTopology } from '@/pages/sovereign/InfrastructureTopology'
import { InfrastructureCompute } from '@/pages/sovereign/InfrastructureCompute'
import { InfrastructureStorage } from '@/pages/sovereign/InfrastructureStorage'
import { InfrastructureNetwork } from '@/pages/sovereign/InfrastructureNetwork'

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

// Provision — Sovereign Admin landing surface, pixel-ported from
// core/console/src/components/AppsPage.svelte (Deployments + Catalog
// tabs + auto-fit card grid). Replaces the legacy DAG view + the
// invented "AdminPage" surface with the canonical console shell.
// StepReview redirects here on submit, so the URL shape stays stable.
const provisionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId',
  component: AppsPage,
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
//
// v3 (PR feat/flow-canvas-polish-and-routing) — the previous
// `?view=table|flow` Tab strip was removed. The Flow surface lives at
// its own /flow route below. JobsPage now has a "Show as Flow" button
// in the header that links to /flow?scope=all.
const provisionJobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs',
  component: JobsPage,
})

// Per-deployment flow canvas — every job (or one batch) as bubbles in
// a Sugiyama-laid DAG. Founder spec (this PR):
//   • ?scope=all              → render every job in the deployment
//   • ?scope=batch:<batchId>  → filter to a single batch
//   • ?view=jobs|batches      → mode toggle (default = jobs)
const provisionFlowRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/flow',
  component: FlowPage,
  validateSearch: (raw: Record<string, unknown>): {
    scope?: string
    view?: 'jobs' | 'batches'
  } => {
    const out: { scope?: string; view?: 'jobs' | 'batches' } = {}
    const scope = raw?.scope
    if (typeof scope === 'string' && scope.length > 0) out.scope = scope
    const view = raw?.view
    if (view === 'jobs' || view === 'batches') out.view = view
    return out
  },
})

// Jobs timeline (Gantt-style retrospective). Static segment, MUST be
// registered BEFORE the dynamic $jobId route below so TanStack Router
// resolves `/jobs/timeline` to this surface, not to JobDetail with
// jobId="timeline". Stretch deliverable for epic openova-io/openova#204
// item 11 (sub-ticket #206).
const provisionJobsTimelineRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/timeline',
  component: JobsTimeline,
})

// Per-Job detail page (epic #204) — surfaces the GitLab-CI-runner-style
// execution log viewer + Dependencies + Apps tabs. Reachable from the
// JobsTable row link and from deep links shared in Slack / runbook /
// failure email. The path lives under /provision/$deploymentId/jobs/
// $jobId so it is namespaced by deployment, not the legacy /job/$jobId
// pattern that the cosmetic guards still reject for the main JobsPage
// row click.
const provisionJobDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs/$jobId',
  component: JobDetail,
})

// Sovereign Dashboard — resource-utilisation treemap (founder spec).
// Box area = allocated capacity, colour = utilisation/health/age. Lives
// alongside the AppsPage / JobsPage Sovereign-portal surfaces under the
// same /provision/$deploymentId namespace so the sidebar nav entry
// resolves with the same tanstack-router params as its siblings.
const provisionDashboardRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/dashboard',
  component: Dashboard,
})

// Sovereign Cloud surface (issue #309 supersedes #227/#228) — the
// previous "Infrastructure" section is renamed to "Cloud" and its
// in-page tab strip is replaced by an accordion in the left sidebar
// (see Sidebar.tsx). The shell renders header + an <Outlet />; bare
// /cloud redirects to the architecture sub-route so the URL shape is
// always explicit.
//
// The legacy /infrastructure/* routes below are preserved for now and
// render the same components — a follow-up commit converts them to
// 301-style redirects to the /cloud/* equivalents. Keeping both
// resolvable in this initial commit keeps the diff additive.
const provisionCloudRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/cloud',
  component: CloudPage,
})

const provisionCloudIndexRoute = createRoute({
  getParentRoute: () => provisionCloudRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/provision/$deploymentId/cloud/architecture',
      params,
    })
  },
})

const provisionCloudArchitectureRoute = createRoute({
  getParentRoute: () => provisionCloudRoute,
  path: '/architecture',
  component: InfrastructureTopology,
})

const provisionCloudComputeRoute = createRoute({
  getParentRoute: () => provisionCloudRoute,
  path: '/compute',
  component: InfrastructureCompute,
})

const provisionCloudStorageRoute = createRoute({
  getParentRoute: () => provisionCloudRoute,
  path: '/storage',
  component: InfrastructureStorage,
})

const provisionCloudNetworkRoute = createRoute({
  getParentRoute: () => provisionCloudRoute,
  path: '/network',
  component: InfrastructureNetwork,
})

// Legacy /infrastructure/* — every legacy path now redirects to its
// /cloud/* equivalent so deep links and bookmarks keep working
// without rendering the renamed surface twice. The components are
// no-op stubs because tanstack-router still needs a `component` for
// the route node to resolve before `beforeLoad` fires.
const NoopRedirectComponent = () => null

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
      to: '/provision/$deploymentId/cloud/architecture',
      params,
    })
  },
  component: NoopRedirectComponent,
})

const provisionInfrastructureTopologyRoute = createRoute({
  getParentRoute: () => provisionInfrastructureRoute,
  path: '/topology',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/provision/$deploymentId/cloud/architecture',
      params,
    })
  },
  component: NoopRedirectComponent,
})

const provisionInfrastructureComputeRoute = createRoute({
  getParentRoute: () => provisionInfrastructureRoute,
  path: '/compute',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/provision/$deploymentId/cloud/compute',
      params,
    })
  },
  component: NoopRedirectComponent,
})

const provisionInfrastructureStorageRoute = createRoute({
  getParentRoute: () => provisionInfrastructureRoute,
  path: '/storage',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/provision/$deploymentId/cloud/storage',
      params,
    })
  },
  component: NoopRedirectComponent,
})

const provisionInfrastructureNetworkRoute = createRoute({
  getParentRoute: () => provisionInfrastructureRoute,
  path: '/network',
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/provision/$deploymentId/cloud/network',
      params,
    })
  },
  component: NoopRedirectComponent,
})

// Per-Batch detail page (epic #204 item #4) — surfaces a single batch
// progress card at the top + a JobsTable filtered to that batch's
// rows. Reachable from the batch chip in any JobsTable row (both
// JobsPage and AppDetail's Jobs tab). Founder verbatim:
//   "the progress bar needs to be shown only when I click a specific
//    batch and it shows the batch page along with its batch progress
//    at the top"
const provisionBatchDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/batches/$batchId',
  component: BatchDetail,
})

// Legacy DAG provision view — preserved at a sub-path so existing
// links and CI smoke tests (which still curl `/provision/legacy/...`)
// don't 404 mid-rollout. Once the public smoke tests move to the new
// /provision/$deploymentId surface, this route can be removed.
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
  provisionFlowRoute,
  provisionJobsTimelineRoute,
  provisionJobDetailRoute,
  provisionDashboardRoute,
  provisionCloudRoute.addChildren([
    provisionCloudIndexRoute,
    provisionCloudArchitectureRoute,
    provisionCloudComputeRoute,
    provisionCloudStorageRoute,
    provisionCloudNetworkRoute,
  ]),
  provisionInfrastructureRoute.addChildren([
    provisionInfrastructureIndexRoute,
    provisionInfrastructureTopologyRoute,
    provisionInfrastructureComputeRoute,
    provisionInfrastructureStorageRoute,
    provisionInfrastructureNetworkRoute,
  ]),
  provisionBatchDetailRoute,
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
