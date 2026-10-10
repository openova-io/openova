// Marketplace — the package LADDER: the v2 BSS document (0.1.61) feeding the
// six-step wizard (#6971).
//
// Renders the REAL pages against `npm run build && npm run preview` with the
// BSS endpoint mocked via page.route(), in the same hermetic shape as
// package-table.spec.ts (which keeps the v1-document and no-document paths
// covered — nothing here replaces those). The fixture IS the live document:
// fixtures/public-packages-v2.json is what BSS 0.1.61 on hw307 answered at
// GET /api/v1/public/packages on 2026-10-10, pretty-printed and otherwise
// untouched — the workbook's numbers: S 2.490 / M 4.490 / L 7.990 /
// XL 13.990, and the L→XL step-up at a 6.000 gap bundling Backup + AI SEO +
// AI builder + Domain (1.500 + 2.000 + 2.000 + 0.500). Every price on every
// page below is one of those; no catalog add-on carries one.
//
//   step 1 /plans   — four cards (price, shape, guarantee, disk), ONE grouped
//                     comparison with the group headers in order, level /
//                     teaser / add-on / access cells, the floor strip once;
//                     computed-style assertions against the PRODUCTION build
//                     (the ladder's CSS is a page import — the 2026-10-10
//                     lesson); no floating bar over any cell; choosing M
//                     continues to Stack with the sku
//   step 3 /addons  — "In your package" / "Add-ons" / "Not on <pkg>", the
//                     running total, the step-up card and what switching
//                     clears, "Upgrade to L" keeping the apps, a stale
//                     catalog id carried over to its BSS twin; the step bar
//                     never covers the content
//   step 4 /bcp     — hot-standby locked on L ("Included from XL", switch
//                     keeps the apps), selectable and INCLUDED on XL
//   /review         — package line + add-on lines from the quote, the plan
//                     cards and the headroom ring sized from the document's
//                     shape, the usage buckets without user counts, the floor
//                     as a footnote
//
// READ-ONLY against clusters: no kubectl, no chart bumps, no Pod ops.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const CHARGEBACK = 'https://chargeback.t99.omani.works'
const PACKAGES_URL = `${CHARGEBACK}/api/v1/public/packages`
const HERE = fileURLToPath(new URL('.', import.meta.url))
const FIXTURE_V2 = JSON.parse(readFileSync(join(HERE, '..', 'fixtures', 'public-packages-v2.json'), 'utf8'))
const FIXTURE_V1 = JSON.parse(readFileSync(join(HERE, '..', 'fixtures', 'public-packages.json'), 'utf8'))

const CATALOG_PLANS = [
  { id: 's', slug: 's', name: 'S', cpu: '1 vCPU', memory: '2 GB', storage: '25 GB', price_omr: 2.49, popular: false, features: [], description: '' },
  { id: 'm', slug: 'm', name: 'M', cpu: '2 vCPU', memory: '4 GB', storage: '50 GB', price_omr: 4.49, popular: true, features: [], description: '' },
  { id: 'l', slug: 'l', name: 'L', cpu: '4 vCPU', memory: '8 GB', storage: '100 GB', price_omr: 7.99, popular: false, features: [], description: '' },
  { id: 'xl', slug: 'xl', name: 'XL', cpu: '8 vCPU', memory: '16 GB', storage: '250 GB', price_omr: 13.99, popular: false, features: [], description: '' },
]

// /api/catalog/addons wire shape — free, as the catalog lane ships them. With
// a document none of these is offered; Daily Backup twins the Backup feature
// (a stale cart id is carried over to `addon.backup`), WAF twins a FLOOR item,
// IPS has no twin.
const CATALOG_ADDONS = [
  { id: 'daily-backup', slug: 'daily-backup', name: 'Daily Backup', description: 'Scheduled backups of your sites and databases', price_omr: 0, included: false },
  { id: 'waf', slug: 'waf', name: 'Web Application Firewall', description: 'Coraza WAF — OWASP CRS protection', price_omr: 0, included: false },
  { id: 'ips', slug: 'ips', name: 'Intrusion Prevention', description: 'Community-powered threat intelligence — CrowdSec', price_omr: 0, included: false },
  { id: 'custom-domain', slug: 'custom-domain', name: 'Custom Domain', description: 'Your brand, your domain — with automatic TLS', price_omr: 0, included: false },
]

const APPS = [
  { id: '1', name: 'WordPress', slug: 'wordpress', tagline: 'Website & blog platform', description: 'Blogs and sites.', category: 'cms', icon: 'W', color: '#21759b', free: true, features: [], website: '', license: 'GPL-2.0', system: false, kind: 'business', deployable: true, dependencies: [] },
]

async function pointAtChargeback(page: Page): Promise<void> {
  await page.addInitScript((url) => {
    ;(window as any).__ORG_CHARGEBACK_URL__ = url
  }, CHARGEBACK)
}

