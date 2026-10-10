// The public calculator's package ladder — a LAYOUT proof in Chromium
// (#6971, bp-chargeback 0.1.62). Seen live on hw307 at 0.1.61: at a 1400 px
// viewport the "Platform plans" family card showed S, M and L and clipped
// the fourth column (XL) off its right edge — the auto table layout let the
// header's no-wrap lines set every column's width. The fix is a fixed
// layout (products/chargeback/ui/src/styles.css, .pkg-table.compare) with
// a scroll container under 1100 px. Nothing here reads a test id for its
// verdict: every assertion is a bounding box or a scroll metric of the
// rendered page.
//
// What is real: the built UI (ui/dist, embedded in the binary that serves
// BASE) rendered by Chromium at three viewports. What is supplied: the two
// public documents the page reads, GET /public/catalog and
// GET /public/packages, fulfilled from the UI's own fixture
// (products/chargeback/ui/src/panels/estimate/fixture.ts — the shape
// internal/api/public_calculator.go publishes) plus the two longest
// feature names the seeder writes (internal/synth/packages.go), so the
// feature column's wrapping is exercised. The e2e seed (tests/e2e/chargeback/
// seed.sh) writes no ladder, and a Sovereign without one shows no table.
//
// Who runs it: .github/workflows/chargeback-e2e.yaml, after the console
// walk, against the same binary. Locally, any server of ui/dist with the
// SPA fallback will do:
//   (cd products/chargeback/ui && npm run build && npx vite preview --port 4173 &)
//   CHARGEBACK_BASE_URL=http://127.0.0.1:4173 npx playwright test tests/chargeback-calculator-ladder.spec.ts
// LADDER_SHOT=<path>.png saves the family card at 1400 px, the screenshot
// committed under docs/ledger/screenshots/.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { reachable } from './_helpers'
import { catalog, morePlans, packages } from '../../../../products/chargeback/ui/src/panels/estimate/fixture'

const BASE = process.env.CHARGEBACK_BASE_URL || 'http://127.0.0.1:18080'
const SHOT = process.env.LADDER_SHOT || ''

const fullCatalog = { ...catalog, plans: [...catalog.plans, ...morePlans] }
// The fixture's ladder plus the two longest names the seeder writes — one
// included everywhere, one an add-on on S/M/L — so a long feature name has
// to wrap inside the 28 % feature column.
const ladder = {
  ...packages,
  features: [
    ...packages.features,
    {
      key: 'kube_api_shell',
      name: 'Kube API / shell (Guacamole) + PAM',
      blurb: 'Kubernetes API and a browser shell behind privileged access management',
      group: 'access',
      kind: 'access',
      teaser: false,
      cells: { 'plan.s': { state: 'not_offered' }, 'plan.m': { state: 'included' }, 'plan.l': { state: 'included' }, 'plan.xl': { state: 'included' } },
    },
    {
      key: 'proactive_maintenance',
      name: 'Proactive maintenance · auto-remediation',
      blurb: 'Issues found and fixed before they page anyone',
      group: 'ops',
      kind: 'boolean',
      addon_sku: 'addon.ai_seo',
      teaser: false,
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '12.000', included_from: 'plan.xl' },
        'plan.m': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '12.000', included_from: 'plan.xl' },
        'plan.l': { state: 'optional', addon_sku: 'addon.ai_seo', price_month: '12.000', included_from: 'plan.xl' },
        'plan.xl': { state: 'included' },
      },
    },
  ],
}

type Box = { x: number; y: number; width: number; height: number }

async function box(l: Locator): Promise<Box> {
  const b = await l.boundingBox()
  if (!b) throw new Error(`no box for ${l}`)
  return b
}

/** True when `inner` lies horizontally inside `outer` (half a pixel of rounding allowed). */
function insideX(inner: Box, outer: Box): boolean {
  return inner.x >= outer.x - 0.5 && inner.x + inner.width <= outer.x + outer.width + 0.5
}

async function openCalculator(page: Page, width: number) {
  await page.setViewportSize({ width, height: 1000 })
  await page.route('**/api/v1/public/catalog', (route) => route.fulfill({ json: fullCatalog }))
  await page.route('**/api/v1/public/packages', (route) => route.fulfill({ json: ladder }))
  // No item is priced in this proof; the preview answers nothing.
  await page.route('**/api/v1/public/estimates*', (route) => route.fulfill({ status: 404, json: { error: 'no preview in the layout proof' } }))
  await page.goto(`${BASE}/estimate`)
  await expect(page.getByRole('heading', { name: 'Cost calculator', level: 1 })).toBeVisible()
  const card = page.getByTestId('family-plans')
  await expect(card.locator('table.pkg-table.compare')).toBeVisible()
  // Four package headers, in the document's order.
  await expect(card.locator('th.pkg-head')).toHaveCount(4)
  await expect(card.locator('th.pkg-head button')).toHaveText(['Choose', 'Choose', 'Choose', 'Choose'])
  // The plans family sits under Compute, Storage and Networking: bring its
  // top to the viewport so the viewport checks below are about WIDTH.
  await card.scrollIntoViewIfNeeded()
  return card
}

function parts(card: Locator) {
  return {
    scroll: card.getByTestId('package-table-scroll'),
    table: card.locator('table.pkg-table.compare'),
    xl: card.locator('th.pkg-head', { has: card.page().locator('button[aria-label="Choose XL"]') }),
    floor: card.getByTestId('floor-strip'),
    configure: card.locator('button[aria-label="Configure Platform plans"]'),
  }
}

