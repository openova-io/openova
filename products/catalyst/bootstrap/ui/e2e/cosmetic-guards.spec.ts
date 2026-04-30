/**
 * cosmetic-guards.spec.ts — 15 Playwright regression guards for the
 * specific cosmetic + step-flow defects the user has repeatedly
 * flagged. Every test in this file FAILS HARD (no .skip) when the bad
 * shape returns; the error message names the canonical reference and
 * the source-of-truth file the implementing agent must edit to fix it.
 *
 * Companion suite: tests/e2e/playwright/ owns the broader sovereign
 * wizard / admin voucher / unified Blueprint smoke (issues #142/#143/
 * #144 and the E2E suite agent's #184). This file is intentionally
 * narrower — only the regression guards.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target-state shape): each test asserts the canonical
 *       contract the user signed off on, never an interim shape.
 *   #2 (never compromise quality): no test.skip(), no soft-pass
 *       shortcuts. When a selector does not yet exist, the test fails
 *       LOUD with a message that names the missing data-testid so the
 *       implementing agent has a precise target.
 *   #4 (never hardcode): every URL / port / basepath is env-driven
 *       (see playwright.config.ts above this file).
 *
 * All tests are tagged with the @cosmetic-guard annotation per the
 * CI-wiring contract in .github/workflows/cosmetic-guards.yaml.
 */

import { test, expect, type Page, type Locator } from '@playwright/test'

/* ──────────────────────────────────────────────────────────────────
 * Canonical references (single source of truth in this file)
 * ────────────────────────────────────────────────────────────────── */

/**
 * Per-component logo tile background, mirrored from
 * src/pages/wizard/steps/logoTone.ts (export LOGO_SURFACE). Values are
 * the canonical brand surfaces — every project's homepage / press kit
 * colour. The wizard MUST place the logo glyph on this exact
 * background; falling back to pure white on a brand whose mark is
 * itself white = invisible glyph (the regression class the user has
 * flagged twice).
 *
 * NOTE: this duplicates a subset of LOGO_SURFACE deliberately — the
 * test fails when LOGO_SURFACE drifts away from these brand values,
 * not silently follow the drift. Adding a new component to
 * LOGO_SURFACE is a content change; changing one of THESE entries is
 * a brand-fidelity regression.
 */
const LOGO_SURFACE_CANON: Record<string, string> = {
  temporal: '#127ED1',
  ferretdb: '#042B41',
  alloy: '#FF671D',
  cilium: '#1A2236',
  grafana: '#0B0F19',
  'cert-manager': '#FFFFFF', // cert-manager genuinely IS on white — see logoTone.ts
  opensearch: '#FFFFFF',     // opensearch genuinely IS on white — see logoTone.ts
  stalwart: '#100E42',
  strimzi: '#192C47',
}

/**
 * Components for which the white-tile-with-white-glyph trap is
 * specifically forbidden. These projects publish a WHITE wordmark on
 * a coloured surface; if the tile renders white, the glyph is
 * invisible. (cert-manager / opensearch / kserve etc. publish a
 * COLOURED mark on white — those are NOT in this list because white
 * IS their canonical surface.)
 */
const REJECT_WHITE_TILE = new Set([
  'temporal',
  'ferretdb',
  'alloy',
  'cilium',
  'grafana',
  'stalwart',
  'strimzi',
])

/**
 * Canonical ordered step labels — pulled from
 * src/app/layouts/WizardLayout.tsx WIZARD_STEPS. Reordering is a
 * regression: the user has called out Domain-before-Components as the
 * specific bad shape twice.
 */
const CANONICAL_STEP_LABELS = [
  'Organisation',
  'Topology',
  'Provider',
  'Credentials',
  'Components',
  'Domain',
  'Review',
] as const

/**
 * Canonical Console sidebar nav labels, mirrored verbatim from
 * core/console/src/components/Sidebar.svelte nav array. Test 12 reads
 * this sidebar from the Sovereign Admin chrome and asserts the label
 * set matches.
 */
const CANONICAL_SIDEBAR_LABELS = [
  'Dashboard',
  'Apps',
  'Jobs',
  'Domains',
  'Billing',
  'Team',
  'Settings',
] as const

/**
 * Canonical AppDetail section order, mirrored from
 * core/console/src/components/AppDetail.svelte. The Sovereign
 * AppDetail page MUST render these as discrete sections (h2 / h3 /
 * data-section), NEVER as button role=tab + div role=tabpanel.
 */
const CANONICAL_APPDETAIL_SECTIONS = [
  'About',
  'Connection',
  'Bundled',
  'Tenant',
  'Configuration',
  'Jobs',
] as const

/* ──────────────────────────────────────────────────────────────────
 * Helpers
 * ────────────────────────────────────────────────────────────────── */

/**
 * Normalise a CSS rgb(...) / rgba(...) / #rrggbb / #rgb value into a
 * canonical lowercase #rrggbb hex. Used by the LOGO_SURFACE_CANON
 * comparison so the test does not care whether the browser computed
 * style is rgb(18, 126, 209) vs #127ED1.
 */
function toHex(cssColour: string): string {
  const trimmed = cssColour.trim().toLowerCase()

  if (trimmed.startsWith('#')) {
    if (trimmed.length === 4) {
      const r = trimmed[1]!
      const g = trimmed[2]!
      const b = trimmed[3]!
      return `#${r}${r}${g}${g}${b}${b}`
    }
    return trimmed
  }

  const m = trimmed.match(/^rgba?\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/)
  if (!m) {
    throw new Error(`toHex: cannot parse colour value "${cssColour}"`)
  }
  const r = Number(m[1]).toString(16).padStart(2, '0')
  const g = Number(m[2]).toString(16).padStart(2, '0')
  const b = Number(m[3]).toString(16).padStart(2, '0')
  return `#${r}${g}${b}`
}

/**
 * Seed the wizard's zustand-persist localStorage key (wizard-store)
 * BEFORE the page loads, via Playwright's addInitScript. Mirrors the
 * shape the app's persist middleware writes — see
 * src/entities/deployment/store.ts.
 */
async function seedWizardStore(page: Page, partial: Record<string, unknown>) {
  await page.addInitScript((seed) => {
    const KEY = 'openova-catalyst-wizard'
    try {
      const existing = JSON.parse(window.localStorage.getItem(KEY) ?? 'null')
      const state = existing?.state ?? {}
      const merged = {
        state: { ...state, ...(seed as Record<string, unknown>) },
        version: existing?.version ?? 0,
      }
      window.localStorage.setItem(KEY, JSON.stringify(merged))
    } catch {
      /* fresh persist init — let the app create its own store */
    }
  }, partial)
}

