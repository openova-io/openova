/**
 * cloud-shell.spec.ts — Sovereign-portal Cloud parent shell E2E
 * lock-in (issue openova-io/openova#350).
 *
 * What this asserts:
 *   • Sidebar exposes a SINGLE flat "Cloud" entry — no chevron, no
 *     accordion, no sub-items, no second-level toggles.
 *   • Clicking Cloud navigates to /cloud (the parent route); the URL
 *     is canonicalised to ?view=graph by default.
 *   • The View toggle (Graph | List) switches the body and persists
 *     across reloads via localStorage `sov-cloud-view`.
 *   • In list view: 12 resource tiles render with counts, clicking a
 *     tile switches the active list, and the dropdown switcher
 *     mirrors the selection.
 *   • Fullscreen toggle button enters fullscreen (DOM state asserted
 *     via the `data-fullscreen` data attribute and `aria-pressed`);
 *     the floating Exit button leaves fullscreen.
 *   • Legacy URLs redirect:
 *       /cloud/architecture                 → ?view=graph
 *       /cloud/compute/clusters             → ?view=list&kind=clusters
 *       /cloud/storage/pvcs                 → ?view=list&kind=pvcs
 *   • Visual screenshots saved at 1440×900 to e2e/screenshots/.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL
 * comes from playwright.config.ts (env-driven HOST + BASEPATH); we
 * use a synthetic deploymentId and rely on the SPA's fixture
 * fallback for the in-page data so the test is fully self-contained.
 */

import { test, expect, type Page } from '@playwright/test'

const DEPLOYMENT_ID = 'p350-shell-e2e'

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

test.describe('Cloud shell — sidebar entry (#350 item 7+8)', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('sidebar exposes a single flat Cloud entry — no accordion, no chevron, no sub-items', async ({
    page,
  }) => {
    await gotoProvision(page)

    const cloud = page.getByTestId('sov-nav-cloud')
    await expect(
      cloud,
      'Sidebar must expose [data-testid=sov-nav-cloud] as the single Cloud nav entry.',
    ).toBeVisible()

    // Cloud entry is an <a> (link), NOT a <button> (no accordion toggle).
    const tag = await cloud.evaluate((el) => el.tagName)
    expect(
      tag,
      'Cloud entry must render as an anchor (single-level link), not a button — issue #350 dropped the accordion.',
    ).toBe('A')

    // Confirm none of the legacy accordion sub-items / toggles render.
    expect(await page.getByTestId('sov-nav-cloud-toggle').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-architecture').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-compute').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-network').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-storage').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-compute-toggle').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-network-toggle').count()).toBe(0)
    expect(await page.getByTestId('sov-nav-cloud-storage-toggle').count()).toBe(0)
  })

  test('clicking Cloud navigates to /cloud and canonicalises ?view=graph', async ({ page }) => {
    await gotoProvision(page)
    await page.getByTestId('sov-nav-cloud').click()

    await page.waitForFunction(
      () => /\/cloud(\/|$|\?)/.test(window.location.pathname + window.location.search),
      undefined,
      { timeout: 5_000 },
    )

    // The CloudPage canonicalises to ?view=graph on mount.
    await page.waitForFunction(
      () => /\?view=graph/.test(window.location.search),
      undefined,
      { timeout: 5_000 },
    )

    await expect(page.getByTestId('cloud-page-toolbar')).toBeVisible()
    await expect(page.getByTestId('cloud-page-view-toggle')).toBeVisible()
  })
})

test.describe('Cloud shell — view toggle persistence (#350 item 7)', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('view toggle switches graph ↔ list and persists across reload', async ({ page }) => {
    await gotoProvision(page, 'cloud')

    // Default = graph.
    const graph = page.getByTestId('cloud-page-view-graph')
    const list = page.getByTestId('cloud-page-view-list')
    await expect(graph).toHaveAttribute('aria-selected', 'true')
    await expect(list).toHaveAttribute('aria-selected', 'false')

    // Switch to list.
    await list.click()
    await expect(list).toHaveAttribute('aria-selected', 'true')
    await expect(graph).toHaveAttribute('aria-selected', 'false')
    await expect(page.getByTestId('cloud-list-view')).toBeVisible()

    // localStorage carries the persisted value.
    const stored = await page.evaluate(() => window.localStorage.getItem('sov-cloud-view'))
    expect(stored).toBe('list')

    // Reload — list view sticks.
    await page.reload()
    await page.waitForLoadState('domcontentloaded')
    await expect(page.getByTestId('cloud-page-view-list')).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByTestId('cloud-list-view')).toBeVisible()
  })
})

