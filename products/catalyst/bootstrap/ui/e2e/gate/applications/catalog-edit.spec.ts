// Gate check FND-G20 (FND-162): editing a Blueprint from its catalogue page. One test per ledger row; the
// title is the row id and its clause. The rows are steps of one walk, so each group runs in order and puts
// back what it changed (the summary, the name, the icon, the committed blueprint.yaml).
import { test, expect, consoleURL, stateOf } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { JSON_SCHEMA, dump, load } from 'js-yaml'
import { RUN_TOKEN, catalogNameOf, editorYaml, openCatalog, setEditorYaml } from './lib.ts'

const PROOF = `RECONCILE-PROOF-${RUN_TOKEN}`
// Each save commits to the IaC repository; a group of rows waits on several of them.
test.describe.configure({ timeout: 600_000 })
const cards = '[data-testid="sov-catalog-grid"] [data-testid^="sov-app-card-"]'

function gridCard(page: Page, title: string) {
  return page.locator(cards).filter({ has: page.locator('.app-name', { hasText: new RegExp(`^${title}$`) }) })
}

async function openGrid(page: Page): Promise<void> {
  await page.goto('/catalog')
  await expect(page.locator(cards).first()).toBeVisible()
}

/** Opens a field's inline editor, writes the value and saves it; returns the save verdict it showed. */
async function saveInline(page: Page, key: 'summary' | 'name', value: string): Promise<string> {
  await page.getByTestId(`cif-${key}-edit`).click()
  await page.getByTestId(`cif-${key}-input`).fill(value)
  await page.getByTestId(`cif-${key}-save`).click()
  await expect(page.getByTestId(`cif-${key}-editor`)).toHaveCount(0)
  const verdict = page.getByTestId(`cif-${key}-save-verdict`)
  await expect(verdict).toBeVisible()
  return (await verdict.innerText()).trim()
}

/** Opens the icon editor, sets the light-theme icon and saves it; returns the save verdict. */
async function saveLightIcon(page: Page, src: string): Promise<string> {
  await page.getByTestId('cif-icon-edit').click()
  await page.getByTestId('iconpicker-light-url').fill(src)
  await page.getByTestId('cif-icon-save').click()
  await expect(page.getByTestId('cif-icon-editor')).toHaveCount(0)
  const verdict = page.getByTestId('cif-icon-save-verdict')
  await expect(verdict).toBeVisible()
  return (await verdict.innerText()).trim()
}

/** The light-theme icon value the icon editor opens with. */
async function lightIconValue(page: Page): Promise<string> {
  await page.getByTestId('cif-icon-edit').click()
  const v = await page.getByTestId('iconpicker-light-url').inputValue()
  await page.getByTestId('cif-icon-cancel').click()
  return v
}

const heroLogo = (page: Page) => page.getByTestId('catalog-hero').locator('img.hero-logo')

/** A distinct image: an SVG data URI that carries the token of this run. */
function distinctImage(tag: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#c026d3"/><text x="4" y="36" font-size="9">${tag}</text></svg>`
  return `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`
}

/** Commits a whole blueprint.yaml through the writer the Edit IaC editor uses (the put-back of a group). */
async function commitIaC(page: Page, name: string, yaml: string): Promise<void> {
  const r = await page.request.put(consoleURL(`/api/v1/catalog/${encodeURIComponent(name)}/iac`), { data: { blueprintYaml: yaml } })
  expect(r.status(), `PUT /api/v1/catalog/${name}/iac`).toBe(200)
}

/** GET /api/v1/catalog/<name>/iac: the blueprint.yaml the Edit IaC editor opens and the truth it came from. */
async function iacSeed(page: Page, name: string): Promise<{ yaml: string; source: string }> {
  const r = await page.request.get(consoleURL(`/api/v1/catalog/${encodeURIComponent(name)}/iac`))
  expect(r.status(), `GET /api/v1/catalog/${name}/iac`).toBe(200)
  const body = (await r.json()) as { blueprintYaml?: string; source?: string }
  expect(typeof body.blueprintYaml === 'string' && body.blueprintYaml !== '', `GET /api/v1/catalog/${name}/iac holds a blueprint.yaml`).toBe(true)
  return { yaml: body.blueprintYaml as string, source: String(body.source) }
}

async function committedIaC(page: Page, name: string): Promise<string> {
  return (await iacSeed(page, name)).yaml
}