/**
 * Read the active step label from the WizardLayout stepper. Returns
 * the rendered text of the element flagged with aria-current=step.
 */
async function activeStepLabel(page: Page): Promise<string> {
  const active = page.locator('button[aria-current="step"]').first()
  await expect(
    active,
    'wizard stepper exposes aria-current=step on the active step button',
  ).toBeVisible({ timeout: 10_000 })
  return (await active.textContent())?.trim() ?? ''
}

/**
 * Compute the average pixel luminance of an element's screenshot
 * (Rec. 709 weights). Used by the logo-glyph-visible test to detect
 * the "tile + glyph have indistinguishable luminance" failure mode
 * (white glyph on white tile). Returns a number in [0, 1].
 */
async function averageLuminance(locator: Locator): Promise<number> {
  const buf = await locator.screenshot()
  return await locator.page().evaluate(async (dataUrlBytes: number[]) => {
    const blob = new Blob([new Uint8Array(dataUrlBytes)], { type: 'image/png' })
    const url = URL.createObjectURL(blob)
    try {
      const img = new Image()
      await new Promise<void>((resolve, reject) => {
        img.onload = () => resolve()
        img.onerror = () => reject(new Error('image load failed'))
        img.src = url
      })
      const canvas = document.createElement('canvas')
      canvas.width = img.naturalWidth
      canvas.height = img.naturalHeight
      const ctx = canvas.getContext('2d')!
      ctx.drawImage(img, 0, 0)
      const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data
      let sum = 0
      let count = 0
      for (let i = 0; i < data.length; i += 4) {
        const r = data[i]! / 255
        const g = data[i + 1]! / 255
        const b = data[i + 2]! / 255
        sum += 0.2126 * r + 0.7152 * g + 0.0722 * b
        count += 1
      }
      return sum / Math.max(1, count)
    } finally {
      URL.revokeObjectURL(url)
    }
  }, Array.from(buf))
}

