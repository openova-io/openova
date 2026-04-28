// #143 — Smoke: admin app voucher (PromoCode) UI works.
//
// What this test asserts:
//
//   1. The SME Admin app (core/admin) is reachable at `${ADMIN_BASE_URL}/billing`
//      (Astro page billing.astro hosts BillingPage.svelte).
//
//   2. The "Vouchers (Promo Codes)" section renders — this is the
//      sovereign-admin-visible surface introduced in #115 (see comments in
//      core/admin/src/components/BillingPage.svelte lines 14-17, 216-219).
//
//   3. The new-promo form fields are present (Code, Credit OMR, Description,
//      Add/Update button) — the issue UI on the surface, before any backend
//      mutation. We don't fire the POST in this smoke test because the admin
//      app requires real auth + a running catalyst-api; the smoke test
//      validates DOM presence so a regression in the Svelte template surfaces
//      immediately at PR time.
//
// Note on the original ticket wording ("status=ISSUED / status=REVOKED"):
// The shipped UI uses a PromoCode model with an `active` boolean toggle, NOT
// an ISSUED/REVOKED status enum. Per principle #1 ("never speculate"), we
// test the actual UI shape — toggling `active` is the existing
// equivalent of revoke. Filing a status-naming alignment is out of scope
// for this smoke test.
//
// Auth: admin pages gate behind a real OAuth/session backend. In CI/dev
// without that backend, the page may redirect to /login. We accept either
// outcome and check whichever surface is rendered, so the test is honest
// about what's exercisable without a full stack.

import { test, expect } from '@playwright/test'
import { reachable } from './_helpers'

// Admin app's astro base path is `/nova` (see core/admin/astro.config.mjs).
// ADMIN_BASE_URL points at the host:port; ADMIN_PATH_PREFIX defaults to
// `/nova` and can be overridden if a future deployment changes it.
const ADMIN_BASE_URL = process.env.ADMIN_BASE_URL || 'http://localhost:4323'
const ADMIN_PATH_PREFIX = process.env.ADMIN_PATH_PREFIX ?? '/nova'
const VOUCHERS_URL = `${ADMIN_BASE_URL}${ADMIN_PATH_PREFIX}/billing`

test.describe('#143 admin vouchers UI smoke', () => {
  test.beforeAll(async () => {
    const ok = await reachable(VOUCHERS_URL)
    test.skip(!ok, `Admin app not reachable at ${VOUCHERS_URL} — run \`npm run dev\` in core/admin or set ADMIN_BASE_URL/ADMIN_PATH_PREFIX`)
  })

  test('billing page loads (or redirects to login)', async ({ page }) => {
    const res = await page.goto(VOUCHERS_URL)
    expect(res, 'page navigation produced a response').not.toBeNull()
    expect(res!.status()).toBeLessThan(500)

    // The admin shell hydrates, fails to find an `sme-admin-token` in
    // localStorage, and client-side-redirects to `${ADMIN_PATH_PREFIX}/login`.
    // We accept BOTH outcomes (authenticated → Vouchers heading; unauthenticated
    // → Admin Login heading) — either proves the app is rendering its real
    // surfaces. We give the redirect a few seconds to land before deciding.
    const loginHeading = page.getByRole('heading', { name: /Admin Login/i }).first()
    const vouchersHeading = page.getByRole('heading', { name: /Vouchers/i }).first()

    // Race the two — whichever shows up first wins.
    const winner = await Promise.race([
      loginHeading.waitFor({ state: 'visible', timeout: 10_000 }).then(() => 'login' as const).catch(() => null),
      vouchersHeading.waitFor({ state: 'visible', timeout: 10_000 }).then(() => 'vouchers' as const).catch(() => null),
    ])
    expect(winner, 'either Admin Login or Vouchers heading should appear').not.toBeNull()

    if (winner === 'login') {
      test.info().annotations.push({ type: 'note', description: 'Admin gated behind login — voucher UI not asserted (set ADMIN_TEST_COOKIE for full coverage).' })
    }
  })

  test('voucher form fields render when authenticated', async ({ page, context }) => {
    // Inject test admin auth if a cookie is provided. Format: name=value
    // (single cookie, scoped to the admin host). Production cookie name is
    // typically `catalyst_session` or similar; we keep the env contract
    // generic so the test harness can supply whatever the live system uses.
    const cookie = process.env.ADMIN_TEST_COOKIE
    if (cookie) {
      const [name, ...rest] = cookie.split('=')
      const value = rest.join('=')
      const url = new URL(ADMIN_BASE_URL)
      await context.addCookies([
        { name, value, domain: url.hostname, path: '/', httpOnly: false, secure: url.protocol === 'https:' },
      ])
    }

    await page.goto(VOUCHERS_URL)

    // Wait briefly for the AdminShell mount-effect to either land on the
    // Admin Login heading (no token) or render the Vouchers heading
    // (authenticated). We then skip if no auth is available.
    const loginHeading = page.getByRole('heading', { name: /Admin Login/i }).first()
    const vouchersHeading = page.getByRole('heading', { name: /^Vouchers/i }).first()

    const winner = await Promise.race([
      loginHeading.waitFor({ state: 'visible', timeout: 5_000 }).then(() => 'login' as const).catch(() => null),
      vouchersHeading.waitFor({ state: 'visible', timeout: 5_000 }).then(() => 'vouchers' as const).catch(() => null),
    ])
    test.skip(winner !== 'vouchers', `Admin gated behind login and no usable ADMIN_TEST_COOKIE provided (winner=${winner ?? 'timeout'}).`)

    // Voucher creation form — see BillingPage.svelte lines 227-264.
    // We assert the labelled inputs exist (Code, Credit OMR, Description)
    // and a submit button labelled "Add / Update".
    await expect(page.locator('input[placeholder*="OPENOVA" i]')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('input[type="number"]').first()).toBeVisible()
    await expect(page.getByRole('button', { name: /Add\s*\/\s*Update/i })).toBeVisible()

    // Table heading row exists (Code / Credit / Description / Used / Active).
    await expect(page.locator('th', { hasText: /Code/i }).first()).toBeVisible()
    await expect(page.locator('th', { hasText: /Active/i }).first()).toBeVisible()
  })
})
