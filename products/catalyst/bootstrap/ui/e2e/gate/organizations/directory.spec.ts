// Gate check FND-G22 (FND-164): the Organizations directory (/organizations), the Billing screen and the
// console bundle, read against the Organization objects of the candidate. One test per ledger row; the
// title is the row id and its clause. Nothing here writes to the Sovereign.
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { BANNED_WORDS, REGIONS, cond, getObj, listCluster, orgCRs, orgItems, slugOf, str, type Obj } from './lib.ts'

async function openDirectory(page: Page): Promise<void> {
  await page.goto(consoleURL('/organizations'))
  await expect(page.getByTestId('organizations-table'), 'the Organizations directory').toBeVisible({ timeout: 60_000 })
}

/** True when a rule of a ClusterRole grants a verb on a resource of a group. */
function grants(role: Obj, group: string, resource: string, verb: string): boolean {
  return (role.rules ?? []).some((r) => (r.apiGroups ?? []).some((g) => g === group || g === '*') &&
    (r.resources ?? []).some((x) => x === resource || x === '*') && (r.verbs ?? []).some((v) => v === verb || v === '*'))
}

test('R10 org-controller RBAC carries update/patch — per-Org provisioning completes (was create-only, never reached Ready).', async () => {
  const role = await getObj('a', 'clusterrole', '', 'catalyst-organization-controller')
  expect(role, 'the ClusterRole catalyst-organization-controller').not.toBeNull()
  const want: Array<[string, string[]]> = [['organizations', ['get', 'list', 'watch', 'update', 'patch']], ['organizations/status', ['get', 'update', 'patch']], ['organizations/finalizers', ['update']]]
  for (const [resource, verbs] of want) for (const v of verbs) expect(grants(role as Obj, 'orgs.openova.io', resource, v), `${resource}: ${v}`).toBe(true)
  const crs = await orgCRs()
  expect(crs.length, 'the candidate has Organizations').toBeGreaterThan(0)
  const done = crs.filter((o) => str(o, 'status.vcluster.phase') === 'Ready' || cond(o, 'Ready') === 'True')
  expect(done.map(slugOf), 'an Organization whose provisioning completed (vCluster Ready or Ready=True)').not.toEqual([])
})

test('R14 console Organizations directory reads the live Organization CRs (not an empty in-memory store).', async ({ page }) => {
  const crs = (await orgCRs()).map(slugOf).sort()
  expect(crs.length, 'the candidate has Organization objects').toBeGreaterThan(0)
  const items = await orgItems(page)
  // A deleted Organization keeps its audit row in the store (state deleted); every other row is an object.
  const live = items.filter((o) => o.state !== 'deleted').map((o) => o.subdomain).sort()
  for (const s of crs) expect(items.map((o) => o.subdomain), `GET /api/v1/organizations has a row for the Organization ${s}`).toContain(s)
  for (const s of live) expect(crs, `the live row ${s} is an Organization object`).toContain(s)
  await openDirectory(page)
  for (const s of crs) await expect(page.locator(`[data-testid^="organizations-cell-name-"][href$="/organizations/${s}"]`), `the directory row of ${s}`).toHaveCount(1)
  await expect(page.locator('[data-testid^="organizations-row-"][data-parent="false"]'), 'one directory row per row of the feed').toHaveCount(items.length)
})

test('106 Every Organization owns EXACTLY ONE host namespace labelled openova.io/organization=<slug> — org-labelled namespace count equals the count of Organization CRs, at every tier; no orphan namespace, no missing one.', async () => {
  let measured = 0
  for (const r of REGIONS) {
    const crs = (await listCluster(r, 'organizations.orgs.openova.io')).filter((o) => !o.metadata.deletionTimestamp)
    const labelled = (await listCluster(r, 'namespaces')).filter((n) => n.metadata.labels?.['openova.io/organization'])
    if (crs.length === 0) {
      expect(labelled.map((n) => n.metadata.name), `region ${r} holds no Organization, so no Organization namespace`).toEqual([])
      continue
    }
    measured += crs.length
    // Counted by the one label the clause names, as `labelled` is.
    for (const o of crs) expect(labelled.filter((n) => n.metadata.labels?.['openova.io/organization'] === slugOf(o)).length, `region ${r}: the namespaces of ${slugOf(o)} (tier ${str(o, 'spec.tier')})`).toBe(1)
    const slugs = new Set(crs.map(slugOf))
    for (const n of labelled) expect(slugs.has(n.metadata.labels?.['openova.io/organization'] ?? ''), `region ${r}: the namespace ${n.metadata.name} belongs to an Organization`).toBe(true)
    expect(labelled.length, `region ${r}: Organization namespaces against Organizations`).toBe(crs.length)
  }
  expect(measured, 'the candidate has Organizations').toBeGreaterThan(0)
})

