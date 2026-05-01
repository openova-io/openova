/**
 * post-v2-polish-366.spec.ts — Playwright E2E for the four post-v2
 * UX polish items from issue openova-io/openova#366.
 *
 *   1. List view: chip strip in toolbar, active table above the fold.
 *   2. Header centre slot: page title in PortalShell header centre on
 *      every Sovereign-portal page (Apps / Jobs / Dashboard / Cloud).
 *   3. ArchiMate edges: edges render their relation-typed stroke +
 *      dashing + marker, and the legend is a Popover (closed by
 *      default, persisted in localStorage).
 *   4. Fullscreen: :fullscreen CSS fills viewport (100vw × 100vh);
 *      fullscreen button is icon-only (no "Fullscreen" text).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL
 * comes from playwright.config.ts (env-driven HOST + BASEPATH); we
 * use a synthetic deploymentId and rely on the SPA's fixture
 * fallback for the in-page data so the test is fully self-contained.
 */

import { test, expect, type Page } from '@playwright/test'

const DEPLOYMENT_ID = 'p366-polish-e2e'

async function gotoProvision(page: Page, suffix = '') {
  const tail = suffix ? `/${suffix}` : ''
  await page.goto(`provision/${DEPLOYMENT_ID}${tail}`)
  await page.waitForLoadState('domcontentloaded')
}

async function clearStorage(page: Page) {
  await page.goto('wizard')
  await page.waitForLoadState('domcontentloaded')
  await page.evaluate(() => {
    try {
      window.localStorage.clear()
    } catch {
      /* noop */
    }
  })
}

/* ── Item 1: list view chip strip + above-the-fold ─────────────── */

test.describe('#366 item 1 — chip strip replaces the 12-tile card grid', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('chip strip is in the toolbar; active list table is above the fold @1440x900', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await gotoProvision(page, 'cloud?view=list&kind=clusters')

    // Chip strip lives inside the toolbar row.
    const toolbar = page.getByTestId('cloud-page-toolbar')
    await expect(toolbar).toBeVisible()
    const chips = page.getByTestId('cloud-kind-chips')
    await expect(chips).toBeVisible()
    expect(
      await toolbar.evaluate((tb, chipsEl) => tb.contains(chipsEl), await chips.elementHandle()),
    ).toBe(true)

    // Legacy 12-tile card grid is gone.
    expect(await page.getByTestId('cloud-list-view-tile-grid').count()).toBe(0)

    // Active list table sits above the fold at 1440×900.
    const active = page.getByTestId('cloud-list-view-active-clusters')
    await expect(active).toBeVisible()
    await expect(active).toBeInViewport()

    await page.screenshot({
      path: 'e2e/screenshots/p366-chip-strip-list.png',
      fullPage: false,
    })
  })

  test('chips are tab-navigable and Enter activates them (a11y)', async ({ page }) => {
    await gotoProvision(page, 'cloud?view=list&kind=clusters')
    const pvcsChip = page.getByTestId('cloud-kind-chip-pvcs')
    await expect(pvcsChip).toBeVisible()
    await pvcsChip.focus()
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => /kind=pvcs/.test(window.location.search), undefined, {
      timeout: 5_000,
    })
    await expect(page.getByTestId('cloud-kind-chip-pvcs')).toHaveAttribute('data-active', 'true')
  })
})

/* ── Item 2: header centre slot title across every portal page ─ */

test.describe('#366 item 2 — page title in PortalShell header centre slot', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  const PORTAL_PAGES: Array<{ suffix: string; title: string }> = [
    { suffix: '', title: 'Applications' },
    { suffix: 'jobs', title: 'Jobs' },
    { suffix: 'dashboard', title: 'Dashboard' },
    { suffix: 'cloud', title: 'Cloud' },
  ]

  for (const p of PORTAL_PAGES) {
    test(`${p.suffix || 'apps'} page renders title "${p.title}" in the header centre slot`, async ({
      page,
    }) => {
      await gotoProvision(page, p.suffix)
      const title = page.getByTestId('portal-header-title')
      await expect(title).toBeVisible()
      await expect(title).toContainText(p.title)
      // Ensure the title sits inside the centre slot (not the right or
      // left ones) — guards against future regressions that move it.
      const center = page.getByTestId('portal-header-center')
      expect(
        await center.evaluate((c, t) => c.contains(t), await title.elementHandle()),
      ).toBe(true)
    })
  }

  test('captures a 1440x900 screenshot of the centred title across the cloud page', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await gotoProvision(page, 'cloud?view=list')
    await page.waitForSelector('[data-testid=portal-header-title]')
    await page.screenshot({
      path: 'e2e/screenshots/p366-centre-title-cloud.png',
      fullPage: false,
    })
  })
})

/* ── Item 3: ArchiMate edges + legend popover ──────────────────── */

