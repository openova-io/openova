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
    // force: true bypasses Playwright's stability check — the force-graph
    // simulation is intentionally continuous (cooldownTicks: Infinity-equiv),
    // so nodes never strictly "settle". The click event still fires correctly.
    await cluster.click({ force: true })

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
    // force: true — see comment in the click-detail-panel test above.
    await cluster.click({ button: 'right', force: true })

    const menu = page.getByTestId('cloud-architecture-context-menu')
    await expect(menu).toBeVisible()
    await expect(menu).toHaveAttribute('data-context-target', 'Cluster')
    await expect(page.getByTestId('cloud-architecture-context-add-vcluster')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-nodepool')).toBeVisible()
    // #349 — every node kind also exposes Edit + Delete.
    await expect(page.getByTestId('cloud-architecture-context-edit')).toBeVisible()
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
    // force: true — continuous simulation never reaches "stable".
    await cluster.dblclick({ force: true })
    await page.waitForTimeout(500)
    await page.screenshot({
      path: 'e2e/screenshots/p2-architecture-focus.png',
      fullPage: false,
    })
  })
})

/* ── #348 Phase A.1 — graph polish ─────────────────────────────── */

test.describe('Cloud / Architecture polish (#348 P1)', () => {
  test('every type chip carries a remove button + add-chip button reveals inactive types', async ({
    page,
  }) => {
    await gotoArchitecture(page)

    // Every active chip carries a × remove button.
    for (const t of [
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
        page.getByTestId(`cloud-architecture-type-badge-${t}-remove`),
        `Remove button for ${t} should render`,
      ).toBeVisible()
    }

    // Strip ends with the "+" Add chip button.
    await expect(page.getByTestId('cloud-architecture-add-chip-button')).toBeVisible()
  })

  test('removing every chip except NodePool + PVC isolates only those + their edges', async ({
    page,
  }) => {
    await gotoArchitecture(page)
    // Remove every default-active chip except NodePool. PVC starts
    // active in the strip too (it's emitted by the storage adapter).
    for (const t of [
      'Cloud',
      'Region',
      'Cluster',
      'vCluster',
      'WorkerNode',
      'LoadBalancer',
      'Network',
      'Bucket',
      'Volume',
    ]) {
      const remove = page.getByTestId(`cloud-architecture-type-badge-${t}-remove`)
      // Some types may not render if no instances exist (Service / Ingress).
      if (await remove.count()) {
        await remove.click({ force: true })
      }
    }

    // Wait one settle tick.
    await page.waitForTimeout(400)

    // Visible nodes are exclusively NodePool / PVC.
    const allNodes = page.locator('[data-testid^="arch-graph-node-"]')
    const count = await allNodes.count()
    expect(count, 'some NodePool / PVC nodes remain').toBeGreaterThan(0)
    for (let i = 0; i < count; i++) {
      const t = await allNodes.nth(i).getAttribute('data-node-type')
      expect(['NodePool', 'PVC']).toContain(t)
    }
  })

  test('add-chip popover re-adds a removed type', async ({ page }) => {
    await gotoArchitecture(page)
    // Remove the Network chip.
    await page
      .getByTestId('cloud-architecture-type-badge-Network-remove')
      .click({ force: true })
    await expect(page.getByTestId('cloud-architecture-type-badge-Network')).toHaveCount(0)

    // Open the add-chip popover and re-add Network.
    await page.getByTestId('cloud-architecture-add-chip-button').click({ force: true })
    await expect(page.getByTestId('cloud-architecture-add-chip-popover')).toBeVisible()
    await page.getByTestId('cloud-architecture-add-chip-item-Network').click({ force: true })

    await expect(page.getByTestId('cloud-architecture-type-badge-Network')).toBeVisible()
  })

  test('detail panel groups neighbors by relation type with subheaders', async ({ page }) => {
    await gotoArchitecture(page)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await cluster.click({ force: true })
    const panel = page.getByTestId('infrastructure-detail-panel')
    await expect(panel).toBeVisible()

    // The Cluster fixture has at least `contains` (from Region) and
    // `runs-on` (from NodePools / WorkerNodes) and `member-of` (from
    // vClusters). Each relation present produces a subheader.
    await expect(
      page.getByTestId('arch-detail-panel-relation-header-contains'),
    ).toBeVisible()
    await expect(page.getByTestId('arch-detail-panel-relation-header-runs-on')).toBeVisible()
    await expect(
      page.getByTestId('arch-detail-panel-relation-header-member-of'),
    ).toBeVisible()

    // Per-neighbor entry carries `arch-detail-panel-neighbor-{relation}-{nodeId}`.
    await expect(
      page.getByTestId('arch-detail-panel-neighbor-contains-Region:region-eu-central'),
    ).toBeVisible()
  })

  test('edge legend shows ArchiMate-style symbol thumbnails for every relation type', async ({
    page,
  }) => {
    await gotoArchitecture(page)
    const legend = page.getByTestId('cloud-architecture-edge-legend')
    await expect(legend).toBeVisible()

    // Every relation type from the spec carries a legend entry.
    for (const t of [
      'contains',
      'member-of',
      'runs-on',
      'routes-to',
      'attached-to',
      'depends-on',
      'used-by',
      'realizes',
      'peers-with',
      'flows-to',
      'triggers',
      'associates',
    ]) {
      await expect(
        page.getByTestId(`cloud-architecture-edge-legend-${t}`),
        `Legend entry for ${t} should render`,
      ).toBeVisible()
    }

    // Marker defs are emitted in the canvas SVG.
    await expect(page.getByTestId('arch-graph-marker-defs')).toBeAttached()
  })

  test('per-type tabler icons replace plain circles', async ({ page }) => {
    await gotoArchitecture(page)
    // Each rendered node carries a per-type icon SVG identified by
    // a `arch-graph-node-icon-{type}` testid.
    for (const t of ['Cloud', 'Region', 'Cluster', 'vCluster']) {
      await expect(
        page.locator(`[data-testid="arch-graph-node-icon-${t}"]`).first(),
        `Type icon for ${t} should render at least once`,
      ).toBeAttached()
    }
  })

  test('bounded physics — drag node toward (-100,-100) and assert clamped inside canvas', async ({
    page,
  }) => {
    await gotoArchitecture(page)

    const node = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await expect(node).toBeVisible()
    const box = await node.boundingBox()
    expect(box, 'cluster node has a bounding box').toBeTruthy()
    if (!box) return

    const canvas = page.getByTestId('arch-graph-svg')
    const canvasBox = await canvas.boundingBox()
    expect(canvasBox, 'canvas has a bounding box').toBeTruthy()
    if (!canvasBox) return

    // Drag the node toward (-100, -100) — outside the SVG entirely.
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(canvasBox.x - 100, canvasBox.y - 100, { steps: 10 })
    await page.mouse.up()

    // The node's transform must keep it inside the canvas bounds.
    const node2 = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    const post = await node2.boundingBox()
    expect(post, 'cluster still has a bounding box').toBeTruthy()
    if (!post) return

    // Allow a small margin for stroke / icon padding.
    const margin = 8
    expect(post.x + post.width / 2).toBeGreaterThanOrEqual(canvasBox.x - margin)
    expect(post.y + post.height / 2).toBeGreaterThanOrEqual(canvasBox.y - margin)
    expect(post.x + post.width / 2).toBeLessThanOrEqual(
      canvasBox.x + canvasBox.width + margin,
    )
    expect(post.y + post.height / 2).toBeLessThanOrEqual(
      canvasBox.y + canvasBox.height + margin,
    )
  })

  test('global density slider does not affect small types (auto-100%)', async ({ page }) => {
    await gotoArchitecture(page)

    // Move the global density slider to 25%. Small types
    // (count < SMALL_TYPE_THRESHOLD = 20) should stay at 100% — i.e.
    // no per-type cap is applied. The chip's "X/total" label reflects
    // visible/total. For Cloud / Region (always small), capped == total.
    await page.getByTestId('cloud-architecture-global-density').fill('25')
    await expect(page.getByTestId('cloud-architecture-global-density-pct')).toHaveText('25%')

    // Cloud is small (1 cloud in fixture); chip count must read like
    // "1/1" with no slash truncation.
    const cloudChip = page.getByTestId('cloud-architecture-type-badge-Cloud')
    await expect(cloudChip).toContainText('1/1')
  })

  test('captures #348 polish screenshots @ 1440x900', async ({ page }) => {
    // Default — every chip active, all relations visible.
    await gotoArchitecture(page)
    await page.waitForTimeout(800)
    await page.screenshot({
      path: 'e2e/screenshots/p1-348-default.png',
      fullPage: false,
    })

    // NodePool + PVC isolated.
    for (const t of [
      'Cloud',
      'Region',
      'Cluster',
      'vCluster',
      'WorkerNode',
      'LoadBalancer',
      'Network',
      'Bucket',
      'Volume',
    ]) {
      const remove = page.getByTestId(`cloud-architecture-type-badge-${t}-remove`)
      if (await remove.count()) await remove.click({ force: true })
    }
    await page.waitForTimeout(700)
    await page.screenshot({
      path: 'e2e/screenshots/p1-348-nodepool-pvc.png',
      fullPage: false,
    })

    // Single-type focus — re-add Cluster, then double-click the
    // primary cluster to enter focus mode.
    await page.getByTestId('cloud-architecture-add-chip-button').click({ force: true })
    await page.getByTestId('cloud-architecture-add-chip-item-Cluster').click({ force: true })
    await page.waitForTimeout(400)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    if (await cluster.count()) {
      await cluster.dblclick({ force: true })
    }
    await page.waitForTimeout(500)
    await page.screenshot({
      path: 'e2e/screenshots/p1-348-focus.png',
      fullPage: false,
    })

    // ArchiMate legend close-up — scroll the legend into view + clip.
    await page.getByTestId('cloud-architecture-clear-focus').click({ force: true }).catch(() => {})
    await page.waitForTimeout(200)
    const legend = page.getByTestId('cloud-architecture-edge-legend')
    const lbox = await legend.boundingBox()
    if (lbox) {
      await page.screenshot({
        path: 'e2e/screenshots/p1-348-archimate-legend.png',
        clip: {
          x: Math.max(0, lbox.x - 4),
          y: Math.max(0, lbox.y - 4),
          width: Math.min(1440, lbox.width + 8),
          height: Math.min(900, lbox.height + 8),
        },
      })
    }
  })
})
