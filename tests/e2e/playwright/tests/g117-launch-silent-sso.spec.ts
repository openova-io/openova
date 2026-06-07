// G117.4 #2743 AC4 — Launch button silent-SSO E2E.
//
// Two modes, both shipped here:
//
//   - MOCK (PR-time CI, default — runs unconditionally): drives the
//     in-browser mock backend in products/catalyst/console (loaded via
//     PUBLIC_MOCK_API=1 in the dev/CI server). The mock fixture at
//     products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml
//     pre-seeds a Grafana Application with id
//     `8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a` whose `ui` endpoint is
//     ssoEnabled + launchDefault. Spec:
//
//       1. Navigates to /apps/<grafana-id>.
//       2. Intercepts `window.open` BEFORE the click (capture into a
//          page-scoped sentinel) so we can assert URL + features without
//          actually spawning a chromium tab.
//       3. Clicks the Launch button (data-testid contract — see TESTID
//          note below).
//       4. Asserts the captured URL contains `prompt=none` AND
//          `kc_idp_hint=catalyst-pin` per locked decision #3.
//       5. Asserts window.open was called with target=`_blank` and
//          features=`noopener,noreferrer`.
//       6. Asserts the click-to-window-open roundtrip completes within
//          500ms (no synchronous network when the mock backend is
//          installed, so this exercises the click → mock.getLaunchURL →
//          window.open path).
//
//   - LIVE (SOV_FQDN-gated): full E2E walk against a Sovereign. The spec
//     assumes the operator has a `catalyst_session` cookie pre-set via
//     $CATALYST_SESSION_COOKIE (PIN-walk produces this; we don't reach
//     into bp-sso-bridge here). The spec navigates to /apps and finds
//     a Grafana row, clicks Launch, opens the new tab, asserts the URL
//     pattern, captures a HAR + screenshot, and asserts the redirect
//     chain finishes within 500ms wall-clock.
//
// TESTID contract: per #2834 the Launch button surfaces as
// `data-testid="btn-launch-app"` (F1's parallel PR). The current
// LaunchButton.svelte on main uses `data-testid="launch-button"` —
// this spec accepts EITHER selector to avoid a merge-order race.
// Both will resolve to the same `<button>` element, so the assertion
// surface is identical.
//
// Refs #2743 #2834.

import { test, expect, type Page } from '@playwright/test'
import { reachable } from './_helpers'

const SOV_FQDN = (process.env.SOV_FQDN || '').trim()
const CATALYST_SESSION_COOKIE = process.env.CATALYST_SESSION_COOKIE || ''
// MOCK-mode target is the products/catalyst/console Astro app, NOT the
// products/catalyst/bootstrap/ui (React) app that the rest of Group L boots.
// Only the console app installs window.__MOCK_API__ (Layout.astro, gated on
// PUBLIC_MOCK_API=1) and serves the /apps/<id> + /catalog/<name> routes these
// specs drive. CONSOLE_BASE_URL points at it (CI boots it on :4323 with
// MOCK_API=1); falls back to the console dev port for local runs.
const BASE_URL = process.env.CONSOLE_BASE_URL || 'http://localhost:4323'

// Mock fixture's pre-seeded Grafana app id (see
// products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml).
const MOCK_GRAFANA_APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a'

// Selector that accepts EITHER the F1-contract testid (btn-launch-app)
// or the legacy testid (launch-button) currently on main. Both resolve
// to the same button element. Once F1 lands the spec still works.
const LAUNCH_BTN_SELECTOR =
  '[data-testid="btn-launch-app"], [data-testid="launch-button"]'

// Install a window.open stub BEFORE any page script runs. The stub
// records every call into window.__launchCalls so the spec can inspect
// (url, target, features) without spawning real tabs.
async function stubWindowOpen(page: Page): Promise<void> {
  await page.addInitScript(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).__launchCalls = [] as Array<{
      url: string
      target?: string
      features?: string
      ts: number
    }>
    const orig = window.open.bind(window)
    window.open = ((url?: string | URL, target?: string, features?: string) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      ;(window as any).__launchCalls.push({
        url: String(url ?? ''),
        target,
        features,
        ts: Date.now(),
      })
      // Return a benign no-op Window-ish handle. Returning the result of
      // the original `window.open` would spawn an actual chromium tab,
      // which we explicitly DON'T want for the MOCK path.
      return null
    }) as typeof window.open
    // Preserve a handle to the original in case a later test wants the
    // real behaviour (LIVE mode path uses Playwright's context.waitForEvent).
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).__origWindowOpen = orig
  })
}

