/**
 * fleet-dashboard.spec.ts — Playwright E2E for the EPIC-6 Slice U-Fleet
 * (#1101) multi-Sovereign dashboard + cross-Sovereign Applications view.
 *
 * Per `feedback_per_issue_playwright_verification.md`: each assertion
 * gets its own snapshot — never collapse.
 *
 * Five assertions, ≥5 1440x900 PNG snapshots:
 *
 *   F1 — Dashboard renders Sovereign cards (3-card grid)
 *   F2 — Empty-state when no Sovereigns exist
 *   F3 — Cross-Sovereign Applications view renders + filter chips
 *   F4 — Click on a Sovereign card emits a chroot navigation
 *   F5 — DR posture badges render correctly per row
 *
 * Each test mounts mock catalyst-api responses so the page renders
 * deterministically without a live backend.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const SOVS_THREE = {
  sovereigns: [
    { id: 'sov-a', fqdn: 'a.example.com', region: 'fsn1', health: 'green', providerType: 'hetzner', createdAt: '2026-04-01T10:00:00Z' },
    { id: 'sov-b', fqdn: 'b.example.com', region: 'hel1', health: 'yellow', providerType: 'hetzner', createdAt: '2026-04-15T10:00:00Z' },
    { id: 'sov-c', fqdn: 'c.example.com', region: 'nbg1', health: 'red', providerType: 'hetzner', createdAt: '2026-05-01T10:00:00Z' },
  ],
  total: 3,
  page: 1,
  pageSize: 25,
}

const SOVS_EMPTY = {
  sovereigns: [],
  total: 0,
  page: 1,
  pageSize: 25,
}

function summaryFor(id: string, fqdn: string, health: 'green' | 'yellow' | 'red') {
  return {
    sovereign: { id, fqdn, health, region: 'fsn1', providerType: 'hetzner' },
    orgs: 2,
    applications: { total: 5, active: 3, failing: 1 },
    regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
    alerts: 0,
    lastActivity: '2026-05-01T10:00:00Z',
  }
}

const APPS_RESP = {
  applications: [
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'wp', blueprint: 'bp-wordpress', version: '1.0' },
      regions: ['hz-fsn-rtz-prod'],
      topology: 'single-region',
      drPosture: '—',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'api', blueprint: 'bp-django', version: '0.9' },
      regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
      topology: 'active-hotstandby',
      drPosture: 'DR active',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
    {
      sovereign: { id: 'sov-b', fqdn: 'b.example.com', health: 'yellow' },
      app: { name: 'broken', blueprint: 'bp-broken', version: '0.1' },
      regions: ['hz-fsn-rtz-prod'],
      topology: 'active-hotstandby',
      drPosture: 'Misconfigured',
      status: 'Pending',
      org: 'widget',
      namespace: 'widget',
    },
  ],
  total: 3,
}

async function mockFleetApi(
  page: Page,
  opts: { sovs?: typeof SOVS_THREE; apps?: typeof APPS_RESP } = {},
) {
  const sovs = opts.sovs ?? SOVS_THREE
  const apps = opts.apps ?? APPS_RESP

  await page.route(/.*\/api\/v1\/whoami$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'owner', email: 'owner@acme.io', tier: 'owner' }),
    })
  })
  await page.route(/.*\/api\/v1\/fleet\/sovereigns(\?.*)?$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(sovs),
    })
  })
  await page.route(/.*\/api\/v1\/fleet\/sovereigns\/([^/]+)\/summary(\?.*)?$/, (route: Route) => {
    const m = /\/sovereigns\/([^/]+)\/summary/.exec(route.request().url())
    const id = m?.[1] ?? 'sov-a'
    const sov = sovs.sovereigns.find((s) => s.id === id) ?? sovs.sovereigns[0]
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(summaryFor(sov.id, sov.fqdn, sov.health as 'green' | 'yellow' | 'red')),
    })
  })
  await page.route(/.*\/api\/v1\/fleet\/applications(\?.*)?$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(apps),
    })
  })
}

test.describe('Fleet dashboard (EPIC-6 U-Fleet, #1101)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('F1: dashboard renders 3 Sovereign cards', async ({ page }) => {
    await mockFleetApi(page)
    await page.goto('/dashboard')
    await expect(page.getByTestId('dashboard-page')).toBeVisible()
    await expect(page.getByTestId('dashboard-sovereign-grid')).toBeVisible()
    await expect(page.getByTestId('sovereign-card-sov-a')).toBeVisible()
    await expect(page.getByTestId('sovereign-card-sov-b')).toBeVisible()
    await expect(page.getByTestId('sovereign-card-sov-c')).toBeVisible()
    await expect(page.getByTestId('sovereign-card-health-sov-a')).toContainText('Healthy')
    await expect(page.getByTestId('sovereign-card-health-sov-c')).toContainText('Failed')
    await page.screenshot({ path: 'test-results/fleet-dashboard/F1-three-cards.png', fullPage: true })
  })

  test('F2: empty state when no Sovereigns', async ({ page }) => {
    await mockFleetApi(page, { sovs: SOVS_EMPTY })
    await page.goto('/dashboard')
    await expect(page.getByTestId('dashboard-empty-state')).toBeVisible()
    await expect(page.getByText('No Sovereigns provisioned yet')).toBeVisible()
    await page.screenshot({ path: 'test-results/fleet-dashboard/F2-empty-state.png', fullPage: true })
  })

  test('F3: cross-Sovereign view renders applications table + filters', async ({ page }) => {
    await mockFleetApi(page)
    await page.goto('/dashboard/applications')
    await expect(page.getByTestId('cross-sov-page')).toBeVisible()
    await expect(page.getByTestId('cross-sov-table')).toBeVisible()
    await expect(page.getByTestId('cross-sov-row-sov-a-wp')).toBeVisible()
    await expect(page.getByTestId('cross-sov-row-sov-a-api')).toBeVisible()
    await expect(page.getByTestId('cross-sov-row-sov-b-broken')).toBeVisible()
    await expect(page.getByTestId('cross-sov-filter-org')).toBeVisible()
    await expect(page.getByTestId('cross-sov-filter-topology')).toBeVisible()
    await expect(page.getByTestId('cross-sov-filter-dr')).toBeVisible()
    await page.screenshot({ path: 'test-results/fleet-dashboard/F3-cross-sov-table.png', fullPage: true })
  })

  test('F4: click on a Sovereign card triggers chroot navigation', async ({ page }) => {
    await mockFleetApi(page)
    // Capture the resulting navigation; mock the destination so we
    // don't actually leave the page (the SovereignCard onClick assigns
    // window.location.href to console.<fqdn>/dashboard which the test
    // browser would refuse to load).
    let navigated = ''
    await page.route(/^https:\/\/console\.a\.example\.com\/.*$/, (route: Route) => {
      navigated = route.request().url()
      route.fulfill({ status: 200, contentType: 'text/html', body: '<html><body>chroot</body></html>' })
    })
    await page.goto('/dashboard')
    await expect(page.getByTestId('sovereign-card-sov-a')).toBeVisible()
    await page.getByTestId('sovereign-card-sov-a').click()
    // Either the navigation succeeded (Playwright follows hrefs in
    // jsdom-style) or the route handler captured it. Tolerate both.
    await page.waitForTimeout(500)
    expect(navigated).toMatch(/console\.a\.example\.com/)
    await page.screenshot({ path: 'test-results/fleet-dashboard/F4-card-click-navigates.png', fullPage: true })
  })

  test('F5: DR posture badges render with correct text per row', async ({ page }) => {
    await mockFleetApi(page)
    await page.goto('/dashboard/applications')
    await expect(page.getByTestId('cross-sov-table')).toBeVisible()
    await expect(page.getByTestId('cross-sov-dr-wp')).toContainText('—')
    await expect(page.getByTestId('cross-sov-dr-api')).toContainText('DR active')
    await expect(page.getByTestId('cross-sov-dr-broken')).toContainText('Misconfigured')
    await page.screenshot({ path: 'test-results/fleet-dashboard/F5-dr-posture-badges.png', fullPage: true })
  })
})
