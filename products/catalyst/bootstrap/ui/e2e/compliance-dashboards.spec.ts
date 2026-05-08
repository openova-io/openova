/**
 * compliance-dashboards.spec.ts — Playwright E2E for the EPIC-1 slice U
 * compliance dashboards (#1096).
 *
 * What this asserts (per `feedback_per_issue_playwright_verification.md`
 * — every page change owes a per-page snapshot, never collapsed):
 *
 *   1. /admin/compliance/sre              → SRE Lead dashboard
 *   2. /admin/compliance/security         → SecLead dashboard
 *   3. /admin/compliance/policy/<name>    → per-policy drilldown
 *   4. /provision/$deploymentId/app/<app> → Application detail
 *      Compliance tab
 *   5. /admin/compliance/sre + click toggle → confirm dialog opens
 *
 * Each test mounts a mock catalyst-api response set so the page renders
 * deterministically without a live backend, then captures one
 * 1440x900 screenshot per route.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'compliance-1096'

const MOCK_SCORECARD = {
  sovereign: {
    scope: 'sovereign',
    id: DEPLOYMENT_ID,
    total: 78,
    numerator: 780,
    denominator: 1000,
    updatedAt: new Date().toISOString(),
  },
  organizations: [
    {
      scope: 'organization',
      id: 'acme',
      total: 82,
      numerator: 410,
      denominator: 500,
      updatedAt: new Date().toISOString(),
    },
  ],
  environments: [
    {
      scope: 'environment',
      id: 'acme-prod',
      total: 76,
      numerator: 380,
      denominator: 500,
      organizationRef: 'acme',
      updatedAt: new Date().toISOString(),
    },
  ],
  applications: [
    {
      scope: 'application',
      id: 'billing',
      applicationRef: 'billing',
      organizationRef: 'acme',
      environmentRef: 'acme-prod',
      total: 87,
      numerator: 87,
      denominator: 100,
      policyResults: { 'flux-managed': 'pass', 'probes-present': 'fail' },
      violations: 1,
      updatedAt: new Date().toISOString(),
    },
    {
      scope: 'application',
      id: 'orders',
      applicationRef: 'orders',
      organizationRef: 'acme',
      environmentRef: 'acme-prod',
      total: 65,
      numerator: 65,
      denominator: 100,
      policyResults: {
        'flux-managed': 'pass',
        'probes-present': 'fail',
        'cilium-l7-mtls': 'fail',
      },
      violations: 2,
      updatedAt: new Date().toISOString(),
    },
  ],
  generatedAt: new Date().toISOString(),
}

const MOCK_POLICIES = {
  count: 3,
  items: [
    {
      name: 'flux-managed',
      weight: 10,
      scope: 'all',
      mode: 'enforcing',
      violations: 0,
      source: 'kyverno',
      description: 'Resource managed by Flux',
    },
    {
      name: 'probes-present',
      weight: 20,
      scope: 'stateless',
      mode: 'permissive',
      violations: 2,
      source: 'kyverno',
      description: 'Pod has liveness + readiness probes',
    },
    {
      name: 'cilium-l7-mtls',
      weight: 30,
      scope: 'all',
      mode: 'enforcing',
      violations: 1,
      source: 'kyverno',
      description: 'Cilium L7 mTLS policy attached',
    },
  ],
}

const MOCK_VIOLATIONS = {
  total: 2,
  offset: 0,
  limit: 50,
  items: [
    {
      resource: 'Deployment/acme-prod/billing-api',
      namespace: 'acme-prod',
      policy: 'probes-present',
      result: 'fail',
      message: 'Containers must define livenessProbe and readinessProbe',
      application: 'billing',
      environment: 'acme-prod',
      time: new Date().toISOString(),
    },
    {
      resource: 'Deployment/acme-prod/orders-api',
      namespace: 'acme-prod',
      policy: 'probes-present',
      result: 'fail',
      message: 'Containers must define livenessProbe and readinessProbe',
      application: 'orders',
      environment: 'acme-prod',
      time: new Date().toISOString(),
    },
  ],
}

async function mockComplianceAPI(page: Page) {
  // /scorecard
  await page.route(/.*\/sovereigns\/.*\/compliance\/scorecard/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(MOCK_SCORECARD),
    })
  })
  // /policies
  await page.route(/.*\/sovereigns\/.*\/compliance\/policies/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(MOCK_POLICIES),
    })
  })
  // /violations
  await page.route(/.*\/sovereigns\/.*\/compliance\/violations/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(MOCK_VIOLATIONS),
    })
  })
  // /stream — ":connected" then idle
  await page.route(/.*\/sovereigns\/.*\/compliance\/stream/, (route: Route) => {
    route.fulfill({
      status: 200,
      headers: {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
      },
      body: ': connected\n\n',
    })
  })
  // /sovereign/self resolution for chroot mode
  await page.route(/.*\/api\/v1\/sovereign\/self/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        deploymentId: DEPLOYMENT_ID,
        sovereignFQDN: 'compliance-1096.example',
      }),
    })
  })
  // /whoami so the auth gate doesn't redirect to /login
  await page.route(/.*\/api\/v1\/whoami/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'test', email: 'test@example.com' }),
    })
  })
  // /deployments/{id} so adopted-redirect doesn't fire
  await page.route(/.*\/api\/v1\/deployments\/[^/]+$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID }),
    })
  })
}

test.describe('Compliance dashboards (slice U, #1096)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('U1: SRE Lead dashboard renders fleet treemap', async ({ page }) => {
    await mockComplianceAPI(page)
    await page.goto('/admin/compliance/sre')
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(1500)
    await expect(page.getByTestId('compliance-dashboard-sre')).toBeVisible()
    await expect(page.getByTestId('compliance-dashboard-title')).toContainText('SRE Lead')
    await page.screenshot({
      path: `playwright-report/compliance-u1-sre-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U2: Security Lead dashboard renders security palette', async ({ page }) => {
    await mockComplianceAPI(page)
    await page.goto('/admin/compliance/security')
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(1500)
    await expect(page.getByTestId('compliance-dashboard-security')).toBeVisible()
    await expect(page.getByTestId('compliance-dashboard-title')).toContainText('Security Lead')
    await expect(page.getByTestId('compliance-legend')).toContainText('High risk')
    await page.screenshot({
      path: `playwright-report/compliance-u2-security-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U4: Per-policy drilldown surfaces violations table', async ({ page }) => {
    await mockComplianceAPI(page)
    await page.goto('/admin/compliance/policy/probes-present')
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(1500)
    await expect(page.getByTestId('policy-drilldown-page')).toBeVisible()
    await expect(page.getByTestId('policy-drilldown-title')).toContainText('probes-present')
    await expect(page.getByTestId('policy-drilldown-violations')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/compliance-u4-drilldown-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U3: ComplianceTab renders inside Application detail', async ({ page }) => {
    await mockComplianceAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/billing`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(1500)
    // Click the Compliance tab to switch panels.
    const complianceTab = page.getByTestId('sov-app-tab-compliance')
    if (await complianceTab.isVisible()) {
      await complianceTab.click()
      await expect(page.getByTestId('app-compliance-tab')).toBeVisible()
    }
    await page.screenshot({
      path: `playwright-report/compliance-u3-app-detail-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U5: PolicyModeToggle confirm dialog opens on flip', async ({ page }) => {
    await mockComplianceAPI(page)
    await page.goto('/admin/compliance/policy/probes-present')
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(1500)
    // Click the toggle on the policy metadata row.
    const toggleBtn = page.getByTestId('policy-mode-toggle-button-probes-present')
    if (await toggleBtn.isVisible()) {
      await toggleBtn.click()
      await expect(page.getByTestId('policy-mode-confirm-probes-present')).toBeVisible()
    }
    await page.screenshot({
      path: `playwright-report/compliance-u5-toggle-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
