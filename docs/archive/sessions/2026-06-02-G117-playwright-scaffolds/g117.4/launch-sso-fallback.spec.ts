// G117.4 DoD — Expired session falls back to interactive prompt=login.
//
// Verifies:
//   1. When the silent OIDC `prompt=none` request returns `error=login_required`
//      (no KC session), the Launch button retries with `prompt=login`
//   2. The fallback is observable as a SECOND window.open() call carrying
//      `prompt=login` (interactive flow)
//   3. The original `kc_idp_hint=catalyst-pin` is preserved across the fallback
//   4. If neither silent nor interactive flow succeeds, the UI surfaces a
//      clear error (not a silent failure)
//
// Promotion gate (Wave-2): unskip once the LaunchButton component implements
// the silent→interactive fallback.

import { test, expect } from '@playwright/test';

const APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';

test.describe('G117.4 — silent SSO fallback to prompt=login', () => {
  test('silent-fail → interactive retry preserves kc_idp_hint', async ({ page }) => {
    await page.addInitScript(() => {
      (window as any).__openCalls = [];
      // Simulate the first silent-SSO open returning a KC error iframe.
      (window as any).open = (url: string) => {
        (window as any).__openCalls.push(url);
        // Synthetic iframe-style postMessage of the error → UI must trigger
        // the fallback. The LaunchButton listens for this in production.
        setTimeout(() => {
          window.postMessage({ source: 'kc', error: 'login_required' }, '*');
        }, 50);
        return null;
      };
    });

    await page.goto(`/apps/${APP_ID}`);
    await page.getByTestId('launch-button').first().click();

    await page.waitForFunction(
      () => ((window as any).__openCalls as string[]).length >= 2,
      undefined,
      { timeout: 5_000 },
    );

    const calls = await page.evaluate(() => (window as any).__openCalls as string[]);
    // ASSERTION-PROOF-OF-DOD-A: first call had prompt=none.
    expect(calls[0]).toMatch(/prompt=none/);
    // ASSERTION-PROOF-OF-DOD-B: second call retries with prompt=login.
    expect(calls[1]).toMatch(/prompt=login/);
    // ASSERTION-PROOF-OF-DOD-C: kc_idp_hint preserved across the retry.
    expect(calls[1]).toMatch(/kc_idp_hint=catalyst-pin/);
  });

  test('Launch button shows transient "Signing in…" indicator', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}`);
    const btn = page.getByTestId('launch-button').first();
    await btn.click();
    // ASSERTION-PROOF-OF-DOD: visible feedback that a flow is in progress.
    await expect(page.getByTestId('launch-status')).toContainText(/Signing|Launching|Opening/i);
  });

  test('hard failure surfaces an actionable error toast', async ({ page }) => {
    await page.addInitScript(() => {
      (window as any).__openCalls = [];
      (window as any).open = (url: string) => {
        (window as any).__openCalls.push(url);
        // Two failures in a row — no fallback recovery.
        setTimeout(() => window.postMessage({ source: 'kc', error: 'server_error' }, '*'), 50);
        return null;
      };
    });
    await page.goto(`/apps/${APP_ID}`);
    await page.getByTestId('launch-button').first().click();
    // ASSERTION-PROOF-OF-DOD: explicit user-visible error after fallback also fails.
    await expect(page.getByTestId('launch-error-toast')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId('launch-error-toast')).toContainText(/sign in|launch failed|try again/i);
  });
});
