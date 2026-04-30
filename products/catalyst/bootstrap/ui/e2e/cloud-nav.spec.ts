/**
 * cloud-nav.spec.ts — Sovereign-portal Cloud accordion + redirects
 * E2E lock-in (P1 of issue #309).
 *
 * What this asserts:
 *   • Sidebar shows the Cloud accordion (no flat "Infrastructure"
 *     entry remains).
 *   • Cloud accordion toggles open/closed; expanded → 4 sub-items
 *     visible (Architecture / Compute / Network / Storage).
 *   • Each sub-item routes to /sovereign/provision/$id/cloud/{suffix}.
 *   • Legacy /infrastructure/* deep links redirect to /cloud/*.
 *   • Expanded state survives page reload (persisted via the
 *     `sov-nav-cloud-expanded` localStorage key).
 *   • Visual screenshots saved at 1440x900 to e2e/screenshots/.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL
 * comes from playwright.config.ts (env-driven HOST + BASEPATH); we
 * use a synthetic deploymentId and rely on the SPA's fixture
 * fallback for the in-page data — that keeps the test fully
 * self-contained.
 */

import { test, expect, type Page } from '@playwright/test'

// Deliberately avoid the strings "cloud" or "infrastructure" in the
// deploymentId so the sidebar's path-segment matcher can't be fooled
// by a substring match.
const DEPLOYMENT_ID = 'p1-309-e2e'

const SUB_ITEMS: ReadonlyArray<{ id: string; suffix: string }> = [
  { id: 'sov-nav-cloud-architecture', suffix: 'architecture' },
  { id: 'sov-nav-cloud-compute', suffix: 'compute' },
  { id: 'sov-nav-cloud-network', suffix: 'network' },
  { id: 'sov-nav-cloud-storage', suffix: 'storage' },
] as const

const LEGACY_REDIRECTS: ReadonlyArray<{ from: string; to: string }> = [
  { from: 'infrastructure', to: '/cloud/architecture' },
  { from: 'infrastructure/topology', to: '/cloud/architecture' },
  { from: 'infrastructure/compute', to: '/cloud/compute' },
  { from: 'infrastructure/network', to: '/cloud/network' },
  { from: 'infrastructure/storage', to: '/cloud/storage' },
] as const

async function gotoProvision(page: Page, suffix = '') {
  // The basepath is folded into Playwright's baseURL via
  // playwright.config.ts; the goto here is path-relative.
  const tail = suffix ? `/${suffix}` : ''
  await page.goto(`provision/${DEPLOYMENT_ID}${tail}`)
  await page.waitForLoadState('domcontentloaded')
}

async function clearCloudExpanded(page: Page) {
  // Wipe persisted accordion state once at the start of each test so
  // we start from the documented default (collapsed unless on
  // /cloud/*). We navigate to a benign URL first so localStorage is
  // available, clear the key, then return — subsequent goto() calls
  // observe a fresh slate.
  //
  // We deliberately DON'T use addInitScript here: that would re-clear
  // the key on every navigation/reload, breaking the cross-reload
  // persistence assertion below.
  await page.goto('wizard')
  await page.waitForLoadState('domcontentloaded')
  await page.evaluate(() => {
    try {
      window.localStorage.removeItem('sov-nav-cloud-expanded')
    } catch {
      /* noop */
    }
  })
}

