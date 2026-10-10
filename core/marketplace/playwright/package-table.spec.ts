// Marketplace — the BSS package document feeding the six-step wizard (#6971).
//
// Renders the REAL pages against `npm run build && npm run preview` with the
// BSS endpoint mocked via page.route(), in the same hermetic shape as
// customer-journey.spec.ts. The page learns its chargeback host from
// `window.__ORG_CHARGEBACK_URL__` here (the runtime hook a Sovereign may seed;
// on a real host it is derived from marketplace.<fqdn>), because the preview
// runs on localhost where there is nothing to derive from.
//
//   step 1 /plans   — the comparison table, no checkboxes; choosing continues
//                     to Stack with the catalog plan id + sku in the cart
//   step 3 /addons  — the chosen package's optional features are the add-ons
//                     (M: Backup 1.500), included ones read-only (XL: Backup),
//                     catalog twins hidden, ticking writes the BSS SKU
//   /review         — the add-on line shows as any add-on; a plan change prunes
//   /checkout       — the add-on line, the total, and both POST bodies
//   fallback        — 503 / no host: every step exactly as today
//
// READ-ONLY against clusters: no kubectl, no chart bumps, no Pod ops.

import { test, expect, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const CHARGEBACK = 'https://chargeback.t99.omani.works'
const PACKAGES_URL = `${CHARGEBACK}/api/v1/public/packages`
// The package is `"type": "module"`, so the spec runs as ESM: no __dirname.
const HERE = fileURLToPath(new URL('.', import.meta.url))
const FIXTURE = JSON.parse(readFileSync(join(HERE, '..', 'fixtures', 'public-packages.json'), 'utf8'))

const CATALOG_PLANS = [
  { id: 's', slug: 's', name: 'S', cpu: '2 vCPU', memory: '4 GB', storage: '25 GB', price_omr: 5, popular: false, features: [], description: '' },
  { id: 'm', slug: 'm', name: 'M', cpu: '4 vCPU', memory: '8 GB', storage: '50 GB', price_omr: 9, popular: true, features: [], description: '' },
  { id: 'l', slug: 'l', name: 'L', cpu: '8 vCPU', memory: '16 GB', storage: '100 GB', price_omr: 16, popular: false, features: [], description: '' },
  { id: 'xl', slug: 'xl', name: 'XL', cpu: '16 vCPU', memory: '32 GB', storage: '200 GB', price_omr: 30, popular: false, features: [], description: '' },
]

// /api/catalog/addons wire shape (api.ts::getAddons: description → tagline,
// price_omr → baisa). Three of these twin a BSS feature; one does not.
const CATALOG_ADDONS = [
  { id: 'daily-backup', slug: 'daily-backup', name: 'Daily Backup', description: 'Automated daily backups with 30-day retention', price_omr: 3, included: false },
  { id: 'waf', slug: 'waf', name: 'Web Application Firewall', description: 'Coraza WAF — OWASP CRS protection', price_omr: 4, included: false },
  { id: 'ips', slug: 'ips', name: 'Intrusion Prevention', description: 'Community-powered threat intelligence — CrowdSec', price_omr: 3, included: false },
  { id: 'custom-domain', slug: 'custom-domain', name: 'Custom Domain', description: 'Your brand, your domain — with automatic TLS', price_omr: 2, included: false },
]

const APPS = [
  { id: '1', name: 'WordPress', slug: 'wordpress', tagline: 'Website & blog platform', description: 'Blogs and sites.', category: 'cms', icon: 'W', color: '#21759b', free: true, features: [], website: '', license: 'GPL-2.0', system: false, kind: 'business', deployable: true, dependencies: [] },
]

async function pointAtChargeback(page: Page): Promise<void> {
  await page.addInitScript((url) => {
    ;(window as any).__ORG_CHARGEBACK_URL__ = url
  }, CHARGEBACK)
}

async function mockPackages(page: Page, status = 200, body: unknown = FIXTURE): Promise<void> {
  await page.route(PACKAGES_URL, (route) =>
    route.fulfill({
      status,
      contentType: 'application/json',
      headers: { 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'public, max-age=60' },
      body: JSON.stringify(body),
    }),
  )
}

async function mockCatalog(page: Page): Promise<void> {
  await page.route('**/api/catalog/plans**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(CATALOG_PLANS) }),
  )
  await page.route('**/api/catalog/addons', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(CATALOG_ADDONS) }),
  )
  await page.route('**/api/catalog/apps**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(APPS) }),
  )
  await page.route('**/api/catalog/industries', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
  )
  await page.route('**/api/tenant/check-slug/**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ available: true }) }),
  )
}

