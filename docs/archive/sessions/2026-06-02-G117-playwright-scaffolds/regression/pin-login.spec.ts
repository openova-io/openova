// Regression — PIN login flow on hw86 (Pillar 0/1 baseline).
//
// Verifies the existing operator PIN-login surface keeps working after the
// G117 series ships. Anti-theater: this MUST be runnable against a fresh
// prov; mock-mode is not acceptable for regression.
//
// Promotion gate (W4): unskip against hw86 (or a fresh prov) once the
// regression dir is created under tests/e2e/regression/.

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';
const PIN = process.env.G117_OPERATOR_PIN || '';

test.describe('Regression — PIN login', () => {
  test.skip(!LIVE_SOVEREIGN || !PIN, 'requires live Sovereign + operator PIN (W4 gate)');

  test('operator visits /pin → submits PIN → lands authenticated', async ({ page }) => {
    await page.goto('/pin');
    // ASSERTION-PROOF-OF-DOD-A: PIN form renders.
    await expect(page.getByTestId('pin-input')).toBeVisible();
    await page.getByTestId('pin-input').fill(PIN);
    await page.getByTestId('pin-submit').click();
    // ASSERTION-PROOF-OF-DOD-B: redirected to authenticated console root.
    await page.waitForURL((url) => !url.toString().includes('/pin'), { timeout: 10_000 });
    await expect(page.getByTestId('console-app-shell')).toBeVisible();
  });

  test('wrong PIN surfaces error + stays on /pin', async ({ page }) => {
    await page.goto('/pin');
    await page.getByTestId('pin-input').fill('000000');
    await page.getByTestId('pin-submit').click();
    await expect(page.getByTestId('pin-error')).toBeVisible();
    // ASSERTION-PROOF-OF-DOD: no leak past PIN gate.
    expect(page.url()).toContain('/pin');
  });

  test('PIN attempt rate-limit kicks in after 5 wrong tries', async ({ page }) => {
    await page.goto('/pin');
    for (let i = 0; i < 6; i++) {
      await page.getByTestId('pin-input').fill('111111');
      await page.getByTestId('pin-submit').click();
    }
    // ASSERTION-PROOF-OF-DOD: brute-force protection visible.
    await expect(page.getByTestId('pin-rate-limit')).toBeVisible();
  });
});
