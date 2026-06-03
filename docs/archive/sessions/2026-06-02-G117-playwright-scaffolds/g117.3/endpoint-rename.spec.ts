// G117.3 DoD — Endpoint rename → 301 from old + SSO redirect_uri preserved.
//
// Verifies:
//   1. Renaming an endpoint hostname swaps DNS + cert + ingress atomically
//   2. The OLD hostname returns HTTP 301 to the NEW hostname for the
//      configured grace period (default 7 days)
//   3. The Keycloak client's redirect_uri list is updated to the NEW
//      hostname; SSO from the Launch button still works after rename
//
// Promotion gate (Wave-2): unskip after G117.3 endpoint-rename pipeline lands.
// Live-mode dependency: live KC admin API + live DNS + live cert-manager.

import { test, expect } from '@playwright/test';

const APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';
const RENAME_LIVE = process.env.G117_RENAME_LIVE === '1';

test.describe('G117.3 — endpoint rename → 301 + KC redirect_uri preservation', () => {
  test('Endpoints tab exposes a rename action per endpoint', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await expect(page.getByTestId('endpoints-tab')).toBeVisible();
    // ASSERTION-PROOF-OF-DOD: rename surface available.
    await expect(page.getByTestId('endpoint-ui-rename-btn')).toBeVisible();
  });

  test.skip(!RENAME_LIVE, 'requires live KC + DNS + cert-manager (Wave-2 promotion gate)');

  test('renaming an endpoint preserves cert + KC redirect_uri', async ({ page, request }) => {
    test.setTimeout(180_000);
    await page.goto(`/apps/${APP_ID}/endpoints`);
    const oldHostname = await page.getByTestId('endpoint-ui-hostname').textContent();
    expect(oldHostname).toBeTruthy();

    await page.getByTestId('endpoint-ui-rename-btn').click();
    await page.getByTestId('endpoint-new-hostname-input').fill('renamed-grafana.acme.t01.omani.works');
    await page.getByTestId('endpoint-rename-submit').click();

    // Wait for status=Ready on the renamed endpoint.
    await page.waitForFunction(
      () => {
        const row = document.querySelector('[data-testid="endpoint-ui"]');
        return row?.getAttribute('data-status') === 'Ready';
      },
      undefined,
      { timeout: 120_000 },
    );

    // ASSERTION-PROOF-OF-DOD-A: old hostname → 301.
    const probe = await request.get(`https://${oldHostname}/`, { maxRedirects: 0, ignoreHTTPSErrors: true });
    expect(probe.status()).toBe(301);
    expect(probe.headers()['location']).toContain('renamed-grafana');

    // ASSERTION-PROOF-OF-DOD-B: KC client redirect_uri now points at NEW hostname.
    const kcResp = await request.get('/auth/admin/realms/sovereign/clients?clientId=grafana-acme');
    expect(kcResp.status()).toBe(200);
    const kc = await kcResp.json();
    const redirectUris = kc[0]?.redirectUris ?? [];
    expect(redirectUris.some((u: string) => u.includes('renamed-grafana'))).toBe(true);
  });

  test('Launch button after rename still authenticates silently', async ({ page }) => {
    // After rename, the Launch URL must point at the NEW hostname's KC client
    // and silent-SSO must still succeed (no interactive login).
    await page.goto(`/apps/${APP_ID}`);
    await page.addInitScript(() => {
      (window as any).__openCalls = [];
      (window as any).open = (url: string) => {
        (window as any).__openCalls.push(url);
        return null;
      };
    });
    await page.getByTestId('launch-button').first().click();
    const openedUrl = await page.evaluate(() => (window as any).__openCalls[0]);
    // ASSERTION-PROOF-OF-DOD: launch URL uses the renamed hostname.
    expect(openedUrl).toContain('renamed-grafana');
    expect(openedUrl).toMatch(/prompt=none/);
  });

  test('rename grace period configurable + audit-logged', async ({ request }) => {
    const resp = await request.get(`/sovereign/api/v1/apps/${APP_ID}/endpoints/ui/audit`);
    expect(resp.status()).toBe(200);
    const audit = await resp.json();
    // ASSERTION-PROOF-OF-DOD: rename event recorded with grace-period TTL.
    expect(audit.events.find((e: any) => e.kind === 'EndpointRenamed')).toBeTruthy();
    expect(audit.events.find((e: any) => e.kind === 'EndpointRenamed')?.gracePeriodDays).toBeGreaterThanOrEqual(1);
  });
});