test.describe.serial('Alloy: the summary and the name', () => {
  let alloy = ''
  // The blueprint.yaml before the first edit; null until beforeAll read it, so no edit ran. The put-back
  // commits it again: the summary field writes card.tagline, which Alloy's committed file does not carry.
  let original: string | null = null
  let current = ''

  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/') })
    const page = await ctx.newPage()
    alloy = await catalogNameOf(page, 'Alloy', 'bp-alloy')
    original = await committedIaC(page, alloy)
    await ctx.close()
  })

  test.afterAll(async ({ browser }) => {
    if (original === null) return
    const ctx = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/') })
    const page = await ctx.newPage()
    if ((await committedIaC(page, alloy)) !== original) await commitIaC(page, alloy, original)
    await ctx.close()
  })

  test('126 Click the admin Edit affordance in the hero: an edit form drops INLINE into the detail page (no modal overlay); fields appear in-place under the hero', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('cif-summary-edit').click()
    await expect(page.getByTestId('catalog-hero').getByTestId('cif-summary-editor')).toBeVisible()
    await expect(page.getByTestId('catalog-hero').getByTestId('cif-summary-input')).toBeVisible()
    await expect(page.locator('[role="dialog"]:visible, dialog[open]')).toHaveCount(0)
    await page.getByTestId('cif-summary-cancel').click()
  })

  test('127 In the inline form, change Summary to RECONCILE-PROOF-<ts>, Save: the page refreshes in place and the new summary shows in the hero', async ({ page }) => {
    await openCatalog(page, alloy)
    const url = page.url()
    current = PROOF
    await saveInline(page, 'summary', current)
    expect(page.url(), 'the same page, no navigation').toBe(url)
    await expect(page.getByTestId('catalog-summary')).toHaveText(current)
  })

  test('128 Back on the grid, the Alloy card summary now reads RECONCILE-PROOF-<ts>: the edit propagated to the card, not just the detail page', async ({ page }) => {
    await openGrid(page)
    await expect(gridCard(page, 'Alloy').locator('.app-desc')).toHaveText(current)
  })

  test('129 Hard-reload the detail page: the summary is still RECONCILE-PROOF-<ts>; the edit persisted across a reload, not an in-memory overlay', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.reload()
    await expect(page.getByTestId('catalog-summary')).toHaveText(current)
  })

  test('140 On Save, the durable-commit verdict (git outcome) is surfaced in-UI, not a silent store success', async ({ page }) => {
    await openCatalog(page, alloy)
    current = `${PROOF}-b`
    expect(await saveInline(page, 'summary', current), 'the save verdict').toBe('Saved to IaC ✓')
  })

  test('141 Hover the summary line in the hero: a pencil/edit affordance appears ON the field (cif-summary-edit -> cif-summary-input), inline, without opening the full form', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('cif-summary-edit').hover()
    await expect(page.getByTestId('cif-summary-edit').locator('.cif-pencil')).toBeVisible()
    await page.getByTestId('cif-summary-edit').click()
    await expect(page.getByTestId('catalog-hero').getByTestId('cif-summary-input')).toBeVisible()
    await expect(page.getByTestId('catalog-edit-iac-section')).toHaveCount(0)
    await page.getByTestId('cif-summary-cancel').click()
  })

  test('142 Click the summary, type a value, Save: only the summary updates in place; no full-form modal opens', async ({ page }) => {
    await openCatalog(page, alloy)
    const title = await page.getByTestId('catalog-title').innerText()
    const version = await page.getByTestId('catalog-version').innerText()
    current = `${PROOF}-c`
    await saveInline(page, 'summary', current)
    await expect(page.locator('[role="dialog"]:visible, dialog[open]')).toHaveCount(0)
    await expect(page.getByTestId('catalog-summary')).toHaveText(current)
    await expect(page.getByTestId('catalog-title')).toHaveText(title)
    await expect(page.getByTestId('catalog-version')).toHaveText(version)
  })

  test('143 Repeat the inline edit for the name field (cif-name-edit -> cif-name-input): it edits in place and saves just that field', async ({ page }) => {
    await openCatalog(page, alloy)
    const name = await page.getByTestId('catalog-title').innerText()
    try {
      await saveInline(page, 'name', `${name}-NAMEPROOF-${RUN_TOKEN}`)
      await expect(page.getByTestId('catalog-title')).toHaveText(`${name}-NAMEPROOF-${RUN_TOKEN}`)
      await expect(page.getByTestId('catalog-summary')).toHaveText(current)
      await page.reload()
      await expect(page.getByTestId('catalog-title')).toHaveText(`${name}-NAMEPROOF-${RUN_TOKEN}`)
    } finally {
      await openCatalog(page, alloy)
      if ((await page.getByTestId('catalog-title').innerText()) !== name) await saveInline(page, 'name', name)
    }
  })

  test('153 A summary edit Saves, updates the page AND the grid card, and persists across a reload: acceptance headline 2', async ({ page }) => {
    await openCatalog(page, alloy)
    current = `${PROOF}-d`
    await saveInline(page, 'summary', current)
    await expect(page.getByTestId('catalog-summary')).toHaveText(current)
    await openGrid(page)
    await expect(gridCard(page, 'Alloy').locator('.app-desc')).toHaveText(current)
    await openCatalog(page, alloy)
    await page.reload()
    await expect(page.getByTestId('catalog-summary')).toHaveText(current)
  })

  test("156 Save's verdict is backed by a REAL commit: resolvable in the IaC repo with matching file content within one reconcile", async ({ page }) => {
    // source "committed": the file read from the IaC repository, not the live CR served when nothing is committed.
    await expect.poll(async () => { const s = await iacSeed(page, alloy); return `${s.source} ${s.yaml.includes(current)}` }, { timeout: 300_000, message: `the committed blueprint.yaml of ${alloy} holds ${current}` }).toBe('committed true')
  })

  test('157 Per-field inline edit for cards (cif-*) + the full-CR YamlEditor (Edit IaC) for the rest, both writing the same IaC source: acceptance headline 6', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('catalog-detail-edit-iac').click()
    expect(await editorYaml(page), 'the Edit IaC editor shows the summary the card field wrote').toContain(current)
  })
})

