// G117.1 DoD — Blueprint admission webhook + topology shape validation.
//
// Verifies:
//   1. Blueprint CR with malformed `spec.topology` (unknown bcpTopology) is
//      REJECTED by the admission webhook (HTTP 400 / kubectl exit≠0)
//   2. Blueprint CR with valid `spec.topology.supported[]` is ACCEPTED and
//      defaults are resolved server-side
//   3. Blueprint CR with `spec.topology` MISSING is REJECTED once the
//      "every blueprint declares placementSchema" gate is enforced (G116 +
//      G117.1 contract)
//
// Promotion gate (Wave-1.B5): unskip after G117.1 admission webhook merges.
// Live-mode dependency: kind cluster with catalyst-api CRDs installed OR a
// catalyst-api mock-mode endpoint at /sovereign/api/v1/blueprints/validate.

import { test, expect } from '@playwright/test';

const ADMISSION_API_LIVE = process.env.G117_ADMISSION_LIVE === '1';

test.describe('G117.1 — Blueprint admission webhook (topology shape)', () => {
  test.skip(!ADMISSION_API_LIVE, 'requires G117.1 admission webhook (Wave-1.B5 promotion gate)');

  test('malformed bcpTopology is rejected', async ({ request }) => {
    // DoD: spec.topology.supported[] must be a subset of the locked enum
    // {active-active, active-hot-standby, active-passive, singleton}.
    const body = {
      apiVersion: 'catalyst.openova.io/v1alpha1',
      kind: 'Blueprint',
      metadata: { name: 'g117-1-bad-bcp' },
      spec: {
        topology: {
          supported: ['definitely-not-a-real-topology'],
          defaults: { 'multi-region': 'active-hot-standby', 'single-region': 'singleton' },
        },
      },
    };
    const resp = await request.post('/sovereign/api/v1/blueprints/validate', { data: body });
    // ASSERTION-PROOF-OF-DOD: 4xx response = admission webhook rejected.
    expect(resp.status()).toBeGreaterThanOrEqual(400);
    expect(resp.status()).toBeLessThan(500);
    const json = await resp.json();
    expect(json.error || json.message || '').toMatch(/topology|bcpTopology|enum/i);
  });

  test('valid topology accepted + defaults resolved', async ({ request }) => {
    const body = {
      apiVersion: 'catalyst.openova.io/v1alpha1',
      kind: 'Blueprint',
      metadata: { name: 'g117-1-good' },
      spec: {
        topology: {
          supported: ['active-hot-standby', 'singleton'],
          defaults: { 'multi-region': 'active-hot-standby', 'single-region': 'singleton' },
          perTopology: {
            'active-hot-standby': { replication: 'sync', switchover: 'continuum', placement: 'mgmt' },
            singleton: { replication: 'none', switchover: 'none', placement: 'mgmt' },
          },
        },
      },
    };
    const resp = await request.post('/sovereign/api/v1/blueprints/validate', { data: body });
    // ASSERTION-PROOF-OF-DOD: 200 = admission webhook accepted.
    expect(resp.status()).toBe(200);
    const json = await resp.json();
    // ASSERTION-PROOF-OF-DOD: server-side default resolution echoed back.
    expect(json.resolved?.defaults?.['multi-region']).toBe('active-hot-standby');
  });

  test('missing spec.topology rejected (G116 + G117.1 gate)', async ({ request }) => {
    const body = {
      apiVersion: 'catalyst.openova.io/v1alpha1',
      kind: 'Blueprint',
      metadata: { name: 'g117-1-missing-topology' },
      spec: {},
    };
    const resp = await request.post('/sovereign/api/v1/blueprints/validate', { data: body });
    // ASSERTION-PROOF-OF-DOD: every Blueprint MUST declare spec.topology.
    expect(resp.status()).toBeGreaterThanOrEqual(400);
  });

  test('multi-region default cannot reference a topology not in supported[]', async ({ request }) => {
    const body = {
      apiVersion: 'catalyst.openova.io/v1alpha1',
      kind: 'Blueprint',
      metadata: { name: 'g117-1-default-outside-supported' },
      spec: {
        topology: {
          supported: ['singleton'],
          defaults: { 'multi-region': 'active-active', 'single-region': 'singleton' },
        },
      },
    };
    const resp = await request.post('/sovereign/api/v1/blueprints/validate', { data: body });
    // ASSERTION-PROOF-OF-DOD: webhook cross-checks defaults ⊂ supported.
    expect(resp.status()).toBeGreaterThanOrEqual(400);
    const json = await resp.json();
    expect(json.error || json.message || '').toMatch(/defaults.*supported|supported.*defaults/i);
  });
});
