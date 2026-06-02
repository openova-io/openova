// G117.6 DoD — 3-instance grafana in 1 Environment reconciles cleanly.
//
// Verifies:
//   1. POSTing 3 distinct grafana Applications under the same Org succeeds
//   2. application-controller renders 3 distinct HelmReleases (3 namespaces
//      OR 3 release names) in the mgmt cluster
//   3. Each instance gets its OWN Ingress with the requested hostname
//   4. Each instance gets its OWN storage PVC (data isolation)
//   5. All 3 reach status=Ready
//
// Promotion gate (W4): unskip once G117.6 application-controller +
// multi-instance HR fanout is live on hw86.

import { test, expect } from '@playwright/test';

const LIVE_SOVEREIGN = process.env.G117_LIVE_SOVEREIGN === '1';
const K_HARNESS = process.env.G117_KUBECTL_HARNESS === '1';

test.describe('G117.6 — multi-instance reconcile (3× grafana)', () => {
  test.skip(!LIVE_SOVEREIGN || !K_HARNESS, 'requires live Sovereign + kubectl harness (W4 gate)');

  test('POST 3 grafana instances under same Org', async ({ request }) => {
    test.setTimeout(120_000);
    const ids: string[] = [];
    for (const name of ['metrics-a', 'metrics-b', 'metrics-c']) {
      const resp = await request.post('/sovereign/api/v1/apps/instances', {
        data: {
          blueprint: 'grafana',
          org: 'acme',
          name,
          topology: 'singleton',
        },
      });
      // ASSERTION-PROOF-OF-DOD: 201 on each — multi-instance allowed.
      expect(resp.status()).toBe(201);
      ids.push((await resp.json()).id);
    }
    expect(ids).toHaveLength(3);
    expect(new Set(ids).size).toBe(3); // 3 distinct UUIDs

    // Poll each Application until status=Ready.
    for (const id of ids) {
      await test.step(`Application ${id} Ready`, async () => {
        const start = Date.now();
        while (Date.now() - start < 90_000) {
          const r = await request.get(`/sovereign/api/v1/apps/${id}`);
          const app = await r.json();
          if (app.status === 'Ready') return;
          await new Promise((res) => setTimeout(res, 3_000));
        }
        throw new Error(`Application ${id} did not reach Ready within 90s`);
      });
    }
  });

  test('each instance has its own HelmRelease + Ingress + PVC', async ({ request }) => {
    // Use kubectl harness (exec via catalyst-api /diag endpoint or shell out).
    const hrs = await request.get('/sovereign/api/v1/diag/kubectl?args=get,hr,-n,acme,-o,json');
    const hrJson = await hrs.json();
    const grafanaHRs = hrJson.items.filter((i: any) => i.spec.chart?.spec?.chart === 'grafana');
    // ASSERTION-PROOF-OF-DOD-A: 3 distinct HRs.
    expect(grafanaHRs.length).toBeGreaterThanOrEqual(3);

    const ing = await request.get('/sovereign/api/v1/diag/kubectl?args=get,ingress,-n,acme,-o,json');
    const ingJson = await ing.json();
    const grafanaIngresses = ingJson.items.filter((i: any) =>
      (i.metadata.labels?.['app.kubernetes.io/name'] || '').includes('grafana'),
    );
    // ASSERTION-PROOF-OF-DOD-B: 3 distinct Ingresses, 3 distinct hosts.
    expect(grafanaIngresses.length).toBeGreaterThanOrEqual(3);
    const hosts = grafanaIngresses.flatMap((i: any) =>
      (i.spec.rules || []).map((r: any) => r.host),
    );
    expect(new Set(hosts).size).toBe(grafanaIngresses.length);

    const pvc = await request.get('/sovereign/api/v1/diag/kubectl?args=get,pvc,-n,acme,-o,json');
    const pvcJson = await pvc.json();
    const grafanaPVCs = pvcJson.items.filter((i: any) =>
      (i.metadata.labels?.['app.kubernetes.io/name'] || '').includes('grafana'),
    );
    // ASSERTION-PROOF-OF-DOD-C: 3 distinct PVCs (data isolation).
    expect(grafanaPVCs.length).toBeGreaterThanOrEqual(3);
  });

  test('listing Applications by blueprint=grafana shows 3 rows in console', async ({ page }) => {
    await page.goto('/catalog/grafana');
    await expect(page.getByTestId('catalog-drilldown')).toBeVisible();
    const rows = page.getByTestId(/instance-row-/);
    // ASSERTION-PROOF-OF-DOD: UI surfaces the 3 instances.
    await expect(rows).toHaveCount(3, { timeout: 10_000 });
  });

  test('deleting one instance does NOT cascade to the other two', async ({ request, page }) => {
    const list = await request.get('/sovereign/api/v1/apps?blueprint=grafana&org=acme');
    const apps = (await list.json()).items;
    expect(apps.length).toBeGreaterThanOrEqual(3);

    const victim = apps[0];
    const del = await request.delete(`/sovereign/api/v1/apps/${victim.id}`);
    expect(del.status()).toBeLessThan(300);

    // Other two remain.
    const after = await request.get('/sovereign/api/v1/apps?blueprint=grafana&org=acme');
    const remaining = (await after.json()).items;
    // ASSERTION-PROOF-OF-DOD: no cross-instance blast-radius.
    expect(remaining.length).toBe(apps.length - 1);
  });
});
