// Gate check FND-G20 (FND-162): the application detail page. One test per ledger row; the title is the
// row id and its clause.
import { test, expect } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { appIdOf, applications, openApp, sharedPg, sovereignApps, str, tabLabels, targetCards, topologyOf } from './lib.ts'

/** The tabs of an application page that come after one another in the order the clause gives. */
function inOrder(labels: string[], want: string[]): boolean {
  let at = -1
  for (const w of want) {
    const i = labels.indexOf(w)
    if (i <= at) return false
    at = i
  }
  return true
}

/** The per-region states of the Topology tab's target cards, and the Overview's state. */
async function health(page: Page, id: string): Promise<{ overall: string; regions: string[] }> {
  await openApp(page, id, 'overview')
  const overall = (await page.getByTestId('app-detail-overview-status-badge').getAttribute('data-state')) ?? ''
  await openApp(page, id, 'topology')
  await expect(page.getByTestId('topology-tab-target-cards')).toBeVisible()
  const regions = await page.locator('[data-testid^="topology-tab-target-"][data-testid$="-status-badge"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-state') ?? ''))
  return { overall, regions }
}

async function healthyInBothRegions(page: Page, names: string[], error: RegExp): Promise<void> {
  const id = await appIdOf(page, ...names)
  const h = await health(page, id)
  expect(h.overall, `${id}: the Overview status`).toBe('Ready')
  expect(h.regions.length, `${id}: a status per region`).toBeGreaterThanOrEqual(2)
  expect(h.regions, `${id}: every region Ready`).toEqual(h.regions.map(() => 'Ready'))
  await expect(page.locator('body'), `${id}: no ${error} in the health panel`).not.toContainText(error)
  await openApp(page, id, 'overview')
  await expect(page.locator('body'), `${id}: no ${error} on the Overview`).not.toContainText(error)
}

test('4 App page tab strip generality: shared-pg and a consumer harbor render the identical tab strip; the consumer Dependencies shows Depends on: shared-pg / db:<ctx>', async ({ page }) => {
  const pg = await sharedPg(page)
  await openApp(page, pg.id)
  const pgTabs = await tabLabels(page)
  const harbor = await appIdOf(page, 'bp-harbor', 'harbor', 'platform-harbor')
  await openApp(page, harbor)
  const harborTabs = await tabLabels(page)
  // Contexts is the one tab a shareable Blueprint adds (row 24); every other tab is the same strip.
  expect(harborTabs, 'harbor and shared-pg: one tab strip').toEqual(pgTabs.filter((t) => t !== 'Contexts'))
  await openApp(page, harbor, 'dependencies')
  const lines = await page.getByTestId('sov-app-dependson-list').locator('li').allInnerTexts()
  expect(lines.some((l) => /^Depends on: shared-pg\S* \/ db:\S+/.test(l.trim())), `harbor depends on a shared-pg context: ${lines.join(' | ')}`).toBe(true)
})

test('14 The shared-PG reuse model is LIVE: the shared-pg app Contexts tab shows multiple consumers sharing ONE PostgreSQL', async ({ page }) => {
  const pg = await sharedPg(page)
  await openApp(page, pg.id, 'contexts')
  await expect(page.getByTestId('sov-contexts-table')).toBeVisible()
  const consumers = await page.locator('[data-testid^="sov-context-consumer-"]').allInnerTexts()
  expect(new Set(consumers.map((c) => c.trim()).filter((c) => c !== '')).size, `consumers of ${pg.id}`).toBeGreaterThanOrEqual(2)
})

test('24 shared-pg renders the canonical tab strip Overview · Contexts · Topology · Dependencies (Contexts tab present only for shareable blueprints)', async ({ page }) => {
  const pg = await sharedPg(page)
  await openApp(page, pg.id)
  const labels = await tabLabels(page)
  expect(inOrder(labels, ['Overview', 'Contexts', 'Topology', 'Dependencies']), `the tab strip of ${pg.id}: ${labels.join(' · ')}`).toBe(true)
  const notShareable = (await sovereignApps(page)).find((a) => a.instance === true && (a.contextCount ?? 0) === 0 && !/^shared-pg/.test(a.id))
  expect(notShareable, 'an application of a Blueprint that is not shareable').toBeTruthy()
  await openApp(page, notShareable?.id ?? '')
  expect(await tabLabels(page), `${notShareable?.id} has no Contexts tab`).not.toContain('Contexts')
})

