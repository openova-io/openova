// Gate check FND-G20 (FND-162): the catalogue grid and a Blueprint's catalogue page. One test per ledger
// row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { CANONICAL_MODES, RUN_TOKEN, applications, catalogItems, catalogNameOf, editorYaml, kube, list, openCatalog, str } from './lib.ts'

const grid = '[data-testid="sov-catalog-grid"]'
const cards = `${grid} [data-testid^="sov-app-card-"]`

async function openGrid(page: Page): Promise<void> {
  await page.goto('/catalog')
  await expect(page.locator(cards).first()).toBeVisible()
}

function alloyCard(page: Page) {
  return page.locator(cards).filter({ has: page.locator('.app-name', { hasText: /^Alloy$/ }) })
}

async function openNewInstance(page: Page): Promise<void> {
  await openCatalog(page, await catalogNameOf(page, 'PostgreSQL', 'bp-postgres'))
  await page.getByTestId('btn-new-instance').click()
  await expect(page.getByTestId('dialog-new-instance')).toBeVisible()
  await expect(page.getByTestId('select-instance-topology')).toBeEnabled()
}

async function topologyOptions(page: Page): Promise<Array<{ value: string; label: string; disabled: boolean }>> {
  return page.getByTestId('select-instance-topology').locator('option').evaluateAll((els) =>
    els.map((e) => ({ value: (e as HTMLOptionElement).value, label: (e.textContent ?? '').trim(), disabled: (e as HTMLOptionElement).disabled })),
  )
}

/** The testids of the edit chrome of a catalogue page: the per-field editors and the Edit IaC button. */
async function editChrome(page: Page): Promise<string[]> {
  return page.locator('[data-testid^="cif-"][data-testid$="-edit"], [data-testid="catalog-detail-edit-iac"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? '').sort())
}

test('13 Catalog grid renders the Blueprint cards; bp-postgres detail has a + New instance button', async ({ page }) => {
  const names = new Set((await catalogItems(page)).map((i) => i.name))
  await openGrid(page)
  const ids = await page.locator(cards).evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace(/^sov-app-card-/, '')))
  expect(ids.length, 'Blueprint cards on the grid').toBeGreaterThan(0)
  for (const id of ids) expect(names.has(id), `the card ${id} is a Blueprint of the catalogue`).toBe(true)
  await openCatalog(page, await catalogNameOf(page, 'PostgreSQL', 'bp-postgres'))
  await expect(page.getByTestId('btn-new-instance')).toHaveText(/\+\s*New instance/)
})

test('124 Catalog grid renders Blueprint cards in a tile grid, each with an icon + summary; the Alloy card is visible', async ({ page }) => {
  await openGrid(page)
  const tiles = await page.locator(cards).evaluateAll((els) =>
    els.map((e) => ({
      id: e.getAttribute('data-testid') ?? '',
      icon: e.querySelector('img.app-logo, .app-icon') !== null,
      summary: (e.querySelector('.app-desc')?.textContent ?? '').trim(),
    })),
  )
  expect(tiles.length).toBeGreaterThan(0)
  expect(tiles.filter((t) => !t.icon).map((t) => t.id), 'cards without an icon').toEqual([])
  expect(tiles.filter((t) => t.summary === '').map((t) => t.id), 'cards without a summary').toEqual([])
  await expect(alloyCard(page)).toBeVisible()
})

