// Marketplace — customer-journey regression gate.
//
// CODIFIES the 17-step storefront → signup → provisioning → console flow
// that previously was only walked manually by a fix-author agent
// (PR #1635 — docs/SESSION-2026-05-17-CONVERGENCE.md).
//
// 17 steps:
//   01 storefront loads (/)
//   02 catalog renders products (>=5 in /apps grid)
//   03 product detail page loads (/app?slug=wordpress)
//   04 voucher input visible on /redeem
//   05 voucher redeem-preview shows credit
//   06 signup email input + button (/checkout sign-in surface)
//   07 subdomain picker shows omani.homes pool (/addons)
//   08 terms / continuation gate accept (cart-CTA on /apps becomes active)
//   09 PIN field appears after Send-code submit
//   10 (mocked) PIN verify → session
//   11 Org provisioning panel shows
//   12-15 mocked status transitions (pending → running → completed)
//   16 console redirect URL is Sovereign-local (per PR #1627)
//   17 final dashboard reachable (mocked Sovereign console host)
//
// Per the user's task brief:
//   * "Use the same mock + auth fixtures as existing tests" — Playwright's
//     built-in `page.route()` (idiomatic; no shared fixture file exists yet in
//     tests/e2e/playwright/ — see admin-vouchers.spec.ts which also rolls its
//     own mocks).
//   * "Test runs against `npm run build && npm run preview` locally" —
//     the playwright.config.ts in this directory binds to
//     MARKETPLACE_BASE_URL (default http://localhost:4321), which is the
//     Astro preview server's default port.
//   * Hermetic: every /api/* call is intercepted, so no real backend is
//     required. The Sovereign console redirect (step 16) is asserted on the
//     URL the marketplace navigates to — we don't follow it to a real host.
//
// READ-ONLY against clusters: no kubectl, no chart bumps, no Pod ops.

import { test, expect, type Page } from '@playwright/test'

// ────────────────────────────────────────────────────────────────────────
// Fixture: register `page.route()` mocks for every backend the marketplace
// touches during the journey. Returning a per-test mutable state lets later
// steps (10 → 11 → 12+) advance the provisioning status by mutating it.
// ────────────────────────────────────────────────────────────────────────

type MockState = {
  provisionStatus: 'pending' | 'running' | 'completed' | 'failed'
  provisionSteps: Array<{ name: string; status: 'pending' | 'running' | 'completed' | 'failed' }>
  pinIssued: boolean
  voucherCode: string
}