test('16 A customer app page (/app/<name>) -> Topology -> change topology -> Save persists; the canonical tab strip includes a Topology tab', async ({ page }) => {
  test.setTimeout(300_000)
  const customer = (await applications('a')).filter((a) => str(a, 'spec.organizationRef') !== '' && a.metadata.labels?.['apps.openova.io/bootstrap-owned'] !== 'true')
  expect(customer.length, 'the Sovereign has a customer application').toBeGreaterThan(0)
  let chosen = ''
  let standby = -1
  for (const a of customer.slice(0, 8)) {
    await openApp(page, a.metadata.name, 'topology')
    expect(await tabLabels(page), `${a.metadata.name}: the tab strip`).toContain('Topology')
    const cards = await targetCards(page)
    const i = cards.findIndex((c) => c.role.startsWith('○ STANDBY'))
    if (i >= 0) {
      chosen = a.metadata.name
      standby = i
      break
    }
  }
  expect(chosen, 'a customer application with a standby target whose topology can change').not.toBe('')
  const before = (await targetCards(page))[standby].role
  const [from, to] = before.endsWith('Cold') ? ['cold', 'hot'] : ['hot', 'cold']
  const setStandby = async (kind: string) => {
    await page.getByTestId('topology-tab-edit-placement').click()
    await page.getByTestId(`placement-editor-target-${standby}-standby-${kind}`).click()
    await page.getByTestId('placement-editor-apply').click()
    await expect(page.getByTestId('placement-editor')).toHaveCount(0)
  }
  try {
    await setStandby(to)
    await page.reload()
    await expect(page.getByTestId(`topology-tab-target-card-${standby}-role`)).toHaveText(`○ STANDBY · ${to === 'cold' ? 'Cold' : 'Hot'}`)
  } finally {
    await openApp(page, chosen, 'topology')
    if (!(await page.getByTestId(`topology-tab-target-card-${standby}-role`).innerText()).endsWith(from === 'cold' ? 'Cold' : 'Hot')) await setStandby(from)
  }
})

test('53 Open a per-app Topology tab and read its placement view (singleton apps note no per-region/standby surface)', async ({ page }) => {
  const single = (await sovereignApps(page)).find((a) => a.instance === true && topologyOf(a) === 'singleton')
  expect(single, 'a singleton application').toBeTruthy()
  await openApp(page, single?.id ?? '', 'topology')
  await expect(page.getByTestId('topology-tab-pattern')).toHaveText('singleton')
  const cards = await targetCards(page)
  expect(cards, `${single?.id}: one placement target`).toHaveLength(1)
  expect(cards[0].role, `${single?.id}: no standby surface`).toBe('● PRIMARY')
})

test('187 Per-app Topology for a multi-region app (grafana) shows Pattern: active-active with 2 PRIMARY region cards, NOT singleton', async ({ page }) => {
  const id = await appIdOf(page, 'bp-grafana', 'grafana')
  await openApp(page, id, 'topology')
  await expect(page.getByTestId('topology-tab-pattern')).toHaveText('active-active')
  const cards = await targetCards(page)
  expect(cards.map((c) => c.role), `${id}: the region cards`).toEqual(['● PRIMARY', '● PRIMARY'])
  expect(new Set(cards.map((c) => c.where.split(' · ')[0])).size, `${id}: two regions`).toBe(2)
})

test('188 A genuine single-region app (catalyst-api) correctly shows singleton (no false multi-region)', async ({ page }) => {
  const id = await appIdOf(page, 'catalyst-api', 'bp-catalyst-platform')
  await openApp(page, id, 'topology')
  await expect(page.getByTestId('topology-tab-pattern')).toHaveText('singleton')
  const cards = await targetCards(page)
  expect(cards.map((c) => c.role), `${id}: one Primary`).toEqual(['● PRIMARY'])
})

test('67 grafana status/overview reports Healthy/Running in both regions: no "cannot resolve write host" crashloop in the app health panel', async ({ page }) => {
  await healthyInBothRegions(page, ['bp-grafana', 'grafana'], /cannot resolve write host/i)
})

test('68 powerdns-admin status reports Healthy/Running: the CNPG-minted DB host resolved (no "could not translate host")', async ({ page }) => {
  const id = await appIdOf(page, 'bp-powerdns-admin', 'powerdns-admin')
  const h = await health(page, id)
  expect(h.overall, `${id}: the Overview status`).toBe('Ready')
  expect(h.regions.length, `${id}: a status per region`).toBeGreaterThan(0)
  expect(h.regions, `${id}: every region Ready`).toEqual(h.regions.map(() => 'Ready'))
  await expect(page.locator('body')).not.toContainText(/could not translate host/i)
})

test('69 keycloak status reports Healthy/Running in both regions: JGroups DB-host resolves, no UnknownHostException in the health panel', async ({ page }) => {
  await healthyInBothRegions(page, ['bp-keycloak', 'keycloak', 'platform-keycloak'], /UnknownHostException/)
})

test('70 guacamole status reports Healthy/Running in both regions: no missing-recordings-PVC error surfaced', async ({ page }) => {
  await healthyInBothRegions(page, ['bp-guacamole', 'guacamole'], /recordings[^.]*(pvc|persistentvolumeclaim)|(pvc|persistentvolumeclaim)[^.]*recordings/i)
})