async function mockPackages(page: Page, body: unknown = FIXTURE_V2): Promise<void> {
  await page.route(PACKAGES_URL, (route) =>
    route.fulfill({
      status: 200,
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
  await page.route('**/api/catalog/regions', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ key: 'me-east-215-a', label: 'Region A' }, { key: 'me-east-215-b', label: 'Region B' }]) }),
  )
  await page.route('**/api/tenant/check-slug/**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ available: true }) }),
  )
}

// The /review total is the SERVER's: the page POSTs the checkout body's
// pricing fields to /billing/quote and renders what it answers. This stand-in
// prices the request the way billing does from the same v2 document.
const QUOTE_PLAN_BAISA: Record<string, number> = { 'plan.s': 2490, 'plan.m': 4490, 'plan.l': 7990, 'plan.xl': 13990 }
const QUOTE_ADDON_BAISA: Record<string, { name: string; amount: number; includedOn?: string[] }> = {
  'addon.backup': { name: 'Backup', amount: 1500, includedOn: ['plan.xl'] },
  'addon.ai_seo': { name: 'AI SEO ready', amount: 2000, includedOn: ['plan.xl'] },
  'addon.ai_builder': { name: 'AI website builder', amount: 2000, includedOn: ['plan.xl'] },
  'addon.domain': { name: 'Domain', amount: 500, includedOn: ['plan.xl'] },
  'addon.dedicated_ip': { name: 'Dedicated IP address', amount: 20833 },
}
async function mockQuote(page: Page, capture?: (body: any) => void): Promise<void> {
  await page.route('**/api/billing/quote', (route) => {
    const body = JSON.parse(route.request().postData() || '{}')
    capture?.(body)
    const pkg: string = body.package_sku || ''
    const planAmount = QUOTE_PLAN_BAISA[pkg] ?? 4490
    const lines = (body.addons || []).flatMap((sku: string) => {
      const a = QUOTE_ADDON_BAISA[sku]
      if (!a) return []
      const redundant = Boolean(a.includedOn?.includes(pkg))
      return [{ sku, name: a.name, amount_baisa: redundant ? 0 : a.amount, ...(redundant ? { redundant: true } : {}) }]
    })
    const topologyAmount = 0
    const total = planAmount + topologyAmount + lines.reduce((s: number, l: any) => s + l.amount_baisa, 0)
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        currency: 'OMR',
        price_source: pkg ? 'bss:OpenOva plans@2026-10-10' : 'catalog',
        package_sku: pkg || undefined,
        plan_id: body.plan_id,
        plan_amount_baisa: planAmount,
        topology: body.topology || 'single-region',
        topology_amount_baisa: topologyAmount,
        lines,
        amount_baisa: total,
        amount_omr: Math.ceil(total / 1000),
      }),
    })
  })
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

type Box = { top: number; left: number; right: number; bottom: number }
async function boxOf(l: Locator): Promise<Box> {
  const b = await l.boundingBox()
  if (!b) throw new Error('no bounding box')
  return { top: b.y, left: b.x, right: b.x + b.width, bottom: b.y + b.height }
}
function intersects(a: Box, b: Box): boolean {
  return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom
}

/** No element of `items` overlaps the fixed step bar, in the viewport as it stands. */
async function expectNothingUnderBar(page: Page, items: Locator): Promise<void> {
  const bar = await boxOf(page.getByTestId('step-bar'))
  const boxes = await items.evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return { top: r.top, left: r.left, right: r.right, bottom: r.bottom, text: (el.textContent || '').trim().slice(0, 40) }
    }),
  )
  const vh = page.viewportSize()!.height
  for (const b of boxes) {
    if (b.bottom <= 0 || b.top >= vh) continue // off-screen: not under anything
    expect(intersects(b, bar), `"${b.text}" sits under the step bar (${JSON.stringify(b)} vs ${JSON.stringify(bar)})`).toBe(false)
  }
}

/**
 * A screenshot of the whole page as a tall screen would show it: the viewport
 * is grown to the page height, so a fixed bar lands at the page's foot and a
 * sticky header at its head — a `fullPage` capture paints both at the scroll
 * position of the moment instead, which misreads as an overlap.
 */
async function shootTall(page: Page, path: string): Promise<void> {
  const original = page.viewportSize() ?? { width: 1280, height: 720 }
  await page.evaluate(() => window.scrollTo(0, 0))
  const h = await page.evaluate(() => Math.min(document.documentElement.scrollHeight, 4000))
  await page.setViewportSize({ width: original.width, height: Math.max(original.height, Math.ceil(h)) })
  await page.waitForTimeout(150)
  await page.screenshot({ path, fullPage: false })
  await page.setViewportSize(original)
}

// ────────────────────────────────────────────────────────────────────────
// Step 1 — /plans
// ────────────────────────────────────────────────────────────────────────

