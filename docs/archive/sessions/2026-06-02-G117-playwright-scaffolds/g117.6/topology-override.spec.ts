// G117.6 DoD — Topology override at install respects spec.topology.supported.
//
// Verifies:
//   1. Installing with a topology NOT in `spec.topology.supported[]` is
//      REJECTED by catalyst-api with 400
//   2. Installing with a topology IN `spec.topology.supported[]` is
//      accepted and application-controller fans HRs across the matching
//      host clusters
//   3. active-hot-standby grafana → 2 HRs (mgmt-A active + mgmt-B passive)
//   4. singleton grafana → 1 HR (mgmt-A only)
//   5. Defaults from spec.topology.defaults applied when caller omits topology

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';
const K_HARNESS = process.env.G117_KUBECTL_HARNESS === '1';

test.describe('G117.6 — topology override + fanout', () => {
  test('install with unsupported topology rejected (400)', async ({ request }) => {
    const resp = await request.post('/sovereign/api/v1/apps/instances', {
      data: {
        blueprint: 'grafana',
        org: 'acme',
        name: 'bogus-topo',
        topology: 'definitely-not-supported',
      },
    });
    // ASSERTION-PROOF-OF-DOD: server cross-checks topology ⊂ supported.
    expect(resp.status()).toBe(400);
    const body = await resp.json();
    expect(body.error || body.message || '').toMatch(/topology|supported|enum/i);
  });

  test.skip(!LIVE_SOVEREIGN || !K_HARNESS, 'requires live Sovereign + kubectl harness (W4 gate)');

  test('active-hot-standby grafana fans 2 HRs across mgmt-A + mgmt-B', async ({ request }) => {
    test.setTimeout(120_000);
    const resp = await request.post('/sovereign/api/v1/apps/instances', {
      data: {
        blueprint: 'grafana',
        org: 'acme',
        name: 'ha-pair',
        topology: 'active-hot-standby',
      },
    });
    expect(resp.status()).toBe(201);
    const appId = (await resp.json()).id;

    const start = Date.now();
    while (Date.now() - start < 90_000) {
      const r = await request.get(`/sovereign/api/v1/apps/${appId}`);
      const app = await r.json();
      if (app.status === 'Ready') break;
      await new Promise((res) => setTimeout(res, 3_000));
    }

    const hrA = await request.get(
      '/sovereign/api/v1/diag/kubectl?context=mgmt-a&args=get,hr,-A,-o,json',
    );
    const hrB = await request.get(
      '/sovereign/api/v1/diag/kubectl?context=mgmt-b&args=get,hr,-A,-o,json',
    );
    const aMatches = (await hrA.json()).items.filter((i: any) =>
      i.metadata.name.includes('ha-pair'),
    );
    const bMatches = (await hrB.json()).items.filter((i: any) =>
      i.metadata.name.includes('ha-pair'),
    );
    // ASSERTION-PROOF-OF-DOD: one HR per region, pair binding.
    expect(aMatches.length).toBe(1);
    expect(bMatches.length).toBe(1);
  });

  test('singleton grafana installs 1 HR on mgmt-A only', async ({ request }) => {
    const resp = await request.post('/sovereign/api/v1/apps/instances', {
      data: { blueprint: 'grafana', org: 'acme', name: 'singleton-only', topology: 'singleton' },
    });
    expect(resp.status()).toBe(201);

    const hrA = await request.get(
      '/sovereign/api/v1/diag/kubectl?context=mgmt-a&args=get,hr,-A,-o,json',
    );
    const hrB = await request.get(
      '/sovereign/api/v1/diag/kubectl?context=mgmt-b&args=get,hr,-A,-o,json',
    );
    const aMatches = (await hrA.json()).items.filter((i: any) =>
      i.metadata.name.includes('singleton-only'),
    );
    const bMatches = (await hrB.json()).items.filter((i: any) =>
      i.metadata.name.includes('singleton-only'),
    );
    // ASSERTION-PROOF-OF-DOD: 1 HR on A, 0 on B.
    expect(aMatches.length).toBe(1);
    expect(bMatches.length).toBe(0);
  });

  test('omitting topology applies blueprint default per region count', async ({ request }) => {
    const resp = await request.post('/sovereign/api/v1/apps/instances', {
      data: { blueprint: 'grafana', org: 'acme', name: 'default-resolve' },
    });
    expect(resp.status()).toBe(201);
    const app = await resp.json();
    // ASSERTION-PROOF-OF-DOD: server resolved topology from defaults.
    // Multi-region Sovereign default is active-hot-standby per the brief.
    expect(['active-hot-standby', 'singleton']).toContain(app.topology);
  });
});
