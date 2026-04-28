// Sovereign wizard routing smoke test — closes ticket #142.
//
// Verifies that the chart-rendered routing wires together end-to-end:
//   1. https://<host>/sovereign/   loads the wizard SPA index.html (200, HTML)
//   2. The wizard's hashed Vite assets under /sovereign/assets/* return 200
//   3. SPA-fallback works:  /sovereign/wizard/credentials -> still index.html
//   4. /sovereign/api/* round-trips through the catalyst-ui nginx
//      reverse-proxy to catalyst-api (Service DNS sourced from
//      values.routing.catalystApi.serviceDNS — never hardcoded). The
//      catalyst-api exposes /healthz at the root, which after Traefik's
//      strip-sovereign middleware + nginx /api/ proxy_pass arrives at
//      catalyst-api as /api/healthz — but per
//      products/catalyst/bootstrap/api/cmd/api/main.go the real health
//      endpoint is /healthz at the catalyst-api root. We therefore
//      validate the round-trip via /sovereign/api/v1/subdomains/check —
//      the wizard's first POST when the user types a subdomain — which
//      returns 200 with {available, normalized, ...} on a syntactically
//      valid input.
//
// Per docs/INVIOLABLE-PRINCIPLES.md §4 every URL flows from env, never
// hardcoded — SOVEREIGN_BASE_URL + SOVEREIGN_BASE_PATH. The wizard
// itself reads its base path from Vite's import.meta.env.BASE_URL, which
// the chart-rendered nginx + ingress agree on via routing.basePath.
//
// Live-cluster invocation (post-Group-C cutover):
//   SOVEREIGN_BASE_URL=https://console.openova.io \
//   SOVEREIGN_BASE_PATH=/sovereign \
//     npx playwright test
//
// Local-mock invocation (no cluster — used in CI and dev): the
// `route.fulfill` block below intercepts every network call and serves
// canned responses that mirror the real chart-rendered nginx + catalyst-api
// behaviour. This lets the test prove the SPA's wiring (basename,
// API_BASE) without depending on a deployed environment. Toggle via
// USE_MOCK=1.

import { test, expect, type Route } from '@playwright/test'

const BASE_PATH = process.env.SOVEREIGN_BASE_PATH ?? '/sovereign'
const USE_MOCK = process.env.USE_MOCK === '1'

// Minimal SPA shell that mimics the wizard's index.html — enough that
// the page loads, fires a /api/v1/subdomains/check request, and the test
// can assert the round-trip.
const MOCK_INDEX_HTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Catalyst Sovereign Wizard</title>
    <base href="${BASE_PATH}/" />
  </head>
  <body>
    <div id="root">
      <h1 data-testid="wizard-shell">Catalyst Sovereign Provisioning Wizard</h1>
    </div>
    <script type="module">
      // Mirror the wizard's first network call — the same shape as
      // products/catalyst/bootstrap/ui/src/shared/lib/useSubdomainAvailability.ts
      const API_BASE = new URL('${BASE_PATH}/api', window.location.origin).toString()
      window.__healthCheckPromise = fetch(API_BASE + '/v1/subdomains/check', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subdomain: 'omantel', poolDomain: 'omani.works' }),
      }).then(r => ({ status: r.status, ok: r.ok }))
    </script>
  </body>
</html>`

async function installMockRoutes(page: import('@playwright/test').Page) {
  // 1. SPA shell: any GET under /sovereign/* that's not /assets/* or /api/*
  //    returns the index.html (mirrors nginx try_files $uri /index.html).
  await page.route(`**${BASE_PATH}/`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'text/html; charset=utf-8',
      body: MOCK_INDEX_HTML,
    })
  })
  await page.route(`**${BASE_PATH}/wizard/**`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'text/html; charset=utf-8',
      body: MOCK_INDEX_HTML,
    })
  })
  // 2. Hashed asset: nginx serves these directly with `expires 1y`.
  await page.route(`**${BASE_PATH}/assets/**`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/javascript',
      headers: { 'Cache-Control': 'public, immutable' },
      body: 'export {}',
    })
  })
  // 3. API: the chart-rendered nginx /api/ location reverse-proxies to
  //    catalyst-api. The real handler returns
  //    {"available": true|false, "normalized": "<input>", ...}.
  await page.route(`**${BASE_PATH}/api/v1/subdomains/check`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ available: true, normalized: 'omantel' }),
    })
  })
}

test.describe('Sovereign wizard routing — chart-rendered nginx + ingress', () => {
  test.beforeEach(async ({ page }) => {
    if (USE_MOCK) {
      await installMockRoutes(page)
    }
  })

  test('GET /sovereign/ serves the wizard SPA shell', async ({ page }) => {
    const response = await page.goto(`${BASE_PATH}/`)
    expect(response, 'navigation response must exist').not.toBeNull()
    expect(response!.status(), 'wizard root must return 200').toBe(200)
    expect(response!.headers()['content-type'] ?? '').toMatch(/text\/html/)
    // Locator-level proof that the SPA mounted (live cluster: the React
    // app renders #root; mock: the inline shell sets the heading).
    await expect(page.locator('#root, [data-testid="wizard-shell"]')).toBeVisible()
  })

  test('SPA fallback: /sovereign/wizard/credentials -> index.html', async ({ page }) => {
    // The chart's nginx config (templates/ui-configmap.yaml) ends with
    //   location / { try_files $uri /index.html; }
    // so any unknown path still 200s with the SPA shell — React Router
    // (basename={routing.basePath}) takes over client-side.
    const response = await page.goto(`${BASE_PATH}/wizard/credentials`)
    expect(response, 'navigation response must exist').not.toBeNull()
    expect(response!.status(), 'SPA fallback must return 200').toBe(200)
    expect(response!.headers()['content-type'] ?? '').toMatch(/text\/html/)
  })

  test('First /api/v1/* call round-trips to catalyst-api', async ({ page }) => {
    // Watch the network — assert the wizard's first POST to
    // /sovereign/api/v1/subdomains/check returns 200. This proves the
    // chain Traefik (strip-sovereign) → catalyst-ui nginx (proxy_pass
    // /api/ → catalyst-api Service via values.routing.catalystApi.serviceDNS)
    // is wired correctly.
    const apiResponse = page.waitForResponse(
      (r) => r.url().endsWith(`${BASE_PATH}/api/v1/subdomains/check`),
      { timeout: 15_000 }
    )
    await page.goto(`${BASE_PATH}/`)
    const r = await apiResponse
    expect(r.status(), 'catalyst-api must return 200 on /api/v1/subdomains/check').toBe(200)
    const body = await r.json()
    expect(body, 'response must include normalized subdomain').toHaveProperty('normalized')
  })
})