test.describe.serial('Alloy: the icon', () => {
  let alloy = ''
  // The committed blueprint.yaml before the icon edits: the put-back commits it again, so an icon that IaC
  // did not carry is not written back as the bundled asset the icon editor pre-fills (row 136).
  let original = ''
  const image = distinctImage(RUN_TOKEN)

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('oo-theme', 'light'))
  })

  test.afterAll(async ({ browser }) => {
    if (alloy === '' || original === '') return
    const ctx = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/') })
    const page = await ctx.newPage()
    if ((await committedIaC(page, alloy)) !== original) await commitIaC(page, alloy, original)
    await ctx.close()
  })

  test('131 Note the current hero logo (the Alloy glyph) as the baseline before an icon edit', async ({ page }) => {
    alloy = await catalogNameOf(page, 'Alloy', 'bp-alloy')
    await openCatalog(page, alloy)
    await expect(heroLogo(page)).toHaveAttribute('src', /alloy/i)
    original = await committedIaC(page, alloy)
  })

  test('132 Click Edit, in the Light-theme icon field paste a distinct image, Save: a "Saved to IaC ✓" confirmation shows and the page refreshes', async ({ page }) => {
    await openCatalog(page, alloy)
    expect(await saveLightIcon(page, image)).toBe('Saved to IaC ✓')
  })

  test('133 Observe the hero: it now shows the new logo; the render reads the edited card.iconLight first (IaC-first), not the bundled asset', async ({ page }) => {
    await openCatalog(page, alloy)
    await expect(heroLogo(page)).toHaveAttribute('src', image)
  })

  test('134 Return to the grid: the Alloy card icon is now the new logo (the grid tile resolves the same edited card.iconLight)', async ({ page }) => {
    await openGrid(page)
    await expect(gridCard(page, 'Alloy').locator('img.app-logo')).toHaveAttribute('src', image)
  })

  test('135 Reload the detail page: the hero is still the new logo (the render reads the persisted IaC icon on every load)', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.reload()
    await expect(heroLogo(page)).toHaveAttribute('src', image)
  })

  test('136 Click Edit again: the Light-theme icon field shows the current IaC value, falling back to the bundled asset only when IaC carries none', async ({ page }) => {
    await openCatalog(page, alloy)
    expect(await lightIconValue(page)).toBe(image)
  })

  test('137 Click Edit, click the icon picker (iconpicker-*): a thumbnail grid of vendored component-logos/* assets opens (a role=listbox grid of logo tiles)', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('cif-icon-edit').click()
    const gallery = page.getByTestId('iconpicker-light-grid')
    await expect(gallery).toHaveAttribute('role', 'listbox')
    const srcs = await gallery.getByRole('option').locator('img').evaluateAll((els) => els.map((e) => e.getAttribute('src') ?? ''))
    expect(srcs.length, 'logo tiles').toBeGreaterThan(1)
    expect(srcs.filter((s) => !/component-logos\//.test(s)), 'tiles that are not vendored component logos').toEqual([])
    await page.getByTestId('cif-icon-cancel').click()
  })

  test('138 Click cilium.svg in the picker grid: the icon field and a live preview swatch update to the Cilium logo', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('cif-icon-edit').click()
    await page.getByTestId('iconpicker-light-tile-cilium').click()
    await expect(page.getByTestId('iconpicker-light-url')).toHaveValue(/cilium\.svg$/)
    await expect(page.getByTestId('iconpicker-light-preview-img')).toHaveAttribute('src', /cilium\.svg$/)
    await page.getByTestId('cif-icon-cancel').click()
  })

  test('139 Save, reload: the hero is now the Cilium logo (the picker selection persisted to IaC and renders on hero + grid card)', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.getByTestId('cif-icon-edit').click()
    await page.getByTestId('iconpicker-light-tile-cilium').click()
    await page.getByTestId('cif-icon-save').click()
    await expect(page.getByTestId('cif-icon-save-verdict')).toHaveText('Saved to IaC ✓')
    await page.reload()
    await expect(heroLogo(page)).toHaveAttribute('src', /cilium\.svg$/)
    await openGrid(page)
    await expect(gridCard(page, 'Alloy').locator('img.app-logo')).toHaveAttribute('src', /cilium\.svg$/)
  })

  test('155 The edited icon renders on hero + grid + survives reload; the form pre-fills the IaC icon; the picker grid works: acceptance headline 4', async ({ page }) => {
    await openCatalog(page, alloy)
    await page.reload()
    await expect(heroLogo(page)).toHaveAttribute('src', /cilium\.svg$/)
    expect(await lightIconValue(page)).toMatch(/cilium\.svg$/)
    await page.getByTestId('cif-icon-edit').click()
    await expect(page.getByTestId('iconpicker-light-grid').getByRole('option').first()).toBeVisible()
    await page.getByTestId('cif-icon-cancel').click()
    await openGrid(page)
    await expect(gridCard(page, 'Alloy').locator('img.app-logo')).toHaveAttribute('src', /cilium\.svg$/)
  })
})

