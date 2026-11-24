// Gate check FND-G23 (FND-165): the deployment wizard of the console (/wizard,
// products/catalyst/bootstrap/ui/src/pages/wizard), walked in a fresh browser signed in as the run's user. The
// wizard keeps its state in the browser (localStorage openova-catalyst-wizard), so every test starts from a
// wizard nobody has filled. One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { apiJSON } from './lib.ts'

/** The identity the wizard once pre-filled (RETIRED_ORG_DEFAULTS_5401, src/entities/deployment/model.ts). */
const FABRICATED = ['Acme Financial', 'acme.io', 'platform@acme.io', 'Financial Services', '2,000–10,000', 'Frankfurt, Germany']
/** Those of them that are no option label of a select of step 1 (Industry and size list the other two). */
const FABRICATED_TEXT = ['Acme Financial', 'acme.io', 'platform@acme.io', 'Frankfurt, Germany']
const STORE_KEY = 'openova-catalyst-wizard'
const ORG_NAME = 'Gate G23 Organisation'

const counter = (page: Page) => page.locator('.corp-step-counter')
const nextButton = (page: Page) => page.locator('.corp-btn-next')
const backButton = (page: Page) => page.locator('.corp-btn-back')

async function openWizard(page: Page): Promise<void> {
  await page.goto(consoleURL('/wizard'))
  await expect(counter(page), 'the wizard opens on its first step').toContainText('Step 1')
}

async function expectStep(page: Page, n: number): Promise<void> {
  await expect(counter(page), `the wizard is on step ${n}`).toHaveText(new RegExp(`^Step ${n}\\b`))
}

async function next(page: Page, from: number): Promise<void> {
  await expect(nextButton(page), `Continue is enabled on step ${from}`).toBeEnabled()
  await nextButton(page).click()
  await expectStep(page, from + 1)
}

/** Walks the wizard from step 1 to step n (n at most 6) the way an operator does: a name, one region, the defaults. */
async function walkTo(page: Page, n: number): Promise<void> {
  await openWizard(page)
  if (n === 1) return
  await page.locator('input[type="text"]').first().fill(ORG_NAME)
  await next(page, 1)
  if (n === 2) return
  await page.getByTestId('topology-choice-solo').click()
  await next(page, 2)
  for (let s = 3; s < n; s++) await next(page, s)
}

/** The wizard's persisted state, as the store wrote it. */
async function stored(page: Page): Promise<{ state: Record<string, unknown>; version: number }> {
  const raw = await page.evaluate((k) => window.localStorage.getItem(k), STORE_KEY)
  expect(raw, `the wizard keeps its state in localStorage ${STORE_KEY}`).not.toBeNull()
  return JSON.parse(raw as string) as { state: Record<string, unknown>; version: number }
}

test('W1 Deployment wizard step 1 does NOT pre-fill a fabricated company into the Organisation identity fields.', async ({ page }) => {
  await openWizard(page)
  await expect(page.getByText('Tell us about your organisation')).toBeVisible()
  const text = page.locator('input[type="text"]')
  await expect(text, 'step 1 has two text fields: Organisation name and Headquarters').toHaveCount(2)
  await expect(text.nth(0), 'Organisation name').toHaveValue('')
  await expect(text.nth(1), 'Headquarters').toHaveValue('')
  const selects = page.locator('select')
  await expect(selects.nth(0), 'Industry').toHaveValue('')
  await expect(selects.nth(1), 'Organisation size').toHaveValue('')
  const body = await page.locator('body').innerText()
  expect(FABRICATED_TEXT.filter((f) => body.includes(f)), 'the fabricated identity on step 1').toEqual([])
  const values = await page.locator('input, select').evaluateAll((els) => els.map((e) => (e as HTMLInputElement).value))
  expect(values.filter((v) => FABRICATED.includes(v)), 'fabricated values in the fields of step 1').toEqual([])
})

test('W2 The wizard does NOT derive cloud provider/region from a fabricated value.', async ({ page }) => {
  // Headquarters is left blank, as a new operator leaves it: step 3 then has no HQ to derive from, offers its
  // per-slot pickers, and shows no "Pre-selected based on HQ" hint.
  await walkTo(page, 3)
  await expect(page.getByText('Cloud provider per region')).toBeVisible()
  await expect(page.getByText(/Pick a provider, region, and instance sizes for each topology slot/)).toBeVisible()
  await expect(page.getByText(/Pre-selected based on HQ/), 'a provider and region derived from a headquarters').toHaveCount(0)
  const { state } = await stored(page)
  expect(state.orgHeadquarters, 'the headquarters the wizard holds').toBe('')
  expect(FABRICATED.filter((f) => JSON.stringify(state).includes(f)), 'fabricated values in the wizard state').toEqual([])
})