test('116 Organizations directory: the page title / heading reads "Organizations", never "Tenants"/"SME Tenants".', async ({ page }) => {
  await openDirectory(page)
  await expect(page.getByTestId('portal-header-title')).toHaveText('Organizations')
  expect(await page.getByTestId('sov-portal-shell').innerText(), 'no banned word on the page').not.toMatch(BANNED_WORDS)
})

test('121 BSS / billing screen: billing is framed as "This organization is in showback mode…", zero "tenant"/"SME" leaks.', async ({ page }) => {
  // The mode the screen states is the parent Organization's, as the directory shows it.
  await openDirectory(page)
  const mode = await page.locator('[data-testid^="organizations-row-"][data-parent="true"] [data-testid^="organizations-cell-billing-"]').getAttribute('data-mode')
  expect(mode, 'the billing mode of the parent Organization').toBe('showback')
  for (const path of ['/billing/revenue', '/billing/orders']) {
    await page.goto(consoleURL(path))
    const notice = page.getByTestId('billing-showback-notice')
    await expect(notice, `${path}: the showback framing`).toBeVisible({ timeout: 60_000 })
    expect((await notice.innerText()).replace(/\s+/g, ' '), path).toContain('This organization is in showback mode')
    await expect(page.getByTestId('billing-mode-gate')).toHaveAttribute('data-mode', 'showback')
    expect(await page.locator('body').innerText(), `${path}: no banned word`).not.toMatch(BANNED_WORDS)
  }
})

test('122 Legacy /bss/tenants URL (PR #3390 alias) resolves and redirects to /organizations, rendering the directory (H1 "Organizations") — not a 404, not a login redirect.', async ({ page }) => {
  const r = await page.goto(consoleURL('/bss/tenants'))
  expect(r?.status(), 'GET /bss/tenants').toBe(200)
  await expect(page).toHaveURL(/\/organizations$/, { timeout: 60_000 })
  await expect(page.getByTestId('organizations-directory-page')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('portal-header-title')).toHaveText('Organizations')
  await expect(page).not.toHaveURL(/\/login/)
})

test('214 No SME/tenant banned-term leak in the console bundle (org-rename complete).', async ({ request }) => {
  const html = await (await request.get(consoleURL('/'))).text()
  const assets = [...html.matchAll(/(?:src|href)="(\/[^"]+\.js)"/g)].map((m) => m[1])
  expect(assets.length, 'the console page loads its bundle').toBeGreaterThan(0)
  const display: string[] = []
  let organization = 0
  for (const a of new Set(assets)) {
    const r = await request.get(consoleURL(a))
    expect(r.status(), a).toBe(200)
    const js = await r.text()
    for (const m of js.matchAll(/"((?:[^"\\\n]|\\.){1,400})"|'((?:[^'\\\n]|\\.){1,400})'|`((?:[^`\\]|\\.){1,400})`/g)) {
      const s = m[1] ?? m[2] ?? m[3] ?? ''
      if (/\bOrganization/.test(s)) organization++
      // A lower-case identifier (an API field, a chart name, the legacy /bss/tenants alias) is not text a User reads.
      if (BANNED_WORDS.test(s) && !/^[a-z0-9_./:#?=&-]+$/.test(s)) display.push(s)
    }
  }
  expect(display.slice(0, 10), 'display strings of the bundle with a banned word').toEqual([])
  expect(organization, 'display strings that say Organization').toBeGreaterThan(0)
})
