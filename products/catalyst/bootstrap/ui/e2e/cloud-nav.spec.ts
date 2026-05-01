/**
 * cloud-nav.spec.ts — Sovereign-portal Cloud sidebar entry + legacy
 * URL redirect E2E lock-in.
 *
 * Originally introduced in P1 of #309 to lock in the Cloud accordion;
 * issue #350 replaced the accordion with a single flat link, so the
 * spec is now scoped to:
 *   • Sidebar exposes a single flat Cloud entry (no accordion, no
 *     chevron, no sub-items).
 *   • Clicking Cloud lands on /cloud (which canonicalises to the
 *     graph view).
 *   • Legacy /infrastructure/* deep links redirect to /cloud?view=…
 *     equivalents.
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

const LEGACY_REDIRECTS: ReadonlyArray<{ from: string; expectedSearch: RegExp }> = [
  { from: 'infrastructure', expectedSearch: /\?view=graph/ },
  { from: 'infrastructure/topology', expectedSearch: /\?view=graph/ },
  { from: 'infrastructure/compute', expectedSearch: /\?view=list&kind=clusters/ },
  { from: 'infrastructure/network', expectedSearch: /\?view=list&kind=load-balancers/ },
  { from: 'infrastructure/storage', expectedSearch: /\?view=list&kind=pvcs/ },
] as const

async function gotoProvision(page: Page, suffix = '') {
  // The basepath is folded into Playwright's baseURL via
  // playwright.config.ts; the goto here is path-relative.
  const tail = suffix ? `/${suffix}` : ''
  await page.goto(`provision/${DEPLOYMENT_ID}${tail}`)
  await page.waitForLoadState('domcontentloaded')
}

async function clearLocalStorage(page: Page) {
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

test.describe('Cloud sidebar entry (#309 P1 → #350 IA restructure)', () => {
  test.beforeEach(async ({ page }) => {
    await clearLocalStorage(page)
  })

  test('sidebar exposes Cloud as a single flat link (not accordion)', async ({ page }) => {
    await gotoProvision(page)

    const cloud = page.getByTestId('sov-nav-cloud')
    await expect(
      cloud,
      'Sidebar must expose [data-testid=sov-nav-cloud] — the Cloud nav entry replaces the legacy Infrastructure flat entry (Sidebar.tsx).',
    ).toBeVisible()

    expect(
      await cloud.evaluate((el) => el.tagName),
      'Cloud nav entry must be an anchor (single-level link), not a button — issue #350 dropped the accordion.',
    ).toBe('A')

    expect(
      await cloud.textContent(),
      'Cloud entry label must read "Cloud" verbatim — issue #309 founder spec.',
    ).toContain('Cloud')

    // Legacy flat entry must remain gone.
    expect(
      await page.getByTestId('sov-nav-infrastructure').count(),
      'Sidebar still renders sov-nav-infrastructure — issue #309 replaced it with the Cloud entry.',
    ).toBe(0)

    // No accordion contracts.
    expect(await page.getByTestId('sov-nav-cloud-toggle').count()).toBe(0)
  })

  test('clicking Cloud navigates to /cloud and canonicalises ?view=graph', async ({ page }) => {
    await gotoProvision(page)

    await page.getByTestId('sov-nav-cloud').click()
    await page.waitForFunction(
      () =>
        window.location.pathname.endsWith('/cloud') &&
        /\?view=graph/.test(window.location.search),
      undefined,
      { timeout: 5_000 },
    )

    const cloud = page.getByTestId('sov-nav-cloud')
    expect(
      await cloud.getAttribute('aria-current'),
      'Cloud nav entry must declare aria-current=page when active.',
    ).toBe('page')
  })

  test('legacy /infrastructure/* paths redirect to /cloud?view=…', async ({ page }) => {
    for (const c of LEGACY_REDIRECTS) {
      await gotoProvision(page, c.from)
      await page.waitForFunction(
        ([searchPattern]) => new RegExp(searchPattern).test(window.location.search),
        [c.expectedSearch.source] as const,
        { timeout: 5_000 },
      )
      const url = new URL(page.url())
      expect(
        c.expectedSearch.test(url.search),
        `Expected provision/${DEPLOYMENT_ID}/${c.from} to redirect to a search matching ${c.expectedSearch}; got ${url.pathname}${url.search}.`,
      ).toBe(true)
      // Pathname must end in /cloud (no /architecture, /compute, /storage,
      // /network suffixes — those are now query-driven).
      expect(
        url.pathname.endsWith('/cloud'),
        `Expected pathname to end in /cloud; got ${url.pathname}.`,
      ).toBe(true)
    }
  })

  test('captures Cloud sidebar screenshot @ 1440x900', async ({ page }) => {
    await gotoProvision(page)
    await page.waitForSelector('[data-testid=admin-sidebar]')
    await page.screenshot({
      path: 'e2e/screenshots/p1-cloud-nav-sidebar.png',
      fullPage: false,
    })

    // Active state on /cloud.
    await gotoProvision(page, 'cloud')
    await page.waitForSelector('[data-testid=admin-sidebar]')
    await page.screenshot({
      path: 'e2e/screenshots/p1-cloud-nav-active.png',
      fullPage: false,
    })
  })
})
