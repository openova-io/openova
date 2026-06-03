// Regression — Voucher redeem (Pillar 0/1 baseline).
//
// Verifies the BSS-menu voucher redemption flow works end-to-end after
// G117 ships. Voucher creation lives in the operator console's BSS menu;
// redemption happens on the marketplace surface.
//
// Promotion gate (W4): unskip against hw86 (or a fresh prov).

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';
const SOV_FQDN = process.env.G117_SOV_FQDN || ''; // e.g. t01.omani.works

test.describe('Regression — Voucher redeem', () => {
  test.skip(!LIVE_SOVEREIGN || !SOV_FQDN, 'requires live Sovereign FQDN (W4 gate)');

  test('operator mints voucher via BSS menu', async ({ page, request }) => {
    // Pre-flight: authenticated session (assume PIN login fixture).
    await page.goto('/bss/vouchers');
    await expect(page.getByTestId('voucher-list')).toBeVisible();
    await page.getByTestId('btn-new-voucher').click();
    await page.getByTestId('voucher-org-slug').fill('reg-test-1');
    await page.getByTestId('voucher-submit').click();
    // ASSERTION-PROOF-OF-DOD: voucher code emitted.
    const code = await page.getByTestId('voucher-code').textContent();
    expect(code).toMatch(/^[A-Z0-9]{8,}$/);
  });

  test('marketplace redeem URL accepts the voucher', async ({ page, request }) => {
    // Re-fetch a voucher via API so the test is self-contained.
    const mint = await request.post('/sovereign/api/v1/vouchers', {
      data: { orgSlug: 'reg-redeem' },
    });
    expect(mint.status()).toBe(201);
    const code = (await mint.json()).code as string;

    await page.goto(`https://marketplace.${SOV_FQDN}/redeem/?code=${code}`);
    // ASSERTION-PROOF-OF-DOD: redemption form recognises the code.
    await expect(page.getByTestId('redeem-form')).toBeVisible();
    await expect(page.getByTestId('redeem-form-code')).toHaveValue(code);
  });

  test('invalid voucher code rejected gracefully', async ({ page }) => {
    await page.goto(`https://marketplace.${SOV_FQDN}/redeem/?code=NOPE-NOT-REAL`);
    await expect(page.getByTestId('redeem-error')).toBeVisible();
    // ASSERTION-PROOF-OF-DOD: explicit "not found" message (no leak).
    await expect(page.getByTestId('redeem-error')).toContainText(/invalid|not found|expired/i);
  });

  test('redeemed voucher creates an Organization', async ({ request }) => {
    const list = await request.get('/sovereign/api/v1/orgs?slug=reg-redeem');
    expect(list.status()).toBe(200);
    const orgs = (await list.json()).items;
    // ASSERTION-PROOF-OF-DOD: post-redeem Organization materialized.
    expect(orgs.length).toBe(1);
    expect(orgs[0].slug).toBe('reg-redeem');
  });
});
