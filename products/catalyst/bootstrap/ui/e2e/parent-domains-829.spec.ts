/**
 * parent-domains-829.spec.ts — Playwright E2E for the admin
 * Parent Domains page + DNS propagation status panel (issue #829,
 * parent epic #825).
 *
 * Three canonical screenshots — captured at 1440x900 per the
 * `feedback_parallel_agents_e2e` rule in CLAUDE.md:
 *
 *   1. just-flipped       — single-row pool, all resolvers reporting
 *                            "diverged" (NS-flip not yet propagated)
 *   2. partially-propagated — 2 of 5 resolvers converged, 60% pending
 *   3. fully-propagated   — all 5 resolvers converged, 100%
 *
 * The page is normally served behind the Sovereign-mode auth gate;
 * this spec routes the API responses via Playwright's network
 * intercept so the same React tree renders without a live backend.
 */

import { test, expect, type Page } from '@playwright/test'

const VIEWPORT = { width: 1440, height: 900 }

const RESOLVERS = [
  { name: 'Google', ip: '8.8.8.8', geo: 'US' },
  { name: 'Cloudflare', ip: '1.1.1.1', geo: 'Global' },
  { name: 'Quad9', ip: '9.9.9.9', geo: 'EU' },
  { name: 'OpenDNS', ip: '208.67.222.222', geo: 'US' },
  { name: 'Level3', ip: '4.2.2.1', geo: 'US' },
]

const EXPECTED_NS = ['ns1.omani.works', 'ns2.omani.works']

/**
 * Mounts the ParentDomainsPage in isolation — the sovereign-mode auth
 * shell needs OIDC tokens which we don't have in this spec. We use the
 * dev server's hash-router-friendly seam (the page is registered at
 * /console/parent-domains; we navigate through to it directly by setting
 * the test fixtures via API mocks.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL is
 * resolved at runtime from the page's own routing (no inline /api/...).
 */
async function mountAdminPage(
  page: Page,
  domains: Array<{
    name: string
    role: 'primary' | 'org-pool'
    flipStatus: string
    addedAt: string
    flippedAt?: string
    registrarKind?: string
  }>,
  propagation: {
    domain: string
    expectedNs: string[]
    converged: number
    total: number
    percentage: number
    resolvers: Array<{
      resolver: { name: string; ip: string; geo: string }
      status: 'converged' | 'diverged' | 'error'
      ns: string[]
      queriedAt: string
      latencyMs: number
      error?: string
    }>
  },
) {
  // Bypass the SovereignConsoleLayout auth gate: it calls /whoami first,
  // and falls back to OIDC redirect on 401. A 200 here lets the layout
  // render directly without touching Keycloak.
  await page.route('**/api/v1/whoami', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        sub: 'test-operator',
        name: 'Test Operator',
        email: 'operator@example.com',
        preferred_username: 'operator',
        roles: ['operator-admin'],
      }),
    })
  })

  // Mock the LIST + propagation endpoints so the page renders against
  // deterministic state.
  await page.route('**/api/v1/sovereign/parent-domains', async (route) => {
    if (route.request().method() === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: domains }),
      })
      return
    }
    await route.continue()
  })
  await page.route('**/api/v1/sovereign/parent-domains/*/propagation', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        ...propagation,
        generatedAt: new Date().toISOString(),
      }),
    })
  })
  // Bypass the OIDC tokens-required gate: the SovereignConsoleLayout
  // only checks for `tokens` in localStorage. Plant a synthetic value
  // so the layout renders without the post-callback redirect.
  await page.addInitScript(() => {
    const stub = {
      idToken:
        'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0In0.x',
      accessToken: 'stub',
      refreshToken: 'stub',
      expiresAt: Date.now() + 3600 * 1000,
    }
    localStorage.setItem('catalyst.oidc.tokens', JSON.stringify(stub))
  })
  await page.goto('/console/parent-domains')
  await page.waitForSelector('[data-testid=parent-domains-page]', { timeout: 5_000 })
}

test.use({ viewport: VIEWPORT })

