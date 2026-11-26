// Gate check FND-G20 (FND-162): the Topology tab of the shared PostgreSQL instance. One test per ledger
// row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { CANONICAL_MODES, kube, openApp, sharedPg, targetCards } from './lib.ts'

/** The region names of the Sovereign's two regions, read from a node of each (label openova.io/region). */
async function regionNames(): Promise<{ a: string; b: string }> {
  const name = async (r: 'a' | 'b') => (await kube(r, ['get', 'nodes', '-o', 'jsonpath={.items[0].metadata.labels.openova\\.io/region}'])).trim()
  const a = await name('a')
  const b = await name('b')
  expect(a, 'the region of region a').not.toBe('')
  expect(b, 'the region of region b').not.toBe('')
  expect(a).not.toBe(b)
  return { a, b }
}

async function pgTopology(page: Page): Promise<string> {
  const pg = await sharedPg(page)
  await openApp(page, pg.id, 'topology')
  await expect(page.getByTestId('topology-tab-target-cards')).toBeVisible()
  return pg.id
}

const regionOf = (where: string) => where.split(' · ')[0].trim()
const LAG = /replication[- ]lag\D{0,40}?(\d+(\.\d+)?)\s*s\b/i

test('51 shared-pg Topology tab renders a per-region placement view listing region-a (active) and region-b (standby) as ONE placement, not two separate instances', async ({ page }) => {
  const { a, b } = await regionNames()
  const id = await pgTopology(page)
  await expect(page.getByTestId('topology-tab-placement-panel'), `${id}: one placement`).toHaveCount(1)
  const cards = await targetCards(page)
  expect(cards.map((c) => [regionOf(c.where), c.role.startsWith('● PRIMARY') ? 'active' : 'standby']), `${id}: the targets of the one placement`).toEqual([[a, 'active'], [b, 'standby']])
})

test('52 shared-pg Topology tab renders ROLE asymmetry: region-a reads ● PRIMARY / serves writes, region-b reads ○ STANDBY · Hot / live replica, joined by a ──▶ repl arrow; never a second co-equal Primary', async ({ page }) => {
  const { a, b } = await regionNames()
  await pgTopology(page)
  const cards = await targetCards(page)
  expect(cards.map((c) => ({ region: regionOf(c.where), role: c.role, sub: c.sub }))).toEqual([
    { region: a, role: '● PRIMARY', sub: 'serves writes' },
    { region: b, role: '○ STANDBY · Hot', sub: 'live replica' },
  ])
  await expect(page.getByTestId('topology-tab-target-cards')).toContainText('──▶')
  await expect(page.getByTestId('topology-tab-target-cards')).toContainText('repl')
})

test('54 shared-pg Topology tab declared-topology strip renders the canonical mode in ONE vocabulary: header dialect and picker dialect MATCH', async ({ page }) => {
  await pgTopology(page)
  const header = (await page.getByTestId('topology-tab-pattern').innerText()).trim()
  expect(CANONICAL_MODES, `the header's mode ${header}`).toContain(header)
  await page.getByTestId('topology-tab-edit-placement').click()
  const picker = (await page.getByTestId('placement-editor-derived-pattern').innerText()).trim()
  await page.getByTestId('placement-editor-cancel').click()
  const modes = CANONICAL_MODES.filter((m) => new RegExp(`(^|[^\\w-])${m}($|[^\\w-])`).test(picker))
  expect(modes, `the picker's mode: ${picker}`).toEqual([header])
})

test('55 shared-pg Topology tab renders EXACTLY ONE topology value, runtime-derived (derivedFromRuntime:true), and NO contradictory second value anywhere on the app screen, including the Overview Placement field', async ({ page }) => {
  const id = await pgTopology(page)
  const pattern = (await page.getByTestId('topology-tab-pattern').innerText()).trim()
  await expect(page.getByTestId('topology-tab-placement-source')).toHaveText('runtime-observed')
  const seen = async () => {
    const text = await page.locator('body').innerText()
    return CANONICAL_MODES.filter((m) => new RegExp(`(^|[^\\w-])${m}($|[^\\w-])`).test(text))
  }
  expect(await seen(), `${id}: the topology values on the Topology tab`).toEqual([pattern])
  await openApp(page, id, 'overview')
  await expect(page.getByTestId('app-detail-overview-placement')).toHaveText(pattern)
  expect(await seen(), `${id}: the topology values on the Overview tab`).toEqual([pattern])
})

test('56 shared-pg Topology tab per-region placement + replication block: region-a primary, region-b replica, and a live replication-lag in seconds (not a hardcoded —)', async ({ page }) => {
  const { a, b } = await regionNames()
  await pgTopology(page)
  const cards = await targetCards(page)
  expect(cards.map((c) => [regionOf(c.where), c.sub])).toEqual([[a, 'serves writes'], [b, 'live replica']])
  await expect(page.getByTestId('app-topology-tabpanel')).toContainText(LAG)
})

test('64 shared-pg Topology tab replication-lag field shows a live numeric seconds value (or explicit "no replica"), never a hardcoded —', async ({ page }) => {
  await pgTopology(page)
  await expect(page.getByTestId('app-topology-tabpanel')).toContainText(/replication[- ]lag\D{0,40}?(\d+(\.\d+)?\s*s\b|no replica)/i)
  await expect(page.getByTestId('app-topology-tabpanel')).not.toContainText(/replication[- ]lag\W*—/i)
})

test('71 Region-kill baseline (before): shared-pg Topology tab shows live Continuum Ready, lease held by region-a, region-b standby present, a live replication-lag number', async ({ page }) => {
  const { a, b } = await regionNames()
  await pgTopology(page)
  const panel = page.getByTestId('app-topology-tabpanel')
  await expect(panel).toContainText(/Continuum\W[^\n]*\bReady\b/)
  await expect(panel).toContainText(new RegExp(`lease[^\\n]*${a.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`, 'i'))
  const cards = await targetCards(page)
  expect(cards.some((c) => regionOf(c.where) === b && c.role.startsWith('○ STANDBY')), 'a standby in region b').toBe(true)
  await expect(panel).toContainText(LAG)
})
