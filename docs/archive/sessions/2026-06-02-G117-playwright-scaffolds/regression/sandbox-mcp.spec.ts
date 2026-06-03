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

  // #2930 — the MCP PROTOCOL surface, not just file presence. Drives a real
  // JSON-RPC 2.0 handshake against the auto-mounted server over its stdio
  // transport from inside the session pod, the exact wire the agent process
  // speaks. The `mcp-jsonrpc` helper inside the pod frames one
  // Content-Length-prefixed request per arg and prints the response body.
  // (Mirrors tests/e2e/sandbox-mcp-contract.sh, which exercises the same
  // three RPCs against the server binary directly with no live env.)
  test('MCP server answers initialize + tools/list + a tool round-trip (#2930)', async ({
    request,
  }) => {
    test.setTimeout(60_000);
    const list = await request.get('/sovereign/api/v1/sandbox');
    const sid = (await list.json()).items[0].id;

    // Helper: issue one JSON-RPC request over the pod's stdio MCP transport.
    // The in-pod `mcp-jsonrpc` shim Content-Length-frames the JSON, pipes it
    // to `openova-sandbox-mcp` on stdin, and emits the response body to
    // stdout. A non-200 exec, empty stdout, or JSON-RPC `error` member all
    // trip the test — a present-but-broken server cannot fake green here.
    const rpc = async (method: string, params?: unknown) => {
      const reqBody = JSON.stringify({ jsonrpc: '2.0', id: 1, method, params });
      const exec = await request.post(`/sovereign/api/v1/sandbox/${sid}/exec`, {
        data: { cmd: ['mcp-jsonrpc', 'openova-sandbox-mcp'], stdin: reqBody },
      });
      expect(exec.status()).toBe(200);
      const out = await exec.json();
      expect(out.stdout, `empty stdout for ${method} — transport dead`).toBeTruthy();
      const resp = JSON.parse(out.stdout);
      expect(resp.error, `${method} returned a JSON-RPC error`).toBeUndefined();
      return resp.result;
    };

    // (a) initialize handshake — the server must negotiate the protocol and
    //     identify itself.
    const init = await rpc('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'playwright-regression', version: '1' },
    });
    // ASSERTION-PROOF-OF-DOD: live MCP server completes the handshake.
    expect(init.protocolVersion).toBe('2024-11-05');
    expect(init.serverInfo?.name).toBe('openova-sandbox-mcp');

    // (b) tools/list — the declared toolset must come back, with the known
    //     Pillar-4 namespaces present and a sane count floor.
    const listed = await rpc('tools/list');
    const names: string[] = (listed.tools || []).map((t: { name: string }) => t.name);
    // ASSERTION-PROOF-OF-DOD: catalogue is advertised, not empty.
    expect(names.length).toBeGreaterThanOrEqual(40);
    for (const want of [
      'k8s.read.list',
      'gitea.repo.list',
      'sandbox.deploy.staging',
      'sandbox.session.info',
    ]) {
      expect(names, `tools/list missing ${want}`).toContain(want);
    }

    // (c) tools/call — a read-only tool must round-trip a non-error result.
    const called = await rpc('tools/call', {
      name: 'sandbox.session.info',
      arguments: {},
    });
    // ASSERTION-PROOF-OF-DOD: the MCP dispatch path actually executes a tool.
    expect(called.isError).not.toBe(true);
    const text = (called.content || []).find((c: { type: string }) => c.type === 'text')?.text;
    expect(text, 'tools/call returned no text content envelope').toBeTruthy();
    const payload = JSON.parse(text);
    expect(payload).toHaveProperty('sandbox_id');
  });

  test('MCP k8s.read reaches Catalyst CRDs without 403 (#2929 RBAC, #2930 coverage)', async ({ request }) => {
    test.setTimeout(60_000);
    const list = await request.get('/sovereign/api/v1/sandbox');
    const sid = (await list.json()).items[0].id;

    // PR #2952 added Sandbox SA read RBAC on the Catalyst CRD groups. The MCP
    // k8s.read.list tool must reach them — returning data or an empty list,
    // never a 403/Forbidden from the apiserver.
    const readCRD = async (group: string, resource: string) => {
      const exec = await request.post(`/sovereign/api/v1/sandbox/${sid}/exec`, {
        data: {
          cmd: [
            'mcp-cli',
            'openova-sandbox-mcp',
            'k8s.read.list',
            '--group',
            group,
            '--resource',
            resource,
            '--namespace',
            sid,
          ],
        },
      });
      const out = await exec.json();
      return `${out.stdout || ''}${out.stderr || ''}`;
    };

    // apps.openova.io/applications
    const apps = await readCRD('apps.openova.io', 'applications');
    // ASSERTION-PROOF-OF-DOD: Sandbox SA reads Applications, no RBAC denial.
    expect(apps).not.toContain('403');
    expect(apps).not.toContain('Forbidden');

    // orgs.openova.io/organizations + catalyst.openova.io/environments
    const orgs = await readCRD('orgs.openova.io', 'organizations');
    expect(orgs).not.toContain('403');
    expect(orgs).not.toContain('Forbidden');

    const envs = await readCRD('catalyst.openova.io', 'environments');
    expect(envs).not.toContain('403');
    expect(envs).not.toContain('Forbidden');
  });
});