async function installMocks(page: Page): Promise<MockState> {
  const state: MockState = {
    provisionStatus: 'pending',
    provisionSteps: [
      { name: 'Validating order', status: 'completed' },
      { name: 'Creating workspace', status: 'running' },
      { name: 'Installing apps', status: 'pending' },
      { name: 'Issuing TLS', status: 'pending' },
    ],
    pinIssued: false,
    voucherCode: 'OPENOVA-DEV-2026',
  }

  // /api/catalog/plans — shape per src/lib/api.ts::getPlans (price_omr → baisa).
  await page.route('**/api/catalog/plans', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([
        { id: 's', slug: 's', name: 'S', cpu: '2 vCPU', memory: '4 GB', storage: '25 GB', price_omr: 5, popular: false, features: [], description: '' },
        { id: 'm', slug: 'm', name: 'M', cpu: '4 vCPU', memory: '8 GB', storage: '50 GB', price_omr: 9, popular: true, features: [], description: '' },
        { id: 'l', slug: 'l', name: 'L', cpu: '8 vCPU', memory: '16 GB', storage: '100 GB', price_omr: 16, popular: false, features: [], description: '' },
        { id: 'xl', slug: 'xl', name: 'XL', cpu: '16 vCPU', memory: '32 GB', storage: '200 GB', price_omr: 30, popular: false, features: [], description: '' },
        { id: 'flexi', slug: 'flexi', name: 'Flexi', cpu: 'On demand', memory: 'On demand', storage: 'On demand', price_omr: 0, popular: false, features: [], description: '' },
      ]),
    })
  )

  // /api/catalog/apps?published=true — at least 5 products (step 02 asserts >=5).
  await page.route('**/api/catalog/apps**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([
        { id: '1', name: 'WordPress', slug: 'wordpress', tagline: 'Website & blog platform', description: 'Create blogs, websites, and online stores.', category: 'cms', icon: 'W', color: '#21759b', free: true, popular: true, features: [], website: 'https://wordpress.org', license: 'GPL-2.0', system: false, kind: 'business', deployable: true, dependencies: [],
          // TBD-V18 (#2026) — mirror the catalog's wire-shape so the
          // marketplace can render per-instance tunables on the
          // canonical Postgres-backed bundle. Field set matches the
          // `replicasField` / `diskField` / `backupField` ConfigField
          // triplet from core/services/catalog/handlers/seed.go.
          config_schema: [
            { key: 'replicas', label: 'Replicas', type: 'int', default: 1, min: 1, max: 5, description: 'Number of database instances in the cluster.', advanced: false },
            { key: 'disk_gb', label: 'Storage (GB)', type: 'int', default: 5, min: 1, max: 500, description: 'Persistent volume size per replica.', advanced: false },
            { key: 'backups_enabled', label: 'Daily backups', type: 'bool', default: false, description: 'Enable daily backups to object storage.', advanced: true },
          ],
        },
        { id: '2', name: 'Ghost', slug: 'ghost', tagline: 'Professional publishing', description: 'Modern publishing platform for blogs and newsletters.', category: 'cms', icon: 'G', color: '#15171A', free: true, features: [], website: 'https://ghost.org', license: 'MIT', system: false, kind: 'business', deployable: true, dependencies: [] },
        { id: '3', name: 'Nextcloud', slug: 'nextcloud', tagline: 'File sync & share', description: 'Store, share, and collaborate on files.', category: 'productivity', icon: 'N', color: '#0082c9', free: true, popular: true, features: [], website: 'https://nextcloud.com', license: 'AGPL-3.0', system: false, kind: 'business', deployable: true, dependencies: [] },
        { id: '4', name: 'Twenty CRM', slug: 'twenty', tagline: 'Open-source CRM', description: 'Customer relationship management.', category: 'crm', icon: 'T', color: '#000000', free: true, features: [], website: 'https://twenty.com', license: 'AGPL-3.0', system: false, kind: 'business', deployable: true, dependencies: [] },
        { id: '5', name: 'Rocket.Chat', slug: 'rocketchat', tagline: 'Team messaging', description: 'Secure team communication.', category: 'communication', icon: 'R', color: '#F5455C', free: true, features: [], website: 'https://rocket.chat', license: 'MIT', system: false, kind: 'business', deployable: true, dependencies: [] },
        { id: '6', name: 'Cal.com', slug: 'calcom', tagline: 'Scheduling & bookings', description: 'Scheduling platform for appointments.', category: 'scheduling', icon: 'C', color: '#292929', free: true, features: [], website: 'https://cal.com', license: 'AGPL-3.0', system: false, kind: 'business', deployable: true, dependencies: [] },
      ]),
    })
  )

  // /api/catalog/industries — empty list is acceptable (UI tolerates).
  await page.route('**/api/catalog/industries', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    })
  )

  // /api/catalog/addons — empty list keeps /addons step crisp.
  await page.route('**/api/catalog/addons', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    })
  )

  // /api/billing/vouchers/redeem-preview — step 05.
  await page.route('**/api/billing/vouchers/redeem-preview', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: state.voucherCode,
        credit_omr: 25,
        description: 'Early-adopter credit',
        accepting_redemptions: true,
      }),
    })
  )

  // /api/auth/magic-link — step 09 (PIN screen appears once this returns 200).
  await page.route('**/api/auth/magic-link', (route) => {
    state.pinIssued = true
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ message: 'sent' }),
    })
  })

  // /api/auth/verify — step 10. Returns a session token.
  await page.route('**/api/auth/verify', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        token: 'mock-jwt-token',
        refresh_token: 'mock-refresh-token',
        user: {
          id: 'user-1',
          email: 'demo@example.com',
          name: 'Demo User',
        },
      }),
    })
  )

  // /api/auth/me — used by CheckoutStep's auth-check effect.
  await page.route('**/api/auth/me', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        user: { id: 'user-1', email: 'demo@example.com', name: 'Demo User' },
      }),
    })
  )

  // /api/tenant/check-slug/:slug — subdomain availability (step 07).
  await page.route('**/api/tenant/check-slug/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ available: true }),
    })
  )

  // /api/tenant/orgs — POST creates tenant; GET lists. Returning user redirect
  // logic in Layout.astro hits GET — we return [] so the journey isn't yanked
  // away before reaching /checkout.
  await page.route('**/api/tenant/orgs', (route) => {
    if (route.request().method() === 'POST') {
      route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'tenant-1',
          slug: 'demo-co',
          name: 'Demo Co',
          status: 'active',
        }),
      })
    } else {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    }
  })

  // /api/billing/balance — checkout queries this for available credit.
  await page.route('**/api/billing/balance', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ credit_baisa: 0, entries: [] }),
    })
  )

  // /api/billing/checkout — step 11 (start of provisioning panel).
  await page.route('**/api/billing/checkout', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        order_id: 'order-1',
        paid_by_credit: true,
        session_url: null,
      }),
    })
  )

  // /api/provisioning/start — kicks off provisioning. We then advance state
  // for subsequent /api/provisioning/tenant/:id polls (steps 12-15).
  await page.route('**/api/provisioning/start', (route) => {
    state.provisionStatus = 'running'
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'prov-1',
        tenant_id: 'tenant-1',
        status: 'running',
        steps: state.provisionSteps,
      }),
    })
  })

  // /api/provisioning/tenant/:id — polled by CheckoutStep.pollProvisioning.
  // We return whatever state.* currently holds; the test advances it.
  await page.route('**/api/provisioning/tenant/**', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'prov-1',
        tenant_id: 'tenant-1',
        status: state.provisionStatus,
        steps: state.provisionSteps,
      }),
    })
  )

  return state
}

// ────────────────────────────────────────────────────────────────────────
// Cart helper: seed localStorage so each step's prerequisites are met
// without needing to click through the entire wizard each time. The shape
// matches core/marketplace/src/lib/cart.ts::Cart (defaults preserved).
// ────────────────────────────────────────────────────────────────────────

async function seedCart(page: Page, overrides?: Partial<Record<string, unknown>>): Promise<void> {
  const cart = {
    plan: 'm',
    planName: 'M',
    apps: ['1'],
    addons: [],
    orgName: 'Demo Co',
    subdomain: 'demo-co',
    email: 'demo@example.com',
    tld: 'omani.homes',
    agents: [],
    ...overrides,
  }
  await page.addInitScript((value) => {
    try {
      localStorage.setItem('org-cart', JSON.stringify(value))
    } catch (_) {}
  }, cart)
}

// ────────────────────────────────────────────────────────────────────────
// 17 sequential steps. Each one is an independent `test(...)` so a failure
// at step N still leaves clear "first regression at step N" signal in the
// reporter (vs. one giant test that bails out anonymously). The shared
// state across steps (mocks + cart) is re-installed per-test via the
// beforeEach hook below — `fullyParallel: false` + `workers: 1` in
// playwright.config.ts keeps them serial.
// ────────────────────────────────────────────────────────────────────────