test.describe('step 1: the package ladder (/plans, v2 document, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('four cards with price, shape, guarantee and disk; the grouped comparison in order; level, teaser, add-on and access cells; the floor once — no checkbox', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans')

    await expect(page.getByTestId('package-ladder')).toBeVisible({ timeout: 10_000 })

    // The cards, in the server's order.
    const cards = page.locator('.ld-card')
    await expect(cards).toHaveCount(4)
    await expect(cards.nth(0)).toContainText('S')
    await expect(cards.nth(0)).toContainText('2.490')
    await expect(cards.nth(0)).toContainText('1 vCPU · 2 GB RAM')
    await expect(cards.nth(0)).toContainText('0.17 vCPU · 0.67 GB guaranteed')
    await expect(cards.nth(0)).toContainText('25 GB disk')
    await expect(cards.nth(1)).toContainText('4.490')
    await expect(cards.nth(1)).toContainText('0.33 vCPU · 1.33 GB guaranteed')
    await expect(cards.nth(3)).toContainText('XL')
    await expect(cards.nth(3)).toContainText('13.990')
    await expect(cards.nth(3)).toContainText('8 vCPU · 16 GB RAM')
    await expect(cards.nth(3)).toContainText('250 GB disk')
    // No tagline and no annual line in this book (empty / 0 → hidden).
    await expect(page.locator('.ld-tagline')).toHaveCount(0)
    await expect(page.locator('.ld-annual')).toHaveCount(0)

    // Recommended from the document's flag (M), pre-selected and stamped —
    // the posture of the legacy deck's "popular" plan.
    await expect(page.getByTestId('package-card-plan.m')).toHaveAttribute('data-recommended', 'true')
    await expect(page.getByTestId('package-card-plan.s')).toHaveAttribute('data-recommended', 'false')
    await expect(page.locator('.ld-hat-pill')).toHaveCount(1)
    await expect(page.locator('.ld-hat-pill')).toHaveText(/Recommended/i)
    await expect(page.getByTestId('package-card-plan.m')).toHaveAttribute('data-selected', 'true')
    const cart = await readCart(page)
    expect(cart.plan).toBe('m')
    expect(cart.planName).toBe('M')
    expect(cart.packageSku).toBe('plan.m')

    // ONE grouped comparison: the group headers in the document's order, a
    // declared group with no feature (Scope, Service level) not shown.
    await expect(page.locator('.ld-group')).toHaveText(['Capacity', 'Features', 'Access', 'Managed operations', 'Resilience'])

    // Cell kinds.
    await expect(page.getByTestId('package-cell-dr_topology-plan.xl')).toHaveText('active-passive')
    await expect(page.getByTestId('package-cell-dr_topology-plan.s')).toHaveText('single region')
    const teaser = page.getByTestId('package-cell-compliance-plan.m')
    await expect(teaser).toHaveAttribute('data-state', 'teaser')
    await expect(teaser).toHaveText('from L')
    await expect(page.getByTestId('package-cell-vuln_dashboard-plan.s')).toHaveText('from M')
    const bw = page.getByTestId('package-cell-bandwidth-plan.l')
    await expect(bw).toContainText('250 Mbps')
    await expect(bw.locator('.ld-hint')).toHaveText('then metered')
    await expect(page.getByTestId('package-cell-bandwidth-plan.s').locator('.ld-hint')).toHaveText('hard cap')
    const backupS = page.getByTestId('package-cell-backup-plan.s')
    await expect(backupS).toHaveAttribute('data-state', 'optional')
    await expect(backupS.locator('.ld-addon-tag')).toHaveText('ADD-ON')
    await expect(backupS.locator('.ld-addon-price')).toHaveText('+ 1.500 / mo')
    await expect(backupS.locator('.ld-hint')).toHaveText('Included from XL')
    await expect(page.getByTestId('package-cell-backup-plan.xl')).toHaveText('✓')
    const giteaM = page.getByTestId('package-cell-gitea_iac-plan.m')
    await expect(giteaM).toContainText('✓')
    await expect(giteaM.locator('.ld-hint')).toHaveText('read')
    await expect(page.getByTestId('package-cell-gitea_iac-plan.s')).toHaveText('—')
    await expect(page.getByTestId('package-cell-kube_api-plan.l')).toHaveAttribute('data-state', 'not_offered')

    // The floor, once, with every item.
    const floor = page.getByTestId('package-floor')
    await expect(floor).toHaveCount(1)
    await expect(floor).toContainText('Every package includes:')
    await expect(floor.locator('.ld-floor-item')).toHaveCount(9)
    await expect(floor.locator('.ld-floor-item').first()).toHaveText('Applications')
    await expect(floor).toContainText('Web application firewall')
    await expect(floor).toContainText('24/7 customer support')
    // A floor item is not also a comparison row.
    await expect(page.locator('[data-testid^="package-row-waf"]')).toHaveCount(0)
    // The live document's internal pricing remark on the Dedicated IP cells
    // (an optional cell's `note`) is not customer copy and is not on the page.
    await expect(page.getByTestId('package-ladder')).not.toContainText(/discount to be decided|250 OMR/)
    await expect(page.getByTestId('package-cell-dedicated_ip-plan.m')).toHaveText(/^\s*ADD-ON\s*\+ 20\.833 \/ mo\s*$/)

    // The foot row repeats the Choose per column; the rest of the footer.
    await expect(page.getByTestId('package-ladder-foot').getByRole('button')).toHaveCount(4)
    await expect(page.getByTestId('package-choose-foot-plan.m')).toHaveText('Continue with M →')
    await expect(page.locator('.ld-meta')).toContainText('Prices as of 2026-10-10 — OpenOva plans')
    await expect(page.locator('.ld-meta')).toContainText('Optional add-ons are picked on the Add-ons step.')

    // Nothing to tick on step 1; no table, no deck underneath.
    await expect(page.getByTestId('package-ladder').getByRole('checkbox')).toHaveCount(0)
    await expect(page.getByTestId('package-table')).toHaveCount(0)
    await expect(page.locator('.pcard')).toHaveCount(0)
  })

  // The ladder's stylesheet is a PAGE import (src/styles/package-ladder.css ←
  // plans.astro) because the component is never part of the server render,
  // and a component-scoped stylesheet on an unreachable branch is dropped from
  // the production bundle (the 2026-10-10 /plans regression). This asserts
  // the COMPUTED layout against `astro build` + `astro preview`.
  test('the ladder is styled in the production build: a five-column grid, cards as flex columns side by side, the floor a flex strip — and no fixed bar over any cell', async ({ page }, testInfo) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans')
    await expect(page.getByTestId('package-ladder')).toBeVisible({ timeout: 10_000 })

    const grid = page.getByTestId('package-ladder-grid')
    await expect(grid).toHaveCSS('display', 'grid')
    const tracks = await grid.evaluate((el) => getComputedStyle(el).gridTemplateColumns.trim().split(/\s+/))
    expect(tracks, `gutter + four package columns: ${tracks.join(' ')}`).toHaveLength(5)

    const cards = page.locator('.ld-card')
    await expect(cards).toHaveCount(4)
    const first = cards.first()
    await expect(first).toHaveCSS('display', 'flex')
    await expect(first).toHaveCSS('flex-direction', 'column')
    await expect(first).toHaveCSS('border-top-left-radius', '12px')
    await expect(first).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    await expect(page.locator('.ld-cta.primary').first()).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
    await expect(page.locator('.ld-hat-pill')).toHaveCSS('display', 'block')
    await expect(page.getByTestId('package-floor')).toHaveCSS('display', 'flex')
    await expect(page.locator('.ld-foot').first()).toHaveCSS('border-bottom-left-radius', '12px')
    // A row wrapper contributes its cells straight to the grid.
    await expect(page.getByTestId('package-row-bandwidth')).toHaveCSS('display', 'contents')

    // The four cards sit side by side on one row: equal tops, increasing lefts.
    const boxes = await cards.evaluateAll((els) =>
      els.map((el) => {
        const r = el.getBoundingClientRect()
        return { top: Math.round(r.top), left: Math.round(r.left), width: Math.round(r.width) }
      }),
    )
    expect(new Set(boxes.map((b) => b.top)).size, `cards share one top: ${JSON.stringify(boxes)}`).toBe(1)
    for (let i = 1; i < boxes.length; i++) {
      expect(boxes[i].left, `card ${i} is to the right of card ${i - 1}`).toBeGreaterThan(boxes[i - 1].left + boxes[i - 1].width - 1)
    }
    expect(Math.min(...boxes.map((b) => b.width))).toBeGreaterThan(150)

    // No fixed element anywhere in the lower half of the viewport, at the top
    // of the page and at its foot: the per-column Choose is the only CTA, so
    // no cell is ever covered.
    const fixedLow = async () =>
      page.evaluate(() => {
        const vh = window.innerHeight
        return Array.from(document.querySelectorAll('body *'))
          .filter((el) => getComputedStyle(el).position === 'fixed')
          .map((el) => el.getBoundingClientRect())
          .filter((r) => r.width > 0 && r.height > 0 && r.bottom > vh / 2)
          .map((r) => ({ top: r.top, left: r.left, right: r.right, bottom: r.bottom }))
      })
    expect(await fixedLow()).toEqual([])
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    expect(await fixedLow()).toEqual([])
    const cellBoxes = await page.locator('.ld-cell').evaluateAll((els) => els.map((el) => el.getBoundingClientRect().toJSON()))
    const fixedAll = await page.evaluate(() =>
      Array.from(document.querySelectorAll('body *'))
        .filter((el) => getComputedStyle(el).position === 'fixed')
        .map((el) => el.getBoundingClientRect().toJSON()),
    )
    for (const c of cellBoxes) {
      for (const f of fixedAll) {
        expect(intersects(c, f), `a cell is under a fixed element: ${JSON.stringify(c)} vs ${JSON.stringify(f)}`).toBe(false)
      }
    }

    await shootTall(page, testInfo.outputPath('ladder-plans.png'))
  })

  test('choosing M (from the foot row) continues to Stack with the catalog plan id and the sku stamped', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 's', planName: 'S', packageSku: 'plan.s', addons: ['addon.domain'] })
    await page.goto('/plans')
    await expect(page.getByTestId('package-ladder')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-card-plan.s')).toHaveAttribute('data-selected', 'true')
    await expect(page.getByTestId('package-choose-plan.m')).toHaveText('Choose M')

    await page.getByTestId('package-choose-foot-plan.m').click()
    await page.waitForURL(/\/apps/, { timeout: 10_000 })
    const cart = await readCart(page)
    expect(cart.plan).toBe('m')
    expect(cart.planName).toBe('M')
    expect(cart.packageSku).toBe('plan.m')
    // Domain is still optional on M, so the pick survives; the apps do too.
    expect(cart.addons).toEqual(['addon.domain'])
    expect(cart.apps).toEqual(['1'])
  })

  test('?recommended=plan.l moves the hat off the flagged package', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await page.goto('/plans?recommended=plan.l')
    await expect(page.getByTestId('package-ladder')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-card-plan.l')).toHaveAttribute('data-recommended', 'true')
    await expect(page.getByTestId('package-card-plan.m')).toHaveAttribute('data-recommended', 'false')
  })

  test('a v1 document (no groups) still renders the flat table, never the ladder', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page, FIXTURE_V1)
    await page.goto('/plans')
    await expect(page.getByTestId('package-table')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('package-ladder')).toHaveCount(0)
    await expect(page.locator('.pcard')).toHaveCount(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
// Step 3 — /addons
// ────────────────────────────────────────────────────────────────────────

test.describe('step 3: the three blocks and the step-up hint (/addons, v2 document, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('for L: "In your package", the five BSS add-ons with prices and hints and nothing from the catalog, "Not on L" with the XL rung, the running total; the step bar covers nothing', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'l', planName: 'L', packageSku: 'plan.l' })
    await page.goto('/addons')

    // Block A — read-only, compact, with the value where there is one.
    const included = page.getByTestId('addons-included')
    await expect(included).toBeVisible({ timeout: 10_000 })
    await expect(included.getByRole('heading', { name: /In your package/i })).toBeVisible()
    await expect(included).toContainText('L · INCLUDED')
    await expect(included.getByTestId('addons-included-bandwidth')).toContainText('250 Mbps · then metered')
    await expect(included.getByTestId('addons-included-dr_topology')).toContainText('single region')
    await expect(included.getByTestId('addons-included-gitea_iac')).toContainText('Gitea + IaC')
    await expect(included.getByTestId('addons-included-compliance')).toBeVisible()
    await expect(included.getByRole('button')).toHaveCount(0)
    await expect(included.getByTestId('addons-included-backup')).toHaveCount(0)

    // Block B — the optional cells as choices, with price and the hint; the
    // five BSS add-ons and nothing else: no catalog entry, no catalog price.
    const optional = page.getByTestId('addons-optional')
    await expect(optional.getByRole('heading', { name: /^Add-ons$/ })).toBeVisible()
    await expect(optional.locator('[data-testid^="addon-tile-"]')).toHaveCount(5)
    await expect(optional.locator('[data-testid^="addon-tile-"]:not([data-testid^="addon-tile-addon."])')).toHaveCount(0)
    const backup = page.getByTestId('addon-tile-addon.backup')
    await expect(backup).toContainText('Backup')
    await expect(backup).toContainText('+OMR 1.500')
    await expect(backup).toContainText('Included from XL')
    await expect(page.getByTestId('addon-tile-addon.ai_seo')).toContainText('+OMR 2.000')
    await expect(page.getByTestId('addon-tile-addon.ai_builder')).toContainText('+OMR 2.000')
    await expect(page.getByTestId('addon-tile-addon.domain')).toContainText('+OMR 0.500')
    const ip = page.getByTestId('addon-tile-addon.dedicated_ip')
    await expect(ip).toContainText('+OMR 20.833')
    await expect(ip).not.toContainText('Included from')
    await expect(page.getByTestId('addon-tile-ips')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-daily-backup')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-waf')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-custom-domain')).toHaveCount(0)

    // Block C — what L does not have, and the rung that does.
    const missing = page.getByTestId('addons-missing')
    await expect(missing).toBeVisible()
    await expect(missing.getByRole('heading', { name: /Not on L/ })).toBeVisible()
    await expect(missing.locator('[data-testid^="addons-missing-"]')).toHaveCount(2)
    await expect(missing.getByTestId('addons-missing-kube_api')).toContainText('Kube API / shell (Guacamole) + PAM')
    await expect(missing.getByTestId('addons-upgrade-kube_api')).toContainText('Upgrade to XL to get this')
    await expect(missing.getByTestId('addons-upgrade-maintenance')).toHaveAttribute('data-target', 'plan.xl')
    await expect(missing.getByTestId('addons-missing-compliance')).toHaveCount(0)
    // The live document's internal pricing remark on the Dedicated IP cells
    // (an optional cell's `note`) is not customer copy and is not on the page.
    await expect(page.locator('.addons-page')).not.toContainText(/discount to be decided|250 OMR/)

    // The running total: the package alone, nothing ticked; no step-up yet.
    const total = page.getByTestId('addons-running-total')
    await expect(total).toHaveAttribute('data-baisa', '7990')
    await expect(total).toContainText('OMR 7.990')
    await expect(page.getByTestId('addons-stepup')).toHaveCount(0)

    // The step bar has its own space: fixed at the foot, the page padded by
    // its height; the document's scroll-padding keeps what is scrolled into
    // view above it, and at the page's end nothing sits under it.
    const bar = page.getByTestId('step-bar')
    await expect(bar).toHaveCSS('position', 'fixed')
    const barBox = await boxOf(bar)
    expect(Math.round(barBox.bottom), 'the bar sits on the viewport floor').toBe(page.viewportSize()!.height)
    const padding = await page.locator('.addons-page').evaluate((el) => parseFloat(getComputedStyle(el).paddingBottom))
    expect(padding).toBeGreaterThanOrEqual(barBox.bottom - barBox.top)
    const scrollPad = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).scrollPaddingBottom))
    expect(scrollPad).toBeGreaterThanOrEqual(barBox.bottom - barBox.top)
    await total.scrollIntoViewIfNeeded()
    await expectNothingUnderBar(page, total)
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    await expectNothingUnderBar(page, page.locator('.extra-tile, .in-pkg-item, [data-testid="addons-running-total"], .step-up'))
  })

  test('on L, ticking Backup + AI SEO + AI builder + Domain (6.000 ≥ the 6.000 gap) shows the step-up card for XL; switching clears those four and keeps the rest', async ({ page }, testInfo) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    // A stale catalog id from before the document existed: Daily Backup is
    // the twin of the Backup feature, so it is carried over to the BSS
    // add-on on load — the customer's intent kept, the catalog price gone.
    await seedCart(page, { plan: 'l', planName: 'L', packageSku: 'plan.l', addons: ['daily-backup'] })
    await page.goto('/addons')
    await expect(page.getByTestId('addons-included')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('addon-tile-addon.backup')).toHaveClass(/checked/)
    expect((await readCart(page)).addons).toEqual(['addon.backup'])
    const total = page.getByTestId('addons-running-total')
    await expect(total).toHaveAttribute('data-baisa', '9490') // 7.990 + Backup 1.500

    // Two more, one short of the gap: 1.500 + 2.000 + 2.000 = 5.500 — no card.
    await page.getByTestId('addon-tile-addon.ai_seo').click()
    await page.getByTestId('addon-tile-addon.ai_builder').click()
    await expect(page.getByTestId('addon-tile-addon.ai_builder')).toHaveClass(/checked/)
    await expect(total).toHaveAttribute('data-baisa', '13490')
    await expect(page.getByTestId('addons-stepup')).toHaveCount(0)

    // The fourth closes the gap: 6.000 ≥ 6.000.
    await page.getByTestId('addon-tile-addon.domain').click()
    await expect(total).toHaveAttribute('data-baisa', '13990')
    const card = page.getByTestId('addons-stepup')
    await expect(card).toBeVisible()
    await expect(card).toHaveAttribute('data-next', 'plan.xl')
    await expect(card).toContainText('XL includes all of this for 6.000 OMR more')
    await expect(card).toContainText('6.000 OMR / mo of add-ons')
    await expect(card.locator('.step-up-list')).toHaveText('Backup · AI SEO ready · AI website builder · Domain')
    await expect(card.getByTestId('addons-stepup-switch')).toContainText('Switch to XL')
    expect((await readCart(page)).addons).toEqual(['addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain'])
    await card.scrollIntoViewIfNeeded()
    await expectNothingUnderBar(page, card)
    await shootTall(page, testInfo.outputPath('ladder-addons-stepup.png'))

    // Switch: the package is XL, the four bundled add-ons are gone, the apps
    // stay — no reload between the click and the read.
    await card.getByTestId('addons-stepup-switch').click()
    await expect(page.getByTestId('addons-stepup')).toHaveCount(0)
    await expect(page.getByTestId('addons-included')).toContainText('XL · INCLUDED')
    await expect(page.getByTestId('addons-included-backup')).toBeVisible()
    await expect(page.getByTestId('addons-included-kube_api')).toBeVisible()
    await expect(page.getByTestId('addon-tile-addon.backup')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-addon.domain')).toHaveCount(0)
    await expect(page.getByTestId('addon-tile-addon.dedicated_ip')).toBeVisible()
    await expect(page.getByTestId('addons-missing')).toHaveCount(0)
    await expect(total).toHaveAttribute('data-baisa', '13990') // XL alone
    const cart = await readCart(page)
    expect(cart.packageSku).toBe('plan.xl')
    expect(cart.plan).toBe('xl')
    expect(cart.planName).toBe('XL')
    expect(cart.addons).toEqual([])
    expect(cart.apps).toEqual(['1'])
  })

  test('for M: "Not on M" names each rung; "Upgrade to L" switches the package, keeps the apps, and the feature moves into the package', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m', addons: ['addon.backup'] })
    await page.goto('/addons')
    const missing = page.getByTestId('addons-missing')
    await expect(missing).toBeVisible({ timeout: 10_000 })
    await expect(missing.getByRole('heading', { name: /Not on M/ })).toBeVisible()
    await expect(missing.getByTestId('addons-missing-compliance')).toHaveAttribute('data-state', 'teaser')
    await expect(missing.getByTestId('addons-upgrade-compliance')).toContainText('Upgrade to L to get this')
    await expect(missing.getByTestId('addons-upgrade-patching')).toHaveAttribute('data-target', 'plan.l')
    await expect(missing.getByTestId('addons-upgrade-kube_api')).toContainText('Upgrade to XL to get this')
    await expect(page.getByTestId('addons-included-gitea_iac')).toContainText('read')

    await missing.getByTestId('addons-upgrade-compliance').click()
    await expect(page.getByTestId('addons-included')).toContainText('L · INCLUDED')
    await expect(page.getByTestId('addons-included-compliance')).toBeVisible()
    await expect(page.getByTestId('addons-missing-compliance')).toHaveCount(0)
    await expect(missing.getByRole('heading', { name: /Not on L/ })).toBeVisible()
    // Backup is still optional on L, so the tick survives the switch.
    await expect(page.getByTestId('addon-tile-addon.backup')).toHaveClass(/checked/)
    const cart = await readCart(page)
    expect(cart.packageSku).toBe('plan.l')
    expect(cart.plan).toBe('l')
    expect(cart.addons).toEqual(['addon.backup'])
    expect(cart.apps).toEqual(['1'])
  })

  test('on M the step-up never fires — its next rung bundles nothing', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'm', planName: 'M', packageSku: 'plan.m' })
    await page.goto('/addons')
    await expect(page.getByTestId('addons-included')).toBeVisible({ timeout: 10_000 })
    for (const id of ['addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain', 'addon.dedicated_ip']) {
      await page.getByTestId(`addon-tile-${id}`).click()
    }
    await expect(page.getByTestId('addon-tile-addon.dedicated_ip')).toHaveClass(/checked/)
    await expect(page.getByTestId('addons-running-total')).toHaveAttribute('data-baisa', String(4490 + 1500 + 2000 + 2000 + 500 + 20833))
    await expect(page.getByTestId('addons-stepup')).toHaveCount(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
// Step 4 — /bcp
// ────────────────────────────────────────────────────────────────────────

test.describe('step 4: the topology the package allows (/bcp, v2 document, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
  })

  test('on L hot-standby is locked — "Included from XL" with a switch that keeps the apps; a stale hot-standby pick is reset; single-region is free', async ({ page }, testInfo) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, {
      plan: 'l', planName: 'L', packageSku: 'plan.l', addons: ['addon.backup'],
      // Picked on a larger package earlier, then switched down to L.
      appConfigs: { postgres: { active_hot_standby: true, primary_region: 'me-east-215-a', replica_region: 'me-east-215-b' } },
    })
    await page.goto('/bcp')

    const hot = page.getByTestId('topology-card-hot')
    await expect(hot).toHaveAttribute('data-locked', 'true', { timeout: 10_000 })
    await expect(hot).toContainText('Included from XL')
    await expect(hot).not.toContainText('5.000')
    await expect(hot.getByRole('radio')).toHaveCount(0)
    await expect(page.getByTestId('topology-card-single')).toContainText('FREE')
    await expect(page.getByTestId('topology-card-single').getByRole('radio')).toBeChecked()
    // The pick this package cannot honour is gone from the cart.
    expect((await readCart(page)).appConfigs.postgres.active_hot_standby).toBe(false)

    // The step bar sits at the foot and covers neither card.
    const bar = page.getByTestId('step-bar')
    await expect(bar).toHaveCSS('position', 'fixed')
    await expectNothingUnderBar(page, page.locator('.topology-card'))
    await shootTall(page, testInfo.outputPath('ladder-topology-locked.png'))

    // Switch to XL: the card becomes selectable and reads INCLUDED; the apps
    // stay; the Backup add-on XL includes is dropped.
    await hot.getByTestId('topology-switch-plan.xl').click()
    await expect(page.getByTestId('topology-card-hot')).toHaveAttribute('data-locked', 'false')
    await expect(page.getByTestId('topology-card-hot')).toContainText('XL · INCLUDED')
    await expect(page.getByTestId('topology-card-hot')).not.toContainText('5.000')
    let cart = await readCart(page)
    expect(cart.packageSku).toBe('plan.xl')
    expect(cart.plan).toBe('xl')
    expect(cart.apps).toEqual(['1'])
    expect(cart.addons).toEqual([])

    // Now it can be chosen, and the region pickers appear.
    await page.getByTestId('topology-card-hot').getByRole('radio').check()
    await expect(page.locator('#primary-region')).toBeVisible()
    cart = await readCart(page)
    expect(cart.appConfigs.postgres.active_hot_standby).toBe(true)
    await shootTall(page, testInfo.outputPath('ladder-topology-xl.png'))
  })

  test('on XL hot-standby is selectable from the start and reads INCLUDED', async ({ page }) => {
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'xl', planName: 'XL', packageSku: 'plan.xl' })
    await page.goto('/bcp')
    const hot = page.getByTestId('topology-card-hot')
    await expect(hot).toContainText('XL · INCLUDED', { timeout: 10_000 })
    await expect(hot).toHaveAttribute('data-locked', 'false')
    await hot.getByRole('radio').check()
    expect((await readCart(page)).appConfigs.postgres.active_hot_standby).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