async function scrollMetrics(scroll: Locator) {
  return scroll.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, scrollLeft: el.scrollLeft }))
}

test.describe('the calculator package ladder fits the family card (#6971, 0.1.62)', () => {
  test.beforeAll(async () => {
    const ok = await reachable(`${BASE}/estimate`)
    test.skip(!ok, `chargeback UI not reachable at ${BASE}/estimate — build and start it first (see file header)`)
  })

  for (const width of [1400, 1280, 1600]) {
    test(`at ${width} px the XL column is inside the card and nothing scrolls`, async ({ page }) => {
      const card = await openCalculator(page, width)
      const { scroll, table, xl, floor } = parts(card)
      const cardBox = await box(card)
      const tableBox = await box(table)
      const xlBox = await box(xl)
      // The whole table, and the fourth column with it, lies inside the card.
      expect(insideX(tableBox, cardBox), `table ${JSON.stringify(tableBox)} inside card ${JSON.stringify(cardBox)}`).toBe(true)
      expect(insideX(xlBox, cardBox), `XL header ${JSON.stringify(xlBox)} inside card ${JSON.stringify(cardBox)}`).toBe(true)
      expect(xlBox.width).toBeGreaterThan(90)
      // Every package header is a column of its own, left to right, each as
      // wide as the others (the fixed layout's equal share).
      const heads = await card.locator('th.pkg-head').all()
      const headBoxes = await Promise.all(heads.map(box))
      for (let i = 1; i < headBoxes.length; i++) {
        expect(headBoxes[i].x).toBeGreaterThan(headBoxes[i - 1].x + headBoxes[i - 1].width - 1)
        expect(Math.abs(headBoxes[i].width - headBoxes[0].width)).toBeLessThan(2)
      }
      // Nothing to scroll: the table is no wider than its container.
      const m = await scrollMetrics(scroll)
      expect(m.scrollWidth, JSON.stringify(m)).toBeLessThanOrEqual(m.clientWidth)
      // All four Choose buttons, and the XL add-on tick once its row is
      // scrolled to, are fully on screen — the page never hides a column.
      for (const name of ['S', 'M', 'L', 'XL']) {
        const b = card.locator(`button[aria-label="Choose ${name}"]`)
        await b.scrollIntoViewIfNeeded()
        await expect(b).toBeInViewport({ ratio: 1 })
      }
      const tick = card.locator('[aria-label="Dedicated IP address on XL"]')
      await tick.scrollIntoViewIfNeeded()
      await expect(tick).toBeInViewport({ ratio: 1 })
      // The long feature name wrapped instead of widening its column.
      const longName = card.locator('[data-testid=compare-kube_api_shell] td.pkg-feature')
      const longBox = await box(longName)
      expect(longBox.width).toBeLessThanOrEqual(cardBox.width * 0.3)
      expect(longBox.height).toBeGreaterThan(24)
      // The floor strip spans the card like the table does.
      const floorBox = await box(floor)
      expect(Math.abs(floorBox.width - tableBox.width)).toBeLessThan(2)
      if (width === 1400 && SHOT) await card.screenshot({ path: SHOT })
    })
  }

  test('at 900 px the table scrolls inside the card and XL is reached by scrolling, not clipped', async ({ page }) => {
    const card = await openCalculator(page, 900)
    const { scroll, table, xl, floor, configure } = parts(card)
    const cardBox = await box(card)
    const scrollBox = await box(scroll)
    const tableBox = await box(table)
    // The scroll container is the card's width; the table is wider than it.
    expect(insideX(scrollBox, cardBox)).toBe(true)
    const before = await scrollMetrics(scroll)
    expect(before.scrollWidth, JSON.stringify(before)).toBeGreaterThan(before.clientWidth)
    expect(tableBox.width).toBeGreaterThan(scrollBox.width)
    // XL starts past the container's right edge — off screen, not cut off.
    const xlBefore = await box(xl)
    expect(xlBefore.x + xlBefore.width).toBeGreaterThan(scrollBox.x + scrollBox.width + 1)
    // Scrolling the container brings it fully into view.
    await xl.scrollIntoViewIfNeeded()
    const after = await scrollMetrics(scroll)
    expect(after.scrollLeft).toBeGreaterThan(0)
    const xlAfter = await box(xl)
    expect(insideX(xlAfter, scrollBox), `XL ${JSON.stringify(xlAfter)} inside scroll ${JSON.stringify(scrollBox)}`).toBe(true)
    await expect(card.locator('button[aria-label="Choose XL"]')).toBeInViewport({ ratio: 1 })
    // The floor strip and the footer stay outside the scroll, at the card's width.
    const floorBox = await box(floor)
    expect(Math.abs(floorBox.width - scrollBox.width)).toBeLessThan(2)
    expect(await scroll.evaluate((el, sel) => el.querySelector(sel) !== null, '[data-testid=floor-strip]')).toBe(false)
    await configure.scrollIntoViewIfNeeded()
    await expect(configure).toBeInViewport({ ratio: 1 })
    // The page itself never scrolls sideways.
    const page_ = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }))
    expect(page_.sw).toBeLessThanOrEqual(page_.cw)
    // Choosing XL from there still adds the plan line.
    await card.locator('button[aria-label="Choose XL"]').click()
    await expect(page.getByTestId('estimate-summary').getByTestId('group-plan')).toContainText('XL plan')
  })
})