test.describe.serial('Alloy: the full blueprint in Edit IaC', () => {
  test('145 Change a field in the editor, Commit: a Show-diff Current/Proposed side-by-side renders the change and the commit succeeds', async ({ page }) => {
    const alloy = await catalogNameOf(page, 'Alloy', 'bp-alloy')
    await openCatalog(page, alloy)
    await page.getByTestId('catalog-detail-edit-iac').click()
    const before = await editorYaml(page)
    const after = before.replace(/^(\s+description:\s*)(.*)$/m, (_l, k: string, v: string) => `${k}${v.replace(/\s+$/, '')} ${PROOF}`)
    expect(after, 'the editor holds a card description to change').not.toBe(before)
    try {
      await setEditorYaml(page, after)
      await page.getByTestId('yaml-editor-toggle-diff').click()
      await expect(page.getByTestId('yaml-editor-diff-left')).toContainText('Current')
      await expect(page.getByTestId('yaml-editor-diff-right')).toContainText('Proposed')
      await expect(page.getByTestId('yaml-editor-diff-right')).toContainText(PROOF)
      await expect(page.getByTestId('yaml-editor-diff-left')).not.toContainText(PROOF)
      await page.getByTestId('yaml-editor-apply').click()
      await expect(page.getByTestId('yaml-editor-apply-ok')).toContainText('Committed to IaC ✓')
      expect(await committedIaC(page, alloy)).toContain(PROOF)
    } finally {
      await commitIaC(page, alloy, before)
    }
  })

  // SVC-200: spec.version has one writer, the Blueprint's own source; a catalogue edit may change only
  // the card fields and spec.visibility, so this row now proves the refusal and that nothing was committed.
  test('154 A non-card field edit (version) is refused with 422 contract-field-read-only and the committed blueprint.yaml keeps its version', async ({ page }) => {
    const alloy = await catalogNameOf(page, 'Alloy', 'bp-alloy')
    const before = await committedIaC(page, alloy)
    const doc = load(before, { schema: JSON_SCHEMA }) as { spec?: { version?: unknown } }
    const version = typeof doc.spec?.version === 'string' ? doc.spec.version : ''
    expect(version, 'the committed blueprint.yaml holds spec.version').not.toBe('')
    doc.spec!.version = `${version.replace(/\+.*$/, '')}+gate.${RUN_TOKEN.replace(/[^0-9a-z]/gi, '')}`
    const r = await page.request.put(consoleURL(`/api/v1/catalog/${encodeURIComponent(alloy)}/iac`), { data: { blueprintYaml: dump(doc, { schema: JSON_SCHEMA }) } })
    expect(r.status(), `PUT /api/v1/catalog/${alloy}/iac with another spec.version`).toBe(422)
    expect(await r.json()).toMatchObject({ error: 'contract-field-read-only', paths: ['spec.version'] })
    expect(await committedIaC(page, alloy)).toBe(before)
  })
})

