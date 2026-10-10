// Marketplace — the package comparison table on /plans (#6971).
//
// Renders the REAL page against `npm run build && npm run preview` with the
// BSS endpoint mocked via page.route(), in the same hermetic shape as
// customer-journey.spec.ts. The page learns its chargeback host from
// `window.__ORG_CHARGEBACK_URL__` here (the runtime hook a Sovereign may seed;
// on a real host it is derived from marketplace.<fqdn>), because the preview
// runs on localhost where there is nothing to derive from.
//
// Walked, not just rendered: the add-on is ticked, the package is chosen, and
// the cart + both checkout POST bodies are read back.
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
    route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
  )
  await page.route('**/api/catalog/apps**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
  )
}

async function readCart(page: Page): Promise<any> {
  return page.evaluate(() => JSON.parse(localStorage.getItem('org-cart') || 'null'))
}

test.describe('package comparison table (/plans, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('renders S/M/L/XL with prices, included quantities, the three cell states and the up-sell hint', async ({ page }) => {
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

    // Included → ✓, Optional → "+ price", with the hint, Not offered → —.
    const backupXL = page.getByTestId('package-cell-backup-plan.xl')
    await expect(backupXL).toHaveAttribute('data-state', 'included')
    await expect(backupXL).toContainText('✓')
    await expect(backupXL.locator('.pk-hint')).toHaveCount(0)

    const backupS = page.getByTestId('package-cell-backup-plan.s')
    await expect(backupS).toHaveAttribute('data-state', 'optional')
    await expect(backupS).toContainText('+ 1.500 OMR')
    await expect(backupS.locator('.pk-hint')).toHaveText('Included from XL')
    await expect(backupS.getByRole('checkbox')).toBeVisible()

    const ipS = page.getByTestId('package-cell-dedicated-ip-plan.s')
    await expect(ipS).toHaveAttribute('data-state', 'not_offered')
    await expect(ipS).toHaveText('—')

    // Quantity feature shows the included amount with its unit.
    await expect(page.getByTestId('package-cell-bandwidth-plan.l')).toHaveText('250 Mbps')

    // Default recommendation is the middle package, M, and it is pre-selected.
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-recommended', 'true')
    await expect(page.getByTestId('package-col-plan.s')).toHaveAttribute('data-recommended', 'false')
    await expect(page.locator('.pk-hat')).toHaveText(/Recommended/i)
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-selected', 'true')
    const cart = await readCart(page)
    expect(cart.plan).toBe('m')
    expect(cart.packageSku).toBe('plan.m')
    expect(cart.packageAddons).toEqual([])

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

  test('ticking an add-on and choosing the package continues to /apps with plan.sku + addons in the cart', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans')
    await expect(page.getByTestId('package-table')).toBeVisible({ timeout: 10_000 })

    // Ticking Backup in the S column selects S and records the add-on.
    await page.getByTestId('package-addon-plan.s-backup').check()
    await expect(page.getByTestId('package-col-plan.s')).toHaveAttribute('data-selected', 'true')
    await expect(page.getByTestId('package-col-plan.m')).toHaveAttribute('data-selected', 'false')
    let cart = await readCart(page)
    expect(cart.plan).toBe('s')
    expect(cart.packageSku).toBe('plan.s')
    expect(cart.packageAddons).toEqual([
      { sku: 'addon.backup', feature: 'backup', name: 'Backup', price_month: '1.500', currency: 'OMR' },
    ])

    // Switching to XL drops the S add-on (Backup is included there).
    await page.getByTestId('package-choose-plan.xl').click()
    await page.waitForURL(/\/apps/, { timeout: 10_000 })
    cart = await readCart(page)
    expect(cart.plan).toBe('xl')
    expect(cart.planName).toBe('XL')
    expect(cart.packageSku).toBe('plan.xl')
    expect(cart.packageAddons).toEqual([])
  })

  test('falls back to the legacy plan deck, unchanged, and logs once when the endpoint fails', async ({ page }) => {
    const warnings: string[] = []
    page.on('console', (m) => {
      if (m.type() === 'warning' && m.text().includes('[packages]')) warnings.push(m.text())
    })
    await pointAtChargeback(page)
    await mockPackages(page, 503, { error: 'down' })
    await page.goto('/plans')

    // The legacy deck: five catalog cards (S/M/L/XL/Flexi) from the mocked
    // catalog, with the hardcoded capability gutter it has always rendered.
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

test.describe('package choice reaches checkout (#6971)', () => {
  test('the order summary lists the ticked add-on and both POSTs carry plan.sku + the add-on SKU', async ({ page }) => {
    const bodies: Record<string, any> = {}
    await mockCatalog(page)
    await page.route('**/api/auth/me', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ user: { id: 'user-1', email: 'demo@example.com', name: 'Demo User' } }) }),
    )
    await page.route('**/api/billing/balance', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ credit_baisa: 0, entries: [] }) }),
    )
    await page.route('**/api/tenant/check-slug/**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ available: true }) }),
    )
    await page.route('**/api/tenant/orgs', (route) => {
      if (route.request().method() === 'POST') {
        bodies.org = JSON.parse(route.request().postData() || '{}')
        return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'org-1', slug: 'demo-co', name: 'Demo Co', status: 'pending_payment' }) })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    })
    // #6971 — the order summary renders the SERVER's quote, not a client-side
    // sum: the same body the checkout POST carries goes to /billing/quote and
    // the page shows the lines + total it answers with.
    await page.route('**/api/billing/quote', (route) => {
      bodies.quote = JSON.parse(route.request().postData() || '{}')
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
        currency: 'OMR', price_source: 'bss:OpenOva plans@2026-09-11', package_sku: 'plan.m', plan_id: 'm',
        plan_amount_baisa: 9000, topology: 'single-region', topology_amount_baisa: 0,
        lines: [{ sku: 'addon.backup', name: 'Backup', amount_baisa: 1500 }],
        amount_baisa: 10500, amount_omr: 11,
      }) })
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

    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
        localStorage.setItem('org-cart', JSON.stringify({
          plan: 'm',
          planName: 'M',
          apps: ['1'],
          addons: ['waf'],
          orgName: 'Demo Co',
          subdomain: 'demo-co',
          email: 'demo@example.com',
          tld: 'omani.homes',
          agents: [],
          appConfigs: {},
          packageSku: 'plan.m',
          packageAddons: [{ sku: 'addon.backup', feature: 'backup', name: 'Backup', price_month: '1.500', currency: 'OMR' }],
        }))
      } catch (_) {}
    })
    await page.goto('/checkout')

    // The add-on is a priced line and is in the total — both from the server's
    // quote: 9.000 (plan M) + 1.500 (backup) = 10.500.
    await expect(page.getByText(/Order summary/i)).toBeVisible({ timeout: 10_000 })
    const line = page.getByTestId('checkout-package-addon-addon.backup')
    await expect(line).toContainText('Backup')
    await expect(line).toContainText('OMR 1.500')
    await expect(page.getByTestId('checkout-total')).toContainText('OMR 10.500')

    // The quote was asked with the checkout body's pricing fields.
    expect(bodies.quote, 'billing quote body captured').toBeTruthy()
    expect(bodies.quote.plan_id).toBe('m')
    expect(bodies.quote.package_sku).toBe('plan.m')
    expect(bodies.quote.addons).toEqual(['waf', 'addon.backup'])
    expect(bodies.quote.topology).toBe('single-region')

    const purchase = page.getByRole('button', { name: /Purchase|Launch my Organization/i }).first()
    await expect(purchase).toBeVisible()
    await Promise.all([
      page.waitForRequest((r) => r.url().includes('/api/provisioning/start') && r.method() === 'POST', { timeout: 15_000 }),
      purchase.click(),
    ])

    expect(bodies.org, 'Organization create body captured').toBeTruthy()
    expect(bodies.org.plan_id).toBe('m')
    expect(bodies.org.package_sku).toBe('plan.m')
    expect(bodies.org.addons).toEqual(['waf', 'addon.backup'])

    expect(bodies.checkout, 'billing checkout body captured').toBeTruthy()
    expect(bodies.checkout.plan_id).toBe('m')
    expect(bodies.checkout.package_sku).toBe('plan.m')
    expect(bodies.checkout.addons).toEqual(['waf', 'addon.backup'])
  })
})
