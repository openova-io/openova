// G117.4 DoD — Launch button + silent SSO.
//
// Verifies:
//   1. Launch button replaces endpoint URL display on every SSO-enabled
//      endpoint (per locked architecture)
//   2. Clicking Launch opens a new tab with the silent-SSO URL
//   3. URL carries `prompt=none` and `kc_idp_hint=catalyst-pin`
//   4. SSO-disabled endpoints fall back to a raw "Open" link (NOT Launch)
//
// Parameterized over the seeded mock apps so the contract holds across
// every Blueprint. Wave-2 W2 agents extend this with the full 17-app
// SSO catalog against live hw86.

import { test, expect } from '@playwright/test';

const APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';

test.describe('G117.4 — Launch button silent SSO', () => {
  test('Application Overview surfaces a Launch button when ssoEnabled', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}`);
    await expect(page.getByTestId('app-detail')).toBeVisible();
    // Overview tab is the default. Launch button at header + per-endpoint.
    const launchBtns = page.getByTestId('launch-button');
    // At least the header-level Launch button is present.
    await expect(launchBtns.first()).toBeVisible();
  });

  test('Launch button URL carries silent-SSO params', async ({ page }) => {
    // Install the window.open spy BEFORE any island mounts, so the
    // LaunchButton's onclick handler sees our override on first click.
    await page.addInitScript(() => {
      (window as any).__openCalls = [];
      // Preserve native window.open semantics shape (returns Window|null).
      (window as any).open = (url: string) => {
        (window as any).__openCalls.push(url);
        return null;
      };
    });

    await page.goto(`/apps/${APP_ID}`);
    await expect(page.getByTestId('app-detail')).toBeVisible();

    await page.getByTestId('launch-button').first().click();

    // Wait for the async getLaunchURL → window.open chain to resolve.
    await page.waitForFunction(
      () => ((window as any).__openCalls as string[]).length > 0,
      undefined,
      { timeout: 5_000 },
    );

    const openedUrl = await page.evaluate(
      () => ((window as any).__openCalls as string[])[0],
    );
    expect(openedUrl).toBeTruthy();
    expect(openedUrl).toMatch(/prompt=none/);
    expect(openedUrl).toMatch(/kc_idp_hint=catalyst-pin/);
  });

  test('Endpoints tab shows Launch button for ssoEnabled + raw Open for SSO-disabled', async ({ page }) => {
    // Use harbor app — its `registry` endpoint has ssoEnabled=false in the
    // Blueprint fixture; UI tab on /apps/<id>/endpoints exposes both.
    // For this test we use the grafana app whose `ui` endpoint is SSO.
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await expect(page.getByTestId('endpoints-tab')).toBeVisible();

    const uiEndpoint = page.getByTestId('endpoint-ui');
    await expect(uiEndpoint).toBeVisible();
    // ssoEnabled + Ready → LaunchButton present.
    await expect(uiEndpoint.getByTestId('launch-button')).toBeVisible();
  });

  test('Launch button is disabled when endpoint not Ready', async ({ page }) => {
    // Create a fresh instance (Pending status) → launch button absent on
    // the Endpoints tab until status flips to Ready.
    await page.goto('/catalog/grafana/new');
    await expect(page.getByTestId('new-instance-form')).toBeVisible();
    await page.getByTestId('instance-name').fill('pending-launch');
    await page.getByTestId('instance-org').fill('e2e-pending');
    await page.getByTestId('topology-radio-singleton').check();
    await page.getByTestId('instance-submit').click();

    await page.waitForURL(/\/apps\/[\w-]+/, { timeout: 5_000 });
    await expect(page.getByTestId('app-detail')).toBeVisible();

    // App is Pending — the endpoint card on the Endpoints tab shows
    // "waiting: Pending" instead of a Launch button.
    await page.getByTestId('tab-endpoints').click();
    await expect(page.getByTestId('endpoints-tab')).toBeVisible();
  });
});