test.describe('Cloud shell — list view chip strip (#366 item 1)', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('list view renders the toolbar chip strip with primary chips and a + More overflow', async ({
    page,
  }) => {
    await gotoProvision(page, 'cloud?view=list')

    const chips = page.getByTestId('cloud-kind-chips')
    await expect(chips).toBeVisible()

    // Primary chips render inline. Founder priority order:
    // Clusters, vClusters, Node Pools, PVCs, Load Balancers, Buckets.
    const primaryKinds = [
      'clusters',
      'vclusters',
      'node-pools',
      'pvcs',
      'load-balancers',
      'buckets',
    ]
    for (const k of primaryKinds) {
      await expect(
        page.getByTestId(`cloud-kind-chip-${k}`),
        `Primary chip [data-testid=cloud-kind-chip-${k}] must render in the toolbar strip.`,
      ).toBeVisible()
      await expect(
        page.getByTestId(`cloud-kind-chip-${k}-count`),
        `Chip count badge for ${k} must render.`,
      ).toBeVisible()
    }

    // The legacy 12-tile card grid is GONE.
    expect(await page.getByTestId('cloud-list-view-tile-grid').count()).toBe(0)
    expect(await page.getByTestId('cloud-list-view-kind-select').count()).toBe(0)

    // Default active kind is "clusters".
    await expect(page.getByTestId('cloud-kind-chip-clusters')).toHaveAttribute(
      'data-active',
      'true',
    )
    await expect(page.getByTestId(`cloud-list-view-active-clusters`)).toBeVisible()

    // The active list table sits directly below the toolbar — assert it
    // is in the viewport at 1440×900 (issue #366 item 1 acceptance:
    // active list visible above the fold).
    const activeTable = page.getByTestId(`cloud-list-view-active-clusters`)
    await expect(activeTable).toBeInViewport()

    // Click the PVCs chip — active switches.
    await page.getByTestId('cloud-kind-chip-pvcs').click()
    await page.waitForFunction(() => /kind=pvcs/.test(window.location.search), undefined, {
      timeout: 5_000,
    })
    await expect(page.getByTestId('cloud-kind-chip-pvcs')).toHaveAttribute(
      'data-active',
      'true',
    )
    await expect(page.getByTestId('cloud-kind-chip-clusters')).toHaveAttribute(
      'data-active',
      'false',
    )
  })

  test('+ More popover exposes the overflow kinds and switches active', async ({ page }) => {
    await gotoProvision(page, 'cloud?view=list&kind=clusters')

    const more = page.getByTestId('cloud-kind-chip-more')
    await expect(more).toBeVisible()

    // Overflow popover opens on click.
    await more.click()
    const popover = page.getByTestId('cloud-kind-chip-more-popover')
    await expect(popover).toBeVisible()

    // Overflow contents — Worker Nodes, Services, Ingresses, DNS Zones,
    // Volumes, Storage Classes.
    for (const k of ['worker-nodes', 'services', 'ingresses', 'dns-zones', 'volumes', 'storage-classes']) {
      await expect(
        page.getByTestId(`cloud-kind-chip-more-item-${k}`),
        `Overflow popover must expose the ${k} entry.`,
      ).toBeVisible()
    }

    // Click the Volumes overflow item — popover closes, kind switches.
    await page.getByTestId('cloud-kind-chip-more-item-volumes').click()
    await page.waitForFunction(() => /kind=volumes/.test(window.location.search), undefined, {
      timeout: 5_000,
    })
  })
})

test.describe('Cloud shell — fullscreen (#350 item 9)', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('fullscreen toggle enters fullscreen and the Exit affordance leaves', async ({ page }) => {
    await gotoProvision(page, 'cloud?view=graph')

    const toggle = page.getByTestId('cloud-page-fullscreen-toggle')
    await expect(toggle).toBeVisible()
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')

    // Headless Chromium doesn't grant the fullscreen permission to the
    // unattended runner without explicit consent; the CloudPage falls
    // back to the synthetic-fullscreen path which flips the
    // data-fullscreen attribute and aria-pressed regardless of the
    // user-agent's API success.
    await toggle.click()
    await expect(page.getByTestId('cloud-content')).toHaveAttribute(
      'data-fullscreen',
      'true',
      { timeout: 3_000 },
    )
    await expect(toggle).toHaveAttribute('aria-pressed', 'true')

    // Exit affordance is rendered inside the overlay.
    const exit = page.getByTestId('cloud-page-fullscreen-exit')
    await expect(exit).toBeVisible()

    await exit.click()
    await expect(page.getByTestId('cloud-content')).toHaveAttribute(
      'data-fullscreen',
      'false',
      { timeout: 3_000 },
    )
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
  })
})