test('W3 Marketplace-mode storefront branding fields are optional and blank (hints only, no fabricated values).', async ({ page }) => {
  await walkTo(page, 5)
  const toggle = page.getByTestId('step-marketplace-toggle')
  if ((await toggle.getAttribute('aria-checked')) !== 'true') await toggle.click()
  await expect(toggle, 'Marketplace mode is on').toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('step-marketplace-brand')).toContainText('Optional. All three fields can be left blank')
  for (const [id, hint] of [['brand-name', 'e.g. Acme Marketplace'], ['brand-tagline', 'e.g. Apps for the regulated cloud'], ['brand-color', '#38BDF8']]) {
    const f = page.getByTestId(`step-marketplace-${id}`)
    await expect(f, `${id} is blank`).toHaveValue('')
    await expect(f, `${id} carries a hint only`).toHaveAttribute('placeholder', hint)
  }
  await expect(nextButton(page), 'Continue with the branding left blank').toBeEnabled()
})

test('W4 Every wizard step offers a Back control (footer navigation is consistent across the flow).', async ({ page }) => {
  test.setTimeout(240_000)
  // Step 1 is the first step and has nothing to go back to. Steps 2 to 5 are reached by Continue; steps 6 to 8
  // need a real domain and cloud credentials to continue, so they are opened the way the wizard reopens a
  // returning operator's step: its own stored state with currentStep set.
  await openWizard(page)
  await expect(backButton(page), 'step 1 (the first) has no Back').toHaveCount(0)
  await walkTo(page, 2)
  for (let n = 2; n <= 8; n++) {
    if (n >= 6) {
      const s = await stored(page)
      await page.evaluate(([k, v]) => window.localStorage.setItem(k, v), [STORE_KEY, JSON.stringify({ ...s, state: { ...s.state, currentStep: n } })])
      await page.reload()
    }
    await expectStep(page, n)
    await expect(backButton(page), `step ${n} offers Back`).toHaveText('← Back')
    await backButton(page).click()
    await expectStep(page, n - 1)
    // Back again forward: to step n, and on to step n + 1 while Continue can take it there.
    if (n <= 5) await next(page, n - 1)
    if (n <= 4) await next(page, n)
  }
})

test('W5 Component-selection step counts are self-consistent and every component id resolves to a real Blueprint.', async ({ page }) => {
  test.setTimeout(180_000)
  await walkTo(page, 4)
  const counted = /^Selected \((\d+)\) of (\d+)$/.exec((await page.getByTestId('selected-counter').innerText()).trim())
  expect(counted, 'the counter reads "Selected (n) of m"').not.toBeNull()
  const [selected, total] = [Number(counted?.[1]), Number(counted?.[2])]
  await page.getByTestId('tab-choose').click()
  await page.getByTestId('category-chip-all').click()
  const choose = page.getByTestId('component-grid').locator('[data-testid^="component-card-"]')
  const chooseIds = await choose.evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').slice('component-card-'.length)))
  const chosen = await choose.evaluateAll((els) => els.filter((e) => e.getAttribute('data-selected') === 'true').length)
  expect(chooseIds.length, 'components to choose from').toBeGreaterThan(0)
  await expect(page.getByText(`${chooseIds.length} shown · ${chosen} selected`, { exact: true }), 'the section summary of the cards shown').toBeVisible()
  await page.getByTestId('tab-always').click()
  const alwaysIds = await page.getByTestId('always-included-grid').locator('[data-testid^="component-card-"]')
    .evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').slice('component-card-'.length)))
  expect(alwaysIds.length, 'components always included').toBeGreaterThan(0)
  expect(chooseIds.filter((id) => alwaysIds.includes(id)), 'components on both tabs').toEqual([])
  expect(total, 'the counter total is the components to choose plus the ones always included').toBe(chooseIds.length + alwaysIds.length)
  expect(selected, 'the counter selection is the chosen components plus the ones always included').toBe(chosen + alwaysIds.length)
  const body = await apiJSON<{ items?: Array<{ name: string }> }>(page, '/api/v1/catalog')
  expect(Array.isArray(body.items), 'GET /api/v1/catalog answers a list items').toBe(true)
  const names = new Set((body.items ?? []).map((i) => i.name))
  expect([...chooseIds, ...alwaysIds].filter((id) => !names.has(`bp-${id}`) && !names.has(id)), 'component ids with no Blueprint in the catalogue').toEqual([])
})
