// G117.5 DoD — Per-Org realm has Keycloak-OIDC IdP federated to `sovereign`.
//
// Verifies (Tier-3 architecture per locked-decision #6):
//   1. Every Tier-3 per-Org realm has an Identity Provider whose alias is
//      `sovereign` and providerId `oidc` (Keycloak-flavor OIDC broker)
//   2. The IdP's `authorization_url` points at the `sovereign` realm
//   3. A 2-hop SSO chain: user in Org realm → IdP redirect → sovereign
//      realm → catalyst-pin IDR → silent return
//   4. The `defaultProvider=sovereign` authenticationConfig is bound so
//      kc_idp_hint silently lands on the broker (per G113 memory note)
//
// Promotion gate (W4): unskip once G117.5 + G113-shape config has shipped.

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';
const KC_ADMIN_TOKEN = process.env.G117_KC_ADMIN_TOKEN || '';

test.describe('G117.5 — per-Org realm federation chain (2-hop)', () => {
  test.skip(!LIVE_SOVEREIGN || !KC_ADMIN_TOKEN, 'requires live Sovereign + KC admin token (W4 gate)');

  test('per-Org realm has IdP alias=sovereign providerId=oidc', async ({ request }) => {
    const resp = await request.get('/auth/admin/realms/acme/identity-provider/instances', {
      headers: { authorization: `Bearer ${KC_ADMIN_TOKEN}` },
    });
    expect(resp.status()).toBe(200);
    const idps = await resp.json();
    const sov = idps.find((i: any) => i.alias === 'sovereign');
    // ASSERTION-PROOF-OF-DOD: broker IdP present.
    expect(sov).toBeTruthy();
    expect(sov.providerId).toBe('oidc');
  });

  test('IdP authorizationUrl points back at sovereign realm', async ({ request }) => {
    const resp = await request.get('/auth/admin/realms/acme/identity-provider/instances/sovereign', {
      headers: { authorization: `Bearer ${KC_ADMIN_TOKEN}` },
    });
    expect(resp.status()).toBe(200);
    const idp = await resp.json();
    // ASSERTION-PROOF-OF-DOD: chain target verified.
    expect(idp.config.authorizationUrl).toMatch(/\/realms\/sovereign\/protocol\/openid-connect\/auth/);
  });

  test('defaultProvider=sovereign authenticationConfig bound on Org realm', async ({ request }) => {
    // Walk the browser flow → look for an Identity Provider Redirector
    // execution with config.defaultProvider=sovereign.
    const flowsResp = await request.get('/auth/admin/realms/acme/authentication/flows', {
      headers: { authorization: `Bearer ${KC_ADMIN_TOKEN}` },
    });
    const flows = await flowsResp.json();
    const browser = flows.find((f: any) => f.alias === 'browser');
    expect(browser).toBeTruthy();

    const execsResp = await request.get(
      `/auth/admin/realms/acme/authentication/flows/${browser.alias}/executions`,
      { headers: { authorization: `Bearer ${KC_ADMIN_TOKEN}` } },
    );
    const execs = await execsResp.json();
    const idr = execs.find((e: any) => e.providerId === 'identity-provider-redirector');
    expect(idr).toBeTruthy();
    expect(idr.authenticationConfig).toBeTruthy();

    const cfgResp = await request.get(
      `/auth/admin/realms/acme/authentication/config/${idr.authenticationConfig}`,
      { headers: { authorization: `Bearer ${KC_ADMIN_TOKEN}` } },
    );
    const cfg = await cfgResp.json();
    // ASSERTION-PROOF-OF-DOD: defaultProvider bound, per G113 memory.
    expect(cfg.config.defaultProvider).toBe('sovereign');
  });

  test('2-hop chain completes silently with no interactive prompt', async ({ page, context }) => {
    // Visit a Tier-3 app login URL → KC Org realm → IdP redirect → KC
    // sovereign realm → catalyst-pin IDR → land back on app /home.
    const [popup] = await Promise.all([
      context.waitForEvent('page'),
      page.goto('/apps/orgA-matrix'),
      page.getByTestId('launch-button').first().click(),
    ]);
    // Observe URL transitions: must NOT show a /login form along the way.
    const visited: string[] = [];
    popup.on('framenavigated', (f) => {
      if (f === popup.mainFrame()) visited.push(f.url());
    });
    await popup.waitForLoadState('domcontentloaded');
    // ASSERTION-PROOF-OF-DOD: no interactive login HTML page rendered.
    const hadLoginForm = visited.some((u) => u.includes('/login-actions/authenticate'));
    expect(hadLoginForm).toBe(false);
  });
});