/* ──────────────────────────────────────────────────────────────────
 * Test 1 — Card height = 108px (canonical SME marketplace value)
 * Test 2 — Card body has no reserved right padding
 * Test 3 — Logo tile uses brand colour, not pure white
 * Test 4 — Logo tile glyph is visible (luminance contrast)
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard StepComponents card geometry', () => {
  test.beforeEach(async ({ page }) => {
    await seedWizardStore(page, {
      currentStep: 5,
      orgName: 'Acme',
      orgIndustry: 'finance',
      orgSize: '50-200',
      orgHeadquarters: 'Frankfurt, Germany',
      topology: 'three-region-ha',
      airgap: false,
    })
    await page.goto('wizard')
  })

  test('card resting height matches canonical 108px (NOT 130px)', async ({ page }) => {
    const firstCard = page.locator('[data-testid^="component-card-"]').first()
    await expect(
      firstCard,
      'StepComponents card grid renders at least one component card',
    ).toBeVisible({ timeout: 10_000 })

    const box = await firstCard.boundingBox()
    expect(
      box,
      'card has a non-null bounding box (it must be in the visual layout, not display:none)',
    ).not.toBeNull()

    expect(
      Math.round(box!.height),
      `StepComponents card height drifted from canonical 108px to ${Math.round(box!.height)}px — see commit 691467b4 (the revert that restored 108px) and the .corp-comp-card { height: 108px } rule in src/pages/wizard/steps/StepComponents.tsx. Was bumped to 130 once, regressed twice.`,
    ).toBe(108)
  })

  test('card body description spans full width (NO reserved right padding)', async ({ page }) => {
    const card = page.locator('[data-testid^="component-card-"]').first()
    await expect(card).toBeVisible({ timeout: 10_000 })

    const cardBox = await card.boundingBox()
    const body = card.locator('.corp-comp-body')
    const bodyBox = await body.boundingBox()
    const desc = body.locator('.corp-comp-desc').first()
    const descBox = await desc.boundingBox()

    expect(cardBox).not.toBeNull()
    expect(bodyBox).not.toBeNull()
    expect(descBox).not.toBeNull()

    // The card outer padding (.corp-comp-card padding 0.6rem) is
    // ~9.6px on each side. Logo tile is on the left ⇒ body right
    // edge MUST sit within ~card-right-edge - card-padding (i.e. NOT
    // pulled in further to reserve space for an absolute-positioned
    // affordance the way SME .app-body { padding-right: 72px } does).
    // Allow 16px slack for sub-pixel + the 0.6rem padding.
    const cardRight = cardBox!.x + cardBox!.width
    const bodyRight = bodyBox!.x + bodyBox!.width
    const reservedGap = cardRight - bodyRight

    expect(
      reservedGap,
      `card body right edge sits ${reservedGap.toFixed(1)}px from card right edge — anything beyond ~16px means a vertical column was reserved for an Add button (regression of the SME-style absolute overlay; canonical contract is inline toggle on line 1, see StepComponents.tsx .corp-comp-body rule).`,
    ).toBeLessThanOrEqual(16)

    expect(
      descBox!.width,
      `description width ${descBox!.width.toFixed(1)}px is narrower than body width ${bodyBox!.width.toFixed(1)}px — desc must span full body width, not reserve space for an affordance.`,
    ).toBeGreaterThanOrEqual(bodyBox!.width - 4)
  })

  test('logo tiles use canonical brand surface (NOT default white)', async ({ page }) => {
    // The default StepComponents tab is the non-mandatory "choose"
    // pool; mandatory components (cilium, cert-manager) live on Tab 2
    // (data-testid="tab-always") at the moment this test was written.
    // We open Tab 2 too so spot-checked mandatory components are
    // findable. NOTE: when tests #6 (no legacy tab labels) flips
    // green, the tabs go away and BOTH mandatory + non-mandatory
    // cards live on the same flat grid; this loop still works because
    // a card found on either tab satisfies the locator.
    const failures: string[] = []
    // Wait for the wizard step + grid to hydrate.
    await expect(page.locator('h2.corp-step-title').first()).toBeVisible({ timeout: 10_000 })

    // Pre-collect computed background colours by id from BOTH tabs.
    // Storing computed values (instead of Locators) sidesteps the
    // Playwright caveat that nth-locators are lazy: after switching
    // tabs the indices shift, so a deferred .evaluate() on a saved
    // nth-locator would re-resolve against the wrong position.
    const collectedHex: Record<string, string> = {}

    async function harvestVisibleCards() {
      const cards = page.locator('[data-testid^="component-card-"]')
      const n = await cards.count()
      for (let i = 0; i < n; i++) {
        const c = cards.nth(i)
        const tid = await c.getAttribute('data-testid')
        if (!tid) continue
        const id = tid.replace(/^component-card-/, '')
        if (id in collectedHex) continue
        const logo = c.locator(`[data-testid="logo-${id}"]`)
        if ((await logo.count()) === 0) continue
        const tile = logo.first().locator('..')
        const computed = await tile.evaluate((el) => window.getComputedStyle(el).backgroundColor)
        collectedHex[id] = computed
      }
    }

    await harvestVisibleCards()
    const tabAlways = page.locator('[data-testid="tab-always"]')
    if ((await tabAlways.count()) > 0) {
      await tabAlways.click()
      await page.waitForTimeout(250)
      await harvestVisibleCards()
    }

    for (const [id, expectedHex] of Object.entries(LOGO_SURFACE_CANON)) {
      const computed = collectedHex[id]
      if (!computed) {
        failures.push(
          `component "${id}" has no card on StepComponents grid (checked default + always-included tabs) — either the component was removed from componentGroups.ts (content fix) or LOGO_SURFACE_CANON in this test is stale`,
        )
        continue
      }
      const computedHex = toHex(computed)
      if (computedHex.toLowerCase() !== expectedHex.toLowerCase()) {
        failures.push(
          `${id}: tile background-color = ${computed} (${computedHex}); canonical = ${expectedHex} (from src/pages/wizard/steps/logoTone.ts LOGO_SURFACE)`,
        )
      }
      if (REJECT_WHITE_TILE.has(id) && computedHex === '#ffffff') {
        failures.push(
          `${id}: tile is pure white (#ffffff) — this brand publishes a WHITE glyph and white-on-white renders an invisible mark. Canonical surface is ${expectedHex}.`,
        )
      }
    }

    expect(
      failures,
      `Logo tile brand-surface drift detected:\n  - ${failures.join('\n  - ')}`,
    ).toEqual([])
  })

  test('Temporal + FerretDB logo glyphs are visible in dark + light themes', async ({ page }) => {
    // Reference luminances are derived analytically from each brand
    // surface hex, treating an "invisible glyph" as a tile that is
    // a flat block of that hex:
    //   #127ED1 = rgb(18,126,209) ⇒ 0.2126*18/255 + 0.7152*126/255 + 0.0722*209/255 ≈ 0.4044
    //   #042B41 = rgb(4,43,65)    ⇒ 0.1481
    const samples: Array<{ id: string; canon: number }> = [
      { id: 'temporal', canon: 0.4044 },
      { id: 'ferretdb', canon: 0.1481 },
    ]

    for (const theme of ['dark', 'light'] as const) {
      await page.addInitScript((t) => {
        try {
          window.localStorage.setItem('wiz-theme', t)
        } catch {
          /* */
        }
      }, theme)
      await page.reload()

      for (const { id, canon } of samples) {
        const card = page.locator(`[data-testid="component-card-${id}"]`).first()
        await expect(
          card,
          `[theme=${theme}] component-card-${id} must be on the visible grid`,
        ).toBeVisible({ timeout: 10_000 })
        const tile = card.locator(`[data-testid="logo-${id}"]`).locator('..')
        const lum = await averageLuminance(tile)

        // 0.02 tolerance on a 0..1 scale — a real glyph perturbs the
        // mean by far more than this in our test images; a flat-fill
        // tile sits within ±0.005 of canon.
        expect(
          Math.abs(lum - canon),
          `[theme=${theme}] ${id} logo tile mean luminance ${lum.toFixed(3)} matches a flat brand-colour swatch (${canon.toFixed(3)}) within 0.02 — glyph appears invisible. Check the vendored SVG renders, that the img is not 0x0, and that no overlay is masking it. See StepComponents.tsx ComponentLogo and src/pages/wizard/steps/logoTone.ts.`,
        ).toBeGreaterThan(0.02)
      }
    }
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 5 — Wizard step order is canonical
 * Test 6 — No "Choose Your Stack" / "Always Included" tabs
 * Test 7 — Domain step appears AFTER Components
 * Test 8 — CPX32 SKU is the recommended Hetzner CP
 * Test 9 — Per-region SKU dropdown shows ONLY chosen provider catalog
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard wizard step flow', () => {
  test('step order is Org -> Topology -> Provider -> Credentials -> Components -> Domain -> Review', async ({
    browser,
  }) => {
    // Use a fresh browser context per iteration so the
    // addInitScript that seeds currentStep fires BEFORE the wizard
    // hydrates. zustand-persist reads localStorage once on mount;
    // changing localStorage after hydration does not flow through.
    const observed: string[] = []
    for (let i = 1; i <= CANONICAL_STEP_LABELS.length; i++) {
      const ctx = await browser.newContext()
      const page = await ctx.newPage()
      await seedWizardStore(page, { currentStep: i })
      await page.goto('wizard')
      const label = await activeStepLabel(page)
      observed.push(label)
      await ctx.close()
    }

    for (let i = 0; i < CANONICAL_STEP_LABELS.length; i++) {
      const expected = CANONICAL_STEP_LABELS[i]!
      const got = observed[i] ?? ''
      expect(
        got.endsWith(expected),
        `step ${i + 1} active label "${got}" does not end with canonical "${expected}". Full observed sequence = [${observed.join(', ')}]. Canonical sequence = [${CANONICAL_STEP_LABELS.join(', ')}]. See src/app/layouts/WizardLayout.tsx WIZARD_STEPS — the order is dependency-driven (Topology decides region count BEFORE Provider sizes per region; Components BEFORE Domain so the operator picks add-ons before naming the Sovereign).`,
      ).toBe(true)
    }
  })

  test('StepComponents does not render legacy "Choose Your Stack" / "Always Included" tab labels', async ({
    page,
  }) => {
    await seedWizardStore(page, {
      currentStep: 5,
      orgHeadquarters: 'Frankfurt, Germany',
      topology: 'three-region-ha',
      airgap: false,
    })
    await page.goto('wizard')

    const stepRoot = page.locator('h2.corp-step-title').first()
    await expect(stepRoot, 'StepComponents step root visible').toBeVisible({ timeout: 10_000 })

    const choose = page.getByText('Choose Your Stack', { exact: false })
    const always = page.getByText('Always Included', { exact: false })

    const chooseN = await choose.count()
    const alwaysN = await always.count()

    expect(
      chooseN,
      'StepComponents still renders text "Choose Your Stack" — that label was retired in favour of the canonical SME marketplace single-grid layout (core/marketplace/src/components/AppsStep.svelte). Update src/pages/wizard/steps/stepComponentsCopy.ts (tabChooseLabel) and remove the top-level role=tablist div.',
    ).toBe(0)
    expect(
      alwaysN,
      'StepComponents still renders text "Always Included" — same retirement as "Choose Your Stack" above. The mandatory components surface is now a separate read-only section, not a peer tab. See core/marketplace/src/components/AppsStep.svelte.',
    ).toBe(0)
  })

  test('operator cannot reach Domain before Components', async ({ page }) => {
    await seedWizardStore(page, { currentStep: 1 })
    await page.goto('wizard')

    const domainStepBtn = page.locator('[data-testid="wizard-step-6"]')
    const componentsStepBtn = page.locator('[data-testid="wizard-step-5"]')

    await expect(
      domainStepBtn,
      'WizardLayout renders a step-6 (Domain) button',
    ).toBeVisible({ timeout: 10_000 })
    await expect(
      componentsStepBtn,
      'WizardLayout renders a step-5 (Components) button',
    ).toBeVisible()

    const dLabel = (await domainStepBtn.textContent())?.trim() ?? ''
    const cLabel = (await componentsStepBtn.textContent())?.trim() ?? ''
    expect(
      dLabel.endsWith('Domain'),
      `step 6 reads "${dLabel}" — must end with "Domain". See WIZARD_STEPS in WizardLayout.tsx.`,
    ).toBe(true)
    expect(
      cLabel.endsWith('Components'),
      `step 5 reads "${cLabel}" — must end with "Components".`,
    ).toBe(true)

    const isDisabled = await domainStepBtn.evaluate(
      (el) => (el as HTMLButtonElement).disabled,
    )
    expect(
      isDisabled,
      'Domain step (#6) is clickable from a fresh wizard — operator can leapfrog Components. WizardLayout.tsx clickable=done guard regressed; only PAST steps must be clickable.',
    ).toBe(true)
  })

  test('CPX32 carries the recommended tag in StepProvider when Hetzner is picked', async ({
    page,
  }) => {
    await seedWizardStore(page, {
      currentStep: 3,
      orgHeadquarters: 'Frankfurt, Germany',
      topology: 'single-region',
      airgap: false,
      regionProviders: { 0: 'hetzner' },
      regionCloudRegions: { 0: 'fsn1' },
    })
    await page.goto('wizard')

    const stepRoot = page.locator('h2.corp-step-title').first()
    await expect(stepRoot, 'StepProvider step root visible').toBeVisible({ timeout: 10_000 })

    // Read the catalog directly via Vite's dev module loader. The TS
    // file is served as a module under the basepath.
    const recommendedId = await page
      .evaluate(async () => {
        const mod = await import(
          /* @vite-ignore */ '/sovereign/src/shared/constants/providerSizes.ts'
        )
        const sizes = (
          mod as {
            PROVIDER_NODE_SIZES: Record<string, Array<{ id: string; recommended?: boolean }>>
          }
        ).PROVIDER_NODE_SIZES
        const hetznerSizes = sizes['hetzner'] ?? []
        return hetznerSizes.filter((s) => s.recommended === true).map((s) => s.id)
      })
      .catch(() => null)

    expect(
      recommendedId,
      'Could not read PROVIDER_NODE_SIZES from src/shared/constants/providerSizes.ts via the dev server — check the file exists and exports PROVIDER_NODE_SIZES.',
    ).not.toBeNull()
    expect(
      recommendedId,
      `Recommended Hetzner SKU set drifted: got [${(recommendedId ?? []).join(', ')}], must be exactly ['cpx32']. See src/shared/constants/providerSizes.ts and the recommended:true flag on the Hetzner CPX32 entry.`,
    ).toEqual(['cpx32'])
  })

  test('switching provider switches the SKU catalog (Huawei vs Hetzner)', async ({ page }) => {
    await seedWizardStore(page, {
      currentStep: 3,
      orgHeadquarters: 'Frankfurt, Germany',
      topology: 'single-region',
      airgap: false,
      regionProviders: { 0: 'hetzner' },
      regionCloudRegions: { 0: 'fsn1' },
    })
    await page.goto('wizard')
    await expect(page.locator('h2.corp-step-title').first()).toBeVisible({ timeout: 10_000 })

    const catalogs = await page
      .evaluate(async () => {
        const mod = await import(
          /* @vite-ignore */ '/sovereign/src/shared/constants/providerSizes.ts'
        )
        const sizes = (mod as {
          PROVIDER_NODE_SIZES: Record<string, Array<{ id: string }>>
        }).PROVIDER_NODE_SIZES
        return {
          hetzner: (sizes['hetzner'] ?? []).map((s) => s.id),
          huawei: (sizes['huawei'] ?? []).map((s) => s.id),
        }
      })
      .catch(() => null)

    expect(
      catalogs,
      'Could not read PROVIDER_NODE_SIZES from the dev server — check src/shared/constants/providerSizes.ts exports PROVIDER_NODE_SIZES.',
    ).not.toBeNull()
    const hetznerIds = catalogs!.hetzner
    const huaweiIds = catalogs!.huawei
    expect(hetznerIds.length, 'Hetzner catalog must be non-empty').toBeGreaterThan(0)
    expect(huaweiIds.length, 'Huawei catalog must be non-empty').toBeGreaterThan(0)

    const overlap = hetznerIds.filter((id) => huaweiIds.includes(id))
    expect(
      overlap,
      `Hetzner and Huawei SKU id sets overlap on [${overlap.join(', ')}] — the per-provider catalog contract requires disjoint id namespaces (CPX32 means nothing on Huawei). See src/shared/constants/providerSizes.ts.`,
    ).toEqual([])

    // Cross-check the rendered dropdown — open the Hetzner CP
    // dropdown trigger; every option row's label MUST belong to
    // hetznerIds, NONE may bleed in from huaweiIds.
    const cpDropdownTrigger = page
      .locator('text=Control-plane size')
      .locator('..')
      .locator('div')
      .first()
    await cpDropdownTrigger.click()
    const optionRows = page.locator('div').filter({ hasText: /vCPU.*RAM/ })
    const huaweiOnly = huaweiIds.filter((id) => !hetznerIds.includes(id))
    const optionTexts = await optionRows.allTextContents()
    const contamination = optionTexts.filter((t) =>
      huaweiOnly.some((id) => t.toLowerCase().includes(id.toLowerCase())),
    )
    expect(
      contamination,
      `Hetzner SKU dropdown is rendering Huawei SKUs: ${JSON.stringify(contamination)}. See StepProvider.tsx skuOptions(provider) — it must only return PROVIDER_NODE_SIZES[provider].`,
    ).toEqual([])
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 10 — Provision page is a SPA route (no .html)
 * Test 11 — No bubble/edge DAG on provision page
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard provision page', () => {
  test('Launch navigates to /sovereign/provision/<id> as a SPA route (no .html)', async ({
    page,
  }) => {
    await page.goto('provision/test-deployment-id')

    const url = page.url()
    expect(
      url.includes('.html'),
      `Provision URL is "${url}" — contains ".html", which means the route is being served as a static document instead of a SPA route. See src/app/router.tsx provisionRoute (path: /provision/$deploymentId, NOT /provision.html).`,
    ).toBe(false)

    expect(
      /\/sovereign\/provision\/[^/]+/.test(url),
      `Provision URL "${url}" does not match /sovereign/provision/<id> — vite base + tanstack router basepath drift. See vite.config.ts (base /sovereign/) and src/app/router.tsx (basepath /sovereign).`,
    ).toBe(true)
  })

  test('provision page has no legacy DAG SVG markup', async ({ page }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    const banned = ['nlabel', 'nsel', 'nhov', 'ng']
    const found: string[] = []
    for (const cls of banned) {
      // Only count SVG <g> elements with that class — the legacy DAG
      // emitted <g class=nlabel>, <g class=nsel>, etc. We do not want
      // to false-positive a CSS module class that happens to share a
      // name in a non-svg context.
      const sel = `svg g.${cls}, g.${cls}`
      const n = await page.locator(sel).count()
      if (n > 0) found.push(`${cls}=${n}`)
    }

    expect(
      found,
      `Legacy bubble/edge DAG markup is back on /provision: ${found.join(', ')}. The DAG view was retired (see src/pages/provision/ProvisionPage.tsx — it now re-exports AdminPage); per-Application cards live in src/pages/sovereign/AdminPage.tsx + ApplicationCard.tsx.`,
    ).toEqual([])
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 12 — Admin sidebar matches canonical core/console
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard admin sidebar parity', () => {
  test('Sovereign admin sidebar is w-56 and mirrors core/console nav labels', async ({ page }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    const sidebar = page.locator('[data-testid="admin-sidebar"]').first()
    const sidebarCount = await sidebar.count()
    expect(
      sidebarCount,
      'Sovereign admin shell does not expose a [data-testid=admin-sidebar] element — add the testid to the Sidebar.tsx aside root so this regression guard can find it. Canonical reference: core/console/src/components/Sidebar.svelte (the aside class with w-56).',
    ).toBeGreaterThan(0)

    // tailwind w-56 = 14rem = 224px. Allow ±1px for sub-pixel rounding.
    const box = await sidebar.boundingBox()
    expect(box).not.toBeNull()
    expect(
      Math.round(box!.width),
      `Admin sidebar width = ${box!.width.toFixed(1)}px; canonical w-56 = 224px. See core/console/src/components/Sidebar.svelte.`,
    ).toBe(224)

    const navItems = sidebar.locator('a, button').filter({ hasText: /\w/ })
    const labels = (await navItems.allTextContents())
      .map((s) => s.trim())
      .filter((s) => s.length > 0)
    const observedSet = new Set(labels.map((l) => l.split(/\s+/)[0]))
    const missing = CANONICAL_SIDEBAR_LABELS.filter((l) => !observedSet.has(l))
    expect(
      missing,
      `Admin sidebar is missing canonical nav labels: [${missing.join(', ')}]. Observed: [${[...observedSet].join(', ')}]. Canonical: [${CANONICAL_SIDEBAR_LABELS.join(', ')}] — copy from core/console/src/components/Sidebar.svelte nav array verbatim.`,
    ).toEqual([])
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 13 — Per-app detail page is sectioned, not tabbed
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard app-detail layout', () => {
  test('AppDetail renders the canonical sections (Jobs is now also a tab — issue #204)', async ({ page }) => {
    await page.goto('provision/test-deployment-id/app/temporal')
    await page.waitForLoadState('domcontentloaded')

    // Issue #204 founder spec item #9: "AppDetail → Jobs tab filtered
    // to that app's jobs only". A Jobs tab is now mandatory inside the
    // Jobs section. The legacy guard ("no role=tablist anywhere on the
    // page") was a regression guard for a DIFFERENT bad shape (the
    // invented Logs/Dependencies/Status/Overview tabset on the legacy
    // ApplicationPage). That bad shape is still forbidden — but the
    // guard now allows the founder-requested Jobs tabset on AppDetail.
    //
    // We assert the legacy-tab vocabulary is GONE (no Logs / Status /
    // Overview tab labels anywhere on the page) instead of banning
    // tablist entirely.
    const forbiddenTabLabels = ['Logs', 'Status', 'Overview']
    for (const label of forbiddenTabLabels) {
      const tabs = page.getByRole('tab', { name: new RegExp(`^${label}$`, 'i') })
      expect(
        await tabs.count(),
        `AppDetail is rendering a tab labelled "${label}" — this is the legacy ApplicationPage tabset (Logs / Status / Overview / Dependencies) the user told us to retire. Only the founder-spec Jobs tab (issue #204 item #9) is permitted here.`,
      ).toBe(0)
    }

    const missing: string[] = []
    for (const section of CANONICAL_APPDETAIL_SECTIONS) {
      const heading = page.getByRole('heading', { name: new RegExp(section, 'i') })
      const n = await heading.count()
      if (n === 0) missing.push(section)
    }
    expect(
      missing,
      `AppDetail is missing canonical sections: [${missing.join(', ')}]. Each must appear as an h2 / h3 heading. Canonical order = [${CANONICAL_APPDETAIL_SECTIONS.join(', ')}] (see core/console/src/components/AppDetail.svelte).`,
    ).toEqual([])
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 14 — Jobs surface is a TABLE view, NOT an accordion (issue #204)
 *
 * Founder spec items 1, 2, 6, 7, 8a, 8b. The legacy expand-in-place
 * accordion has been retired ("NEVER use accordions") — the Jobs page
 * now renders a <table data-testid="jobs-table"> with one row per job,
 * a search box, and per-column filter dropdowns. The row is a link,
 * not a button — clicking navigates to /provision/<id>/jobs/<jobId>.
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard jobs surface (issue #204 — table view)', () => {
  test('1. jobs-table testid exists and no legacy accordion testids remain', async ({ page }) => {
    await page.goto('provision/test-deployment-id/jobs')
    await page.waitForLoadState('domcontentloaded')

    const table = page.locator('[data-testid="jobs-table"]')
    await expect(
      table,
      'JobsPage is missing [data-testid=jobs-table] — issue #204 retired the accordion in favour of a <table>. See products/catalyst/bootstrap/ui/src/pages/sovereign/JobsTable.tsx.',
    ).toBeVisible({ timeout: 10_000 })
    expect(
      await table.evaluate((el) => el.tagName.toLowerCase()),
      'jobs-table testid is attached to a non-<table> element — must be a real HTML <table> for accessibility + semantic correctness.',
    ).toBe('table')

    const accordionRows = page.locator('[data-testid^="job-row-"]')
    expect(
      await accordionRows.count(),
      `Legacy [data-testid^=job-row-] accordion rows are STILL on the page. Issue #204 founder spec item #1: "NEVER use accordions anywhere — the wizard filled them everywhere for jobs. Unacceptable." Remove JobCard.tsx and any callers.`,
    ).toBe(0)
    const accordionPanels = page.locator('[data-testid^="job-expansion-"]')
    expect(
      await accordionPanels.count(),
      `Legacy [data-testid^=job-expansion-] accordion panels are STILL on the page. Same retirement as above — the table view is the only canonical jobs surface now.`,
    ).toBe(0)
  })

  test('2. table headers are name / app / deps / batch / status / started / duration', async ({ page }) => {
    await page.goto('provision/test-deployment-id/jobs')
    await page.waitForLoadState('domcontentloaded')

    const table = page.locator('[data-testid="jobs-table"]')
    await expect(table).toBeVisible({ timeout: 10_000 })
    const headerLocators = table.locator('thead th')
    const headers = (await headerLocators.allTextContents()).map((s) => s.trim().toLowerCase())
    expect(
      headers,
      `JobsTable column header set = [${headers.join(', ')}]; founder spec issue #204 items #6/#7 require [name, app, deps, batch, status, started, duration] in this order.`,
    ).toEqual(['name', 'app', 'deps', 'batch', 'status', 'started', 'duration'])
  })

  test('3. typing in jobs-search filters the row count', async ({ page }) => {
    await page.goto('provision/test-deployment-id/jobs')
    await page.waitForLoadState('domcontentloaded')

    await expect(page.locator('[data-testid="jobs-table"]')).toBeVisible({ timeout: 10_000 })
    const search = page.locator('[data-testid="jobs-search"]')
    await expect(
      search,
      'JobsTable is missing [data-testid=jobs-search] — issue #204 item #8a requires a search box on the table.',
    ).toBeVisible()

    const tableRows = page.locator('[data-testid^="jobs-table-row-"]')
    const beforeCount = await tableRows.count()
    expect(
      beforeCount,
      'JobsTable rendered with zero rows — the table needs at least the bootstrap-kit + Phase 0 jobs to exist before a search can be tested.',
    ).toBeGreaterThan(0)

    // Type a query that matches a known bootstrap-kit row label
    // ("Cilium") — the Phase 0 / cluster-bootstrap jobs do NOT contain
    // that string, so the visible row count must drop.
    await search.fill('cilium')
    // Allow React state to flush; useMemo recomputes synchronously but
    // the next tick is needed for the table to re-render.
    await page.waitForTimeout(120)
    const afterCount = await page.locator('[data-testid^="jobs-table-row-"]').count()
    expect(
      afterCount,
      `Typing "cilium" into [data-testid=jobs-search] did not narrow the visible row count (before=${beforeCount}, after=${afterCount}). Search filter is not wired through to the row list — see matchJob() in JobsTable.tsx.`,
    ).toBeLessThan(beforeCount)
    expect(
      afterCount,
      `Typing "cilium" into the search box returned zero rows — the bp-cilium row must always match a "cilium" query.`,
    ).toBeGreaterThan(0)
  })

  test('4. AppDetail page has a tab labelled "Jobs"', async ({ page }) => {
    await page.goto('provision/test-deployment-id/app/temporal')
    await page.waitForLoadState('domcontentloaded')

    // Founder spec issue #204 item #9: "AppDetail → Jobs tab filtered
    // to that app's jobs only." The tab MUST exist and MUST carry the
    // canonical role=tab semantic for keyboard nav + screen-reader UX.
    const jobsTab = page.locator('[data-testid="sov-app-tab-jobs"]')
    await expect(
      jobsTab,
      'AppDetail is missing [data-testid=sov-app-tab-jobs] — issue #204 item #9 requires a Jobs tab on AppDetail. See AppDetail.tsx.',
    ).toBeVisible({ timeout: 10_000 })
    expect(
      await jobsTab.getAttribute('role'),
      'sov-app-tab-jobs must carry role="tab" so it is exposed correctly to AT and the cosmetic-guard regression suite. See AppDetail.tsx tablist markup.',
    ).toBe('tab')
    const text = (await jobsTab.textContent())?.toLowerCase() ?? ''
    expect(
      text.includes('jobs'),
      `sov-app-tab-jobs label is "${text}"; issue #204 item #9 requires the tab to be labelled "Jobs".`,
    ).toBe(true)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 15 — No Phase 0 banners on AdminPage
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard admin page banners', () => {
  test('AdminPage does not render "Hetzner infra" + "Cluster bootstrap" Phase 0 banners', async ({
    page,
  }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    // The legacy PhaseBanners.tsx emitted:
    //   <div data-testid="sov-phase-row">
    //     <PhaseBanner ... name="Hetzner infra" />
    //     <PhaseBanner ... name="Cluster bootstrap" />
    //   </div>
    // Both have been retired in favour of per-Application/per-Job
    // cards (each phase is now its own JobCard with expand-in-place).
    const phaseRow = page.locator('[data-testid="sov-phase-row"]')
    expect(
      await phaseRow.count(),
      'Legacy Phase 0 banner row [data-testid=sov-phase-row] is back on AdminPage. The "Hetzner infra" + "Cluster bootstrap" phases were promoted to JobCards (see core/console/src/components/JobsPage.svelte and src/pages/sovereign/JobCard.tsx). Remove src/pages/sovereign/PhaseBanners.tsx and its <PhaseBanners> import in AdminPage.tsx.',
    ).toBe(0)

    const hetznerBanner = page.getByText('Hetzner infra', { exact: false })
    const clusterBanner = page.getByText('Cluster bootstrap', { exact: false })
    expect(
      await hetznerBanner.count(),
      'AdminPage still renders the literal text "Hetzner infra" — the Phase 0 banner was retired (per-job cards now carry that surface). The string lived in PhaseBanners.tsx; remove the file + its import.',
    ).toBe(0)
    expect(
      await clusterBanner.count(),
      'AdminPage still renders the literal text "Cluster bootstrap" — same retirement. See PhaseBanners.tsx removal.',
    ).toBe(0)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 16 — PortalShell header exposes a theme toggle
 * Test 17 — Theme toggle flips data-theme on the html element
 *
 * Light/dark theme parity for the Sovereign portal — the PortalShell
 * (Apps / Jobs / AppDetail) inherited the wizard's `data-theme` swap
 * but had no UI affordance to flip the theme post-handover. Issue
 * caught by the founder during console review of omantel.omani.works.
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard PortalShell theme toggle', () => {
  test('theme-toggle is present in PortalShell header', async ({ page }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    const header = page.locator('[data-testid="portal-header"]').first()
    await expect(
      header,
      'PortalShell does not expose a [data-testid=portal-header] element — add the testid to the header band hosting the theme toggle in src/pages/sovereign/PortalShell.tsx.',
    ).toBeVisible({ timeout: 10_000 })

    const toggle = header.locator('[data-testid="theme-toggle"]').first()
    await expect(
      toggle,
      'PortalShell header is missing [data-testid=theme-toggle] — mount <ThemeToggle /> from src/components/ThemeToggle.tsx in the PortalShell header band (top-right).',
    ).toBeVisible({ timeout: 10_000 })
  })

  test('clicking theme-toggle flips data-theme attribute on html element', async ({ page }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    const toggle = page.locator('[data-testid="theme-toggle"]').first()
    await expect(toggle).toBeVisible({ timeout: 10_000 })

    const before = await page.evaluate(() =>
      document.documentElement.getAttribute('data-theme'),
    )
    expect(
      before,
      'html element is missing data-theme attribute on first paint — the bootstrap script in index.html should set it to dark/light from localStorage[oo-theme] before the React tree mounts.',
    ).toMatch(/^(dark|light)$/)

    await toggle.click()
    // Allow React state -> useEffect -> documentElement.setAttribute to flush.
    await page.waitForTimeout(120)

    const after = await page.evaluate(() =>
      document.documentElement.getAttribute('data-theme'),
    )
    expect(
      after,
      `Clicking the theme toggle did not flip data-theme on <html>: before=${before}, after=${after}. Check that ThemeToggle.tsx wires onClick → useTheme().toggle and that useTheme writes to documentElement.setAttribute('data-theme', t).`,
    ).not.toBe(before)
    expect(after).toMatch(/^(dark|light)$/)

    // localStorage persistence — the next page load should respect the flip.
    const persisted = await page.evaluate(() => window.localStorage.getItem('oo-theme'))
    expect(
      persisted,
      `Theme flip did not persist to localStorage[oo-theme] (got "${persisted}"). The persistence path is the only thing the index.html bootstrap script reads on subsequent loads — without it, the user's theme choice resets on every page navigation.`,
    ).toBe(after)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test 18 — Component cards reserve 2 lines for description text
 *
 * The 4-line card grid (logo+title / desc-line-1 / desc-line-2 /
 * chip row) requires the description's vertical footprint to be
 * IDENTICAL across cards regardless of how short the actual copy
 * is — without a min-height, single-line descriptions collapsed by
 * ~14px and pulled the chip row up, leaving a visibly ragged Y for
 * the chips across a 4-card row.
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard StepComponents card description', () => {
  test.beforeEach(async ({ page }) => {
    await seedWizardStore(page, {
      currentStep: 5,
      orgName: 'Acme',
      orgIndustry: 'finance',
      orgSize: '50-200',
      orgHeadquarters: 'Frankfurt, Germany',
      topology: 'three-region-ha',
      airgap: false,
    })
    await page.goto('wizard')
  })

  test('every component card has min-h:108px and 2-line description', async ({ page }) => {
    const cards = page.locator('[data-testid^="component-card-"]')
    await expect(cards.first()).toBeVisible({ timeout: 10_000 })
    const n = await cards.count()
    expect(n, 'StepComponents grid rendered no cards — search filter or seed state broke the grid').toBeGreaterThan(0)

    // Sample up to 12 cards to keep the assertion fast while still
    // catching the regression at the grid scale (a 4×3 first viewport).
    const sampleSize = Math.min(n, 12)
    const failures: string[] = []
    const chipYs: number[] = []

    for (let i = 0; i < sampleSize; i++) {
      const card = cards.nth(i)
      const tid = (await card.getAttribute('data-testid')) ?? `idx-${i}`
      const cardBox = await card.boundingBox()
      if (!cardBox) {
        failures.push(`${tid}: bounding box null`)
        continue
      }
      if (Math.round(cardBox.height) < 108) {
        failures.push(`${tid}: card height ${Math.round(cardBox.height)}px < 108px floor`)
      }

      const desc = card.locator('.corp-comp-desc').first()
      const lineClamp = await desc.evaluate(
        (el) => window.getComputedStyle(el).webkitLineClamp,
      )
      if (lineClamp !== '2') {
        failures.push(
          `${tid}: getComputedStyle(.corp-comp-desc).webkitLineClamp = "${lineClamp}", canonical = "2" (see .corp-comp-desc rule in StepComponents.tsx)`,
        )
      }

      // Description min-height — the regression we're guarding. The
      // CSS rule is min-height: 2.5em; we read computed pixels and
      // compare to 2 × line-height × font-size. A flat-1-line desc
      // without min-height returns ~17px; with the rule it returns
      // ~31px (2.5 × 0.76rem × 1.4 lh × 16px = ~26px floor).
      const descBox = await desc.boundingBox()
      if (!descBox) {
        failures.push(`${tid}: .corp-comp-desc has no bounding box`)
      } else if (descBox.height < 26) {
        failures.push(
          `${tid}: .corp-comp-desc height ${descBox.height.toFixed(1)}px < 26px (the 2-line min-height floor); short descriptions are collapsing the card body`,
        )
      }

      // Capture the chip-row Y for the row-uniformity assertion below.
      const chips = card.locator('.corp-comp-chips').first()
      const chipsBox = await chips.boundingBox()
      if (chipsBox) chipYs.push(chipsBox.y)
    }

    expect(
      failures,
      `Card description geometry failures:\n  - ${failures.join('\n  - ')}`,
    ).toEqual([])

    // Row-uniformity guard — within a single visual row, chip-row Y
    // should be identical (sub-pixel jitter only). We only assert this
    // for cards in the SAME row, so we cluster by Y of the card itself.
    if (chipYs.length >= 2) {
      // Group sampled cards by visual row (cards within ~10px of each
      // other vertically belong to the same row).
      const rows = new Map<number, number[]>()
      for (let i = 0; i < chipYs.length; i++) {
        const card = cards.nth(i)
        const cb = await card.boundingBox()
        if (!cb) continue
        const rowKey = Math.round(cb.y / 10) * 10
        const arr = rows.get(rowKey) ?? []
        arr.push(chipYs[i]!)
        rows.set(rowKey, arr)
      }
      for (const [rowKey, ys] of rows.entries()) {
        if (ys.length < 2) continue
        const min = Math.min(...ys)
        const max = Math.max(...ys)
        expect(
          max - min,
          `Chip-row Y drifts across cards in the same visual row (rowY≈${rowKey}px): min=${min.toFixed(1)}, max=${max.toFixed(1)}, spread=${(max - min).toFixed(1)}px. Anything > 2px means descriptions of varying length pull the chip row to different Ys; the .corp-comp-desc rule must reserve the 2-line min-height (see StepComponents.tsx).`,
        ).toBeLessThanOrEqual(2)
      }
    }
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Infrastructure page (issue #227)
 *
 * Founder spec lock-in:
 *   • Bare /infrastructure URL redirects to /infrastructure/topology
 *   • Topology tab is the active default landing
 *   • Tabs are exactly Topology / Compute / Storage / Network (in that
 *     order, no accordions, no extras)
 * ────────────────────────────────────────────────────────────────── */

test.describe('@cosmetic-guard infrastructure page', () => {
  test('Infrastructure page redirects /infrastructure → /infrastructure/topology', async ({ page }) => {
    await page.goto('provision/test-deployment-id/infrastructure')
    await page.waitForLoadState('domcontentloaded')

    // Wait for the redirect to settle. TanStack-router's beforeLoad
    // redirect fires synchronously on first paint so the URL should
    // already carry the topology suffix by the time domcontentloaded
    // resolves; we still poll briefly to allow the SPA shell to
    // hydrate.
    await page.waitForFunction(
      () => window.location.pathname.endsWith('/infrastructure/topology'),
      { timeout: 5000 },
    )

    const url = new URL(page.url())
    expect(
      url.pathname.endsWith('/infrastructure/topology'),
      `Expected /infrastructure to redirect to /infrastructure/topology; got pathname=${url.pathname}. The redirect lives in src/app/router.tsx (provisionInfrastructureIndexRoute beforeLoad). Founder spec: "the infrastructure page must be opened by default with the topology page".`,
    ).toBe(true)
  })

  test('Topology tab is the active default and tabs are Topology / Compute / Storage / Network', async ({ page }) => {
    await page.goto('provision/test-deployment-id/infrastructure/topology')
    await page.waitForLoadState('domcontentloaded')

    const tablist = page.getByTestId('infrastructure-tabs')
    await expect(
      tablist,
      'Infrastructure page does not expose a [data-testid=infrastructure-tabs] tablist. Add the testid to the <nav role=tablist> in InfrastructurePage.tsx.',
    ).toBeVisible()

    // Tab order + labels.
    const expected = ['Topology', 'Compute', 'Storage', 'Network']
    for (let i = 0; i < expected.length; i++) {
      const tab = tablist.getByRole('tab').nth(i)
      const label = (await tab.textContent())?.trim()
      expect(
        label,
        `Infrastructure tab #${i} label = "${label}"; expected "${expected[i]}". Founder spec verbatim: "tabs of compute (clusters and worker nodes), storage (pvcs, buckets etc) and network (lbs, drgs, peerings etc)" — Topology is the canonical default landing.`,
      ).toBe(expected[i])
    }

    // Topology tab is the active default.
    const topologyTab = page.getByTestId('infra-tab-topology')
    const ariaSelected = await topologyTab.getAttribute('aria-selected')
    expect(
      ariaSelected,
      'Topology tab is not aria-selected by default. The default landing for /sovereign/provision/$deploymentId/infrastructure must be the topology view per founder spec.',
    ).toBe('true')

    // Topology canvas mounts (loading, error, empty, or populated state — any of those is acceptable here).
    const canvas = page.getByTestId('infrastructure-topology-canvas')
    await expect(
      canvas,
      'Topology tab is the default but the canvas frame is missing. Add data-testid=infrastructure-topology-canvas to the canvas wrapper.',
    ).toBeVisible()
  })

  test('Sidebar exposes an Infrastructure nav item that links to /infrastructure', async ({ page }) => {
    await page.goto('provision/test-deployment-id')
    await page.waitForLoadState('domcontentloaded')

    const navItem = page.getByTestId('sov-nav-infrastructure')
    await expect(
      navItem,
      'Sidebar is missing the Infrastructure nav item. Add a NAV entry with id=infrastructure pointing at /provision/$deploymentId/infrastructure (see Sidebar.tsx).',
    ).toBeVisible()

    const href = await navItem.getAttribute('href')
    expect(
      href ?? '',
      `Infrastructure nav item href = "${href}"; expected to contain /infrastructure. The link target lives in Sidebar.tsx NAV[].to.`,
    ).toMatch(/\/infrastructure/)
  })
})