test.describe('#366 item 3 — ArchiMate edges and legend popover', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('edges expose the relation-type marker / dash / stroke attributes', async ({ page }) => {
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=arch-graph-svg]')

    // Inspect the edge SVG <line> elements. At least one `contains`
    // edge must carry the composition-marker URL on its source end
    // (filled diamond at parent, per ArchiMate notation).
    const containsCount = await page.evaluate(() => {
      const els = document.querySelectorAll('[data-edge-type=contains]')
      let withMarker = 0
      els.forEach((el) => {
        const ms = el.getAttribute('marker-start') ?? ''
        if (ms.includes('composition')) withMarker += 1
      })
      return { total: els.length, withMarker }
    })
    expect(containsCount.total).toBeGreaterThan(0)
    expect(containsCount.withMarker).toBeGreaterThan(0)

    // At least one `runs-on` edge has assignment-dot markers on both ends.
    const runsOnCount = await page.evaluate(() => {
      const els = document.querySelectorAll('[data-edge-type=runs-on]')
      let bothEnds = 0
      els.forEach((el) => {
        const ms = el.getAttribute('marker-start') ?? ''
        const me = el.getAttribute('marker-end') ?? ''
        if (ms.includes('assignment-dot') && me.includes('assignment-dot')) bothEnds += 1
      })
      return { total: els.length, bothEnds }
    })
    if (runsOnCount.total > 0) {
      expect(runsOnCount.bothEnds).toBeGreaterThan(0)
    }
  })

  test('legend is a popover — closed by default, persists open state in localStorage', async ({
    page,
  }) => {
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=arch-graph-svg]')

    // Trigger button visible; legend body NOT visible by default.
    const trigger = page.getByTestId('cloud-architecture-edge-legend-trigger')
    await expect(trigger).toBeVisible()
    expect(await page.getByTestId('cloud-architecture-edge-legend').count()).toBe(0)

    // Click → opens.
    await trigger.click()
    await expect(page.getByTestId('cloud-architecture-edge-legend')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-edge-legend-contains')).toBeVisible()

    // localStorage flag persists.
    const stored = await page.evaluate(() =>
      window.localStorage.getItem('sov-arch-legend-open'),
    )
    expect(stored).toBe('true')

    await page.screenshot({
      path: 'e2e/screenshots/p366-archimate-legend-popover.png',
      fullPage: false,
    })

    // Close: localStorage flips back.
    await page.getByTestId('cloud-architecture-edge-legend-close').click()
    expect(await page.getByTestId('cloud-architecture-edge-legend').count()).toBe(0)
    const closed = await page.evaluate(() =>
      window.localStorage.getItem('sov-arch-legend-open'),
    )
    expect(closed).toBe('false')
  })

  test('captures a zoomed screenshot showing ArchiMate edge styles', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=arch-graph-svg]')
    // Settle the simulation a beat.
    await page.waitForTimeout(800)
    const svg = page.getByTestId('arch-graph-svg')
    await svg.screenshot({
      path: 'e2e/screenshots/p366-archimate-edges-zoomed.png',
    })
  })
})

/* ── Item 4: fullscreen 100% height + icon-only button ─────────── */

test.describe('#366 item 4 — fullscreen 100% height + icon-only button', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('fullscreen toggle is icon-only — no "Fullscreen" text label', async ({ page }) => {
    await gotoProvision(page, 'cloud?view=graph')
    const toggle = page.getByTestId('cloud-page-fullscreen-toggle')
    await expect(toggle).toBeVisible()
    // Aria-label preserved for accessibility.
    await expect(toggle).toHaveAttribute('aria-label', /fullscreen/i)
    // No visible text content (svg only).
    const text = (await toggle.textContent()) ?? ''
    expect(text.trim()).toBe('')
  })

  test('clicking fullscreen flips data-fullscreen and the canvas fills the viewport', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=cloud-page-fullscreen-toggle]')

    const toggle = page.getByTestId('cloud-page-fullscreen-toggle')
    await toggle.click()

    // The CloudPage's content container flips to fullscreen state.
    await expect(page.getByTestId('cloud-content')).toHaveAttribute(
      'data-fullscreen',
      'true',
      { timeout: 3_000 },
    )

    // The fullscreen container fills (or close to) the viewport. Headless
    // chromium's synthetic-fullscreen path applies the .cloud-page-content-
    // fullscreen class which has 100vh / 100vw rules. Allow a small
    // tolerance for borders / padding on the wrapper.
    const box = await page.getByTestId('cloud-content').boundingBox()
    expect(box).not.toBeNull()
    if (box) {
      expect(box.height).toBeGreaterThan(700)
    }

    await page.screenshot({
      path: 'e2e/screenshots/p366-fullscreen-100pct.png',
      fullPage: false,
    })
  })
})
