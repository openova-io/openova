// G117.3 DoD — Endpoint add via Gitea PR pipeline.
//
// Verifies:
//   1. UI "Add endpoint" → POST opens a PR against gitea.<sov>/<org>/iac
//   2. PR auto-merges after Kyverno + cert-manager + DNS-conflict checks
//   3. Endpoint goes live (cert Issued + DNS resolves) within 2 minutes
//   4. DNS-conflict pre-check blocks a colliding hostname
//
// Promotion gate (Wave-2): unskip after G117.3 Endpoints tab + PR pipeline
// land in catalyst-api.
// Live-mode dependency: live gitea.<sov> + cert-manager + DNS server.

import { test, expect } from '@playwright/test';

const APP_ID = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';
const GITEA_LIVE = process.env.G117_GITEA_LIVE === '1';

test.describe('G117.3 — endpoint add via Gitea PR pipeline', () => {
  test('Endpoints tab exposes "+ Add endpoint" button', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await expect(page.getByTestId('endpoints-tab')).toBeVisible();
    // ASSERTION-PROOF-OF-DOD: UI surface present for endpoint mutation.
    await expect(page.getByTestId('btn-add-endpoint')).toBeVisible();
  });

  test.skip(!GITEA_LIVE, 'requires live Gitea + PR auto-merge harness (Wave-2 promotion gate)');

  test('submitting a new endpoint opens a Gitea PR', async ({ page, request }) => {
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await page.getByTestId('btn-add-endpoint').click();
    await page.getByTestId('endpoint-name-input').fill('staging-ui');
    await page.getByTestId('endpoint-hostname-input').fill('staging-grafana.acme.t01.omani.works');
    await page.getByTestId('endpoint-submit').click();

    // ASSERTION-PROOF-OF-DOD: response contains a PR URL on gitea.<sov>.
    const prUrl = await page.getByTestId('endpoint-pr-link').getAttribute('href');
    expect(prUrl).toMatch(/^https:\/\/gitea\.[^/]+\/acme\/iac\/pulls\/\d+$/);

    // Cross-check that Gitea actually has the PR open.
    const apiUrl = prUrl!.replace('/pulls/', '/api/v1/repos/acme/iac/pulls/');
    const apiResp = await request.get(apiUrl);
    expect(apiResp.status()).toBe(200);
  });

  test('endpoint goes live in <2 min after submit', async ({ page }) => {
    test.setTimeout(180_000);
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await page.getByTestId('btn-add-endpoint').click();
    await page.getByTestId('endpoint-name-input').fill('latency-probe');
    await page.getByTestId('endpoint-hostname-input').fill('latency-grafana.acme.t01.omani.works');
    const t0 = Date.now();
    await page.getByTestId('endpoint-submit').click();

    // Poll until the new endpoint row shows status=Ready + cert=Issued.
    await page.waitForFunction(
      () => {
        const row = document.querySelector('[data-testid="endpoint-latency-probe"]');
        const status = row?.querySelector('[data-status]')?.getAttribute('data-status');
        const cert = row?.querySelector('[data-cert]')?.getAttribute('data-cert');
        return status === 'Ready' && cert === 'Issued';
      },
      undefined,
      { timeout: 120_000 },
    );
    const elapsed = Date.now() - t0;
    // ASSERTION-PROOF-OF-DOD: end-to-end PR-merge → cert-issued → DNS-live < 2min.
    expect(elapsed).toBeLessThan(120_000);
  });

  test('DNS-conflict pre-check blocks a colliding hostname', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await page.getByTestId('btn-add-endpoint').click();
    // Reuse the hostname of an existing endpoint somewhere on the Sovereign.
    await page.getByTestId('endpoint-hostname-input').fill('grafana.acme.t01.omani.works');
    await page.getByTestId('endpoint-hostname-input').blur();
    // ASSERTION-PROOF-OF-DOD: UI surfaces the pre-check failure before submit.
    await expect(page.getByTestId('endpoint-hostname-error')).toContainText(/conflict|already.*registered|in use/i);
    await expect(page.getByTestId('endpoint-submit')).toBeDisabled();
  });

  test('Kyverno policy violation surfaces in the PR-merge log', async ({ page }) => {
    await page.goto(`/apps/${APP_ID}/endpoints`);
    await page.getByTestId('btn-add-endpoint').click();
    // E.g. visibility=public but no TLS → Kyverno should block.
    await page.getByTestId('endpoint-name-input').fill('insecure-leak');
    await page.getByTestId('endpoint-hostname-input').fill('insecure.acme.t01.omani.works');
    await page.getByTestId('endpoint-tls-toggle').uncheck();
    await page.getByTestId('endpoint-visibility-public').check();
    await page.getByTestId('endpoint-submit').click();
    // PR opens, but auto-merge fails because Kyverno blocks.
    await expect(page.getByTestId('endpoint-pr-status')).toContainText(/Kyverno|policy.*violation|blocked/i);
  });
});
