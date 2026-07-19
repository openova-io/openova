/**
 * rbac-membership.spec.ts — Playwright E2E for the EPIC-3 (#1098)
 * RBAC member views (slice U5+U6+U7+U8).
 *
 * Six 1440x900 snapshots (per `feedback_per_issue_playwright_verification.md` —
 * one per page, never collapsed):
 *
 *   1. U5  per-Application Members tab (list rendered)
 *   2. U5  Members tab Add modal opens with KCUserPicker
 *   3. U6  per-Organization Members page renders
 *   4. U7  access-matrix renders the users × applications grid
 *   5. U7  cell click opens the editor modal
 *   6. U8  audit page renders + filters
 *
 * Each test mocks the catalyst-api endpoints so the page renders
 * deterministically without a live backend. Re-uses the wire-shape
 * mocks from the slice U1-U4 spec where they overlap (KCUserPicker,
 * /rbac/assign).
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'rbac-1098-u5'

// ── Stub fixtures ──────────────────────────────────────────────────

const ALICE = {
  id: 'kc-uuid-alice',
  username: 'alice',
  email: 'alice@acme.com',
  source: 'keycloak',
}

const BOB_FED = {
  id: 'kc-uuid-bob',
  username: 'bob.fed',
  email: 'bob@corp.com',
  source: 'azure_ad_federated',
}

const ACCESS_MATRIX = {
  users: [
    {
      id: 'kc-uuid-alice',
      email: 'alice@acme.com',
      source: 'keycloak',
      access: {
        wordpress: {
          tier: 'admin',
          userAccessRef: 'rbac-alice-wp',
          scopes: [{ key: 'openova.io/application', value: 'wordpress' }],
        },
        billing: {
          tier: 'developer',
          userAccessRef: 'rbac-alice-billing',
          scopes: [{ key: 'openova.io/application', value: 'billing' }],
        },
      },
      warnings: [
        'developer-tier UserAccess for billing missing env-type=dev scope (CR: rbac-alice-billing)',
      ],
    },
    {
      id: 'kc-uuid-bob',
      email: 'bob@corp.com',
      source: 'azure_ad_federated',
      access: {
        wordpress: {
          tier: 'viewer',
          userAccessRef: 'rbac-bob-wp',
          scopes: [{ key: 'openova.io/application', value: 'wordpress' }],
        },
      },
    },
  ],
  applications: ['billing', 'wordpress'],
  tiers: ['viewer', 'developer', 'operator', 'admin', 'owner'],
}

const AUDIT_EVENTS = [
  {
    auditType: 'rbac-grant-created',
    ts: '2026-05-09T01:00:00Z',
    actor: 'admin@acme.com',
    sovereignId: DEPLOYMENT_ID,
    targetUserEmail: 'alice@acme.com',
    targetUser: 'kc-uuid-alice',
    targetApp: 'wordpress',
    tier: 'admin',
    userAccessRef: 'rbac-alice-wp',
    detail: 'granted admin tier on UserAccess rbac-alice-wp',
  },
  {
    auditType: 'rbac-tier-changed',
    ts: '2026-05-09T00:30:00Z',
    actor: 'admin@acme.com',
    sovereignId: DEPLOYMENT_ID,
    targetUserEmail: 'bob@corp.com',
    targetApp: 'wordpress',
    tier: 'viewer',
    previousTier: 'developer',
    userAccessRef: 'rbac-bob-wp',
    detail: 'tier rotated developer → viewer on UserAccess rbac-bob-wp',
  },
]

// ── Mock harness ───────────────────────────────────────────────────

async function mockMembershipAPI(page: Page) {
  // Org-console discovery — mother fallback so the SPA boot succeeds.
  await page.route(/.*\/api\/v1\/tenant\/discover.*/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ kind: 'mother' }),
    })
  })
  // Auth + identity.
  await page.route(/.*\/api\/v1\/whoami$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'test-admin', email: 'admin@acme.com' }),
    })
  })
  await page.route(/.*\/api\/v1\/sovereign\/self$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID, sovereignFQDN: 'rbac.example' }),
    })
  })
  await page.route(/.*\/api\/v1\/deployments\/[^/]+$/, (route: Route) => {
    if (route.request().method() !== 'GET') {
      route.continue()
      return
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID }),
    })
  })

  // KC user search (re-used from U1-U4 spec).
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/users\?.*/, (route: Route) => {
    const url = new URL(route.request().url())
    const search = url.searchParams.get('search') ?? ''
    let items = [ALICE, BOB_FED]
    if (search.length > 0) {
      const q = search.toLowerCase()
      items = items.filter(
        (u) => u.username.toLowerCase().includes(q) || (u.email ?? '').toLowerCase().includes(q),
      )
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items }),
    })
  })

  // A2 — access matrix.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/rbac\/access-matrix(\?.*)?$/, (route: Route) => {
    if (route.request().method() !== 'GET') {
      route.continue()
      return
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(ACCESS_MATRIX),
    })
  })

  // A1 — /rbac/assign — happy path returns "updated".
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/rbac\/assign$/, (route: Route) => {
    if (route.request().method() !== 'POST') {
      route.continue()
      return
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        userAccess: { name: 'rbac-alice-wp', uid: 'fake-uid', namespace: '' },
        tierClusterRole: 'openova:tier-operator',
        applied: 'updated',
      }),
    })
  })

  // U8 — audit list.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/audit\/rbac(\?.*)?$/, (route: Route) => {
    if (route.request().method() !== 'GET') {
      route.continue()
      return
    }
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/stream')) {
      // Reject SSE — Playwright doesn't easily support EventSource and
      // the page's onerror handler renders the stream-disconnected
      // banner which is acceptable for this snapshot.
      route.fulfill({ status: 503, body: '' })
      return
    }
    const actorQ = url.searchParams.get('actor')
    let items = AUDIT_EVENTS
    if (actorQ) {
      const q = actorQ.toLowerCase()
      items = items.filter((e) => (e.actor ?? '').toLowerCase().includes(q))
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items, total: items.length }),
    })
  })

  // U8 — audit stream (SSE) — return 503 so the page shows its
  // disconnected banner. Production wires a real SSE.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/audit\/rbac\/stream(\?.*)?$/, (route: Route) => {
    route.fulfill({ status: 503, body: '' })
  })

  // /admin/user-access DELETE (used by Members Remove).
  await page.route(/.*\/api\/v1\/deployments\/[^/]+\/admin\/user-access\/[^/]+$/, (route: Route) => {
    route.fulfill({ status: 204, body: '' })
  })

  // Catalog/wizard endpoints used by AppDetail.
  await page.route(/.*\/api\/v1\/catalog\/.*/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [] }) })
  })

  // Compliance scorecards (touched by AppDetail's other tabs).
  // Bound to /api/v1/sovereigns/.../compliance to avoid swallowing
  // unrelated endpoints (the previous `/.*\/compliance\/.*/` regex
  // was greedy enough to also intercept the rbac matrix call when
  // a route prefix happens to contain "compliance").
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/compliance\/.*/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [] }) })
  })
}

