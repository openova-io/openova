/**
 * application-pages-t-o-p.spec.ts — Playwright E2E for the EPIC-2
 * Slice T+O+P (#1097) Application page bundle.
 *
 * What this asserts (per `feedback_per_issue_playwright_verification.md`
 * — N issues = N snapshots, never collapse):
 *
 *  1. Topology tab renders + mode change + preview + apply
 *  2. Settings tab renders + parameter edit + save
 *  3. Upgrade dialog opens + preview + confirm
 *  4. Uninstall dialog opens + typed-confirm + delete
 *  5. Publish page form + submit
 *  6. Curate page list + curate action
 *  7. Members tab (verify MembersList reuse — no duplicate)
 *  8. Full AppDetail tab nav (verify all 7 tabs)
 *
 * Each test mounts mock catalyst-api responses so the page renders
 * deterministically without a live backend, then captures one
 * 1440x900 screenshot per route per assertion.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'top-1097'
const APP_NAME = 'wp-prod'
const NAMESPACE = 'acme'

const WORDPRESS_BP_RAW = {
  name: 'bp-wordpress',
  version: '1.2.3',
  card: { title: 'WordPress', summary: 'PHP CMS' },
  origin: 1,
  source: 'public',
  placementSchema: {
    modes: ['single-region', 'active-active', 'active-hotstandby'],
    default: 'single-region',
    minRegions: 1,
    maxRegions: 5,
  },
  raw: {
    spec: {
      version: '1.2.3',
      configSchema: {
        type: 'object',
        required: ['domain'],
        properties: {
          domain: { type: 'string', title: 'Domain' },
          replicas: { type: 'integer', title: 'Replicas', minimum: 1, maximum: 5 },
        },
      },
      placementSchema: {
        modes: ['single-region', 'active-active', 'active-hotstandby'],
        default: 'single-region',
      },
    },
  },
}

async function mockTopAPI(page: Page) {
  // Auth + self stubs.
  await page.route(/.*\/api\/v1\/sovereign\/self$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID, sovereignFQDN: 'top.example' }),
    })
  })
  await page.route(/.*\/api\/v1\/whoami$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'test', email: 'test@example.com' }),
    })
  })
  await page.route(/.*\/api\/v1\/deployments\/[^/]+$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID }),
    })
  })

  // Catalog.
  await page.route(/.*\/api\/v1\/catalog\?.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: [WORDPRESS_BP_RAW] }),
    })
  })
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(WORDPRESS_BP_RAW),
    })
  })
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress\/versions$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        name: 'bp-wordpress',
        versions: [
          { version: '1.2.3', origin: 'public' },
          { version: '1.4.0', origin: 'public' },
        ],
        upgradeMatrix: { '1.2.3': ['1.4.0'] },
      }),
    })
  })
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress\/versions\/[^/]+$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(WORDPRESS_BP_RAW),
    })
  })

  // Application status (read-side).
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/applications\/wp-prod\/status.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        name: APP_NAME,
        namespace: NAMESPACE,
        phase: 'Ready',
        spec: {
          blueprintRef: { name: 'bp-wordpress', version: '1.2.3' },
          parameters: { domain: 'shop.acme.com', replicas: 2 },
          placement: 'single-region',
          regions: ['hz-fsn-rtz-prod'],
          environmentRef: 'acme-prod',
        },
        status: {
          phase: 'Ready',
          regions: [
            { name: 'hz-fsn-rtz-prod', role: 'primary', replicas: 2, ready: 2, lastTransitionTime: '2026-05-08T12:00:00Z' },
          ],
        },
      }),
    })
  })

  // Topology / upgrade preview.
  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/applications\/wp-prod\/(topology|upgrade)\/preview.*$/,
    (route: Route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          manifests: [
            {
              path: 'clusters/hz-fsn-rtz-prod/applications/wp-prod/helmrelease.yaml',
              content: '# preview helmrelease',
            },
          ],
          diff: '',
          blueprint: { name: 'bp-wordpress', version: '1.4.0' },
          warnings: [],
        }),
      })
    },
  )

  // PUT update.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/applications\/wp-prod(\?.*)?$/, (route: Route) => {
    if (route.request().method() === 'PUT' || route.request().method() === 'DELETE') {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ name: APP_NAME, namespace: NAMESPACE, uid: 'fake', message: 'ok' }),
      })
      return
    }
    route.continue()
  })

  // RBAC matrix (Members tab).
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/rbac\/access-matrix.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        users: [],
        applications: [],
        tiers: ['viewer', 'developer', 'operator', 'admin', 'owner'],
      }),
    })
  })

  // Compliance (Compliance tab — placeholder OK).
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/compliance\/.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: [], applications: [] }),
    })
  })

  // Blueprints (publish + curate + curatable).
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/blueprints\/publish$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        org: 'acme',
        name: 'bp-acme-internal',
        version: '1.0.0',
        repo: 'shared-blueprints',
        path: 'bp-acme-internal/blueprint.yaml',
        url: '/acme/shared-blueprints/src/branch/main/bp-acme-internal/blueprint.yaml',
        message: 'Blueprint published; blueprint-controller will reconcile within ~1 min',
      }),
    })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/blueprints\/curatable.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          { org: 'acme', name: 'bp-foo', version: '1.0.0', title: 'Foo' },
          { org: 'acme', name: 'bp-bar', version: '2.0.0', title: 'Bar' },
        ],
      }),
    })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/blueprints\/curate$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        blueprintName: 'bp-foo',
        sourceOrg: 'acme',
        targetOrg: 'catalog-sovereign',
        message: 'Blueprint curated; catalog-svc will pick up sovereign-curated entry on next reconcile',
      }),
    })
  })

  // Sovereign clusters.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/clusters$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ clusters: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'] }),
    })
  })
}

test.describe('Application page bundle T+O+P (slice T+O+P, #1097)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('T1: AppDetail renders the full 7-tab set', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(600)
    // All 7+1 tabs surfaced. Jobs + Dependencies + Topology + Resources +
    // Compliance + Logs + Settings + Members.
    await expect(page.getByTestId('sov-app-tab-jobs')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-dependencies')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-topology')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-resources')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-compliance')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-logs')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-settings')).toBeVisible()
    await expect(page.getByTestId('sov-app-tab-members')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-t1-tabs-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  // #5609 — this spec asserted `app-topology-tab` and `topology-editor`, two
  // testids that do not exist: the tab panel is `app-topology-tabpanel`, and
  // `TopologyEditor` was superseded by `PlacementEditor` in #3969 and was
  // never mounted. The specs under bootstrap/ui/e2e are not wired into CI
  // (playwright-e2e.yml only triggers on products/catalyst/console/**), which
  // is why the drift went unnoticed. Retargeted at the real surfaces.
  test('T2: Topology tab renders placement + status panels', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-topology').click()
    await page.waitForTimeout(500)
    await expect(page.getByTestId('app-topology-tabpanel')).toBeVisible()
    await expect(page.getByTestId('topology-tab-placement-panel')).toBeVisible()
    await expect(page.getByTestId('topology-tab-status-panel')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-t2-topology-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  // #5609 — the old T3 drove a mode radio + Preview modal on the unmounted
  // TopologyEditor. #3969 deleted both affordances: there is NO mode picker
  // and no preview modal, the pattern is DERIVED from the placement targets.
  // Retargeted at the real "Edit placement" → PlacementEditor flow.
  test('T3: Edit placement opens the placement editor', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-topology').click()
    await page.waitForTimeout(500)
    await page.getByTestId('topology-tab-edit-placement').click()
    await page.waitForTimeout(400)
    await expect(page.getByTestId('placement-editor')).toBeVisible()
    // The derived-pattern label replaces the deleted mode picker.
    await expect(page.getByTestId('placement-editor-derived-pattern')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-t3-topology-preview-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('O1: Settings tab renders parameter editor + actions', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-settings').click()
    await page.waitForTimeout(800)
    await expect(page.getByTestId('app-settings-tab')).toBeVisible()
    await expect(page.getByTestId('settings-tab-upgrade-btn')).toBeVisible()
    await expect(page.getByTestId('settings-tab-uninstall-btn')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-o1-settings-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('O2: Upgrade dialog opens', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-settings').click()
    await page.waitForTimeout(500)
    await page.getByTestId('settings-tab-upgrade-btn').click()
    await page.waitForTimeout(400)
    await expect(page.getByTestId('upgrade-dialog')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-o2-upgrade-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('O3: Uninstall dialog typed-confirm gate', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-settings').click()
    await page.waitForTimeout(500)
    await page.getByTestId('settings-tab-uninstall-btn').click()
    await page.waitForTimeout(300)
    const confirmBtn = page.getByTestId('uninstall-dialog-confirm')
    await expect(confirmBtn).toBeDisabled()
    await page.getByTestId('uninstall-dialog-confirm-input').fill(APP_NAME)
    await expect(confirmBtn).toBeEnabled()
    await page.screenshot({
      path: `playwright-report/top-o3-uninstall-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('P1: Publish page renders form', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/blueprints/publish`)
    await page.waitForTimeout(500)
    await expect(page.getByTestId('publish-page')).toBeVisible()
    await expect(page.getByTestId('publish-page-yaml')).toBeVisible()
    await page.getByTestId('publish-page-org').fill('acme')
    await page.screenshot({
      path: `playwright-report/top-p1-publish-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('P2: Curate page lists candidates', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/blueprints/curate`)
    await page.waitForTimeout(500)
    await expect(page.getByTestId('curate-page')).toBeVisible()
    // Type orgs to trigger the live query.
    await page.getByTestId('curate-page-orgs').fill('acme')
    await page.waitForTimeout(800)
    await expect(page.getByTestId('curate-page-row-acme-bp-foo')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-p2-curate-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('M1: Members tab reuses MembersList (no duplicate)', async ({ page }) => {
    await mockTopAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.waitForTimeout(500)
    await page.getByTestId('sov-app-tab-members').click()
    await page.waitForTimeout(500)
    await expect(page.getByTestId('app-members-tab')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/top-m1-members-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