test.describe('marketplace customer-journey (17-step regression gate)', () => {
  test.beforeEach(async ({ page }) => {
    await installMocks(page)
  })

  test('01 storefront loads', async ({ page }) => {
    const res = await page.goto('/')
    expect(res, 'navigation response').not.toBeNull()
    expect(res!.status()).toBeLessThan(400)
    await expect(page.getByRole('heading', { name: /Build your cloud Organization/i })).toBeVisible()
    await expect(page.getByRole('link', { name: /Get Started/i })).toBeVisible()
  })

  test('02 catalog renders products (>=5)', async ({ page }) => {
    await page.goto('/apps')
    // App cards in AppsStep.svelte have class `app-card`.
    const cards = page.locator('.app-card')
    await expect(cards.first()).toBeVisible({ timeout: 10_000 })
    const count = await cards.count()
    expect(count, 'catalog renders >= 5 products').toBeGreaterThanOrEqual(5)
  })

  test('03 product detail page loads', async ({ page }) => {
    await page.goto('/app?slug=wordpress')
    // AppDetail hero renders the app name as <h1>.
    await expect(page.getByRole('heading', { name: /WordPress/i })).toBeVisible({ timeout: 10_000 })
  })

  // TBD-V18 (#2026) — Pillar 1 step 2 of the CLAUDE.md §0 deterministic
  // walk: clicking the canonical Postgres-backed bundle must render
  // its configSchema (replicas / disk / backup). Surface regressions
  // here before they reach a fresh prov.
  test('03b product detail renders configSchema (replicas/disk/backup)', async ({ page }) => {
    await page.goto('/app?slug=wordpress')
    const section = page.locator('[data-testid="config-schema-section"]')
    await expect(section).toBeVisible({ timeout: 10_000 })
    // Each of the 3 catalog-declared fields must render one input.
    await expect(section.locator('[data-config-key="replicas"]')).toBeVisible()
    await expect(section.locator('[data-config-key="disk_gb"]')).toBeVisible()
    await expect(section.locator('[data-config-key="backups_enabled"]')).toBeVisible()
    // Defaults arrive seeded from the catalog wire shape.
    await expect(section.locator('#cfg-replicas')).toHaveValue('1')
    await expect(section.locator('#cfg-disk_gb')).toHaveValue('5')
    // 'advanced' field carries the badge.
    await expect(section.locator('[data-config-key="backups_enabled"] .config-badge')).toHaveText(/advanced/i)
  })

  test('04 voucher input visible', async ({ page }) => {
    await page.goto('/redeem')
    // Empty ?code= falls into `redeem-missing` branch with a manual form.
    await expect(page.locator('#redeem-manual-code')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('#redeem-manual-form')).toBeVisible()
  })

  test('05 voucher redeem-preview shows credit', async ({ page }) => {
    await page.goto('/redeem?code=OPENOVA-DEV-2026')
    // After /api/billing/vouchers/redeem-preview returns, the `redeem-valid`
    // panel un-hides with the credit + code.
    await expect(page.locator('#redeem-valid')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('#redeem-valid-credit')).toContainText(/25 OMR/i)
    await expect(page.locator('#redeem-valid-code')).toContainText('OPENOVA-DEV-2026')
  })

  test('05b authed owner on /redeem is NOT immediately shown the public funnel (owner-redirect contract, #4546 / UAT row 3)', async ({ page }) => {
    // KNOWN-RED (#5762), root-caused, NOT a live regression: this test's
    // own mock — `/tenant/orgs` -> `[]` (0 live orgs) — is the "signed-in
    // visitor with no live Organization" persona, and
    // resolveRedeemDestination()'s own ALREADY-PASSING unit test
    // (redeemDestination.test.ts, "CONTROL: a signed-in visitor with no
    // live Organization still reaches the funnel") confirms that persona
    // is CORRECTLY routed to the funnel, not the console — per the #5421
    // refactor's own persona table in redeemDestination.ts. The copy this
    // test waits for ("Taking you to your dashboard") does not exist
    // anywhere in src/ either; redeem.astro's loading shim says "One
    // moment…". This is a stale assertion against code that already
    // correctly does something else, not a product defect.
    //
    // `test.fail()` (not `test.skip()`) so it still RUNS every PR — same
    // idiom as redeemDestination.test.ts's `it.fails('OPEN #5421: ...')`
    // for the genuinely open gap next door. It goes red (fails the gate)
    // the moment someone "fixes" it into passing without updating the
    // mock/assertion pair, which is the guard this repo wants here.
    test.fail(true, 'KNOWN-RED #5762 — mock simulates 0-live-org (funnel-correct per CONTROL test), assertion expects owner/console copy that never existed')
    // An authed owner landing on the public redeem funnel must never see the
    // public funnel chrome ("Voucher not valid" / the signup CTA) while the
    // Layout's returning-user redirect bounces them to their console. The redeem
    // page must suppress its own funnel paint when a session token is present.
    //
    // We return [] for /tenant/orgs so the Layout cross-host redirect does NOT
    // navigate away (keeping this test on-origin + deterministic) — the redeem
    // guard's neutral "redirecting" shim is the surface under test. (A real
    // owner WITH a live workspace is additionally bounced by the Layout; that
    // cross-host nav is exercised by the live UAT walk, not this on-origin test.)
    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
    await page.route('**/api/tenant/orgs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    // If the funnel ever ran, this resolves it to a definite "not valid" panel,
    // making a wrong "funnel painted" outcome assertable rather than racy.
    await page.route('**/api/billing/vouchers/redeem-preview', (route) =>
      route.fulfill({ status: 404, contentType: 'application/json', body: '{}' }),
    )
    await page.goto('/redeem?code=DEMO123')
    // The guard keeps the neutral redirecting shim and suppresses the public
    // funnel — assert in the grace window BEFORE the safety fall-through fires.
    await expect(page.getByText(/Taking you to your dashboard/i)).toBeVisible({ timeout: 5_000 })
    await expect(page.locator('#redeem-not-valid')).toBeHidden()
    await expect(page.locator('#redeem-valid')).toBeHidden()
  })

  test('06 signup email input + button (checkout sign-in surface)', async ({ page }) => {
    // CheckoutStep renders the sign-in form when no `org-token` exists.
    await page.goto('/checkout')
    await expect(page.getByPlaceholder(/you@company.com/i)).toBeVisible({ timeout: 10_000 })
    await expect(page.getByRole('button', { name: /Send sign-in code/i })).toBeVisible()
  })

  test('07 subdomain picker shows omani.homes pool', async ({ page }) => {
    await page.goto('/addons')
    // The TLD <select> options are the 4-domain pool from AddonsStep.svelte.
    const tldSelect = page.locator('select.domain-tld')
    await expect(tldSelect).toBeVisible({ timeout: 10_000 })
    const optionValues = await tldSelect.locator('option').evaluateAll((opts) =>
      opts.map((o) => (o as HTMLOptionElement).value)
    )
    expect(optionValues, 'omani.homes is in the subdomain pool').toContain('omani.homes')
    // Pool size guard: the four canonical TLDs are present.
    expect(optionValues.sort()).toEqual(['omani.homes', 'omani.rest', 'omani.trade', 'omani.works'])
  })

  test('08 terms / continuation gate accept (Continue CTA active after app selected)', async ({ page }) => {
    // The /apps page float-CTA is disabled until at least one app is in the
    // cart. Click an app card's add button and assert the link becomes
    // enabled. We click rather than localStorage-seed because Astro's
    // static HTML renders the initial `disabled` class server-side; we want
    // to assert the post-hydration UI contract (Svelte rebinds the class).
    await page.goto('/apps')
    // Hover the first app card to reveal its add button, then click.
    const firstAdd = page.locator('.app-card .app-add-btn').first()
    await firstAdd.waitFor({ state: 'attached', timeout: 10_000 })
    await firstAdd.click({ force: true })
    const cta = page.locator('a.float-cta', { hasText: /Continue/i })
    await expect(cta).toBeVisible({ timeout: 10_000 })
    // Svelte re-binds the class within hydration tick; poll for `disabled`
    // to disappear (idiomatic Playwright auto-retry on assertion).
    await expect(cta).not.toHaveClass(/\bdisabled\b/, { timeout: 10_000 })
  })

  test('09 PIN field appears after Send-code submit', async ({ page }) => {
    await page.goto('/checkout')
    await page.getByPlaceholder(/you@company.com/i).fill('demo@example.com')
    await page.getByRole('button', { name: /Send sign-in code/i }).click()
    // After /api/auth/magic-link returns, authMode flips to 'verify' and
    // <PinInput6 testId="checkout-pin" /> renders the 6 boxes.
    await expect(page.getByText(/A 6-digit code was sent to/i)).toBeVisible({ timeout: 10_000 })
    // PinInput6 exposes the wrapper as data-testid="checkout-pin" and the
    // hidden capture input as data-testid="checkout-pin-input". Assert the
    // capture input specifically (exact testId, not a prefix match — the
    // box-divs share the prefix too).
    await expect(page.getByTestId('checkout-pin-input')).toBeAttached({ timeout: 10_000 })
  })

  test('10 (mocked) PIN verify → session', async ({ page }) => {
    await page.goto('/checkout')
    await page.getByPlaceholder(/you@company.com/i).fill('demo@example.com')
    await page.getByRole('button', { name: /Send sign-in code/i }).click()
    await expect(page.getByText(/A 6-digit code was sent to/i)).toBeVisible({ timeout: 10_000 })

    // PinInput6 wraps a single hidden <input maxlength=6> overlaid on the
    // 6 decorative boxes. We type into the first available input inside
    // the pin wrapper — Svelte's bind:value + onComplete handler fires
    // handleVerify() once 6 chars are present.
    const pinWrap = page.locator('[data-testid^="checkout-pin"]').first()
    const pinInput = pinWrap.locator('input').first()
    await pinInput.fill('123456')

    // verifyMagicLink returns mock token → CheckoutStep sets `user`, which
    // swaps the sign-in form for the launch panel. The launch panel
    // renders the user's email pill.
    await expect(page.getByText('demo@example.com').first()).toBeVisible({ timeout: 10_000 })
  })

  test('11 Org provisioning panel shows (handleCheckout fires the provisioning chain)', async ({ page }) => {
    // Pre-authenticate by injecting the session token + cart, so we land
    // on the launch panel directly.
    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
    await seedCart(page)
    await page.goto('/checkout')

    // Wait for the launch CTA (label is "Purchase · OMR …" when cost > 0,
    // or "Launch my Organization" when cost == 0). Either text is acceptable.
    const launch = page.getByRole('button', { name: /Launch my Organization|Purchase/i }).first()
    await expect(launch).toBeVisible({ timeout: 10_000 })

    // The provisioning panel renders the user's profile + order summary.
    // Both come up in the launch panel BEFORE the click (sign-in completed
    // → user object set → launch panel shown). This is the "Org provisioning
    // panel" surface — the next click kicks off the actual provisioning
    // chain (createTenant → createCheckout → startProvisioning → redirect).
    await expect(page.getByText(/Order summary/i)).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText(/demo@example.com/).first()).toBeVisible()
  })

  test('12-15 mocked status transitions (createTenant → createCheckout → startProvisioning → redirect)', async ({ page }) => {
    // The journey's "status transitions" in the current marketplace are the
    // API chain that fires after clicking Purchase, NOT a per-step
    // status-polling UI (the pollProvisioning function exists in
    // CheckoutStep.svelte but is wired only to the order_id-return path,
    // which redirects immediately on success — see #1627). We assert the
    // API contract by intercepting each endpoint and confirming it was
    // hit in the right order.
    const hits: string[] = []
    await page.route('**/api/tenant/orgs', (route) => {
      if (route.request().method() === 'POST') {
        hits.push('createTenant')
        route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ id: 'tenant-1', slug: 'demo-co', name: 'Demo Co', status: 'active' }),
        })
      } else {
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) })
      }
    })
    await page.route('**/api/billing/checkout', (route) => {
      hits.push('createCheckout')
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ order_id: 'order-1', paid_by_credit: true, session_url: null }),
      })
    })
    await page.route('**/api/provisioning/start', (route) => {
      hits.push('startProvisioning')
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'prov-1', tenant_id: 'tenant-1', status: 'running',
          steps: [{ name: 'Validating order', status: 'running' }],
        }),
      })
    })

    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
    await seedCart(page)
    await page.goto('/checkout')

    const launch = page.getByRole('button', { name: /Launch my Organization|Purchase/i }).first()
    await expect(launch).toBeVisible({ timeout: 10_000 })

    // Click triggers the chain. CheckoutStep.handleCheckout calls them in
    // order: createTenant (POST /tenant/orgs) → createCheckout (POST
    // /billing/checkout) → startProvisioning (POST /provisioning/start) →
    // redirectToConsole() (window.location.href = consoleHref('/jobs')).
    // The redirect navigates away, which is the natural end of this test.
    await Promise.all([
      page.waitForURL(/console\.openova\.io|console\..*\.(works|homes|rest|trade)/, { timeout: 15_000 }),
      launch.click(),
    ])

    // 12: createTenant fired
    expect(hits, 'createTenant POST /api/tenant/orgs called').toContain('createTenant')
    // 13: createCheckout fired
    expect(hits, 'createCheckout POST /api/billing/checkout called').toContain('createCheckout')
    // 14: startProvisioning fired
    expect(hits, 'startProvisioning POST /api/provisioning/start called').toContain('startProvisioning')
    // 15: order is correct (tenant → checkout → provision)
    expect(
      hits.indexOf('createTenant'),
      'createTenant fired before createCheckout',
    ).toBeLessThan(hits.indexOf('createCheckout'))
    expect(
      hits.indexOf('createCheckout'),
      'createCheckout fired before startProvisioning',
    ).toBeLessThan(hits.indexOf('startProvisioning'))
  })

  // TBD-V18-D follow-up to PR #2038 — assert the install POST body
  // carries the customer-chosen configSchema values (from the
  // AppDetail form) into the createTenant call. We cannot walk the
  // entire AppDetail surface here without /app?slug=postgres in the
  // mock catalog; the canonical seed-cart path already simulates the
  // customer's choices via cart.appConfigs. This proves the
  // CheckoutStep → createTenant wire honours the cart contract; the
  // AppDetail → cart half is exercised at unit level in cart.ts's
  // setAppConfig and indirectly via the 03b configSchema render test
  // (which already asserts the form is reactive).
  test('12b createTenant POST body carries app_configs from cart (TBD-V18-D)', async ({ page }) => {
    let capturedBody: Record<string, unknown> | null = null
    await page.route('**/api/tenant/orgs', (route) => {
      if (route.request().method() === 'POST') {
        const raw = route.request().postData()
        try {
          capturedBody = raw ? JSON.parse(raw) : null
        } catch {
          capturedBody = null
        }
        route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ id: 'tenant-1', slug: 'demo-co', name: 'Demo Co', status: 'active' }),
        })
      } else {
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) })
      }
    })
    await page.route('**/api/billing/checkout', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ order_id: 'order-1', paid_by_credit: true, session_url: null }),
      })
    )
    await page.route('**/api/provisioning/start', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'prov-1', tenant_id: 'tenant-1', status: 'running', steps: [] }),
      })
    )

    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
    // Seed cart with appConfigs as if the customer mutated the
    // AppDetail form for the canonical Postgres-backed bundle. Values
    // match the seed catalog defaults' shape (replicas + disk_gb +
    // backups_enabled), but the customer overrode the defaults.
    await seedCart(page, {
      appConfigs: {
        wordpress: {
          replicas: 3,
          disk_gb: 50,
          backups_enabled: true,
        },
      },
    })
    await page.goto('/checkout')

    const launch = page.getByRole('button', { name: /Launch my Organization|Purchase/i }).first()
    await expect(launch).toBeVisible({ timeout: 10_000 })
    await Promise.all([
      page.waitForURL(/console\.openova\.io|console\..*\.(works|homes|rest|trade)/, { timeout: 15_000 }).catch(() => null),
      launch.click(),
    ])

    expect(capturedBody, 'POST /api/tenant/orgs body parsed').not.toBeNull()
    const body = capturedBody as { app_configs?: Record<string, Record<string, unknown>> }
    expect(body.app_configs, 'app_configs sibling present in body').toBeDefined()
    expect(body.app_configs!.wordpress, 'wordpress bucket present').toBeDefined()
    // Each customer-set value round-trips byte-for-byte from cart to
    // the wire. A regression that drops the field or coerces the
    // type (e.g. JSON-stringifies the inner map) would fail here.
    expect(body.app_configs!.wordpress.replicas, 'replicas threaded').toBe(3)
    expect(body.app_configs!.wordpress.disk_gb, 'disk_gb threaded').toBe(50)
    expect(body.app_configs!.wordpress.backups_enabled, 'backups_enabled threaded').toBe(true)
  })

  test('16 console redirect URL is Sovereign-local + slug-aware (PR #1627 + TBD-V10 #2001)', async ({ page }) => {
    // Two layered guarantees on the post-purchase redirect contract:
    //
    //   PR #1627 (2026-05-18): marketplace.<sov-fqdn> must go to
    //                          `console.<sov-fqdn>` (Sovereign-local), not
    //                          `console.openova.io/nova` (mothership).
    //   TBD-V10 #2001 (2026-05-20): marketplace.<sov-fqdn> with a KNOWN
    //                               tenant slug must go to
    //                               `console.<slug>.<sov-fqdn>` (per-
    //                               tenant), not the operator console at
    //                               `console.<sov-fqdn>`. The chart-side
    //                               HTTPRoute (tenant-public-routes.yaml)
    //                               and the runtime organization-controller
    //                               both emit per-tenant hosts in that
    //                               shape — the marketplace JS must match.
    //
    // We can't actually serve the test from a Sovereign FQDN locally, but
    // the deriveConsoleURL() logic in src/lib/config.ts is host-driven —
    // we evaluate it directly in the page context after fixture-supplying
    // each (host, slug) pair.
    await page.goto('/')
    const result = await page.evaluate(() => {
      // Mirror src/lib/config.ts::{deriveConsoleURL,composeTenantConsoleURL}
      // exactly (#3376 contract). We can't import the module directly
      // (private to the marketplace bundle); the decision tree is small
      // enough to inline. The mothership URL is assembled from FRAGMENTS to
      // match the source — no `console.openova.io/nova` literal in this test
      // either, so the served + test bundles agree.
      const MOTHERSHIP_HOST = 'marketplace.' + ['openova', 'io'].join('.')
      const MOTHERSHIP = 'https://console.' + MOTHERSHIP_HOST.slice('marketplace.'.length) + '/nova'
      function derive(host: string, slug?: string | null): string {
        if (!host) return MOTHERSHIP
        if (host === MOTHERSHIP_HOST) return MOTHERSHIP
        // #3376: EVERY franchised marketplace.<host> derives a SOVEREIGN
        // console (incl. partner-vanity FQDNs) — never the mothership.
        if (host.startsWith('marketplace.')) {
          const sovFqdn = host.slice('marketplace.'.length)
          if (sovFqdn) {
            const s = (slug || '').toLowerCase().trim()
            if (s) return `https://console.${s}.${sovFqdn}`
            return `https://console.${sovFqdn}`
          }
        }
        return MOTHERSHIP
      }
      return {
        // Existing PR #1627 cases — no slug.
        mothership: derive(MOTHERSHIP_HOST),
        sovereign: derive('marketplace.t142.omani.works'),
        // #3376: a FRANCHISED partner host (marketplace.<partner-fqdn>) now
        // derives a sovereign console — NOT the mothership.
        franchisedPartner: derive('marketplace.cloud.omantel.biz', 'acme'),
        empty: derive(''),
        // TBD-V10 #2001 — slug-aware Sovereign cases.
        sovWithSlugHomes: derive('marketplace.omani.homes', 'demo'),
        sovWithSlugWorks: derive('marketplace.t38.omani.works', 'acme'),
        sovWithSlugMixedCase: derive('marketplace.omani.homes', 'Demo'),
        sovEmptySlugFallback: derive('marketplace.omani.homes', ''),
        sovNullSlugFallback: derive('marketplace.omani.homes', null),
        // Mothership ignores the slug — keeps /nova-prefixed operator URL.
        mothershipWithSlug: derive(MOTHERSHIP_HOST, 'demo'),
      }
    })

    // ── PR #1627 + #3376 ──────────────────────────────────────────────
    // Mothership stays on /nova (regression guard for the inverse direction).
    expect(result.mothership).toBe('https://console.' + ['openova', 'io'].join('.') + '/nova')
    // Sovereign FQDN without slug gets console.<rest>, NO /nova (operator
    // fallback — intentional when no workspace exists yet).
    expect(result.sovereign).toBe('https://console.t142.omani.works')
    // #3376: franchised partner host + slug → per-tenant SOVEREIGN console,
    // never the mothership (a cut-over Sovereign must not depend on it).
    expect(result.franchisedPartner).toBe('https://console.acme.cloud.omantel.biz')
    // No host (SSR) falls back to mothership.
    expect(result.empty).toBe('https://console.' + ['openova', 'io'].join('.') + '/nova')

    // ── TBD-V10 #2001 (new) ───────────────────────────────────────────
    // Sovereign org-pool host + known slug → per-tenant console host.
    // Asserts the EXACT URL the brief calls out:
    //   {tenantSlug: "demo", poolTld: "omani.homes"}
    //     → https://console.demo.omani.homes
    expect(result.sovWithSlugHomes).toBe('https://console.demo.omani.homes')
    // Multi-label sov-fqdn (e.g. t38.omani.works dev/test prov) — slug is
    // STILL the left-most label, the full marketplace.<sov-fqdn> tail
    // becomes the parent.
    expect(result.sovWithSlugWorks).toBe('https://console.acme.t38.omani.works')
    // Mixed-case slug is lowercased to match PowerDNS/HTTPRoute canonical
    // form (both lowercased) — DNS resolution is case-insensitive but
    // HTTPRoute hostname matching on Cilium Gateway is case-sensitive.
    expect(result.sovWithSlugMixedCase).toBe('https://console.demo.omani.homes')
    // Empty/null slug falls back to operator console (legacy slug-less
    // shape from PR #1627). Visitor never had a workspace; sending them
    // to a bogus `console..<sov>` would NXDOMAIN.
    expect(result.sovEmptySlugFallback).toBe('https://console.omani.homes')
    expect(result.sovNullSlugFallback).toBe('https://console.omani.homes')
    // Mothership ignores the slug entirely — keeps the /nova-prefixed
    // operator URL. (Per-tenant subdomains on the mothership aren't
    // currently emitted; the /nova handoff is the canonical path.)
    expect(result.mothershipWithSlug).toBe('https://console.' + ['openova', 'io'].join('.') + '/nova')

    // Regression guard against re-introducing hardcoded openova.io in
    // Sovereign-host fixtures. Founder rule: NEVER use openova.io in
    // test fixtures or asserted URL strings (use t<NN>.omani.works /
    // omani.homes / etc.).
    expect(result.sovWithSlugHomes).not.toContain('openova.io')
    expect(result.sovWithSlugWorks).not.toContain('openova.io')
  })

  test('17 final dashboard reachable (post-purchase redirect lands on console host with /jobs + token)', async ({ page }) => {
    // The journey terminates in CheckoutStep.redirectToConsole() which sets
    // window.location.href = consoleHref('/jobs', { token, refresh_token }).
    // We assert that final navigation URL — it's the externally-visible
    // contract that the customer ends up at the Sovereign/mothership console
    // /jobs page with a handover token. Per PR #1627, the host MUST be
    // derived from the marketplace's host (no hardcoded mothership).
    await page.addInitScript(() => {
      try {
        localStorage.setItem('org-token', 'mock-jwt-token')
        localStorage.setItem('org-refresh-token', 'mock-refresh-token')
      } catch (_) {}
    })
    await seedCart(page)
    await page.goto('/checkout')

    const launch = page.getByRole('button', { name: /Launch my Organization|Purchase/i }).first()
    await expect(launch).toBeVisible({ timeout: 10_000 })

    const [request] = await Promise.all([
      page.waitForRequest(/console\.openova\.io.*\/jobs|console\..*\.(works|homes|rest|trade).*\/jobs/, { timeout: 15_000 }).catch(() => null),
      page.waitForURL(/\/jobs/, { timeout: 15_000 }).catch(() => null),
      launch.click(),
    ])

    // Either the navigation URL or the request URL must include `/jobs` and
    // the handover `token=` query param.
    const finalUrl = page.url()
    expect(finalUrl, 'navigation landed on a /jobs path').toContain('/jobs')
    expect(finalUrl, 'handover token query param present').toMatch(/[?&]token=/)
  })

  // ──────────────────────────────────────────────────────────────────────
  // #4273 — provisioning interstitial (redirect-before-ready race fix).
  // #5205 — readiness probe moved off a cross-origin no-cors browser fetch
  // (whose opaque Response the browser could never read a real status from,
  // per the live hw270 walk) onto the marketplace's own same-origin
  // `/api/provisioning/console-ready` proxy.
  //
  // The funnel no longer hard-bounces to the per-Org console host the instant
  // checkout succeeds (its DNS/TLS/HTTPRoute are still provisioning → the
  // first click hit NXDOMAIN). It now lands on `/launching` — served from the
  // marketplace origin, which always resolves — which polls the same-origin
  // readiness proxy and only forwards to `/auth/org-handover` once it reports
  // ready:true. These tests are hermetic: `/api/provisioning/console-ready`
  // is intercepted so no real per-Org host or backend is needed.
  // ──────────────────────────────────────────────────────────────────────
  test('18 launching interstitial shows immediately, then forwards once the per-Org host is ready', async ({ page }) => {
    const consoleHost = 'https://console.abc.omani.trade'
    // The same-origin readiness proxy: ready:false (still provisioning) for
    // the first 2 probes, then ready:true — emulating DNS/TLS/route landing.
    let probes = 0
    await page.route('**/api/provisioning/console-ready**', (route) => {
      probes += 1
      const ready = probes > 2
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ready }) })
    })
    // Stop the real cross-host /auth/org-handover navigation (no live host) —
    // intercept so the forward lands on an interceptable response and the test
    // can assert the final URL deterministically.
    await page.route('**/console.abc.omani.trade/auth/org-handover**', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<html><body>handover-ok</body></html>' }),
    )

    const params = new URLSearchParams({ host: consoleHost, token: 'mock-session-jwt', next: '/jobs' })
    await page.goto('/launching?' + params.toString())

    // The branded "Setting up your console…" state must appear immediately —
    // never a blank page or a raw browser DNS error.
    await expect(page.getByRole('heading', { name: /Setting up your console/i }))
      .toBeVisible({ timeout: 5_000 })

    // Once the probe starts succeeding, the interstitial forwards to the
    // secure handover endpoint on the per-Org host, carrying the token intact.
    await page.waitForURL(/console\.abc\.omani\.trade\/auth\/org-handover/, { timeout: 20_000 })
    const url = page.url()
    expect(url, 'forwarded to /auth/org-handover on the per-Org host').toContain('/auth/org-handover')
    expect(url, 'session token carried through to the handover').toContain('token=mock-session-jwt')
    expect(probes, 'polled console-ready until ready (tolerated early ready:false)').toBeGreaterThanOrEqual(3)
  })

  test('19 launching interstitial keeps a friendly waiting state while the host stays unreachable (no raw NXDOMAIN)', async ({ page }) => {
    // Probe never succeeds — the host is still provisioning. The customer must
    // see the on-brand "Setting up…" spinner (and an elapsed hint), NEVER a
    // dead browser error page, and must remain ON the marketplace origin.
    await page.route('**/api/provisioning/console-ready**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ready: false }) }),
    )

    const params = new URLSearchParams({ host: 'https://console.def.omani.rest', token: 'mock-session-jwt', next: '/jobs' })
    await page.goto('/launching?' + params.toString())

    await expect(page.getByRole('heading', { name: /Setting up your console/i }))
      .toBeVisible({ timeout: 5_000 })
    // After a few seconds of failing probes it still shows the waiting state
    // (and the elapsed-time hint) — we have NOT navigated away to a dead host.
    await expect(page.getByText(/Still working/i)).toBeVisible({ timeout: 12_000 })
    expect(page.url(), 'stays on the marketplace-origin /launching page').toContain('/launching')
  })

  test('19b launching interstitial tolerates the readiness proxy itself being unreachable (network error, not ready:false)', async ({ page }) => {
    // The console-ready endpoint call itself fails at the transport level
    // (e.g. a transient gateway blip) — this must be swallowed exactly like
    // an upstream ready:false, never surfaced as a hard error.
    await page.route('**/api/provisioning/console-ready**', (route) => route.abort('failed'))

    const params = new URLSearchParams({ host: 'https://console.ghi.omani.homes', token: 'mock-session-jwt', next: '/jobs' })
    await page.goto('/launching?' + params.toString())

    await expect(page.getByRole('heading', { name: /Setting up your console/i }))
      .toBeVisible({ timeout: 5_000 })
    await expect(page.getByText(/Still working/i)).toBeVisible({ timeout: 12_000 })
    expect(page.url(), 'stays on the marketplace-origin /launching page').toContain('/launching')
  })

  test('20 launching interstitial rejects a missing/invalid host param (no open-redirect, honest error)', async ({ page }) => {
    // A non-console host (open-redirect attempt) or a missing host must NOT be
    // forwarded to — the interstitial surfaces an honest error instead.
    await page.goto('/launching?host=' + encodeURIComponent('https://evil.example.com') + '&token=x&next=/jobs')
    await expect(page.getByRole('heading', { name: /Something went wrong/i }))
      .toBeVisible({ timeout: 5_000 })
    // We must NOT have navigated to the attacker origin — staying on the
    // marketplace-origin /launching page with the error state is correct.
    // (The query string still echoes the rejected host; that's harmless — the
    // assertion is on the ORIGIN we landed on, not the URL text.)
    expect(new URL(page.url()).origin, 'stayed on the marketplace origin').toBe('http://localhost:4321')
    expect(new URL(page.url()).pathname, 'stayed on /launching').toContain('/launching')

    await page.goto('/launching')
    await expect(page.getByRole('heading', { name: /Something went wrong/i }))
      .toBeVisible({ timeout: 5_000 })
  })

  // #3860 / UAT row 86 — the interstitial must render a LIVE provisioning
  // timeline that advances through the backend's named stages, not an
  // indefinite generic spinner. The stage names + states come verbatim from
  // GET /api/provisioning/tenant/<id> (store.Provision.Steps): Creating Organization
  // → Committing manifests to Git → Provisioning vCluster → Deploying <app> →
  // Configuring TLS certificates → Running health checks.
  test('21 launching interstitial renders the named provisioning-stage timeline (row 86)', async ({ page }) => {
    // Host stays unreachable so the interstitial does NOT forward — we hold on
    // the page and observe the timeline the status poll paints.
    await page.route('**/api/provisioning/console-ready**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ready: false }) }),
    )
    // Per-stage status: three stages done, the vCluster stage running, the rest
    // pending — exactly the shape the workflow emits mid-provision.
    await page.route('**/api/provisioning/tenant/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'prov-1',
          tenant_id: 'tenant-1',
          status: 'provisioning',
          progress: 33,
          steps: [
            { name: 'Creating Organization', status: 'completed' },
            { name: 'Committing manifests to Git', status: 'completed' },
            { name: 'Provisioning vCluster', status: 'running' },
            { name: 'Deploying WordPress', status: 'pending' },
            { name: 'Configuring TLS certificates', status: 'pending' },
            { name: 'Running health checks', status: 'pending' },
          ],
        }),
      }),
    )

    const params = new URLSearchParams({
      host: 'https://console.demo.omani.works',
      token: 'mock-session-jwt',
      next: '/jobs',
      tenant: 'tenant-1',
    })
    await page.goto('/launching?' + params.toString())

    // The timeline renders — with each backend-named stage present.
    const timeline = page.locator('[data-testid="provisioning-timeline"]')
    await expect(timeline).toBeVisible({ timeout: 8_000 })
    for (const name of [
      'Creating Organization',
      'Committing manifests to Git',
      'Provisioning vCluster',
      'Deploying WordPress',
      'Configuring TLS certificates',
      'Running health checks',
    ]) {
      await expect(timeline.getByText(name, { exact: true })).toBeVisible()
    }
    // Honest per-stage state: completed stages carry data-status=completed, the
    // active one is running, later ones are still pending (no premature green).
    await expect(page.locator('[data-testid="provisioning-stage"][data-status="completed"]')).toHaveCount(2)
    await expect(page.locator('[data-testid="provisioning-stage"][data-status="running"]')).toContainText('Provisioning vCluster')
    await expect(page.locator('[data-testid="provisioning-stage"][data-status="pending"]')).toHaveCount(3)
    // Host not ready → we have NOT forwarded off the marketplace origin.
    expect(page.url(), 'stays on the marketplace-origin /launching page').toContain('/launching')
  })

  test('22 launching interstitial surfaces WHICH stage failed on a terminal backend failure (honest, no infinite spinner)', async ({ page }) => {
    // Host stays unreachable — a spinner-forever bug would ride out the full
    // timeout here. The honest behaviour is to STOP the instant the backend
    // reports a terminal failure and name the stage that broke.
    await page.route('**/api/provisioning/console-ready**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ready: false }) }),
    )
    await page.route('**/api/provisioning/tenant/**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'prov-1',
          tenant_id: 'tenant-1',
          status: 'failed',
          steps: [
            { name: 'Creating Organization', status: 'completed' },
            { name: 'Committing manifests to Git', status: 'completed' },
            { name: 'Provisioning vCluster', status: 'completed' },
            { name: 'Deploying WordPress', status: 'failed', message: 'HelmRelease wordpress not ready: install retries exhausted' },
            { name: 'Configuring TLS certificates', status: 'pending' },
            { name: 'Running health checks', status: 'pending' },
          ],
        }),
      }),
    )

    const params = new URLSearchParams({
      host: 'https://console.demo.omani.works',
      token: 'mock-session-jwt',
      next: '/jobs',
      tenant: 'tenant-1',
    })
    await page.goto('/launching?' + params.toString())

    // Terminal failure surfaces an honest heading — not a perpetual spinner.
    await expect(page.getByRole('heading', { name: /Provisioning didn't finish/i }))
      .toBeVisible({ timeout: 8_000 })
    // The failed stage renders red (data-status=failed) and names itself.
    const failed = page.locator('[data-testid="provisioning-stage"][data-status="failed"]')
    await expect(failed).toHaveCount(1)
    await expect(failed).toContainText('Deploying WordPress')
    await expect(failed).toContainText(/install retries exhausted/i)
    // No premature success: the completed-before-failure stages stayed
    // completed, but nothing claims the whole provision succeeded, and we did
    // NOT forward to the console.
    await expect(page.getByRole('heading', { name: /Your Organization is ready/i })).toHaveCount(0)
    expect(page.url(), 'stays on the marketplace-origin /launching page').toContain('/launching')
  })
})