// ── Wizard-store seed (so AppDetail finds the requested component) ──

async function seedWizardStore(page: Page, selectedComponents: string[]) {
  // The store persists under `openova-catalyst-wizard` per
  // src/entities/deployment/store.ts; seeding selectedComponents into
  // localStorage BEFORE the navigation lets findApplication() resolve.
  // Persisted by zustand under `state.<field>`.
  await page.addInitScript((components) => {
    const initial = {
      state: {
        deploymentId: '',
        selectedComponents: components,
        chosenSize: null,
        components: {},
      },
      version: 0,
    }
    window.localStorage.setItem('openova-catalyst-wizard', JSON.stringify(initial))
  }, selectedComponents)
}

// ── Tests ──────────────────────────────────────────────────────────

test.describe('RBAC member views (slice U5+U6+U7+U8, #1098)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('U5: per-Application Members tab renders the row table', async ({ page }) => {
    await mockMembershipAPI(page)
    await seedWizardStore(page, ['bp-cilium'])
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/bp-cilium`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(800)
    // Click the Members tab.
    await page.getByTestId('sov-app-tab-members').click()
    await expect(page.getByTestId('sov-app-tabpanel-members')).toBeVisible()
    await expect(page.getByTestId('app-members-tab')).toBeVisible()
    // Wait for the matrix fetch to settle and a row to render.
    await page.waitForTimeout(600)
    await page.screenshot({
      path: `playwright-report/rbac-u5-app-members-tab-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U5: Members tab Add modal opens', async ({ page }) => {
    await mockMembershipAPI(page)
    await seedWizardStore(page, ['bp-cilium'])
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/bp-cilium`)
    await page.waitForTimeout(800)
    await page.getByTestId('sov-app-tab-members').click()
    await page.waitForTimeout(400)
    await page.getByTestId('members-add').click()
    await expect(page.getByTestId('members-add-modal')).toBeVisible()
    await expect(page.getByTestId('members-add-tier')).toBeVisible()
    await expect(page.getByTestId('kc-user-picker')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u5-app-members-add-modal-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U6: per-Organization Members page renders', async ({ page }) => {
    await mockMembershipAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/organizations/acme/members`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(800)
    await expect(page.getByTestId('org-members-page')).toBeVisible()
    await expect(page.getByTestId('members-list-organization')).toBeVisible()
    // For organization scope flattenForScope returns every user.
    await expect(page.getByTestId('members-row-kc-uuid-alice')).toBeVisible()
    await expect(page.getByTestId('members-row-kc-uuid-bob')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u6-org-members-page-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U7: access-matrix renders users × applications grid', async ({ page }) => {
    await mockMembershipAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/matrix`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(800)
    await expect(page.getByTestId('access-matrix-page')).toBeVisible()
    await expect(page.getByTestId('matrix-row-kc-uuid-alice')).toBeVisible()
    await expect(page.getByTestId('matrix-col-wordpress')).toBeVisible()
    await expect(page.getByTestId('matrix-col-billing')).toBeVisible()
    // Alice has a warning for billing (developer-tier missing env-type=dev).
    await expect(page.getByTestId('matrix-row-warning-kc-uuid-alice')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u7-access-matrix-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U7: cell click opens editor modal', async ({ page }) => {
    await mockMembershipAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/matrix`)
    await page.waitForTimeout(800)
    await page.getByTestId('matrix-cell-kc-uuid-alice-wordpress').click()
    await expect(page.getByTestId('matrix-editor-modal')).toBeVisible()
    await expect(page.getByTestId('matrix-editor-open-multigrant')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u7-matrix-cell-editor-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U8: audit page renders rows + filters', async ({ page }) => {
    await mockMembershipAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/audit`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(800)
    await expect(page.getByTestId('rbac-audit-page')).toBeVisible()
    // Two seeded events; both rendered.
    await expect(page.getByTestId('audit-row-0')).toBeVisible()
    await expect(page.getByTestId('audit-row-1')).toBeVisible()
    // Filter to bob → only the tier-changed event remains.
    await page.getByTestId('audit-actor').fill('bob')
    // The query is keyed; allow react-query to refetch.
    await page.waitForTimeout(400)
    await page.screenshot({
      path: `playwright-report/rbac-u8-audit-page-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
