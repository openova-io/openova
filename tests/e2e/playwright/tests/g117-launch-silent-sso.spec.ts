// G117.4 #2743 AC4 — Launch button silent-SSO E2E.
//
// One mode: LIVE (SOV_FQDN-gated), a full E2E walk against a Sovereign.
// The spec assumes the sovereign-admin has a `catalyst_session` cookie set via
// $CATALYST_SESSION_COOKIE (PIN-walk produces this; we don't reach into
// bp-sso-bridge here). The spec navigates to /apps and finds a Grafana
// row, clicks Launch, opens the new tab, asserts the URL pattern, captures
// a screenshot, and asserts the redirect chain finishes within 500ms
// wall-clock.
//
// A second mode, MOCK, drove the mock backend of a console scaffold that
// was never shipped. The scaffold and the MOCK cases were removed by work
// package FND-097.
//
// TESTID contract: per #2834 the Launch button surfaces as
// `data-testid="btn-launch-app"`. This spec also accepts the older
// testid `launch-button`. Both resolve to the same `<button>` element,
// so the assertion surface is identical.
//
// Refs #2743 #2834.

import { test, expect } from '@playwright/test'

const SOV_FQDN = (process.env.SOV_FQDN || '').trim()
const CATALYST_SESSION_COOKIE = process.env.CATALYST_SESSION_COOKIE || ''

// Selector that accepts EITHER the current testid (btn-launch-app) or the
// older testid (launch-button). Both resolve to the same button element.
const LAUNCH_BTN_SELECTOR =
  '[data-testid="btn-launch-app"], [data-testid="launch-button"]'

test.describe('G117.4 #2743 AC4 — Launch silent-SSO', () => {
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
