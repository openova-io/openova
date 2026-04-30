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
import { BatchDetail } from '@/pages/sovereign/BatchDetail'

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
// a clickable link that navigates to the per-job detail page (owned by
// the JobDetail sibling agent and merged via #208).
const provisionJobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/provision/$deploymentId/jobs',
  component: JobsPage,
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
  provisionJobsTimelineRoute,
  provisionJobDetailRoute,
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
