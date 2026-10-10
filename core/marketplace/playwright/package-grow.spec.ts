// Marketplace — "When you reach your package": Capped or Grow with me (#6971).
//
// A package (S / M / L / XL) is a prepaid monthly commitment whose shape is an
// allowance. Capped (the default) keeps the bill at the package + add-ons;
// Grow lets resources grow above the allowance up to a ceiling, the usage
// billed after the month at the package's OWN rates (bigger packages grow
// cheaper). DR (active-passive) is included on XL and, on S / M / L, available
// only with Grow, the standby billed as usage.
//
// Renders the REAL pages against `npm run build && npm run preview`, the BSS
// document mocked via page.route() — fixtures/public-packages-v4-grow.json is
// the v3 document plus `packages[].grow` (ceiling + overage_rates) on all four
// packages (XL's ceiling is its own headline: it cannot grow) and DR
// `grow_only` cells on S / M / L. package-ladder.spec.ts keeps the v2 / v3
// documents covered; the first test below re-checks that they show no grow.
//
// READ-ONLY against clusters: no kubectl, no chart bumps, no Pod ops.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const CHARGEBACK = 'https://chargeback.t99.omani.works'
const PACKAGES_URL = `${CHARGEBACK}/api/v1/public/packages`
const HERE = fileURLToPath(new URL('.', import.meta.url))
const FIXTURE_V4 = JSON.parse(readFileSync(join(HERE, '..', 'fixtures', 'public-packages-v4-grow.json'), 'utf8'))
const FIXTURE_V3 = JSON.parse(readFileSync(join(HERE, '..', 'fixtures', 'public-packages-v3-icons.json'), 'utf8'))
const SHOTS = join(HERE, '..', '..', '..', 'docs', 'ledger', 'screenshots')

const CATALOG_PLANS = [
  { id: 's', slug: 's', name: 'S', cpu: '1 vCPU', memory: '2 GB', storage: '25 GB', price_omr: 2.49, popular: false, features: [], description: '' },
  { id: 'm', slug: 'm', name: 'M', cpu: '2 vCPU', memory: '4 GB', storage: '50 GB', price_omr: 4.49, popular: true, features: [], description: '' },
  { id: 'l', slug: 'l', name: 'L', cpu: '4 vCPU', memory: '8 GB', storage: '100 GB', price_omr: 7.99, popular: false, features: [], description: '' },
  { id: 'xl', slug: 'xl', name: 'XL', cpu: '8 vCPU', memory: '16 GB', storage: '250 GB', price_omr: 13.99, popular: false, features: [], description: '' },
]
const APPS = [
  { id: '1', name: 'WordPress', slug: 'wordpress', tagline: 'Website & blog platform', description: 'Blogs and sites.', category: 'cms', icon: 'W', color: '#21759b', free: true, features: [], website: '', license: 'GPL-2.0', system: false, kind: 'business', deployable: true, dependencies: [] },
]

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v))
}

async function pointAtChargeback(page: Page): Promise<void> {
  await page.addInitScript((url) => {
    ;(window as any).__ORG_CHARGEBACK_URL__ = url
  }, CHARGEBACK)
}

async function mockPackages(page: Page, body: unknown = FIXTURE_V4): Promise<void> {
  await page.route(PACKAGES_URL, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'public, max-age=60' },
      body: JSON.stringify(body),
    }),
  )
}

const ICON_DIR = join(HERE, 'fixtures', 'icons')
const ICONS_BY_HASH = new Map<string, string>(
  readdirSync(ICON_DIR)
    .filter((f) => f.endsWith('.svg'))
    .map((f) => {
      const body = readFileSync(join(ICON_DIR, f), 'utf8')
      return [createHash('sha256').update(body).digest('hex'), body] as const
    }),
)
async function mockIcons(page: Page): Promise<void> {
  await page.route('**/api/v1/public/icons/**', (route) => {
    const body = ICONS_BY_HASH.get(new URL(route.request().url()).pathname.split('/').pop() || '')
    if (!body) return route.fulfill({ status: 404, body: '' })
    return route.fulfill({ status: 200, contentType: 'image/svg+xml', headers: { 'Access-Control-Allow-Origin': '*' }, body })
  })
}

