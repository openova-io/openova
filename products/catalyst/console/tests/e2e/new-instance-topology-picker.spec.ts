// G117.2 DoD — new-instance dialog + topology picker.
//
// Verifies:
//   1. Topology picker renders ALL supportedTopologies from the Blueprint
//   2. Default radio is auto-selected per Sovereign region count
//   3. Multi-region-required topologies are disabled on single-region
//   4. Form submission sends POST /apps/instances with the picked topology

import { test, expect } from '@playwright/test';

test.describe('G117.2 — new-instance topology picker', () => {
  test('topology picker renders blueprint.supportedTopologies', async ({ page }) => {
    await page.goto('/catalog/grafana/new');
    await expect(page.getByTestId('new-instance-form')).toBeVisible();

    const picker = page.getByTestId('topology-picker');
    await expect(picker).toBeVisible();
    // grafana mock fixture: supportedTopologies = [active-hot-standby, singleton]
    await expect(page.getByTestId('topology-radio-active-hot-standby')).toBeVisible();
    await expect(page.getByTestId('topology-radio-singleton')).toBeVisible();
  });

  test('single-region Sovereign auto-selects singleton + disables multi-region', async ({ page }) => {
    await page.goto('/catalog/grafana/new');
    await expect(page.getByTestId('new-instance-form')).toBeVisible();

    // Astro shell defaults regionsCount=1 → singleton picked by default.
    const singletonRadio = page.getByTestId('topology-radio-singleton');
    await expect(singletonRadio).toBeChecked();

    // active-hot-standby requires multi-region → disabled on regionsCount=1.
    const multiRegionRadio = page.getByTestId('topology-radio-active-hot-standby');
    await expect(multiRegionRadio).toBeDisabled();
  });

  test('form submit creates instance and navigates to /apps/{id}', async ({ page }) => {
    await page.goto('/catalog/grafana/new');
    await expect(page.getByTestId('new-instance-form')).toBeVisible();

    // Fill required fields. Mock backend assigns a UUID.
    await page.getByTestId('instance-name').fill('e2e-test');
    await page.getByTestId('instance-org').fill('acme-e2e');

    // Click singleton explicitly (already pre-selected; this confirms
    // the radio is interactive).
    await page.getByTestId('topology-radio-singleton').check();

    await page.getByTestId('instance-submit').click();

    // Mock backend returns a fresh UUID; URL should change to /apps/<uuid>.
    // We accept any UUID-shaped path or 'mock-*' prefix.
    await page.waitForURL(/\/apps\/[\w-]+/, { timeout: 5_000 });
    await expect(page.getByTestId('app-detail')).toBeVisible();
  });

  test('singleton-per-Org Blueprint enforces uniqueness server-side', async ({ page }) => {
    // harbor mock fixture: multiInstance.enabled=false; org=acme already has
    // an instance. Submitting another acme instance must surface the 409.
    await page.goto('/catalog/harbor/new');
    await expect(page.getByTestId('new-instance-form')).toBeVisible();

    await page.getByTestId('instance-name').fill('extra-harbor');
    await page.getByTestId('instance-org').fill('acme');

    await page.getByTestId('instance-submit').click();

    // Mock backend throws code=conflict; the form surfaces the message.
    const err = page.getByTestId('instance-error');
    await expect(err).toBeVisible({ timeout: 5_000 });
    await expect(err).toContainText(/singleton-per-Org|already has an instance/i);
  });
});