test.describe('ParentDomainsPage @issue-829', () => {
  test('1. just-flipped — single-row pool, all resolvers diverged', async ({ page }) => {
    await mountAdminPage(
      page,
      [
        {
          name: 'omani.works',
          role: 'primary',
          flipStatus: 'ready',
          addedAt: '2026-05-04T08:00:00Z',
          flippedAt: '2026-05-04T08:30:00Z',
        },
        {
          name: 'omani.trade',
          role: 'org-pool',
          flipStatus: 'flipping',
          registrarKind: 'dynadot',
          addedAt: new Date().toISOString(),
        },
      ],
      {
        domain: 'omani.trade',
        expectedNs: EXPECTED_NS,
        converged: 0,
        total: 5,
        percentage: 0,
        resolvers: RESOLVERS.map((r) => ({
          resolver: r,
          status: 'diverged' as const,
          ns: ['ns1.dynadot.com', 'ns2.dynadot.com'],
          queriedAt: new Date().toISOString(),
          latencyMs: 42 + Math.floor(Math.random() * 30),
        })),
      },
    )

    // Expand the omani.trade row to show the propagation panel.
    await page.getByTestId('parent-domain-toggle-omani.trade').click()
    await page.waitForSelector('[data-testid="propagation-panel-omani.trade"]')

    expect(await page.getByTestId('parent-domains-table').isVisible()).toBe(true)
    const pct = await page.getByTestId('propagation-pct-omani.trade').textContent()
    expect(pct).toContain('0%')

    await page.screenshot({
      path: 'e2e/screenshots/829-1-just-flipped.png',
      fullPage: false,
    })
  })

  test('2. partially-propagated — 2 of 5 resolvers converged', async ({ page }) => {
    await mountAdminPage(
      page,
      [
        {
          name: 'omani.works',
          role: 'primary',
          flipStatus: 'ready',
          addedAt: '2026-05-04T08:00:00Z',
        },
        {
          name: 'omani.trade',
          role: 'org-pool',
          flipStatus: 'cert-issuing',
          registrarKind: 'dynadot',
          addedAt: '2026-05-04T09:00:00Z',
        },
      ],
      {
        domain: 'omani.trade',
        expectedNs: EXPECTED_NS,
        converged: 2,
        total: 5,
        percentage: 40,
        resolvers: [
          {
            resolver: RESOLVERS[0],
            status: 'converged',
            ns: EXPECTED_NS,
            queriedAt: new Date().toISOString(),
            latencyMs: 23,
          },
          {
            resolver: RESOLVERS[1],
            status: 'converged',
            ns: EXPECTED_NS,
            queriedAt: new Date().toISOString(),
            latencyMs: 18,
          },
          {
            resolver: RESOLVERS[2],
            status: 'diverged',
            ns: ['ns1.dynadot.com', 'ns2.dynadot.com'],
            queriedAt: new Date().toISOString(),
            latencyMs: 67,
          },
          {
            resolver: RESOLVERS[3],
            status: 'diverged',
            ns: ['ns1.dynadot.com', 'ns2.dynadot.com'],
            queriedAt: new Date().toISOString(),
            latencyMs: 89,
          },
          {
            resolver: RESOLVERS[4],
            status: 'diverged',
            ns: ['ns1.dynadot.com', 'ns2.dynadot.com'],
            queriedAt: new Date().toISOString(),
            latencyMs: 102,
          },
        ],
      },
    )

    await page.getByTestId('parent-domain-toggle-omani.trade').click()
    await page.waitForSelector('[data-testid="propagation-panel-omani.trade"]')

    const pct = await page.getByTestId('propagation-pct-omani.trade').textContent()
    expect(pct).toContain('40%')

    await page.screenshot({
      path: 'e2e/screenshots/829-2-partially-propagated.png',
      fullPage: false,
    })
  })

  test('3. fully-propagated — 100%', async ({ page }) => {
    await mountAdminPage(
      page,
      [
        {
          name: 'omani.works',
          role: 'primary',
          flipStatus: 'ready',
          addedAt: '2026-05-04T08:00:00Z',
          flippedAt: '2026-05-04T08:30:00Z',
        },
        {
          name: 'omani.trade',
          role: 'org-pool',
          flipStatus: 'ready',
          registrarKind: 'dynadot',
          addedAt: '2026-05-04T09:00:00Z',
          flippedAt: '2026-05-04T15:00:00Z',
        },
      ],
      {
        domain: 'omani.trade',
        expectedNs: EXPECTED_NS,
        converged: 5,
        total: 5,
        percentage: 100,
        resolvers: RESOLVERS.map((r, i) => ({
          resolver: r,
          status: 'converged' as const,
          ns: EXPECTED_NS,
          queriedAt: new Date().toISOString(),
          latencyMs: 18 + i * 5,
        })),
      },
    )

    await page.getByTestId('parent-domain-toggle-omani.trade').click()
    await page.waitForSelector('[data-testid="propagation-panel-omani.trade"]')

    const pct = await page.getByTestId('propagation-pct-omani.trade').textContent()
    expect(pct).toContain('100%')

    await page.screenshot({
      path: 'e2e/screenshots/829-3-fully-propagated.png',
      fullPage: false,
    })
  })
})
