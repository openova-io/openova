/**
 * org-multi-domain.spec.ts — Playwright E2E for the multi-
 * domain Organization onboarding flow (issue #828, parent epic #825).
 *
 * Two paths exercised:
 *
 *   1. Free-subdomain mode — operator picks a parent from the pool
 *      dropdown, the console URL preview updates live, and the
 *      submitted POST carries the chosen parent_domain.
 *   2. BYO mode — operator types the Organization apex; the parent dropdown
 *      is hidden; the submitted POST omits parent_domain.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the test
 * mocks the back end's parent-domain pool with a 2-entry list
 * (omani.works + omani.trade) so the SPA exercises the data-driven
 * dropdown shape without depending on a live Sovereign cluster.
 *
 * Per CLAUDE.md memory feedback_parallel_agents_e2e: every UI-touching
 * agent ends with 1440 px screenshots saved under e2e/screenshots/.
 */

import { expect, test } from '@playwright/test'

const PORTAL_DISCOVERY = {
  host: 'console.otech.example',
  tenant_id: 'portal-otech',
  tenant_kind: 'otech',
  keycloak_realm_url: 'https://kc.otech.example/realms/otech',
  keycloak_client_id: 'catalyst-ui',
}

const POOL_RESPONSE = {
  items: [
    { name: 'omani.works', role: 'org-pool', flipStatus: 'ready' },
    { name: 'omani.trade', role: 'org-pool', flipStatus: 'ready' },
    { name: 'not-flipped.example', role: 'org-pool', flipStatus: 'flipping' },
  ],
}

test.describe('Organization multi-domain onboarding (issue #828)', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/tenant/discover*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(PORTAL_DISCOVERY),
      })
    })
    await page.route('**/api/v1/whoami', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          email: 'operator@otech.example',
          sub: 'kc-op-uid',
          name: 'Otech Operator',
        }),
      })
    })
    await page.route('**/api/v1/sovereign/parent-domains*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(POOL_RESPONSE),
      })
    })
  })

  test('free-subdomain: operator picks parent_domain from dropdown', async ({ page }) => {
    let lastBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/organizations', async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue()
        return
      }
      lastBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({
          org_tenant_id: 'org-uuid-free',
          state: 'done',
          subdomain: 'acme',
          domain_mode: 'free-subdomain',
          parent_domain: 'omani.trade',
          admin_email: 'admin@acme.example',
          company_name: 'Acme',
          otech_fqdn: 'otech.example',
          vcluster_name: 'vc-acme',
          tenant_namespace: 'org-uuid-free-ns',
          console_host: 'console.acme.omani.trade',
          commit_sha: 'sha-test',
          steps: {
            vcluster: 'done',
            bp_charts: 'done',
            dns: 'done',
            certs: 'done',
            keycloak_clients: 'done',
            registry: 'done',
          },
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }),
      })
    })

    await page.goto('/console/organizations/new')
    await expect(page.getByTestId('org-create-page')).toBeVisible()
    await expect(page.getByTestId('org-create-form')).toBeVisible()

    // Initial 1440 px screenshot — pristine form with the parent
    // dropdown populated from the mocked pool.
    await page.screenshot({
      path: 'e2e/screenshots/828-create-org-form-1440.png',
      fullPage: true,
    })

    await page.getByTestId('org-create-subdomain').fill('acme')
    await page.getByTestId('org-create-company').fill('Acme')
    await page.getByTestId('org-create-email').fill('admin@acme.example')
    // Pick omani.trade.
    await page
      .getByTestId('org-create-parent-select')
      .selectOption('omani.trade')

    // The URL preview reflects the chosen parent.
    await expect(page.getByTestId('org-create-url-preview')).toHaveText(
      'console.acme.omani.trade',
    )

    await page.screenshot({
      path: 'e2e/screenshots/828-create-org-free-filled-1440.png',
      fullPage: true,
    })

    await page.getByTestId('org-create-submit').click()
    await expect(page.getByTestId('org-create-result')).toBeVisible({
      timeout: 5000,
    })

    // The submitted body carried parent_domain=omani.trade.
    expect(lastBody).toMatchObject({
      subdomain: 'acme',
      domain_mode: 'free-subdomain',
      parent_domain: 'omani.trade',
      admin_email: 'admin@acme.example',
    })

    // Post-submit: the result panel renders with the chosen parent.
    await expect(
      page.getByTestId('org-create-result-parent'),
    ).toHaveText('omani.trade')

    await page.screenshot({
      path: 'e2e/screenshots/828-create-org-free-success-1440.png',
      fullPage: true,
    })
  })

  test('byo: operator types apex; parent dropdown hidden', async ({ page }) => {
    let lastBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/organizations', async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue()
        return
      }
      lastBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({
          org_tenant_id: 'org-uuid-byo',
          state: 'done',
          subdomain: 'acme',
          domain_mode: 'byo',
          byo_domain: 'acme.com',
          admin_email: 'admin@acme.com',
          otech_fqdn: 'otech.example',
          vcluster_name: 'vc-acme',
          tenant_namespace: 'org-uuid-byo-ns',
          console_host: 'console.acme.com',
          commit_sha: 'sha-byo',
          steps: {
            vcluster: 'done',
            bp_charts: 'done',
            dns: 'done',
            certs: 'done',
            keycloak_clients: 'done',
            registry: 'done',
          },
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }),
      })
    })

    await page.goto('/console/organizations/new')
    await expect(page.getByTestId('org-create-page')).toBeVisible()

    // Switch to BYO mode — the parent dropdown disappears.
    await page
      .getByTestId('org-create-mode-byo')
      .locator('input[type="radio"]')
      .click()
    await expect(page.getByTestId('org-create-parent-row')).toHaveCount(
      0,
    )
    await expect(page.getByTestId('org-create-byo')).toBeVisible()

    await page.getByTestId('org-create-subdomain').fill('acme')
    await page.getByTestId('org-create-email').fill('admin@acme.com')
    await page.getByTestId('org-create-byo').fill('acme.com')

    await expect(page.getByTestId('org-create-url-preview')).toHaveText(
      'console.acme.com',
    )

    await page.screenshot({
      path: 'e2e/screenshots/828-create-org-byo-filled-1440.png',
      fullPage: true,
    })

    await page.getByTestId('org-create-submit').click()
    await expect(page.getByTestId('org-create-result')).toBeVisible({
      timeout: 5000,
    })

    expect(lastBody).toMatchObject({
      subdomain: 'acme',
      domain_mode: 'byo',
      byo_domain: 'acme.com',
      admin_email: 'admin@acme.com',
    })
    // BYO submission MUST omit parent_domain (the back end derives the
    // host from byo_domain instead).
    expect(lastBody?.parent_domain).toBeUndefined()

    await page.screenshot({
      path: 'e2e/screenshots/828-create-org-byo-success-1440.png',
      fullPage: true,
    })
  })
})
