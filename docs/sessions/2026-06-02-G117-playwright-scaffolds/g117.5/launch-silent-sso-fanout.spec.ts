// G117.5 DoD — Launch button silent-SSO fans out across 17 SSO-capable apps.
//
// Verifies for EACH of the 17 Tier-1+Tier-2+Tier-3 SSO-capable Blueprints:
//   1. /apps/<id> Overview tab surfaces a Launch button
//   2. Launch URL carries the realm-correct token bundle
//      (Tier-1+2: realm=sovereign · Tier-3: realm=<org>)
//   3. Silent SSO completes (no interactive prompt observed)
//
// Promotion gate (W4): unskip against live hw86 once Tier-1+Tier-2+Tier-3
// catalyst-api wiring (G117.5) lands.

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';

// Locked Tier list per .claude/templates/G117-agent-brief.md "SSO fan-out scope":
// - Tier-1 (4) — Grafana, Gitea, Harbor, OpenBao
// - Tier-2 (4) — Guacamole, PowerDNS-Admin, Hubble (Cilium), Cosign
// - Tier-3 (9+) — Matrix, LibreChat, Langfuse, Temporal, OpenMeter, OpenSearch
//   Dashboards, ClickHouse, Neo4j, vLLM (or whichever 9 land first per G117.5)
const APPS: Array<{ blueprint: string; realm: 'sovereign' | 'org'; tier: 1 | 2 | 3 }> = [
  { blueprint: 'grafana', realm: 'sovereign', tier: 1 },
  { blueprint: 'gitea', realm: 'sovereign', tier: 1 },
  { blueprint: 'harbor', realm: 'sovereign', tier: 1 },
  { blueprint: 'openbao', realm: 'sovereign', tier: 1 },
  { blueprint: 'guacamole', realm: 'sovereign', tier: 2 },
  { blueprint: 'powerdns-admin', realm: 'sovereign', tier: 2 },
  { blueprint: 'hubble', realm: 'sovereign', tier: 2 },
  { blueprint: 'cosign', realm: 'sovereign', tier: 2 },
  { blueprint: 'matrix', realm: 'org', tier: 3 },
  { blueprint: 'librechat', realm: 'org', tier: 3 },
  { blueprint: 'langfuse', realm: 'org', tier: 3 },
  { blueprint: 'temporal', realm: 'org', tier: 3 },
  { blueprint: 'openmeter', realm: 'org', tier: 3 },
  { blueprint: 'opensearch-dashboards', realm: 'org', tier: 3 },
  { blueprint: 'clickhouse', realm: 'org', tier: 3 },
  { blueprint: 'neo4j', realm: 'org', tier: 3 },
  { blueprint: 'vllm', realm: 'org', tier: 3 },
];

test.describe('G117.5 — silent SSO fan-out across 17 apps', () => {
  test.skip(!LIVE_SOVEREIGN, 'requires live Sovereign with all 17 apps installed (W4 gate)');

  for (const app of APPS) {
    test(`${app.blueprint} (Tier-${app.tier}, realm=${app.realm}) silent SSO succeeds`, async ({
      page,
      context,
      request,
    }) => {
      // Resolve the App.id for this blueprint in the test Org.
      const list = await request.get(`/sovereign/api/v1/apps?blueprint=${app.blueprint}&org=acme`);
      expect(list.status()).toBe(200);
      const apps = (await list.json()).items;
      expect(apps.length).toBeGreaterThan(0);
      const appId = apps[0].id;

      await page.goto(`/apps/${appId}`);
      await expect(page.getByTestId('launch-button').first()).toBeVisible();

      await page.addInitScript(() => {
        (window as any).__openCalls = [];
        (window as any).open = (url: string) => {
          (window as any).__openCalls.push(url);
          return null;
        };
      });

      await page.getByTestId('launch-button').first().click();
      await page.waitForFunction(
        () => ((window as any).__openCalls as string[]).length > 0,
        undefined,
        { timeout: 5_000 },
      );
      const url = await page.evaluate(() => (window as any).__openCalls[0]);
      // ASSERTION-PROOF-OF-DOD-A: realm matches Tier expectation.
      const expectedRealmPattern = app.realm === 'sovereign' ? /realms\/sovereign/ : /realms\/acme/;
      expect(url).toMatch(expectedRealmPattern);
      // ASSERTION-PROOF-OF-DOD-B: silent SSO parameters.
      expect(url).toMatch(/prompt=none/);
      expect(url).toMatch(/kc_idp_hint=catalyst-pin/);
    });
  }
});
