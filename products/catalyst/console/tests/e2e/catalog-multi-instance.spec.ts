// G117.2 DoD — catalog drill-down + multi-instance flow.
//
// Verifies:
//   1. Catalog page renders Blueprint metadata + instance table (NOT a
//      single card per blueprint — locked architecture)
//   2. Multi-instance Blueprints (grafana) expose "+ New instance"
//   3. Singleton-per-Org Blueprints (harbor) hide "+ New" when instance
//      already exists
//   4. Clicking an instance row navigates to /apps/{id}
//   5. Endpoint shape contract (catalystApi-compatible)

import { test, expect } from '@playwright/test';

test.describe('G117.2 — catalog drill-down + multi-instance', () => {
  test('grafana page renders Blueprint + multi-instance "+ New" button', async ({ page }) => {
    await page.goto('/catalog/grafana');

    // Wait for the catalog drill-down container to populate (it triggers a
    // mocked catalystApi.getBlueprint + listBlueprintInstances pair).
    const drilldown = page.getByTestId('catalog-drilldown');
    await expect(drilldown).toBeVisible();
    await expect(drilldown).toHaveAttribute('data-blueprint', 'grafana');

    // Blueprint header rendered from mock fixture. Scope to the drill-down
    // container so we don't collide with the PortalShell breadcrumbs/title.
    await expect(drilldown.getByRole('heading', { name: 'Grafana' })).toBeVisible();
    await expect(drilldown.getByText('v1.0.4')).toBeVisible();
    await expect(drilldown.getByText('multi-instance')).toBeVisible();

    // Multi-instance Blueprint exposes "+ New instance".
    const newBtn = page.getByTestId('btn-new-instance');
    await expect(newBtn).toBeVisible();
    await expect(newBtn).toHaveAttribute('href', '/catalog/grafana/new');

    // Two seeded grafana instances render as rows (NOT a single card).
    await expect(page.getByTestId('instance-row-8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a')).toBeVisible();
    await expect(page.getByTestId('instance-row-c2d8e4a1-3b6f-4e7c-8a9d-1f5b7c3e9a2d')).toBeVisible();
  });

  test('harbor page (singleton-per-Org) hides "+ New" when instance exists', async ({ page }) => {
    await page.goto('/catalog/harbor');
    const drilldown = page.getByTestId('catalog-drilldown');
    await expect(drilldown).toBeVisible();
    // Two matches expected: header badge + "already installed" hint. The
    // presence is what matters for the singleton-Blueprint contract.
    await expect(drilldown.getByText('singleton-per-Org').first()).toBeVisible();
    await expect(page.getByTestId('btn-new-instance')).toHaveCount(0);
    await expect(page.getByTestId('btn-install-singleton')).toHaveCount(0);
    // The existing singleton instance is shown.
    await expect(page.getByTestId('instance-row-f3a9b1d8-4c2e-4d5f-9a7b-6e8c1d3f5a7b')).toBeVisible();
  });

  test('clicking an instance row navigates to /apps/{id}', async ({ page }) => {
    await page.goto('/catalog/grafana');
    await expect(page.getByTestId('catalog-drilldown')).toBeVisible();

    const id = '8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a';
    await page.getByTestId(`instance-link-${id}`).click();
    await expect(page).toHaveURL(new RegExp(`/apps/${id}`));
    await expect(page.getByTestId('app-detail')).toBeVisible();
  });

  test('listed instances honor topology + status shape', async ({ page }) => {
    await page.goto('/catalog/grafana');
    await expect(page.getByTestId('catalog-drilldown')).toBeVisible();

    // Verify each row carries topology + status badges per the
    // ApplicationSummary shape (G117 contract). Drift would surface as
    // empty cells or missing classes.
    const row = page.getByTestId('instance-row-8a1e9bf2-7c4d-4b3a-9f1e-2c5a8d3f6e0a');
    await expect(row.getByText('active-hot-standby')).toBeVisible();
    await expect(row.getByText('Ready')).toBeVisible();
  });
});
