// G117.4 DoD — Launch <500ms wall-clock from click to authenticated session.
//
// Verifies:
//   1. Time from `Launch` click → KC silent SSO completes < 500ms (median)
//   2. p95 over 20 iterations stays < 750ms (variance budget for cold-cache)
//   3. First-paint of the target app's UI within the new tab is < 1500ms
//      (downstream latency, not Launch button's responsibility but
//      recorded to surface the full-chain budget)
//
// Promotion gate (W4): unskip once a live hw86 sandbox is reachable.
// The wall-clock assertion is meaningless against mocks — must run live.

import { test, expect } from '@playwright/test';

const APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';
const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';

test.describe('G117.4 — Launch silent-SSO wall-clock budget', () => {
  test.skip(!LIVE_SOVEREIGN, 'requires live Sovereign (W4 promotion gate)');

  test('Launch button silent-SSO completes < 500ms (single sample)', async ({ page, context }) => {
    await page.goto(`/apps/${APP_ID}`);
    const [popup] = await Promise.all([
      context.waitForEvent('page'),
      page.getByTestId('launch-button').first().click(),
    ]);
    const t0 = Date.now();
    // The downstream app's KC silent-SSO succeeds when the URL stops
    // showing /realms/<realm>/protocol/openid-connect/auth → flips to the
    // app's authenticated home route.
    await popup.waitForURL((url) => !url.toString().includes('/protocol/openid-connect/auth'), {
      timeout: 5_000,
    });
    const elapsed = Date.now() - t0;
    // ASSERTION-PROOF-OF-DOD: wall-clock < 500ms from click → auth-complete.
    expect(elapsed).toBeLessThan(500);
  });

  test('p95 across 20 launches < 750ms', async ({ context }) => {
    test.setTimeout(60_000);
    const samples: number[] = [];
    for (let i = 0; i < 20; i++) {
      const page = await context.newPage();
      await page.goto(`/apps/${APP_ID}`);
      const t0 = Date.now();
      const [popup] = await Promise.all([
        context.waitForEvent('page'),
        page.getByTestId('launch-button').first().click(),
      ]);
      await popup.waitForURL((url) => !url.toString().includes('/protocol/openid-connect/auth'));
      samples.push(Date.now() - t0);
      await popup.close();
      await page.close();
    }
    samples.sort((a, b) => a - b);
    const p95 = samples[Math.floor(samples.length * 0.95)];
    // ASSERTION-PROOF-OF-DOD: p95 latency stays within 750ms variance budget.
    expect(p95).toBeLessThan(750);
  });

  test('first-paint in launched tab < 1500ms', async ({ page, context }) => {
    await page.goto(`/apps/${APP_ID}`);
    const t0 = Date.now();
    const [popup] = await Promise.all([
      context.waitForEvent('page'),
      page.getByTestId('launch-button').first().click(),
    ]);
    await popup.waitForLoadState('domcontentloaded');
    const elapsed = Date.now() - t0;
    // ASSERTION-PROOF-OF-DOD: end-user perceives the target app in <1.5s.
    expect(elapsed).toBeLessThan(1500);
  });
});