async function mockCatalog(page: Page): Promise<void> {
  await page.route('**/api/catalog/plans**', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(CATALOG_PLANS) }))
  await page.route('**/api/catalog/addons', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  await page.route('**/api/catalog/apps**', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(APPS) }))
  await page.route('**/api/catalog/industries', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  await page.route('**/api/catalog/regions', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ key: 'me-east-215-a', label: 'Region A' }, { key: 'me-east-215-b', label: 'Region B' }]) }),
  )
  await page.route('**/api/tenant/check-slug/**', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ available: true }) }))
  await page.route('**/api/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ user: { id: 'user-1', email: 'demo@example.com', name: 'Demo User' } }) }),
  )
}

// A stand-in for POST /billing/quote that prices the way billing does and
// ECHOES the grow fields — so the page's read-back is the quote's.
const PLAN_BAISA: Record<string, number> = { 'plan.s': 2490, 'plan.m': 4490, 'plan.l': 7990, 'plan.xl': 13990 }
const ADDON_BAISA: Record<string, { name: string; amount: number }> = {
  'addon.backup': { name: 'Backup', amount: 1500 },
  'addon.domain': { name: 'Domain', amount: 500 },
}
async function mockQuote(page: Page, bodies: any[] = []): Promise<void> {
  await page.route('**/api/billing/quote', (route) => {
    const body = JSON.parse(route.request().postData() || '{}')
    bodies.push(body)
    const pkg: string = body.package_sku || ''
    const plan = PLAN_BAISA[pkg] ?? 4490
    const lines = (body.addons || []).flatMap((sku: string) => (ADDON_BAISA[sku] ? [{ sku, name: ADDON_BAISA[sku].name, amount_baisa: ADDON_BAISA[sku].amount }] : []))
    const total = plan + lines.reduce((s: number, l: any) => s + l.amount_baisa, 0)
    const p = FIXTURE_V4.packages.find((x: any) => x.sku === pkg)
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        currency: 'OMR',
        price_source: 'bss:OpenOva plans@2026-10-10',
        package_sku: pkg,
        plan_id: body.plan_id,
        plan_amount_baisa: plan,
        topology: body.topology || 'single-region',
        topology_amount_baisa: 0,
        lines,
        amount_baisa: total,
        amount_omr: Math.ceil(total / 1000),
        overage_mode: body.overage_mode || 'capped',
        ...(body.overage_mode === 'grow' ? { grow_ceiling: body.grow_ceiling ?? p?.grow?.ceiling, overage_rates: p?.grow?.overage_rates } : {}),
        ...(body.spend_limit_month ? { spend_limit_month: body.spend_limit_month } : {}),
      }),
    })
  })
}