test.describe('Cloud accordion sidebar (#309)', () => {
  test.beforeEach(async ({ page }) => {
    await clearCloudExpanded(page)
  })

  test('sidebar exposes Cloud (not Infrastructure) accordion', async ({ page }) => {
    await gotoProvision(page)

    const cloudHeader = page.getByTestId('sov-nav-cloud')
    await expect(
      cloudHeader,
      'Sidebar must expose [data-testid=sov-nav-cloud] — the accordion replaces the legacy Infrastructure flat entry (Sidebar.tsx).',
    ).toBeVisible()

    expect(
      await cloudHeader.evaluate((el) => el.tagName),
      'Cloud accordion header must be a <button> (toggles state, not navigates).',
    ).toBe('BUTTON')

    expect(
      await cloudHeader.textContent(),
      'Cloud accordion label must read "Cloud" verbatim — issue #309 founder spec ("we call it as cloud").',
    ).toContain('Cloud')

    // Legacy flat entry must be gone.
    expect(
      await page.getByTestId('sov-nav-infrastructure').count(),
      'Sidebar still renders sov-nav-infrastructure — issue #309 replaced the flat entry with the Cloud accordion.',
    ).toBe(0)
  })

  test('clicking Cloud header toggles expanded state and exposes 4 sub-items', async ({ page }) => {
    await gotoProvision(page)

    const cloudHeader = page.getByTestId('sov-nav-cloud')

    // Initial state — collapsed (we cleared the persisted key).
    expect(
      await cloudHeader.getAttribute('aria-expanded'),
      'Cloud accordion should default to collapsed when not on a /cloud/* route and no persisted state exists.',
    ).toBe('false')

    await cloudHeader.click()

    expect(
      await cloudHeader.getAttribute('aria-expanded'),
      'Cloud accordion did not flip aria-expanded after click.',
    ).toBe('true')

    for (const sub of SUB_ITEMS) {
      const item = page.getByTestId(sub.id)
      await expect(
        item,
        `Sub-item [data-testid=${sub.id}] missing after expanding the Cloud accordion.`,
      ).toBeVisible()
    }

    await cloudHeader.click()
    expect(
      await cloudHeader.getAttribute('aria-expanded'),
      'Cloud accordion did not flip back to collapsed after second click.',
    ).toBe('false')
  })

  test('each sub-item routes to /provision/$id/cloud/{suffix}', async ({ page }) => {
    await gotoProvision(page)

    // Open the accordion first.
    await page.getByTestId('sov-nav-cloud').click()

    for (const sub of SUB_ITEMS) {
      await page.getByTestId(sub.id).click()
      await page.waitForFunction(
        (s) => window.location.pathname.endsWith(`/cloud/${s}`),
        sub.suffix,
        { timeout: 5_000 },
      )
      const pathname = new URL(page.url()).pathname
      expect(
        pathname.endsWith(`/cloud/${sub.suffix}`),
        `Clicking ${sub.id} should navigate to /cloud/${sub.suffix}; got ${pathname}.`,
      ).toBe(true)

      // The clicked sub-item must carry aria-current=page.
      expect(
        await page.getByTestId(sub.id).getAttribute('aria-current'),
        `Active sub-item ${sub.id} must declare aria-current=page.`,
      ).toBe('page')
    }
  })

  test('legacy /infrastructure/* paths redirect to /cloud/*', async ({ page }) => {
    for (const c of LEGACY_REDIRECTS) {
      await gotoProvision(page, c.from)
      await page.waitForFunction(
        (suffix) => window.location.pathname.endsWith(suffix),
        c.to,
        { timeout: 5_000 },
      )
      const pathname = new URL(page.url()).pathname
      expect(
        pathname.endsWith(c.to),
        `Expected provision/${DEPLOYMENT_ID}/${c.from} to redirect to a path ending in ${c.to}; got ${pathname}.`,
      ).toBe(true)
    }
  })

  test('accordion remembers expanded state across reloads', async ({ page }) => {
    await gotoProvision(page)

    const cloudHeader = page.getByTestId('sov-nav-cloud')
    expect(await cloudHeader.getAttribute('aria-expanded')).toBe('false')

    // Open + persist.
    await cloudHeader.click()
    expect(await cloudHeader.getAttribute('aria-expanded')).toBe('true')

    // Reload — state must come back from localStorage.
    await page.reload()
    await page.waitForLoadState('domcontentloaded')

    const reloaded = page.getByTestId('sov-nav-cloud')
    expect(
      await reloaded.getAttribute('aria-expanded'),
      'Cloud accordion forgot its expanded state after a page reload — the sov-nav-cloud-expanded localStorage key must restore it.',
    ).toBe('true')
  })

  test('accordion auto-expands when navigating directly to a /cloud/* deep link', async ({ page }) => {
    await gotoProvision(page, 'cloud/compute')

    const cloudHeader = page.getByTestId('sov-nav-cloud')
    expect(
      await cloudHeader.getAttribute('aria-expanded'),
      'Cloud accordion must auto-expand when the operator lands directly on a /cloud/* route.',
    ).toBe('true')

    // Compute sub-item is the active one.
    expect(
      await page.getByTestId('sov-nav-cloud-compute').getAttribute('aria-current'),
      'Cloud / Compute sub-item must declare aria-current=page on /cloud/compute.',
    ).toBe('page')
  })

  test('captures Cloud accordion screenshots @ 1440x900', async ({ page }) => {
    // 1: Cloud accordion COLLAPSED (the wizard provision root).
    await gotoProvision(page)
    await page.waitForSelector('[data-testid=admin-sidebar]')
    await page.screenshot({
      path: 'e2e/screenshots/p1-cloud-nav-collapsed.png',
      fullPage: false,
    })

    // 2: Expanded, Architecture active.
    await gotoProvision(page, 'cloud/architecture')
    await page.waitForSelector('[data-testid=sov-nav-cloud-architecture]')
    await page.screenshot({
      path: 'e2e/screenshots/p1-cloud-nav-expanded-architecture.png',
      fullPage: false,
    })

    // 3: Expanded, Compute active.
    await gotoProvision(page, 'cloud/compute')
    await page.waitForSelector('[data-testid=sov-nav-cloud-compute]')
    await page.screenshot({
      path: 'e2e/screenshots/p1-cloud-nav-expanded-compute.png',
      fullPage: false,
    })
  })
})