test.describe('Cloud shell — legacy URL redirects (#350 item 7 — back-compat)', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  const REDIRECTS: ReadonlyArray<{ from: string; expectedSearch: RegExp }> = [
    { from: 'cloud/architecture', expectedSearch: /\?view=graph/ },
    { from: 'cloud/compute', expectedSearch: /\?view=list&kind=clusters/ },
    { from: 'cloud/compute/clusters', expectedSearch: /\?view=list&kind=clusters/ },
    { from: 'cloud/compute/vclusters', expectedSearch: /\?view=list&kind=vclusters/ },
    { from: 'cloud/network', expectedSearch: /\?view=list&kind=load-balancers/ },
    { from: 'cloud/network/load-balancers', expectedSearch: /\?view=list&kind=load-balancers/ },
    { from: 'cloud/network/services', expectedSearch: /\?view=list&kind=services/ },
    { from: 'cloud/storage', expectedSearch: /\?view=list&kind=pvcs/ },
    { from: 'cloud/storage/pvcs', expectedSearch: /\?view=list&kind=pvcs/ },
    { from: 'cloud/storage/buckets', expectedSearch: /\?view=list&kind=buckets/ },
  ] as const

  for (const r of REDIRECTS) {
    test(`/${r.from} redirects to ${r.expectedSearch}`, async ({ page }) => {
      await gotoProvision(page, r.from)
      await page.waitForFunction(
        ([searchPattern]) => new RegExp(searchPattern).test(window.location.search),
        [r.expectedSearch.source] as const,
        { timeout: 5_000 },
      )
      const url = new URL(page.url())
      expect(
        r.expectedSearch.test(url.search),
        `Expected /${r.from} to redirect to ${r.expectedSearch}; got ${url.pathname}${url.search}`,
      ).toBe(true)
      // The pathname must always end in /cloud — no /cloud/architecture
      // or /cloud/compute/clusters segments after the redirect.
      expect(
        url.pathname.endsWith('/cloud'),
        `Expected the pathname to end in /cloud; got ${url.pathname}`,
      ).toBe(true)
    })
  }
})

test.describe('Cloud shell — visual regression @ 1440×900', () => {
  test.beforeEach(async ({ page }) => {
    await clearStorage(page)
  })

  test('captures graph view + list view + fullscreen graph + sidebar icon', async ({ page }) => {
    // 1: Sidebar Cloud entry (close-up of the rail).
    await gotoProvision(page)
    await page.waitForSelector('[data-testid=admin-sidebar]')
    const sidebar = page.getByTestId('admin-sidebar')
    await sidebar.screenshot({
      path: 'e2e/screenshots/p350-sidebar-cloud-icon.png',
    })

    // 2: Graph view default.
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=cloud-page-view-toggle]')
    // Settle the simulation a beat.
    await page.waitForTimeout(800)
    await page.screenshot({
      path: 'e2e/screenshots/p350-cloud-graph.png',
      fullPage: false,
    })

    // 3: List view (PVCs) — show the chip strip + active list table.
    await gotoProvision(page, 'cloud?view=list&kind=pvcs')
    await page.waitForSelector('[data-testid=cloud-kind-chips]')
    await page.screenshot({
      path: 'e2e/screenshots/p350-cloud-list-pvcs.png',
      fullPage: false,
    })

    // 4: Fullscreen graph — synthetic overlay state.
    await gotoProvision(page, 'cloud?view=graph')
    await page.waitForSelector('[data-testid=cloud-page-fullscreen-toggle]')
    await page.getByTestId('cloud-page-fullscreen-toggle').click()
    // Wait for the data-fullscreen flip + transition to settle.
    await page.waitForFunction(() => {
      const el = document.querySelector('[data-testid=cloud-content]')
      return el?.getAttribute('data-fullscreen') === 'true'
    }, undefined, { timeout: 3_000 })
    await page.waitForTimeout(400)
    await page.screenshot({
      path: 'e2e/screenshots/p350-cloud-fullscreen-graph.png',
      fullPage: false,
    })
  })
})