async function seedCart(page: Page, overrides: Record<string, unknown>): Promise<void> {
  const cart = {
    plan: 'm', planName: 'M', apps: ['1'], addons: [], orgName: 'Demo Co', subdomain: 'demo-co',
    email: 'demo@example.com', tld: 'omani.homes', agents: [], appConfigs: {}, packageSku: 'plan.m',
    ...overrides,
  }
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

type Box = { top: number; left: number; right: number; bottom: number }
async function boxOf(l: Locator): Promise<Box> {
  const b = await l.boundingBox()
  if (!b) throw new Error('no bounding box')
  return { top: b.y, left: b.x, right: b.x + b.width, bottom: b.y + b.height }
}
function intersects(a: Box, b: Box): boolean {
  return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom
}

async function shootTall(page: Page, path: string): Promise<void> {
  const original = page.viewportSize() ?? { width: 1280, height: 720 }
  await page.evaluate(() => window.scrollTo(0, 0))
  const h = await page.evaluate(() => Math.min(document.documentElement.scrollHeight, 4000))
  await page.setViewportSize({ width: original.width, height: Math.max(original.height, Math.ceil(h)) })
  await page.waitForTimeout(200)
  await page.screenshot({ path, fullPage: false })
  await page.setViewportSize(original)
}

test.describe('grow mode on the wizard (#6971, document v4)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
    await mockIcons(page)
    await pointAtChargeback(page)
  })

  test('/plans: the "every package can grow" line and the cheapest growable vCPU rate from the document; DR on S/M/L reads "with Grow"; a v3 document shows none of it', async ({ page }) => {
    await mockPackages(page)
    await page.goto('/plans')
    const note = page.getByTestId('package-grow-note')
    await expect(note).toBeVisible({ timeout: 10_000 })
    await expect(note).toContainText('Every package can grow with you.')
    // L is the cheapest package that CAN grow (XL's ceiling is its headline).
    await expect(page.getByTestId('package-grow-cheapest')).toHaveText('Bigger packages grow cheaper: an extra vCPU from 1.598 OMR / mo on L.')
    for (const sku of ['plan.s', 'plan.m', 'plan.l']) await expect(page.getByTestId(`package-cell-dr_topology-${sku}`)).toContainText('with Grow')
    await expect(page.getByTestId('package-cell-dr_topology-plan.xl')).toHaveText('active-passive')
    // The allowance cells follow the customer's mode, not the cell: the
    // allowance alone, and ONE legend line beside the cards — no "hard cap" /
    // "then metered" anywhere in the matrix.
    await expect(page.getByTestId('package-cell-bandwidth-plan.m')).toHaveText('100 Mbps')
    await expect(page.getByTestId('package-cell-disk-plan.l')).toHaveText('100 GB')
    await expect(page.getByTestId('package-ladder')).not.toContainText(/hard cap|then metered/)
    await expect(page.getByTestId('package-grow-legend')).toHaveCount(1)
    await expect(page.getByTestId('package-grow-legend')).toHaveText('Capped by default · or grow, billed per use')

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await mockCatalog(page)
    await mockIcons(page)
    await mockPackages(page, FIXTURE_V3)
    await page.goto('/plans')
    await expect(page.getByTestId('package-ladder')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-grow-note')).toHaveCount(0)
    // Without the grow model the cell's own overage word stays, and no legend.
    await expect(page.getByTestId('package-grow-legend')).toHaveCount(0)
    await expect(page.getByTestId('package-cell-bandwidth-plan.m').locator('.ld-hint')).toHaveText('hard cap')
  })

  test('/addons "In your package": the allowance says "· capped" while Capped and "· grows up to N" (the chosen ceiling) in Grow — never the cell\'s word', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, {})
    await page.goto('/addons')
    const bw = page.getByTestId('addons-included-value-bandwidth')
    await expect(bw).toHaveText('100 Mbps · capped', { timeout: 10_000 })
    await expect(page.getByTestId('addons-included-value-disk')).toHaveText('50 GB · capped')
    await expect(page.getByTestId('addons-included')).not.toContainText(/hard cap|then metered/)
    await page.getByTestId('mode-grow').click()
    // M grows to 250 Mbps / 100 GB by default…
    await expect(bw).toHaveText('100 Mbps · grows up to 250 Mbps')
    await expect(page.getByTestId('addons-included-value-disk')).toHaveText('50 GB · grows up to 100 GB')
    // …and follows the ceiling the customer sets.
    await page.getByTestId('grow-step-bandwidth_mbps-dec').click()
    const v = await page.getByTestId('grow-ceiling-bandwidth_mbps').getAttribute('data-value')
    await expect(bw).toHaveText(`100 Mbps · grows up to ${v} Mbps`)
    await page.getByTestId('mode-capped').click()
    await expect(bw).toHaveText('100 Mbps · capped')
  })

  test('/addons at 1400 px: the Grow card says one sentence — the rates are listed once, in the grid; an add-on name is never squeezed beside its price', async ({ page }) => {
    await page.setViewportSize({ width: 1400, height: 900 })
    await mockPackages(page)
    await seedCart(page, { overageMode: 'grow' })
    await page.goto('/addons')
    await expect(page.getByTestId('grow-panel')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('mode-grow-rates')).toHaveCount(0)
    await expect(page.getByTestId('mode-grow')).not.toContainText('per extra')
    await expect(page.getByTestId('mode-grow')).not.toContainText('1.796')
    await expect(page.getByTestId('grow-rates').getByText('+1.796 OMR')).toHaveCount(1)
    await expect(page.getByTestId('addons-grow').getByText(/1\.796/)).toHaveCount(1)
    // Every add-on name: one line at this width (its price sits under it).
    const names = page.locator('[data-testid^="addon-tile-"] .addon-card-name')
    const n = await names.count()
    expect(n).toBeGreaterThan(3)
    for (let i = 0; i < n; i++) {
      const h = await names.nth(i).evaluate((el) => el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight))
      expect(h, `${await names.nth(i).textContent()} wraps`).toBeLessThan(2.2)
    }
  })

  test('/addons: Capped is the default and promises the package + add-ons; switching to Grow shows M\'s own rates from the document', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, { addons: ['addon.backup'] })
    await page.goto('/addons')
    const block = page.getByTestId('addons-grow')
    await expect(block).toBeVisible({ timeout: 10_000 })
    await expect(block).toHaveAttribute('data-mode', 'capped')
    await expect(page.getByTestId('mode-capped')).toHaveAttribute('aria-checked', 'true')
    // 4.490 + 1.500 backup.
    await expect(page.getByTestId('mode-capped-body')).toContainText('Never pay more than OMR 5.990 / mo')
    await expect(page.getByTestId('grow-panel')).toHaveCount(0)

    await page.getByTestId('mode-grow').click()
    await expect(block).toHaveAttribute('data-mode', 'grow')
    await expect(page.getByTestId('grow-rate-vcpu')).toContainText('+1.796 OMR per extra vCPU / mo')
    await expect(page.getByTestId('grow-rate-memory')).toContainText('+0.337 OMR per extra GB of memory / mo')
    await expect(page.getByTestId('grow-rate-disk')).toContainText('+0.035 OMR per extra GB of disk / mo')
    await expect(page.getByTestId('grow-rate-bandwidth')).toContainText('+1.253 OMR per extra Mbps / mo')
    expect((await readCart(page)).overageMode).toBe('grow')
    // Persisted: a reload keeps Grow.
    await page.reload()
    await expect(page.getByTestId('addons-grow')).toHaveAttribute('data-mode', 'grow', { timeout: 10_000 })
  })

  test('the rates follow the document: change M\'s vCPU rate and the card says the new one (nothing hardcoded)', async ({ page }) => {
    const doc = clone(FIXTURE_V4)
    doc.packages.find((p: any) => p.sku === 'plan.m').grow.overage_rates[0].price_month = '2.345'
    await mockPackages(page, doc)
    await seedCart(page, { overageMode: 'grow' })
    await page.goto('/addons')
    await expect(page.getByTestId('grow-rate-vcpu')).toContainText('+2.345 OMR per extra vCPU / mo', { timeout: 10_000 })
    await expect(page.getByTestId('grow-rate-vcpu')).toHaveAttribute('data-price', '2.345')
    // The rate is listed once, in the grid — the Grow card no longer repeats it.
    await expect(page.getByTestId('mode-grow-rates')).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('1.796')
  })

  test('ceiling steppers are bounded by M\'s allowance (2 vCPU) and its grow ceiling (4 vCPU); the ceiling persists and the spend limit validates', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, { overageMode: 'grow' })
    await page.goto('/addons')
    const val = page.getByTestId('grow-ceiling-vcpu')
    const inc = page.getByTestId('grow-step-vcpu-inc')
    const dec = page.getByTestId('grow-step-vcpu-dec')
    // Starts at the package's own ceiling: + is disabled.
    await expect(val).toHaveAttribute('data-value', '4', { timeout: 10_000 })
    await expect(inc).toBeDisabled()
    await dec.click()
    await expect(val).toHaveAttribute('data-value', '3')
    await dec.click()
    await expect(val).toHaveAttribute('data-value', '2')
    await expect(dec).toBeDisabled()
    await expect(page.getByTestId('grow-ceiling-row-vcpu')).toContainText('2 vCPU included')
    expect((await readCart(page)).growCeiling).toEqual({ vcpu: 2, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 })
    // Disk steps by 10 GB within 50 … 100.
    await page.getByTestId('grow-step-disk_gb-dec').click()
    await expect(page.getByTestId('grow-ceiling-disk_gb')).toHaveAttribute('data-value', '90')
    // Back to the package's own ceiling → the cart carries none.
    await inc.click(); await inc.click(); await page.getByTestId('grow-step-disk_gb-inc').click()
    expect((await readCart(page)).growCeiling).toBeNull()

    const spend = page.getByTestId('grow-spend')
    await spend.fill('abc')
    await expect(page.getByTestId('grow-spend-hint')).toContainText('Enter an amount')
    await spend.fill('25')
    await spend.blur()
    await expect(spend).toHaveValue('25.000')
    expect((await readCart(page)).spendLimitMonth).toBe('25.000')
  })

  test('the upgrade hint: on M, L\'s headline at M\'s rates (2 × 1.796 + 4 × 0.337) makes 9.430 / mo against L\'s 7.990; switching moves to L; XL has no grow block', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, { overageMode: 'grow' })
    await page.goto('/addons')
    const hint = page.getByTestId('grow-upgrade')
    await expect(hint).toBeVisible({ timeout: 10_000 })
    await expect(hint).toContainText('If you regularly use 2 extra vCPU and 4 extra GB, L is cheaper')
    await expect(hint).toContainText('M plus that usage comes to OMR 9.430 / mo. L includes it for OMR 7.990 / mo')
    await page.getByTestId('grow-upgrade-switch').click()
    await expect.poll(async () => (await readCart(page)).packageSku).toBe('plan.l')
    await expect(page.getByTestId('grow-rate-vcpu')).toContainText('+1.598 OMR')

    // XL cannot grow in this book: no block.
    await page.evaluate(() => {
      const c = JSON.parse(localStorage.getItem('org-cart') || '{}')
      localStorage.setItem('org-cart', JSON.stringify({ ...c, plan: 'xl', planName: 'XL', packageSku: 'plan.xl' }))
    })
    await page.reload()
    await expect(page.getByTestId('addons-included')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('addons-grow')).toHaveCount(0)
  })

  test('/bcp on M: active-passive is locked "Needs Grow" while capped, selectable after "Switch to Grow" — billed as usage; on XL it is included', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, {})
    await page.goto('/bcp')
    const hot = page.getByTestId('topology-card-hot')
    await expect(hot).toHaveAttribute('data-locked', 'true', { timeout: 10_000 })
    await expect(hot).toContainText('Needs Grow — standby billed as usage')
    await expect(hot.getByRole('radio')).toHaveCount(0)
    await expect(page.getByTestId('topology-switch-plan.xl')).toContainText('or switch to XL')
    await page.getByTestId('topology-switch-grow').click()
    await expect(hot).toHaveAttribute('data-locked', 'false')
    await expect(page.getByTestId('topology-billed-as-usage')).toHaveText('Billed as usage')
    await hot.getByRole('radio').check()
    await expect.poll(async () => (await readCart(page)).appConfigs?.postgres?.active_hot_standby).toBe(true)
    expect((await readCart(page)).overageMode).toBe('grow')

    // Back on /addons, going Capped drops the hot-standby pick.
    await page.goto('/addons')
    await page.getByTestId('mode-capped').click()
    await expect.poll(async () => (await readCart(page)).appConfigs?.postgres?.active_hot_standby).toBe(false)
  })

  test('/review shows the mode from the quote\'s echo, Edit goes back to it; the quote body carries overage_mode / grow_ceiling / spend_limit_month', async ({ page }) => {
    const bodies: any[] = []
    await mockQuote(page, bodies)
    await mockPackages(page)
    await seedCart(page, { addons: ['addon.backup'] })
    await page.goto('/review')
    const ov = page.getByTestId('review-overage')
    await expect(page.getByTestId('review-overage-title')).toHaveText('Capped at OMR 5.990 / mo', { timeout: 10_000 })
    await expect(ov).toHaveAttribute('data-mode', 'capped')
    expect(bodies.at(-1)).toMatchObject({ package_sku: 'plan.m', overage_mode: 'capped' })
    expect(bodies.at(-1).grow_ceiling).toBeUndefined()

    await page.getByTestId('review-overage-edit').click()
    await page.waitForURL(/\/addons/, { timeout: 10_000 })
    await page.getByTestId('mode-grow').click()
    await page.getByTestId('grow-step-vcpu-dec').click()
    await page.getByTestId('grow-spend').fill('25')
    await page.getByTestId('step-bar').getByRole('link', { name: /Continue/ }).click()
    await page.waitForURL(/\/bcp/, { timeout: 10_000 })
    await page.getByTestId('step-bar').getByRole('link', { name: /Review/ }).click()
    await page.waitForURL(/\/review/, { timeout: 10_000 })
    await expect(page.getByTestId('review-overage-title')).toHaveText('Grow with me', { timeout: 10_000 })
    await expect(page.getByTestId('review-overage-detail')).toHaveText(
      'up to 3 vCPU · 8 GB memory · 100 GB disk · 250 Mbps · spend limit OMR 25.000 / mo · usage above the package billed after the month',
    )
    await expect(page.getByTestId('review-total-usage')).toContainText('billed after the month')
    expect(bodies.at(-1)).toMatchObject({
      package_sku: 'plan.m',
      overage_mode: 'grow',
      grow_ceiling: { vcpu: 3, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 },
      spend_limit_month: '25.000',
    })
  })

  test('/review: the docked back button never overlaps the last add-on row (1280 and 400 px)', async ({ page }) => {
    await mockQuote(page)
    await mockPackages(page)
    await seedCart(page, { addons: ['addon.backup', 'addon.domain'] })
    for (const width of [1280, 400]) {
      await page.setViewportSize({ width, height: 800 })
      await page.goto('/review')
      await expect(page.getByTestId('review-addon-addon.domain')).toBeVisible({ timeout: 10_000 })
      // Scroll so the last add-on row sits at the very bottom of the viewport.
      const row = page.getByTestId('review-addon-addon.domain')
      await row.evaluate((el) => el.scrollIntoView({ block: 'end' }))
      await page.waitForTimeout(150)
      const back = await boxOf(page.getByTestId('review-back'))
      const bar = await boxOf(page.getByTestId('step-bar'))
      const last = await boxOf(row)
      expect(intersects(back, last), `back button over the last add-on at ${width}px`).toBe(false)
      expect(intersects(bar, last), `step bar over the last add-on at ${width}px`).toBe(false)
      // At the foot of the page, the page's last content ends above the bar.
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
      await page.waitForTimeout(150)
      const foot = await boxOf(page.locator('[data-testid="review-addons-summary"]'))
      expect(foot.bottom).toBeLessThanOrEqual((await boxOf(page.getByTestId('step-bar'))).top)
    }
  })

  test('/addons: a click on the step-bar Continue lands on /bcp', async ({ page }) => {
    await mockPackages(page)
    await seedCart(page, {})
    await page.goto('/addons')
    await expect(page.getByTestId('addons-grow')).toBeVisible({ timeout: 10_000 })
    await page.getByTestId('step-bar').getByRole('link', { name: /Continue/ }).click()
    await page.waitForURL(/\/bcp$/, { timeout: 10_000 })
    await expect(page.getByTestId('topology-card-hot')).toBeVisible({ timeout: 10_000 })
  })
})

