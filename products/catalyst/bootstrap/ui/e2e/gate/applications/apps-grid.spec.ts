// Gate check FND-G20 (FND-162): the Applications grid. One test per ledger row; the title is the row id
// and its clause.
import { test, expect } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { CANONICAL_MODES, apiJSON, applications, field, openApp, sovereignApps, str } from './lib.ts'

const instanceCards = '[data-testid="sov-apps-grid"] [data-card-kind="instance"]'
const catalogCards = '[data-testid="sov-apps-grid"] [data-card-kind="catalog"]'

async function cardIds(page: Page, selector: string): Promise<string[]> {
  await page.goto('/apps')
  await expect(page.getByTestId('sov-apps-grid')).toBeVisible()
  // The grid fills from the live query: wait until it holds at least one card.
  await expect(page.locator(`${instanceCards}, ${catalogCards}`).first()).toBeVisible()
  const ids = await page.locator(selector).evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace(/^sov-app-card-/, '')))
  return ids
}

test('15 Apps list renders one card per Application; a customer-launched app card appears under its Org', async ({ page }) => {
  const crs = await applications('a')
  const customer = crs.filter((a) => str(a, 'spec.organizationRef') !== '' && a.metadata.labels?.['apps.openova.io/bootstrap-owned'] !== 'true')
  expect(customer.length, 'the Sovereign has a customer-launched Application (spec.organizationRef set)').toBeGreaterThan(0)
  const ids = await cardIds(page, instanceCards)
  for (const a of customer) {
    expect(ids.filter((id) => id === a.metadata.name), `one card for Application ${a.metadata.name}`).toHaveLength(1)
    await expect(page.getByTestId(`sov-app-org-${a.metadata.name}`)).toHaveAttribute('data-org', str(a, 'spec.organizationRef'))
  }
})

test('19 Count the ESTATE cards on /apps: exactly one per Application CR, two Applications of one Blueprint render two cards, catalog slots are deduped, bootstrap apps carry a Bootstrap badge', async ({ page }) => {
  // One name per Application CR, not deduplicated: one name in two Organizations' namespaces is two CRs.
  const crNames = (await applications('a')).map((a) => a.metadata.name).sort()
  expect(crNames.length, 'the Sovereign has Application CRs').toBeGreaterThan(0)
  const ids = await cardIds(page, instanceCards)
  // Exactly one estate card per Application CR: not one per HelmRelease or Pod, and not one card for two CRs.
  expect([...ids].sort(), 'the estate cards are the Application CRs, one card each').toEqual(crNames)
  // Two Applications sharing one Blueprint render two cards.
  const rows = (await sovereignApps(page)).filter((r) => r.instance === true && crNames.includes(r.id))
  const byBlueprint = new Map<string, string[]>()
  for (const r of rows) byBlueprint.set(r.blueprint ?? '', [...(byBlueprint.get(r.blueprint ?? '') ?? []), r.id])
  const shared = [...byBlueprint.entries()].find(([bp, names]) => bp !== '' && names.length >= 2)
  expect(shared, 'two Applications share one Blueprint').toBeTruthy()
  for (const name of shared?.[1] ?? []) expect(ids, `the card of ${name} (Blueprint ${shared?.[0]})`).toContain(name)
  // The catalog half: Blueprint slots, none for a Blueprint that already has a running Application.
  const slots = await page.locator(catalogCards).evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace(/^sov-app-card-/, '')))
  const running = new Set(rows.map((r) => r.blueprint))
  for (const slot of slots) expect(running.has(slot), `catalog slot ${slot} has no running Application`).toBe(false)
  // Bootstrap apps carry the Platform/Bootstrap badge.
  const bootstrap = rows.filter((x) => x.bootstrapKit === true && ids.includes(x.id))
  expect(bootstrap.length, 'estate cards of bootstrap apps (bootstrapKit of GET /api/v1/sovereign/apps)').toBeGreaterThan(0)
  for (const r of bootstrap) {
    await expect(page.getByTestId(`sov-app-card-${r.id}`).locator('.app-chips'), `bootstrap badge on ${r.id}`).toContainText('BOOTSTRAP')
  }
})

test('205 /fleet/applications returns the real app count (non-zero), not 0', async ({ page }) => {
  const body = await apiJSON<{ total?: number; items?: unknown[] }>(page, '/api/v1/fleet/applications')
  const crs = new Set((await applications('a')).map((a) => `${a.metadata.namespace}/${a.metadata.name}`))
  expect(crs.size, 'the Sovereign has Application CRs').toBeGreaterThan(0)
  expect(body.total, 'total').toBeGreaterThan(0)
  expect(body.items ?? [], 'items').toHaveLength(body.total ?? -1)
  expect(body.total, 'the count of the Application CRs of the Sovereign').toBe(crs.size)
})

test('105 A per-app placement detail matches the treemap block the app renders in: the two surfaces cannot disagree', async ({ page }) => {
  interface Cell { name: string; children?: Cell[] }
  const tm = await apiJSON<{ items?: Cell[] }>(page, '/api/v1/dashboard/treemap?group_by=region,application&color_by=health&size_by=cpu_request')
  const regionsOf = new Map<string, Set<string>>()
  for (const region of tm.items ?? []) {
    for (const app of region.children ?? []) {
      regionsOf.set(app.name, new Set([...(regionsOf.get(app.name) ?? []), region.name]))
    }
  }
  const apps = (await sovereignApps(page)).filter((a) => regionsOf.has(a.id))
  expect(apps.length, 'applications of the console that the treemap renders in a region block').toBeGreaterThan(0)
  // The multi-region ones first: they are where the two surfaces can disagree.
  apps.sort((x, y) => (regionsOf.get(y.id)?.size ?? 0) - (regionsOf.get(x.id)?.size ?? 0))
  for (const a of apps.slice(0, 4)) {
    await openApp(page, a.id)
    const chips = await page.locator('[data-testid^="app-detail-overview-region-"]').allInnerTexts()
    expect(chips.map((c) => c.trim()).sort(), `regions of ${a.id}: detail page and treemap`).toEqual([...(regionsOf.get(a.id) ?? [])].sort())
  }
})

test('G10 Placement EPIC acceptance: EVERY Application CR carries a non-empty spec.placement drawn from the canonical set; the placed count equals the Application CR count', async () => {
  const crs = await applications('a')
  expect(crs.length, 'the Sovereign has Application CRs').toBeGreaterThan(0)
  const placed = crs.filter((a) => {
    const p = field(a, 'spec.placement')
    const mode = typeof p === 'string' ? p : str(a, 'spec.placement.mode')
    return CANONICAL_MODES.includes(mode)
  })
  const unplaced = crs.filter((a) => !placed.includes(a)).map((a) => `${a.metadata.namespace}/${a.metadata.name}`)
  expect(unplaced, 'Application CRs without a canonical spec.placement').toEqual([])
  expect(placed.length).toBe(crs.length)
})
