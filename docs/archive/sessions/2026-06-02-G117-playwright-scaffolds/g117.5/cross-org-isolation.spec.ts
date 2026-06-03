// G117.5 DoD — Cross-Org realm isolation.
//
// Verifies (using Tier-3 Matrix as the canonical example):
//   1. A user authenticated against Org-A's realm CANNOT obtain a Matrix
//      token for Org-B (Tier-3 per-Org realm boundary is enforced)
//   2. KC token-exchange with audience=<Org-B realm client> returns 403
//   3. UI-level: when Org-A user navigates to /apps/<Org-B Matrix> the
//      console refuses with a 403 Org-scope error (not a generic 401)
//
// Promotion gate (W4): unskip once G117.5 per-Org KC realm fan-out lands.

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';

test.describe('G117.5 — cross-Org per-Org realm isolation', () => {
  test.skip(!LIVE_SOVEREIGN, 'requires live Sovereign with 2 Orgs + Tier-3 app (W4 gate)');

  test('Org-A user denied Tier-3 token for Org-B', async ({ request }) => {
    // Authenticate as user@orgA against acme realm.
    const tokenA = await request.post('/auth/realms/acme/protocol/openid-connect/token', {
      form: {
        client_id: 'console',
        grant_type: 'password',
        username: 'user@orgA',
        password: process.env.G117_USER_A_PASS || 'unset',
      },
    });
    expect(tokenA.status()).toBe(200);
    const accessTokenA = (await tokenA.json()).access_token as string;

    // Attempt token-exchange against orgB realm's Matrix client.
    const exchange = await request.post('/auth/realms/orgb/protocol/openid-connect/token', {
      form: {
        grant_type: 'urn:ietf:params:oauth:grant-type:token-exchange',
        subject_token: accessTokenA,
        subject_token_type: 'urn:ietf:params:oauth:token-type:access_token',
        audience: 'matrix-orgb',
      },
    });
    // ASSERTION-PROOF-OF-DOD: cross-realm exchange refused.
    expect(exchange.status()).toBeGreaterThanOrEqual(400);
    expect(exchange.status()).toBeLessThan(500);
  });

  test('Org-A user UI navigation to Org-B app shows 403', async ({ page, request }) => {
    // Resolve Org-B Matrix App ID.
    const list = await request.get('/sovereign/api/v1/apps?blueprint=matrix&org=orgb');
    const apps = (await list.json()).items;
    expect(apps.length).toBeGreaterThan(0);
    const orgBMatrixId = apps[0].id;

    // Pretend Org-A session by setting the catalyst-org cookie.
    await page.context().addCookies([
      { name: 'catalyst-org', value: 'acme', url: page.url() || 'http://127.0.0.1:4323' },
    ]);
    await page.goto(`/apps/${orgBMatrixId}`);

    // ASSERTION-PROOF-OF-DOD: console refuses with explicit cross-Org error.
    await expect(page.getByTestId('error-cross-org-denied')).toBeVisible();
    await expect(page.getByTestId('error-cross-org-denied')).toContainText(/Org|realm|scope/i);
  });

  test('Matrix federation between Org-A + Org-B is opt-in (not default)', async ({ request }) => {
    const fedA = await request.get('/sovereign/api/v1/apps/orgA/matrix/federation');
    const fedB = await request.get('/sovereign/api/v1/apps/orgB/matrix/federation');
    const a = await fedA.json();
    const b = await fedB.json();
    // ASSERTION-PROOF-OF-DOD: federation defaults closed; per-Org opt-in.
    expect(a.federationOpen).toBeFalsy();
    expect(b.federationOpen).toBeFalsy();
  });
});