test.describe('grow screenshots (#6971)', () => {
  for (const [width, scheme] of [[1400, 'light'], [400, 'light'], [1400, 'dark'], [400, 'dark']] as const) {
    test(`addons grow block, bcp and review at ${width}px, ${scheme}`, async ({ page }) => {
      test.setTimeout(60_000)
      await page.emulateMedia({ colorScheme: scheme })
      await mockCatalog(page)
      await mockIcons(page)
      await mockQuote(page)
      await pointAtChargeback(page)
      await mockPackages(page)
      await seedCart(page, {
        addons: ['addon.backup', 'addon.domain'],
        overageMode: 'grow',
        growCeiling: { vcpu: 3, memory_gb: 6, disk_gb: 100, bandwidth_mbps: 250 },
        spendLimitMonth: '25.000',
        appConfigs: { postgres: { active_hot_standby: true, primary_region: 'me-east-215-a', replica_region: 'me-east-215-b' } },
      })
      await page.setViewportSize({ width, height: 900 })
      const suffix = `${width}${scheme === 'dark' ? '-dark' : ''}`
      await page.goto('/plans')
      await expect(page.getByTestId('package-grow-note')).toBeVisible({ timeout: 10_000 })
      await page.waitForTimeout(300)
      await page.screenshot({ path: join(SHOTS, `marketplace-grow-plans-${suffix}.png`) })
      await page.goto('/addons')
      const block = page.getByTestId('addons-grow')
      await expect(block).toBeVisible({ timeout: 10_000 })
      // A tall viewport puts the fixed header and step bar at the page's head
      // and foot, so neither paints over the block in its own capture.
      const h = await page.evaluate(() => Math.min(document.documentElement.scrollHeight, 6000))
      await page.setViewportSize({ width, height: Math.ceil(h) })
      await page.waitForTimeout(250)
      await block.screenshot({ path: join(SHOTS, `marketplace-grow-addons-${suffix}.png`) })
      await page.setViewportSize({ width, height: 900 })
      for (const step of ['bcp', 'review']) {
        await page.goto(`/${step}`)
        await expect(page.getByTestId(step === 'bcp' ? 'topology-card-hot' : 'review-overage')).toBeVisible({ timeout: 10_000 })
        await shootTall(page, join(SHOTS, `marketplace-grow-${step}-${suffix}.png`))
      }
      // /bcp while Capped: active-passive "Needs Grow".
      await page.evaluate(() => {
        const c = JSON.parse(localStorage.getItem('org-cart') || '{}')
        localStorage.setItem('org-cart', JSON.stringify({ ...c, overageMode: 'capped', appConfigs: {} }))
      })
      await page.goto('/bcp')
      await expect(page.getByTestId('topology-needs-grow')).toBeVisible({ timeout: 10_000 })
      await shootTall(page, join(SHOTS, `marketplace-grow-bcp-needs-grow-${suffix}.png`))
    })
  }
})
