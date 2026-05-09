/**
 * install-flow.spec.ts — Playwright E2E for the EPIC-2 Slice I (#1097)
 * live install flow.
 *
 * What this asserts (per `feedback_per_issue_playwright_verification.md`
 * — N issues = N snapshots, never collapse):
 *
 *   1. /console/install renders the catalog grid from the live useCatalog hook
 *   2. Click a Blueprint card → selects, renders fixed scaffold + auto-form
 *   3. Submit form → POST verified
 *   4. Preview → modal shows manifests + paths
 *   5. x-catalyst-ui-hint=password renders masked input
 *   6. install-with-defaults branch (Blueprint without configSchema)
 *
 * Each test mounts mock catalyst-api responses so the page renders
 * deterministically without a live backend, then captures one
 * 1440x900 screenshot per route per assertion.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'install-1097'

const WORDPRESS_BP = {
  name: 'bp-wordpress',
  version: '1.2.3',
  card: {
    title: 'WordPress',
    summary: 'PHP CMS for content websites',
    category: 'cms',
  },
  origin: 1,
  source: 'public',
}

const WORDPRESS_BP_RAW = {
  ...WORDPRESS_BP,
  raw: {
    spec: {
      version: '1.2.3',
      configSchema: {
        type: 'object',
        required: ['domain'],
        properties: {
          domain: { type: 'string', title: 'Domain' },
          replicas: { type: 'integer', title: 'Replicas', minimum: 1, maximum: 5 },
          adminPassword: {
            type: 'string',
            title: 'Admin password',
            'x-catalyst-ui-hint': 'password',
          },
        },
      },
    },
  },
}

const SIMPLE_BP = {
  name: 'bp-simple',
  version: '0.1.0',
  card: { title: 'Simple App', summary: 'Stateless container with no parameters' },
  origin: 2,
  source: 'sovereign',
}

const SIMPLE_BP_RAW = {
  ...SIMPLE_BP,
  raw: {
    spec: { version: '0.1.0' },
  },
}

async function mockInstallAPI(page: Page) {
  await page.route(/.*\/api\/v1\/catalog(\?.*)?$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: [WORDPRESS_BP, SIMPLE_BP] }),
    })
  })
  await page.route(
    /.*\/api\/v1\/catalog\/bp-wordpress\/versions\/1\.2\.3$/,
    (route: Route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(WORDPRESS_BP_RAW),
      })
    },
  )
  await page.route(
    /.*\/api\/v1\/catalog\/bp-simple\/versions\/0\.1\.0$/,
    (route: Route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(SIMPLE_BP_RAW),
      })
    },
  )

  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/applications$/,
    (route: Route) => {
      if (route.request().method() === 'POST') {
        const body = route.request().postDataJSON()
        route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            name: body.name,
            namespace: body.organizationRef,
            uid: 'fake-uid',
            status: { phase: 'Pending' },
          }),
        })
        return
      }
      route.continue()
    },
  )

  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/applications\/preview$/,
    (route: Route) => {
      const body = route.request().postDataJSON()
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          manifests: [
            {
              path: 'clusters/hz-fsn-rtz-prod/applications/wp-prod/kustomization.yaml',
              content: '# kustomization for wp-prod\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - helmrelease.yaml\n',
            },
            {
              path: 'clusters/hz-fsn-rtz-prod/applications/wp-prod/helmrelease.yaml',
              content: `# HelmRelease for wp-prod\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: wp-prod\nspec:\n  values:\n    domain: ${body?.parameters?.domain ?? 'preview.test'}\n`,
            },
          ],
          diff: '',
          blueprint: { name: body.blueprintRef.name, version: body.blueprintRef.version },
          warnings: ['preview shows the manifests catalyst-api will commit; live-vs-preview diff against the per-Org Gitea repo is deferred to a follow-up slice'],
        }),
      })
    },
  )

  // Auth gate stubs.
  await page.route(/.*\/api\/v1\/sovereign\/self$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID, sovereignFQDN: 'install.example' }),
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
}

test.describe('Install flow (slice I, #1097)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('I1: catalog grid renders blueprints from useCatalog', async ({ page }) => {
    await mockInstallAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/install`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(800)
    await expect(page.getByTestId('install-page')).toBeVisible()
    await expect(page.getByTestId('install-page-card-bp-wordpress')).toBeVisible()
    await expect(page.getByTestId('install-page-card-bp-simple')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/install-i1-catalog-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('I2: clicking a blueprint renders the auto-form with configSchema fields', async ({ page }) => {
    await mockInstallAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/install`)
    await page.waitForTimeout(600)
    await page.getByTestId('install-page-card-bp-wordpress').click()
    await page.waitForTimeout(800)
    // Fixed scaffold visible.
    await expect(page.getByTestId('install-page-app-name')).toBeVisible()
    await expect(page.getByTestId('install-page-org-ref')).toBeVisible()
    await expect(page.getByTestId('install-page-env-ref')).toBeVisible()
    await expect(page.getByTestId('install-page-region')).toBeVisible()
    await expect(page.getByTestId('install-page-placement')).toBeVisible()
    // Auto-form fields from configSchema.
    await expect(page.locator('#root_domain')).toBeVisible()
    await expect(page.locator('#root_replicas')).toBeVisible()
    // Password hint engages the masked widget.
    await expect(page.getByTestId('install-form-password-input')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/install-i2-form-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('I3: submit posts to /applications and shows status modal', async ({ page }) => {
    await mockInstallAPI(page)
    let installPostBody: unknown = null
    page.on('request', (req) => {
      if (req.method() === 'POST' && /\/applications$/.test(req.url())) {
        installPostBody = req.postDataJSON()
      }
    })
    await page.goto(`/provision/${DEPLOYMENT_ID}/install`)
    await page.waitForTimeout(600)
    await page.getByTestId('install-page-card-bp-wordpress').click()
    await page.waitForTimeout(800)
    await page.locator('#root_domain').fill('shop.acme.com')
    await page.locator('#root_replicas').fill('2')
    await page.getByTestId('install-form-password-input').fill('SuperSecret123!@#')
    await page.getByTestId('install-form-submit-btn').click()
    await page.waitForTimeout(800)
    expect(installPostBody).toBeTruthy()
    expect(installPostBody).toMatchObject({
      blueprintRef: { name: 'bp-wordpress', version: '1.2.3' },
      organizationRef: 'default',
      environmentRef: 'default-prod',
      placement: { mode: 'single-region', regions: ['hz-fsn-rtz-prod'] },
      parameters: { domain: 'shop.acme.com', replicas: 2 },
    })
    await expect(page.getByTestId('install-page-status-modal')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/install-i3-status-modal-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('I4: preview button opens modal with manifests + warnings', async ({ page }) => {
    await mockInstallAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/install`)
    await page.waitForTimeout(600)
    await page.getByTestId('install-page-card-bp-wordpress').click()
    await page.waitForTimeout(800)
    await page.locator('#root_domain').fill('preview.test')
    await page.getByTestId('install-form-preview-btn').click()
    await page.waitForTimeout(600)
    await expect(page.getByTestId('install-page-preview-modal')).toBeVisible()
    // Both manifest paths appear.
    await expect(
      page.getByTestId('install-page-preview-manifest-clusters/hz-fsn-rtz-prod/applications/wp-prod/kustomization.yaml'),
    ).toBeVisible()
    await expect(
      page.getByTestId('install-page-preview-manifest-clusters/hz-fsn-rtz-prod/applications/wp-prod/helmrelease.yaml'),
    ).toBeVisible()
    await page.screenshot({
      path: `playwright-report/install-i4-preview-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('I5: install-with-defaults branch renders for blueprint without configSchema', async ({ page }) => {
    await mockInstallAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/install`)
    await page.waitForTimeout(600)
    await page.getByTestId('install-page-card-bp-simple').click()
    await page.waitForTimeout(800)
    await expect(page.getByTestId('install-form-no-schema')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/install-i5-no-schema-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