// Review
// ────────────────────────────────────────────────────────────────────────

test.describe('review carries the package, its add-ons and the floor (v2 document, #6971)', () => {
  test.beforeEach(async ({ page }) => {
    await mockCatalog(page)
    await page.route('**/api/auth/me', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ user: { id: 'user-1', email: 'demo@example.com', name: 'Demo User' } }) }),
    )
    await page.route('**/api/billing/balance', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ credit_baisa: 0, entries: [] }) }),
    )
    await page.route('**/api/tenant/orgs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
  })

  test('/review lists the package line and the add-on lines from the quote, sizes the cards, the ring and the hint from the document shape, labels the buckets without user counts, and carries the floor footnote', async ({ page }, testInfo) => {
    const quotes: any[] = []
    await mockQuote(page, (b) => quotes.push(b))
    await pointAtChargeback(page)
    await mockPackages(page)
    await seedCart(page, { plan: 'l', planName: 'L', packageSku: 'plan.l', addons: ['addon.backup', 'addon.domain'] })
    await page.goto('/review')

    const side = page.locator('.side-card')
    await expect(side).toBeVisible({ timeout: 10_000 })
    await expect(side.getByTestId('review-total-plan')).toContainText('L plan')
    await expect(side.getByTestId('review-total-plan')).toContainText('OMR 7.990')
    await expect(side.getByTestId('review-total-package-addon-addon.backup')).toContainText('+OMR 1.500')
    await expect(side.getByTestId('review-total-package-addon-addon.domain')).toContainText('+OMR 0.500')
    await expect(side.getByTestId('review-total')).toHaveText('OMR 9.990')
    const floor = side.getByTestId('review-floor')
    await expect(floor).toContainText('Every package includes:')
    await expect(floor).toContainText('Applications · Databases · Mail server (unlimited accounts)')
    await expect(floor).toContainText('24/7 customer support')
    expect(quotes[0]).toMatchObject({ plan_id: 'l', package_sku: 'plan.l', addons: ['addon.backup', 'addon.domain'], topology: 'single-region' })

    // The plan cards: price and specs from the document's shape.
    await expect(page.getByTestId('review-plan-specs-l')).toHaveText('4 vCPU · 8 GB · 100 GB')
    await expect(page.getByTestId('review-plan-specs-xl')).toHaveText('8 vCPU · 16 GB · 250 GB')
    await expect(page.locator('.plan-option', { hasText: 'L' }).first().locator('.plan-opt-price strong')).toHaveText('7.990')

    // The headroom ring measures against the SAME shape (L = 8 GB · 4 vCPU ·
    // 100 GB), not the old catalog figures (16384 MiB / 8000 m).
    await expect(page.getByTestId('review-cap-ram')).toContainText('/ 8192 MiB')
    await expect(page.getByTestId('review-cap-cpu')).toContainText('/ 4000 m')
    await expect(page.getByTestId('review-cap-disk')).toContainText('/ 100 GiB')
    // The hint names the smallest package that fits, by that shape.
    await expect(page.locator('.rv-note', { hasText: /fits your 1 app/ })).toHaveText('S fits your 1 app')
    // The usage buckets are labelled only.
    await expect(page.locator('.conc-btn')).toHaveText(['Low', 'Medium', 'High'])
    await expect(page.locator('.conc-row')).not.toContainText('users')

    // Optional extras: the BSS add-ons only, the two in the cart ticked.
    const extras = page.locator('.addon-tile')
    await expect(extras).toHaveCount(5)
    await expect(page.locator('.addon-tile', { hasText: 'Intrusion Prevention' })).toHaveCount(0)
    await expect(page.locator('.addon-tile.checked')).toHaveCount(2)

    await shootTall(page, testInfo.outputPath('ladder-review.png'))
  })
})