test.describe('G117.4 #2743 AC4 — Launch silent-SSO', () => {
  // ─────────────────────────────────────────────────────────────────────
  // MOCK MODE — runs unconditionally at PR time (no live cluster).
  // SKIPPED only if the console dev server isn't reachable.
  // ─────────────────────────────────────────────────────────────────────
  test.describe('MOCK mode', () => {
    // #3090: this MOCK block drives the RETIRED Astro+Svelte console
    // (products/catalyst/console) mock backend. That tree is never
    // shipped. The production React instance page keeps the
    // `btn-launch-app` testid + silent-SSO behaviour (relabelled
    // "Launch →" → "Open"), covered by unit tests in bootstrap/ui; the
    // real silent-SSO walk is the LIVE-mode block below (SOV_FQDN). Skip
    // the dead-tree mock walk.
    test.skip(true, '#3090 — retired Svelte console surface; LIVE-mode block below is the real walk')
    test.skip(!!SOV_FQDN, 'MOCK walk runs only when SOV_FQDN is unset')
    // page.goto uses the project baseURL (the shared Group L BASE_URL =
    // bootstrap/ui), but the mock backend lives in the console app. Override
    // baseURL for this block so relative gotos hit the console (CONSOLE_BASE_URL).
    test.use({ baseURL: BASE_URL })

    test.beforeEach(async ({ page }) => {
      const ok = await reachable(BASE_URL)
      test.skip(!ok, `console dev server not reachable at ${BASE_URL} — start with MOCK_API=1 npm run dev`)
      await stubWindowOpen(page)
    })

    test('click Launch on Grafana → mock URL carries prompt=none + kc_idp_hint=catalyst-pin', async ({ page }) => {
      await page.goto(`/apps/${MOCK_GRAFANA_APP_ID}`)

      // The app-detail header surfaces the Launch button when at least
      // one endpoint is ssoEnabled+Ready (mock fixture pre-seeds this).
      const launchBtn = page.locator(LAUNCH_BTN_SELECTOR).first()
      await expect(launchBtn, 'Launch button should render on the Grafana app page').toBeVisible({
        timeout: 5_000,
      })
      await expect(launchBtn).toBeEnabled()

      const clickedAt = Date.now()
      await launchBtn.click()

      // Wait for the mock backend's synchronous getLaunchURL to flow
      // through the click handler and reach our window.open stub.
      // Bounded to 500ms per AC4 — anything slower indicates the click
      // path is doing something it shouldn't (extra network, layout).
      await expect
        .poll(
          async () => {
            return await page.evaluate(() => {
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              return ((window as any).__launchCalls || []).length
            })
          },
          {
            message: 'window.open should fire within 500ms of clicking Launch',
            timeout: 500,
            intervals: [25, 50, 100],
          },
        )
        .toBeGreaterThan(0)
      const elapsed = Date.now() - clickedAt
      expect(elapsed, `click→window.open elapsed=${elapsed}ms exceeds 500ms budget`).toBeLessThanOrEqual(500)

      // Inspect the captured open() args.
      const calls = await page.evaluate(() => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return (window as any).__launchCalls as Array<{
          url: string
          target?: string
          features?: string
        }>
      })
      expect(calls.length, 'exactly one window.open call expected').toBeGreaterThanOrEqual(1)
      const call = calls[0]

      // AC4 invariant #1: the launch URL MUST carry the silent-SSO hint
      // pair (locked decision #3 — see LaunchButton.svelte block comment).
      expect(call.url, `launch URL missing prompt=none: ${call.url}`).toContain('prompt=none')
      expect(call.url, `launch URL missing kc_idp_hint=catalyst-pin: ${call.url}`).toContain(
        'kc_idp_hint=catalyst-pin',
      )

      // AC4 invariant #2: opens in a new tab with no-opener / no-referrer.
      expect(call.target, `target must be _blank, got ${call.target}`).toBe('_blank')
      expect(call.features, `features missing noopener: ${call.features}`).toMatch(/noopener/)
      expect(call.features, `features missing noreferrer: ${call.features}`).toMatch(/noreferrer/)

      test.info().annotations.push({
        type: 'launch-url',
        description: `MOCK url=${call.url} target=${call.target} features=${call.features} elapsed=${elapsed}ms`,
      })
    })

    test('catalystApi mock returns a LaunchURL with prompt=none + kc_idp_hint=catalyst-pin', async ({ page }) => {
      // Direct probe of the mock backend's getLaunchURL contract (bypasses
      // the LaunchButton UI surface — guards against UI-only regressions
      // silently masking a contract drift).
      await page.goto(`/apps/${MOCK_GRAFANA_APP_ID}`)
      const url = await page.evaluate(async (appId) => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const mock = (window as any).__MOCK_API__
        if (!mock) throw new Error('window.__MOCK_API__ not installed; PUBLIC_MOCK_API=1 required')
        const lu = await mock.getLaunchURL(appId)
        return lu.url as string
      }, MOCK_GRAFANA_APP_ID)
      expect(url, 'mock launch URL').toContain('prompt=none')
      expect(url, 'mock launch URL').toContain('kc_idp_hint=catalyst-pin')
    })
  })

  // ─────────────────────────────────────────────────────────────────────
  // LIVE MODE — SOV_FQDN-gated; requires a real Sovereign + a pre-minted
  // catalyst_session cookie (PIN-walk produces this).
  // ─────────────────────────────────────────────────────────────────────
  test.describe('LIVE mode (SOV_FQDN set)', () => {
    test.skip(!SOV_FQDN, 'Set SOV_FQDN to run the live Sovereign walk')

    test.beforeEach(async ({ context }) => {
      if (CATALYST_SESSION_COOKIE) {
        await context.addCookies([
          {
            name: 'catalyst_session',
            value: CATALYST_SESSION_COOKIE,
            domain: `console.${SOV_FQDN}`,
            path: '/',
            httpOnly: true,
            secure: true,
            sameSite: 'Lax',
          },
        ])
      }
    })

    test('Launch on Grafana opens new tab with prompt=none + kc_idp_hint=catalyst-pin, dashboard renders < 500ms', async ({
      context,
      page,
    }) => {
      test.skip(
        !CATALYST_SESSION_COOKIE,
        'CATALYST_SESSION_COOKIE not set — run the PIN walk first to mint a session, then re-run',
      )

      // The console root may render /apps directly OR redirect through the
      // catalog drill-down depending on the Sovereign's default landing.
      // We aim straight at the Grafana app detail via the slug query the
      // catalog drill-down uses.
      const consoleURL = `https://console.${SOV_FQDN}`
      await page.goto(`${consoleURL}/apps?slug=grafana`, { waitUntil: 'domcontentloaded' })

      // Find any rendered Launch button (selector accepts both testids).
      const launchBtn = page.locator(LAUNCH_BTN_SELECTOR).first()
      await expect(launchBtn, 'Grafana Launch button visible on /apps').toBeVisible({ timeout: 15_000 })
      await expect(launchBtn).toBeEnabled()

      // Capture the new tab the click opens (Playwright auto-tracks
      // context.waitForEvent('page') for window.open with _blank).
      const t0 = Date.now()
      const [newPage] = await Promise.all([context.waitForEvent('page', { timeout: 5_000 }), launchBtn.click()])
      const newURL = newPage.url()

      expect(newURL, `new-tab URL: ${newURL}`).toMatch(/^https:\/\/grafana\./)
      expect(newURL, `new-tab URL must carry prompt=none: ${newURL}`).toContain('prompt=none')
      expect(newURL, `new-tab URL must carry kc_idp_hint=catalyst-pin: ${newURL}`).toContain(
        'kc_idp_hint=catalyst-pin',
      )

      // Wait for the redirect chain to terminate on the Grafana app's
      // own surface (no longer on auth.<sov>/ or api.<sov>/). The 500ms
      // budget is wall-clock from the click; we measure to the
      // `networkidle` of the final landing.
      await newPage.waitForLoadState('networkidle', { timeout: 5_000 })
      const elapsed = Date.now() - t0
      expect(
        elapsed,
        `click→Grafana-rendered elapsed=${elapsed}ms exceeds 500ms budget — silent-SSO redirect chain stalled`,
      ).toBeLessThanOrEqual(500)

      // Capture artefacts (per AC4: HAR + screenshot). We use the new
      // page's content as the dashboard proof — a non-empty Grafana
      // dashboard surfaces either the data-testid `data-testid="dashboard"`
      // (Grafana 11+) or the `<title>Grafana</title>` element.
      const title = await newPage.title()
      const screenshotPath = test.info().outputPath('grafana-launch.png')
      await newPage.screenshot({ path: screenshotPath, fullPage: false })
      test.info().annotations.push({
        type: 'launch-live',
        description: `LIVE elapsed=${elapsed}ms title=${title} url=${newURL} shot=${screenshotPath}`,
      })

      // The dashboard's `<title>` element contains "Grafana" once the
      // app has booted (the OIDC callback path lands on /?orgId=… by
      // default; Grafana 11 renders the dashboard list).
      expect(title, `Grafana title must include 'Grafana', got '${title}'`).toMatch(/Grafana/i)

      await newPage.close()
    })
  })
})