test('125 Click the Alloy card: the detail page renders a hero (icon + name + summary + Edit IaC), an About section and an Instances list; no login redirect', async ({ page }) => {
  await openGrid(page)
  await alloyCard(page).click()
  await expect(page).toHaveURL(/\/catalog\/[^/]*alloy/)
  expect(page.url()).not.toMatch(/login|\/auth\//)
  const hero = page.getByTestId('catalog-hero')
  await expect(hero.locator('img.hero-logo')).toBeVisible()
  await expect(page.getByTestId('catalog-title')).toHaveText('Alloy')
  await expect(page.getByTestId('catalog-summary')).not.toHaveText('')
  await expect(page.getByTestId('catalog-detail-edit-iac')).toBeVisible()
  await expect(page.getByTestId('catalog-section-about')).toBeVisible()
  await expect(page.locator('[data-testid="sov-section-instances"], [data-testid="catalog-section-platform-singleton"]').first()).toBeVisible()
})

test('46 bp-postgres catalog detail renders; click New instance and the create dialog opens with a topology <select>', async ({ page }) => {
  await openNewInstance(page)
  await expect(page.getByTestId('dialog-new-instance').locator('select[data-testid="select-instance-topology"]')).toHaveCount(1)
})

test('47 Topology <select> options read exactly the ONE canonical vocabulary: singleton, active-passive, active-hot-standby, active-active', async ({ page }) => {
  await openNewInstance(page)
  expect((await topologyOptions(page)).map((o) => o.value)).toEqual(CANONICAL_MODES)
})

test('48 active-passive is a selectable option in the create <select> (not folded away)', async ({ page }) => {
  await openNewInstance(page)
  const ap = (await topologyOptions(page)).find((o) => o.value === 'active-passive')
  expect(ap, 'the option active-passive').toBeTruthy()
  expect(ap?.disabled, 'active-passive can be selected').toBe(false)
  await page.getByTestId('select-instance-topology').selectOption('active-passive')
  await expect(page.getByTestId('select-instance-topology')).toHaveValue('active-passive')
})

test('49 singleton is a separate selectable option (single-region single-instance), distinct from the multi-region modes', async ({ page }) => {
  await openNewInstance(page)
  const opts = await topologyOptions(page)
  const single = opts.find((o) => o.value === 'singleton')
  expect(single?.disabled, 'singleton can be selected').toBe(false)
  for (const o of opts.filter((x) => x.value !== 'singleton')) expect(o.label, `${o.value} reads differently from singleton`).not.toBe(single?.label)
  await page.getByTestId('select-instance-topology').selectOption('singleton')
  await expect(page.getByTestId('select-instance-topology')).toHaveValue('singleton')
})

test('50 Pick active-hot-standby, name the instance, Provision: the create succeeds (redirect to the new app), NOT a red topology not in supported error', async ({ page }) => {
  test.setTimeout(300_000)
  const name = `gate-ahs-${RUN_TOKEN}`.toLowerCase().slice(0, 40)
  await openNewInstance(page)
  try {
    await page.getByTestId('select-instance-topology').selectOption('active-hot-standby')
    await page.getByTestId('input-instance-name').fill(name)
    const boxes = page.locator('[data-testid^="region-checkbox-"]')
    for (let i = 0; i < (await boxes.count()); i++) await boxes.nth(i).check()
    await page.getByTestId('btn-submit-instance').click()
    await expect(page.getByTestId('dialog-error')).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(/topology not in supported/i)
    await expect(page.getByTestId('dialog-new-instance')).toHaveCount(0, { timeout: 60_000 })
    await expect
      .poll(async () => (await applications('a')).some((a) => a.metadata.name === name), { timeout: 120_000, message: `an Application ${name} exists` })
      .toBe(true)
    const crs = (await applications('a')).filter((a) => a.metadata.name === name)
    expect(crs.map((a) => str(a, 'spec.placement') || str(a, 'spec.placement.mode')), 'the placement it was created with').toEqual(['active-hot-standby'])
  } finally {
    for (const a of (await applications('a')).filter((x) => x.metadata.name === name)) {
      await kube('a', ['delete', 'applications.apps.openova.io', '-n', a.metadata.namespace ?? '', name, '--wait=false'])
    }
  }
})

test('130 The non-card version field renders as a v<version> chip in the hero, and Edit IaC exposes the full CR including version for editing', async ({ page }) => {
  const name = await catalogNameOf(page, 'Alloy', 'bp-alloy')
  const item = (await catalogItems(page)).find((i) => i.name === name)
  await openCatalog(page, name)
  await expect(page.getByTestId('catalog-version')).toHaveText(`v${item?.version}`)
  await page.getByTestId('catalog-detail-edit-iac').click()
  const yaml = await editorYaml(page)
  expect(yaml).toMatch(new RegExp(`^\\s+version:\\s*['"]?${(item?.version ?? '').replace(/\./g, '\\.')}['"]?\\s*$`, 'm'))
})

test('144 Click Edit IaC (admin only): the full blueprint.yaml opens in the YAML editor (the entire CR, not just 7 card fields)', async ({ page, signIn }) => {
  const name = await catalogNameOf(page, 'Alloy', 'bp-alloy')
  await openCatalog(page, name)
  await page.getByTestId('catalog-detail-edit-iac').click()
  const yaml = await editorYaml(page)
  for (const key of [/^apiVersion:/m, /^kind:\s*Blueprint\s*$/m, /^metadata:/m, /^spec:/m, /^\s+card:/m, /^\s+version:/m]) expect(yaml).toMatch(key)
  expect(yaml.split('\n').length, 'more than the card fields').toBeGreaterThan(20)
  const viewer = await (await signIn('viewer')).newPage()
  await openCatalog(viewer, name)
  await expect(viewer.getByTestId('catalog-detail-edit-iac'), 'Edit IaC is for admins only').toHaveCount(0)
})

test('146 The editor subtitle states it directly: Commit writes the IaC source of truth; Flux reconciles it. Both this editor and the card form above write the same file.', async ({ page }) => {
  await openCatalog(page, await catalogNameOf(page, 'Alloy', 'bp-alloy'))
  await page.getByTestId('catalog-detail-edit-iac').click()
  const hint = (await page.getByTestId('catalog-edit-iac-section').locator('.section-hint').innerText()).replace(/\s+/g, ' ')
  expect(hint).toContain('Commit writes the IaC source of truth; Flux reconciles it')
  expect(hint).toContain('Both this editor and the card form above write the same file.')
})

test('150 PostgreSQL detail renders the SAME edit surface (hero icon, Edit IaC, clickable cards) and Edit IaC exposes contextSchema', async ({ page }) => {
  await openCatalog(page, await catalogNameOf(page, 'PostgreSQL', 'bp-postgres'))
  for (const id of ['cif-icon-edit', 'cif-name-edit', 'cif-summary-edit', 'catalog-detail-edit-iac']) await expect(page.getByTestId(id)).toBeVisible()
  await page.getByTestId('catalog-detail-edit-iac').click()
  expect(await editorYaml(page)).toMatch(/^\s+contextSchema:/m)
})

test('151 Alloy + Postgres render the IDENTICAL edit chrome (same cif-* editors, same Edit-IaC YamlEditor): no blueprint-specific UI', async ({ page }) => {
  const chrome: string[][] = []
  for (const [title, name] of [['Alloy', 'bp-alloy'], ['PostgreSQL', 'bp-postgres']]) {
    await openCatalog(page, await catalogNameOf(page, title, name))
    const fields = await editChrome(page)
    await page.getByTestId('catalog-detail-edit-iac').click()
    const editor = await page.getByTestId('catalog-edit-iac-section').locator('[data-testid^="yaml-editor"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? '').sort())
    chrome.push([...fields, ...editor])
  }
  expect(chrome[0].length).toBeGreaterThan(0)
  expect(chrome[1], 'the edit chrome of PostgreSQL and of Alloy').toEqual(chrome[0])
})

test('152 The catalog detail page renders (hero · About · Instances) and opens an INLINE Edit form (no modal): acceptance headline 1', async ({ page }) => {
  await openCatalog(page, await catalogNameOf(page, 'Alloy', 'bp-alloy'))
  await expect(page.getByTestId('catalog-section-about')).toBeVisible()
  await expect(page.locator('[data-testid="sov-section-instances"], [data-testid="catalog-section-platform-singleton"]').first()).toBeVisible()
  await page.getByTestId('cif-summary-edit').click()
  await expect(page.getByTestId('catalog-hero').getByTestId('cif-summary-editor')).toBeVisible()
  await expect(page.locator('[role="dialog"]:visible, dialog[open]')).toHaveCount(0)
  await page.getByTestId('cif-summary-cancel').click()
})

test('218 chepherd is installable from the catalog as an Application (bp-agenity): the card renders and the install wizard opens with the Org Environment pre-selected', async ({ page }) => {
  await openGrid(page)
  const name = await catalogNameOf(page, 'Agenity', 'bp-agenity')
  await expect(page.getByTestId(`sov-app-card-${name}`)).toBeVisible()
  await page.getByTestId(`sov-app-card-${name}`).click()
  await expect(page.getByTestId('catalog-hero')).toBeVisible()
  await page.getByTestId('btn-new-instance').click()
  await expect(page.getByTestId('dialog-new-instance')).toBeVisible()
  const org = await page.getByTestId('select-instance-org').inputValue()
  expect(org, 'an Organization is selected').not.toBe('')
  await expect(page.getByTestId('select-instance-environment')).toHaveValue(new RegExp(`^${org}-`))
  await page.getByTestId('btn-cancel-instance').click()
})

test('R21 catalog-seed pins match the published ghcr chart versions: no inert lagging Blueprint CR', async ({ request }) => {
  test.setTimeout(600_000)
  const bps = await list('a', 'blueprints.catalyst.openova.io')
  expect(bps.length, 'the Sovereign has Blueprint CRs').toBeGreaterThan(0)
  const missing: string[] = []
  for (const bp of bps) {
    const version = str(bp, 'spec.version')
    const chart = str(bp, 'spec.manifests.chart') || bp.metadata.name
    if (version === '') {
      missing.push(`${bp.metadata.name}: no spec.version`)
      continue
    }
    const repo = `openova-io/${chart}`
    const tok = await request.get(`https://ghcr.io/token?scope=repository:${repo}:pull`)
    const token = tok.ok() ? ((await tok.json()) as { token?: string }).token ?? '' : ''
    const m = await request.head(`https://ghcr.io/v2/${repo}/manifests/${version}`, {
      headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.oci.image.manifest.v1+json' },
    })
    if (m.status() !== 200) missing.push(`${bp.metadata.name}: ghcr.io/${repo}:${version} answers ${m.status()}`)
  }
  expect(missing, 'Blueprint CRs whose version is not a published chart').toEqual([])
})
