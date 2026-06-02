// Regression — Sandbox + openova-sandbox-mcp auto-mount (Pillar 2 baseline).
//
// Verifies Pillar 2 (Sandbox + auto-mounted MCP) still works after G117 ships.
//
// Promotion gate (W4): unskip against hw86 (or a fresh prov).

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';

test.describe('Regression — Sandbox + openova-sandbox-mcp', () => {
  test.skip(!LIVE_SOVEREIGN, 'requires live Sovereign (W4 gate)');

  test('Sandbox tile present in console nav', async ({ page }) => {
    await page.goto('/');
    // ASSERTION-PROOF-OF-DOD: Pillar 2 entry-point reachable.
    await expect(page.getByTestId('nav-sandbox')).toBeVisible();
    await page.getByTestId('nav-sandbox').click();
    await expect(page).toHaveURL(/\/sandbox/);
  });

  test('Sandbox spins up a session pod', async ({ page, request }) => {
    test.setTimeout(60_000);
    await page.goto('/sandbox');
    await page.getByTestId('btn-new-sandbox').click();
    const sessionId = await page.getByTestId('sandbox-session-id').textContent();
    expect(sessionId).toMatch(/^[a-z0-9-]+$/);

    // Poll until Ready.
    const start = Date.now();
    while (Date.now() - start < 45_000) {
      const r = await request.get(`/sovereign/api/v1/sandbox/${sessionId}`);
      const s = await r.json();
      if (s.status === 'Ready') break;
      await new Promise((res) => setTimeout(res, 2_000));
    }
    // ASSERTION-PROOF-OF-DOD: session Pod scheduled + ready.
    const r = await request.get(`/sovereign/api/v1/sandbox/${sessionId}`);
    const s = await r.json();
    expect(s.status).toBe('Ready');
  });

  test('openova-sandbox-mcp auto-mounted in session pod', async ({ request }) => {
    const list = await request.get('/sovereign/api/v1/sandbox');
    const sessions = (await list.json()).items;
    expect(sessions.length).toBeGreaterThan(0);
    const sid = sessions[0].id;

    // Exec into the pod and check for the MCP socket / config.
    const exec = await request.post(`/sovereign/api/v1/sandbox/${sid}/exec`, {
      data: { cmd: ['cat', '/etc/mcp/servers.json'] },
    });
    expect(exec.status()).toBe(200);
    const out = await exec.json();
    // ASSERTION-PROOF-OF-DOD: openova-sandbox-mcp is mounted by default.
    expect(out.stdout).toContain('openova-sandbox-mcp');
  });

  test('MCP can read Catalyst knowledge (Pillar 2.b)', async ({ request }) => {
    const list = await request.get('/sovereign/api/v1/sandbox');
    const sid = (await list.json()).items[0].id;
    const exec = await request.post(`/sovereign/api/v1/sandbox/${sid}/exec`, {
      data: { cmd: ['mcp-cli', 'openova-sandbox-mcp', 'list_blueprints'] },
    });
    const out = await exec.json();
    // ASSERTION-PROOF-OF-DOD: MCP surfaces the catalog from within the Sandbox.
    expect(out.stdout).toMatch(/grafana|gitea|harbor/i);
  });
});
