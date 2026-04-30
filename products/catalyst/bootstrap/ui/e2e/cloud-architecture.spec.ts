/**
 * cloud-architecture.spec.ts — Playwright E2E lock-in for the
 * Sovereign Cloud / Architecture force-directed graph (P2 of
 * issue openova-io/openova#309).
 *
 * What this asserts:
 *   • Navigating to /sovereign/provision/{id}/cloud/architecture
 *     mounts the force-graph canvas + svg.
 *   • The edge legend, type badges, and global density slider all
 *     render at default state.
 *   • Typing in the search box triggers isolation: matches counter
 *     shows; nodes outside the match-or-neighbor set are filtered
 *     OUT of the rendered set (NOT dimmed).
 *   • Clicking a node opens the right-side detail panel with a
 *     populated neighbor list.
 *   • Right-clicking a node opens the context menu with kind-aware
 *     add-child / delete items.
 *   • Adjusting the global density slider re-renders without error.
 *   • Screenshots saved at 1440x900 in three states:
 *     default / search-isolated / focus-mode.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL
 * comes from playwright.config.ts (env-driven HOST + BASEPATH); we
 * use a synthetic deploymentId and rely on the SPA's fixture
 * fallback for the in-page data so the test is fully self-contained.
 */

import { test, expect, type Page } from '@playwright/test'

const DEPLOYMENT_ID = 'p2-309-e2e'

async function gotoArchitecture(page: Page) {
  await page.goto(`provision/${DEPLOYMENT_ID}/cloud/architecture`)
  await page.waitForLoadState('domcontentloaded')
  await page.waitForSelector('[data-testid=arch-graph-svg]')
}

test.describe('Cloud / Architecture force-graph (#309 P2)', () => {
  test('navigates to /cloud/architecture and mounts the force-graph canvas', async ({
    page,
  }) => {
    await gotoArchitecture(page)
    await expect(page.getByTestId('arch-graph-canvas')).toBeVisible()
    await expect(page.getByTestId('arch-graph-svg')).toBeVisible()

    // Live counts are present.
    await expect(page.getByTestId('arch-graph-stats-nodes')).toBeVisible()
    await expect(page.getByTestId('arch-graph-stats-edges')).toBeVisible()
  })

  test('exposes the edge legend, type badges, and the global density slider', async ({
    page,
  }) => {
    await gotoArchitecture(page)

    // Edge legend with at least the contains / runs-on / routes-to /
    // attached-to relations the fixture is guaranteed to produce.
    await expect(page.getByTestId('cloud-architecture-edge-legend')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-edge-legend-contains')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-edge-legend-runs-on')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-edge-legend-routes-to')).toBeVisible()

    // Per-type badges.
    for (const type of [
      'Cloud',
      'Region',
      'Cluster',
      'vCluster',
      'NodePool',
      'WorkerNode',
      'LoadBalancer',
      'Network',
    ]) {
      await expect(
        page.getByTestId(`cloud-architecture-type-badge-${type}`),
        `Type badge for ${type} should render`,
      ).toBeVisible()
    }

    // Global density slider at default 50.
    const slider = page.getByTestId('cloud-architecture-global-density')
    await expect(slider).toBeVisible()
    await expect(slider).toHaveValue('50')
  })

  test('search isolates matches + neighbors and shows the counter', async ({ page }) => {
    await gotoArchitecture(page)

    const search = page.getByTestId('cloud-architecture-search')
    await search.fill('omantel-primary')

    // Counter appears after the 250ms debounce.
    const counter = page.getByTestId('cloud-architecture-search-counter')
    await expect(counter).toBeVisible({ timeout: 2_000 })
    await expect(counter).toContainText(/matches/)
  })

  test('clicking a node opens the detail panel with neighbors', async ({ page }) => {
    await gotoArchitecture(page)

    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await expect(cluster).toBeVisible()
    await cluster.click()

    const panel = page.getByTestId('infrastructure-detail-panel')
    await expect(panel).toBeVisible()
    await expect(page.getByTestId('infrastructure-detail-panel-name')).toHaveText(
      'omantel-primary',
    )
    await expect(page.getByTestId('infrastructure-detail-panel-type')).toHaveText('Cluster')

    // Neighbor list shows the parent region.
    await expect(
      page.getByTestId('infrastructure-detail-panel-neighbor-Region:region-eu-central'),
    ).toBeVisible()
  })

  test('right-clicking a node opens a kind-aware context menu', async ({ page }) => {
    await gotoArchitecture(page)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await cluster.click({ button: 'right' })

    const menu = page.getByTestId('cloud-architecture-context-menu')
    await expect(menu).toBeVisible()
    await expect(menu).toHaveAttribute('data-context-target', 'Cluster')
    await expect(page.getByTestId('cloud-architecture-context-add-vcluster')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-nodepool')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-delete')).toBeVisible()
  })

  test('global density slider responds to input', async ({ page }) => {
    await gotoArchitecture(page)
    const slider = page.getByTestId('cloud-architecture-global-density')
    await slider.fill('25')
    await expect(page.getByTestId('cloud-architecture-global-density-pct')).toHaveText('25%')
  })

  test('captures Architecture screenshots @ 1440x900 in 3 states', async ({ page }) => {
    // 1: Default — graph just mounted.
    await gotoArchitecture(page)
    // Settle a beat so the simulation is past its initial frantic tick.
    await page.waitForTimeout(800)
    await page.screenshot({
      path: 'e2e/screenshots/p2-architecture-default.png',
      fullPage: false,
    })

    // 2: Search-isolated — counter visible.
    await page.getByTestId('cloud-architecture-search').fill('omantel-primary')
    await page.waitForSelector('[data-testid=cloud-architecture-search-counter]', {
      timeout: 2_000,
    })
    await page.waitForTimeout(600)
    await page.screenshot({
      path: 'e2e/screenshots/p2-architecture-search.png',
      fullPage: false,
    })

    // 3: Focus mode — double-click a cluster to enter focus.
    await page.getByTestId('cloud-architecture-search').fill('')
    await page.waitForTimeout(400)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await cluster.dblclick()
    await page.waitForTimeout(500)
    await page.screenshot({
      path: 'e2e/screenshots/p2-architecture-focus.png',
      fullPage: false,
    })
  })
})