async function seedCart(page: Page, overrides: Record<string, unknown>): Promise<void> {
  const cart = {
    plan: 'm',
    planName: 'M',
    apps: ['1'],
    addons: [],
    orgName: 'Demo Co',
    subdomain: 'demo-co',
    email: 'demo@example.com',
    tld: 'omani.homes',
    agents: [],
    appConfigs: {},
    packageSku: 'plan.m',
    ...overrides,
  }
  // Init scripts run on EVERY navigation; seed once per tab so a step that
  // navigates (Choose → /apps) is not re-seeded with the starting cart.
  await page.addInitScript((value) => {
    try {
      if (sessionStorage.getItem('pw-cart-seeded')) return
      localStorage.setItem('org-cart', JSON.stringify(value))
      sessionStorage.setItem('pw-cart-seeded', '1')
    } catch (_) {}
  }, cart)
}

async function readCart(page: Page): Promise<any> {
  return page.evaluate(() => JSON.parse(localStorage.getItem('org-cart') || 'null'))
}

// ────────────────────────────────────────────────────────────────────────
// Step 1 — /plans
// ────────────────────────────────────────────────────────────────────────

test.describe('step 1: package comparison table (/plans, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('renders S/M/L/XL with prices, included quantities, the three cell states and the up-sell hint — and no checkboxes', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans')

    const table = page.getByTestId('package-table')
    await expect(table).toBeVisible({ timeout: 10_000 })

    // Columns, in the server's order, with price + includes.
    const headers = page.locator('thead th.pk-col')
    await expect(headers).toHaveCount(4)
    await expect(headers.nth(0)).toContainText('S')
    await expect(headers.nth(0)).toContainText('9.000')
    await expect(headers.nth(0)).toContainText('1 vCPU')
    await expect(headers.nth(0)).toContainText('2 GB RAM')
    await expect(headers.nth(0)).toContainText('25 GB storage')
    await expect(headers.nth(0)).toContainText('50 Mbps')
    await expect(headers.nth(3)).toContainText('XL')
    await expect(headers.nth(3)).toContainText('72.000')

    // One row per feature.
    await expect(page.locator('tbody tr')).toHaveCount(15)

    // Included → ✓; Optional → muted "add-on" + price + hint; Not offered → —.
    const backupXL = page.getByTestId('package-cell-backup-plan.xl')
    await expect(backupXL).toHaveAttribute('data-state', 'included')
    await expect(backupXL).toContainText('✓')
    await expect(backupXL.locator('.pk-hint')).toHaveCount(0)

    const backupS = page.getByTestId('package-cell-backup-plan.s')
    await expect(backupS).toHaveAttribute('data-state', 'optional')
    await expect(backupS.locator('.pk-addon-tag')).toHaveText('add-on')
    await expect(backupS).toContainText('+ 1.500 OMR')
    await expect(backupS.locator('.pk-hint')).toHaveText('Included from XL')

    const ipS = page.getByTestId('package-cell-dedicated-ip-plan.s')
    await expect(ipS).toHaveAttribute('data-state', 'not_offered')
    await expect(ipS).toHaveText('—')

    // Quantity feature shows the included amount with its unit.
    await expect(page.getByTestId('package-cell-bandwidth-plan.l')).toHaveText('250 Mbps')

    // Nothing to tick on step 1 — add-ons are picked on step 3.
    await expect(table.getByRole('checkbox')).toHaveCount(0)

    // Default recommendation is the middle package, M, and it is pre-selected
    // the way the legacy deck pre-selects the popular plan.
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-recommended', 'true')
    await expect(page.getByTestId('package-col-plan.s')).toHaveAttribute('data-recommended', 'false')
    await expect(page.locator('.pk-hat')).toHaveText(/Recommended/i)
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-selected', 'true')
    const cart = await readCart(page)
    expect(cart.plan).toBe('m')
    expect(cart.planName).toBe('M')
    expect(cart.packageSku).toBe('plan.m')

    // No legacy deck underneath the table.
    await expect(page.locator('.pcard')).toHaveCount(0)
  })

  test('?recommended=plan.l moves the highlight', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans?recommended=plan.l')
    await expect(page.getByTestId('package-table')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-col-plan.l')).toHaveAttribute('data-recommended', 'true')
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-recommended', 'false')
  })

  test('choosing a package sets the cart plan as PlanStep did (catalog id + sku) and continues to Stack', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    // A BSS add-on picked earlier for M that XL includes as standard.
    await seedCart(page, { plan: 'm', packageSku: 'plan.m', addons: ['ips', 'addon.backup'] })
    await page.goto('/plans')
    await expect(page.getByTestId('package-table')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-selected', 'true')

    await page.getByTestId('package-choose-plan.xl').click()
    await page.waitForURL(/\/apps/, { timeout: 10_000 })
    const cart = await readCart(page)
    expect(cart.plan).toBe('xl')
    expect(cart.planName).toBe('XL')
    expect(cart.packageSku).toBe('plan.xl')
    // Catalog add-on kept; the BSS add-on XL includes is dropped.
    expect(cart.addons).toEqual(['ips'])
  })

  test('falls back to the legacy plan deck, unchanged, and logs once when the endpoint fails', async ({ page }) => {
    const warnings: string[] = []
    page.on('console', (m) => {
      if (m.type() === 'warning' && m.text().includes('[packages]')) warnings.push(m.text())
    })
    await pointAtChargeback(page)
    await mockPackages(page, 503, { error: 'down' })
    await page.goto('/plans')

    await expect(page.locator('.pcard')).toHaveCount(4, { timeout: 10_000 })
    await expect(page.getByRole('heading', { name: /Pick a plan/i })).toBeVisible()
    await expect(page.locator('.pd-g-row', { hasText: 'Backup retention' })).toBeVisible()
    await expect(page.getByTestId('package-table')).toHaveCount(0)
    expect(warnings, 'one warning, not zero and not one per retry').toHaveLength(1)
    expect(warnings[0]).toMatch(/answered 503/)
  })

  test('with no chargeback host at all (dev localhost) the legacy deck renders', async ({ page }) => {
    // No init script: localhost has no marketplace. prefix and no override.
    await page.goto('/plans')
    await expect(page.locator('.pcard')).toHaveCount(4, { timeout: 10_000 })
    await expect(page.getByTestId('package-table')).toHaveCount(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
// Step 3 — /addons
// ────────────────────────────────────────────────────────────────────────

test.describe('step 3: add-ons are the chosen package\'s optional features (/addons, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('for M: Backup is a 1.500 add-on with the hint, included features are read-only, catalog twins are hidden', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m' })
    await page.goto('/addons')

    // Included in your package — read-only, no price, no toggle.
    const included = page.getByTestId('addons-included')
    await expect(included).toBeVisible({ timeout: 10_000 })
    await expect(included.getByRole('heading', { name: /Included in your package/i })).toBeVisible()
    await expect(included).toContainText('M · INCLUDED')
    await expect(included.getByTestId('addons-included-ssl')).toContainText('Unlimited free SSL')
    await expect(included.getByTestId('addons-included-ssl')).toContainText('Included')
    await expect(included.getByRole('button')).toHaveCount(0)
    // Backup is optional on M, so it is NOT in the included group.
    await expect(included.getByTestId('addons-included-backup')).toHaveCount(0)

    // Optional extras — BSS add-ons.
    const backup = page.getByTestId('addon-tile-addon.backup')
    await expect(backup).toBeVisible()
    await expect(backup).toContainText('Backup')
    await expect(backup).toContainText('+OMR 1.500')
    await expect(backup).toContainText('Included from XL')
    await expect(page.getByTestId('addon-tile-addon.dedicated-ip')).toContainText('+OMR 2.000')

    // Catalog twins (Daily Backup, Custom Domain, WAF) yield to the BSS feature;
    // the catalog add-on with no twin (IPS) keeps today's behaviour.
    await expect(page.getByTestId('addon-tile-daily-backup')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-custom-domain')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-waf')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-ips')).toContainText('Intrusion Prevention')
    await expect(page.getByTestId('addon-tile-ips')).toContainText('+OMR 3.000')

    // Ticking writes the BSS SKU into the cart's one add-on list.
    await backup.click()
    await expect(backup).toHaveClass(/checked/)
    expect((await readCart(page)).addons).toEqual(['addon.backup'])
  })

  test('for XL: Backup is in the included group and not an add-on; Dedicated IP is still optional', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'xl', planName: 'XL', packageSku: 'plan.xl' })
    await page.goto('/addons')
    const included = page.getByTestId('addons-included')
    await expect(included).toBeVisible({ timeout: 10_000 })
    await expect(included).toContainText('XL · INCLUDED')
    await expect(included.getByTestId('addons-included-backup')).toContainText('Backup')
    await expect(page.getByTestId('addon-tile-addon.backup')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-addon.dedicated-ip')).toBeVisible()
  })

  test('for S: a not-offered feature (Dedicated IP) appears nowhere', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 's', planName: 'S', packageSku: 'plan.s' })
    await page.goto('/addons')
    await expect(page.getByTestId('addons-included')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('addon-tile-addon.dedicated-ip')).toHaveCount(0)
    await expect(page.getByTestId('addons-included-dedicated-ip')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-addon.backup')).toBeVisible()
  })

  test('fallback: with the endpoint down the step is exactly today\'s catalog list', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page, 503, { error: 'down' })
    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m' })
    await page.goto('/addons')
    await expect(page.getByTestId('addon-tile-daily-backup')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('addon-tile-custom-domain')).toBeVisible()
    await expect(page.getByTestId('addon-tile-waf')).toBeVisible()
    await expect(page.getByTestId('addon-tile-ips')).toBeVisible()
    await expect(page.getByTestId('addons-included')).toHaveCount(0)
    await expect(page.locator('[data-testid^="addon-tile-addon."]')).toHaveCount(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
// Review + Checkout
// ────────────────────────────────────────────────────────────────────────

test.describe('review and checkout carry the package and its add-ons (#6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
    await page.route('**/api/auth/me', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ user: { id: 'user-1', email: 'demo@example.com', name: 'Demo User' } }) }),
    )
    await page.route('**/api/billing/balance', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ credit_baisa: 0, entries: [] }) }),
    )
    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
  })

  test('/review lists the BSS add-on like any add-on and a plan change to XL prunes it', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.route('**/api/tenant/orgs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m', addons: ['ips', 'addon.backup'] })
    await page.goto('/review')

    const side = page.locator('.side-card')
    await expect(side).toBeVisible({ timeout: 10_000 })
    await expect(side).toContainText('Backup')
    await expect(side).toContainText('+OMR 1.500')
    await expect(side).toContainText('Intrusion Prevention')
    // 9.000 (plan M) + 3.000 (IPS) + 1.500 (Backup).
    await expect(side.locator('.total-row')).toContainText('OMR 13.500')

    // Change the plan to XL on this step: Backup is included there, so the
    // BSS add-on is dropped and the catalog one stays.
    await page.locator('input[type="radio"][name="plan"][value="xl"]').check()
    await expect(side.locator('.total-row')).toContainText('OMR 33.000')
    await expect(side).not.toContainText('Backup')
    const cart = await readCart(page)
    expect(cart.plan).toBe('xl')
    expect(cart.packageSku).toBe('plan.xl')
    expect(cart.addons).toEqual(['ips'])
  })

  test('/checkout shows the add-on line, the total, and both POSTs carry addons + package_sku', async ({ page }) => {
    const bodies: Record<string, any> = {}
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.route('**/api/tenant/orgs', (route) => {
      if (route.request().method() === 'POST') {
        bodies.org = JSON.parse(route.request().postData() || '{}')
        return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'org-1', slug: 'demo-co', name: 'Demo Co', status: 'pending_payment' }) })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    })
    await page.route('**/api/billing/checkout', (route) => {
      bodies.checkout = JSON.parse(route.request().postData() || '{}')
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ order_id: 'order-1', paid_by_credit: true }) })
    })
    await page.route('**/api/provisioning/start', (route) => {
      bodies.provision = JSON.parse(route.request().postData() || '{}')
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'prov-1', status: 'running', steps: [] }) })
    })
    // The post-checkout redirect leaves the storefront; sink it so the test stays hermetic.
    await page.route('https://console.**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<title>console</title>' }))

    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m', addons: ['ips', 'addon.backup'] })
    await page.goto('/checkout')

    await expect(page.getByText(/Order summary/i)).toBeVisible({ timeout: 10_000 })
    const summary = page.getByText(/Order summary/i).locator('..')
    await expect(summary).toContainText('+ Backup')
    await expect(summary).toContainText('OMR 1.500')
    await expect(summary).toContainText('+ Intrusion Prevention')
    await expect(page.getByText('Total (monthly)').locator('..')).toContainText('OMR 13.500')

    const purchase = page.getByRole('button', { name: /Purchase|Launch my Organization/i }).first()
    await expect(purchase).toBeVisible()
    await Promise.all([
      page.waitForRequest((r) => r.url().includes('/api/provisioning/start') && r.method() === 'POST', { timeout: 15_000 }),
      purchase.click(),
    ])

    expect(bodies.org, 'Organization create body captured').toBeTruthy()
    expect(bodies.org.plan_id).toBe('m')
    expect(bodies.org.package_sku).toBe('plan.m')
    expect(bodies.org.addons).toEqual(['ips', 'addon.backup'])

    expect(bodies.checkout, 'billing checkout body captured').toBeTruthy()
    expect(bodies.checkout.plan_id).toBe('m')
    expect(bodies.checkout.package_sku).toBe('plan.m')
    expect(bodies.checkout.addons).toEqual(['ips', 'addon.backup'])
  })
})