test.describe.serial('WordPress', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('oo-theme', 'light'))
  })

  test('147 Open the WordPress detail page, Edit, change Summary, Save, reload: the summary persists, exactly as for Alloy', async ({ page }) => {
    const wp = await catalogNameOf(page, 'WordPress', 'bp-wordpress', 'bp-wordpress-tenant')
    await openCatalog(page, wp)
    // The put-back commits the original blueprint.yaml, not the hero text as a new card.tagline.
    const original = await committedIaC(page, wp)
    try {
      await saveInline(page, 'summary', `${PROOF}-wp`)
      await page.reload()
      await expect(page.getByTestId('catalog-summary')).toHaveText(`${PROOF}-wp`)
    } finally {
      if ((await committedIaC(page, wp)) !== original) await commitIaC(page, wp, original)
    }
  })

  test('148 WordPress: Edit IaC, edit spec.manifests, Commit, reload: the same YamlEditor edits a structurally-different blueprint manifests in place', async ({ page }) => {
    const wp = await catalogNameOf(page, 'WordPress', 'bp-wordpress', 'bp-wordpress-tenant')
    await openCatalog(page, wp)
    await page.getByTestId('catalog-detail-edit-iac').click()
    const before = await editorYaml(page)
    const mark = `# ${PROOF} manifests`
    const after = before.replace(/^(\s*)manifests:\s*$/m, (l, indent: string) => `${l}\n${indent}  ${mark}`)
    expect(after, 'the editor holds spec.manifests').not.toBe(before)
    try {
      await setEditorYaml(page, after)
      await page.getByTestId('yaml-editor-apply').click()
      await expect(page.getByTestId('yaml-editor-apply-ok')).toContainText('Committed to IaC ✓')
      await page.reload()
      await page.getByTestId('catalog-detail-edit-iac').click()
      const reloaded = await editorYaml(page)
      expect(reloaded).toMatch(new RegExp(`^\\s*manifests:\\s*\\n\\s*${mark.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`, 'm'))
    } finally {
      await commitIaC(page, wp, before)
    }
  })

  test('149 WordPress: Edit, Light-theme icon, distinct image, Save, reload: the WordPress hero icon visibly changes', async ({ page }) => {
    const wp = await catalogNameOf(page, 'WordPress', 'bp-wordpress', 'bp-wordpress-tenant')
    await openCatalog(page, wp)
    // The put-back commits the original blueprint.yaml, not the icon editor's pre-filled value (row 136).
    const original = await committedIaC(page, wp)
    const image = distinctImage(`wp-${RUN_TOKEN}`)
    try {
      await saveLightIcon(page, image)
      await page.reload()
      await expect(heroLogo(page)).toHaveAttribute('src', image)
    } finally {
      if ((await committedIaC(page, wp)) !== original) await commitIaC(page, wp, original)
    }
  })
})

test('158 The identical edit mechanism works on a 2nd + 3rd blueprint, no per-blueprint UI (Alloy + Postgres + a third): acceptance headline 7', async ({ page }) => {
  const chrome: string[][] = []
  for (const [title, ...names] of [['Alloy', 'bp-alloy'], ['PostgreSQL', 'bp-postgres'], ['WordPress', 'bp-wordpress', 'bp-wordpress-tenant']]) {
    const name = await catalogNameOf(page, title, ...names)
    await openCatalog(page, name)
    chrome.push(await page.locator('[data-testid^="cif-"][data-testid$="-edit"], [data-testid="catalog-detail-edit-iac"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? '').sort()))
    const original = await committedIaC(page, name)
    try {
      await saveInline(page, 'summary', `${PROOF}-${title.toLowerCase()}`)
      await page.reload()
      await expect(page.getByTestId('catalog-summary'), `${title}: the summary persisted`).toHaveText(`${PROOF}-${title.toLowerCase()}`)
    } finally {
      if ((await committedIaC(page, name)) !== original) await commitIaC(page, name, original)
    }
  }
  expect(chrome[0].length).toBeGreaterThan(0)
  expect(chrome[1], 'PostgreSQL: the edit chrome of Alloy').toEqual(chrome[0])
  expect(chrome[2], 'WordPress: the edit chrome of Alloy').toEqual(chrome[0])
})
