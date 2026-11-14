/**
 * endpoint-add-via-pr.spec.ts — Playwright spec stub for G117.3.
 *
 * Wave-1.B5 (UI author) wires this against the real Endpoints tab UI.
 * For now the spec is a CONTRACT STUB: it documents the steps the
 * UI must enable so the IaC PR pipeline is observable end-to-end.
 *
 * Stub responsibilities (NOT yet executed):
 *
 *  1. Sign in to the operator console as a tier-admin user.
 *  2. Navigate to /apps/<applicationName>/endpoints (a UI route the
 *     B5 author registers in console-routes).
 *  3. Click "Add endpoint" → fill the form (name + hostname + protocol
 *     + tls + ssoEnabled) → click "Open PR".
 *  4. Verify the response banner shows the PR URL + status `merged`.
 *  5. Click the PR URL → assert the Gitea page loads with status
 *     "Merged" and the manifest at apps/<app>/endpoints/<name>.yaml.
 *  6. Wait ~60s for Flux reconcile, then GET /apps/<id>/endpoints and
 *     assert the new endpoint appears with status=Ready.
 *
 * The endpoint-CRUD handlers are LIVE on `feat/g117-3-catalyst-api-endpoint-crud`
 * (PR #TBD); this spec waits on UI wiring before becoming runnable.
 */
import { test, expect } from '@playwright/test';

test.describe('G117.3 — Endpoint add via PR pipeline', () => {
  test.skip('UI wiring pending — B5 author runs this once the Endpoints tab lands', async ({ page }) => {
    // Step 1 — sign in
    await page.goto('/');
    // PIN login — see G89 PIN-auth runbook.
    // await page.getByLabel('email').fill('operator@acme');
    // await page.getByRole('button', { name: 'Send PIN' }).click();
    // await page.getByLabel('pin').fill('123456');
    // await page.getByRole('button', { name: 'Verify' }).click();
    // await expect(page).toHaveURL(/\/dashboard/);

    // Step 2 — navigate to the Endpoints tab on a known Application
    // const appName = 'wp-prod';
    // await page.goto(`/apps/${appName}/endpoints`);

    // Step 3 — open the add-endpoint dialog
    // await page.getByRole('button', { name: 'Add endpoint' }).click();
    // await page.getByLabel('name').fill('ui');
    // await page.getByLabel('hostname').fill('wp-prod.acme.t01.omani.works');
    // await page.getByLabel('protocol').selectOption('https');
    // await page.getByLabel('tls').check();
    // await page.getByLabel('ssoEnabled').check();
    // await page.getByRole('button', { name: 'Open PR' }).click();

    // Step 4 — confirm banner
    // const banner = page.getByTestId('endpoint-pr-banner');
    // await expect(banner).toContainText('merged');
    // const prLink = banner.getByRole('link');
    // const prURL = await prLink.getAttribute('href');
    // expect(prURL).toMatch(/^https:\/\/gitea\..+\/acme\/iac\/pulls\/\d+$/);

    // Step 5 — visit the Gitea PR
    // await page.goto(prURL!);
    // await expect(page.getByText('Merged')).toBeVisible();

    // Step 6 — verify Flux reconcile via the API
    // const apiResp = await page.request.get(`/catalyst/v1/apps/${appName}/endpoints`);
    // expect(apiResp.status()).toBe(200);
    // const body = await apiResp.json();
    // expect(body.items.find((e: { name: string }) => e.name === 'ui')).toBeDefined();
  });
});
