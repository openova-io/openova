/**
 * cloud-list-pages.spec.ts — Sovereign-portal Cloud per-resource list
 * pages E2E lock-in (P3 of issue #309).
 *
 * What this asserts:
 *   • The sidebar's second-level accordion (Compute / Network /
 *     Storage) expands to reveal sub-sub items.
 *   • Each sub-sub link routes to its /cloud/<category>/<resource>
 *     page; the page renders with at least one row + a clickable
 *     row that opens the detail drawer.
 *   • Placeholder pages (Services / Ingresses / DNS Zones / Storage
 *     Classes) render the canonical empty-state shell.
 *   • 1440x900 visual screenshots saved to e2e/screenshots/.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL
 * comes from playwright.config.ts (env-driven HOST + BASEPATH); we
 * use a synthetic deploymentId and rely on the SPA's fixture
 * fallback for in-page data.
 */

import { test, expect, type Page } from '@playwright/test'

// Deliberately avoid the strings "cloud" or "infrastructure" in the
// deploymentId so the sidebar's path-segment matcher can't be fooled
// by a substring match.
const DEPLOYMENT_ID = 'p3-309-e2e'

interface ListPageCase {
  category: 'compute' | 'network' | 'storage'
  child: string
  /** Page-container testid. */
  pageTestId: string
  /** First row id we expect from the fixture (or '' for placeholder pages). */
  firstRowId: string
  /** True for placeholder routes (Services / Ingresses / DNS Zones / Storage Classes). */
  placeholder: boolean
}

const CASES: readonly ListPageCase[] = [
  // Compute — real data
  { category: 'compute', child: 'clusters',     pageTestId: 'cloud-clusters-page',     firstRowId: 'cloud-clusters-row-cluster-eu-central-primary',     placeholder: false },
  { category: 'compute', child: 'vclusters',    pageTestId: 'cloud-vclusters-page',    firstRowId: 'cloud-vclusters-row-vc-eu-central-dmz',             placeholder: false },
  { category: 'compute', child: 'node-pools',   pageTestId: 'cloud-node-pools-page',   firstRowId: 'cloud-node-pools-row-pool-eu-cp',                   placeholder: false },
  { category: 'compute', child: 'worker-nodes', pageTestId: 'cloud-worker-nodes-page', firstRowId: 'cloud-worker-nodes-row-node-eu-cp-0',               placeholder: false },
  // Network
  { category: 'network', child: 'load-balancers', pageTestId: 'cloud-load-balancers-page', firstRowId: 'cloud-load-balancers-row-lb-eu-central-edge',  placeholder: false },
  { category: 'network', child: 'services',       pageTestId: 'cloud-services-page',       firstRowId: '',                                              placeholder: true },
  { category: 'network', child: 'ingresses',      pageTestId: 'cloud-ingresses-page',      firstRowId: '',                                              placeholder: true },
  { category: 'network', child: 'dns-zones',      pageTestId: 'cloud-dns-zones-page',      firstRowId: '',                                              placeholder: true },
  // Storage
  { category: 'storage', child: 'pvcs',            pageTestId: 'cloud-pvcs-page',            firstRowId: 'cloud-pvcs-row-pvc-postgres-data',            placeholder: false },
  { category: 'storage', child: 'buckets',         pageTestId: 'cloud-buckets-page',         firstRowId: 'cloud-buckets-row-bucket-backups',            placeholder: false },
  { category: 'storage', child: 'volumes',         pageTestId: 'cloud-volumes-page',         firstRowId: 'cloud-volumes-row-vol-postgres-eu',           placeholder: false },
  { category: 'storage', child: 'storage-classes', pageTestId: 'cloud-storage-classes-page', firstRowId: '',                                            placeholder: true },
] as const

async function gotoProvision(page: Page, suffix = '') {
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

test.describe('Cloud list pages (#309 P3)', () => {
  test.beforeEach(async ({ page }) => {
    await clearLocalStorage(page)
  })

  test('sidebar second-level accordion exposes sub-sub items for Compute / Network / Storage', async ({ page }) => {
    await gotoProvision(page)
    // First-level Cloud accordion auto-collapses by default; expand it.
    await page.getByTestId('sov-nav-cloud').click()

    // Each category exposes a Link (landing page) + a toggle button.
    for (const id of ['compute', 'network', 'storage'] as const) {
      const row = page.getByTestId(`sov-nav-cloud-${id}-row`)
      await expect(row).toBeVisible()
      const toggle = page.getByTestId(`sov-nav-cloud-${id}-toggle`)
      await expect(toggle).toBeVisible()
      // Initially collapsed (no persisted state).
      expect(await toggle.getAttribute('aria-expanded')).toBe('false')

      await toggle.click()
      expect(await toggle.getAttribute('aria-expanded')).toBe('true')
    }

    // Now sub-sub items must be visible.
    for (const c of CASES) {
      const item = page.getByTestId(`sov-nav-cloud-${c.category}-${c.child}`)
      await expect(item).toBeVisible()
    }
  })

  for (const c of CASES) {
    test(`${c.category}/${c.child} renders + ${c.placeholder ? 'shows empty state' : 'opens drawer on row click'}`, async ({ page }) => {
      await gotoProvision(page, `cloud/${c.category}/${c.child}`)
      // Page container must mount.
      await expect(
        page.getByTestId(c.pageTestId),
        `expected /cloud/${c.category}/${c.child} to mount [data-testid=${c.pageTestId}]`,
      ).toBeVisible()

      if (c.placeholder) {
        // Placeholder pages must surface the canonical empty state.
        const emptyId = c.pageTestId.replace('-page', '-empty')
        await expect(
          page.getByTestId(emptyId),
          `placeholder page ${c.pageTestId} must surface [data-testid=${emptyId}]`,
        ).toBeVisible()
      } else {
        // Data-backed pages must render the seeded first row + open
        // the detail drawer when clicked.
        const row = page.getByTestId(c.firstRowId)
        await expect(row, `expected first-row [${c.firstRowId}] on ${c.pageTestId}`).toBeVisible()
        await row.click()
        const detailId = c.pageTestId.replace('-page', '-detail')
        await expect(
          page.getByTestId(detailId),
          `clicking row should open [${detailId}] on ${c.pageTestId}`,
        ).toBeVisible()
        // Esc + close button parity (sanity).
        await page.getByTestId(`${detailId}-close`).click()
        await expect(page.getByTestId(detailId)).toHaveCount(0)
      }
    })
  }

  test('category landing pages show 4 tiles each', async ({ page }) => {
    for (const cat of ['compute', 'network', 'storage'] as const) {
      await gotoProvision(page, `cloud/${cat}`)
      await expect(page.getByTestId(`cloud-${cat}-page`)).toBeVisible()
      // Each category landing renders a tile grid with at least 4
      // tiles. The exact tile ids depend on the category; just count
      // the grid children.
      const grid = page.getByTestId(`cloud-${cat}-page-tiles`)
      await expect(grid).toBeVisible()
      const tiles = grid.locator('a')
      await expect(tiles).toHaveCount(4)
    }
  })

  test('captures 1440×900 screenshots of every list page', async ({ page }) => {
    for (const c of CASES) {
      await gotoProvision(page, `cloud/${c.category}/${c.child}`)
      await page.waitForSelector(`[data-testid=${c.pageTestId}]`)
      // Allow drawer animation to settle if any prior test left it open.
      await page.waitForTimeout(150)
      await page.screenshot({
        path: `e2e/screenshots/p3-cloud-${c.category}-${c.child}.png`,
        fullPage: false,
      })
    }
  })
})
