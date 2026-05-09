/**
 * rbac-management.spec.ts — Playwright E2E for the EPIC-3 (#1098)
 * RBAC management UI bundle (slice U1+U2+U3+U4).
 *
 * Six 1440x900 snapshots (per `feedback_per_issue_playwright_verification.md` —
 * one per page, never collapsed):
 *
 *   1. multi-grant editor renders the full form
 *   2. KCUserPicker search → results dropdown
 *   3. multi-grant editor submit → toast "created"
 *   4. group browser tree renders
 *   5. group browser add subgroup
 *   6. role browser realm-roles list + members panel
 *
 * Each test mocks the catalyst-api endpoints so the page renders
 * deterministically without a live backend.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'rbac-1098'

// ── Stub fixtures ──────────────────────────────────────────────────

const ALICE = {
  id: 'kc-uuid-alice',
  username: 'alice',
  email: 'alice@acme.com',
  firstName: 'Alice',
  lastName: 'Example',
  source: 'keycloak',
}

const BOB_FED = {
  id: 'kc-uuid-bob',
  username: 'bob.fed',
  email: 'bob@corp.com',
  source: 'azure_ad_federated',
}

const KCROLES = [
  {
    id: 'r1',
    name: 'catalyst-viewer',
    composite: false,
    attributes: { 'tier-level': ['10'] },
    description: 'read-only',
  },
  {
    id: 'r2',
    name: 'catalyst-developer',
    composite: true,
    attributes: { 'tier-level': ['20'] },
  },
  {
    id: 'r3',
    name: 'catalyst-admin',
    composite: true,
    attributes: { 'tier-level': ['40'] },
  },
]

const KCGROUPS = [
  {
    id: 'g1',
    name: 'acme',
    path: '/acme',
    attributes: { org: ['acme'] },
    subGroups: [
      { id: 'g3', name: 'sre', path: '/acme/sre' },
    ],
  },
  { id: 'g2', name: 'platform', path: '/platform' },
]

// ── Mock harness ───────────────────────────────────────────────────

async function mockRBACAPI(page: Page) {
  await page.route(/.*\/api\/v1\/whoami$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'test-admin', email: 'admin@example.com' }),
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
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID }),
    })
  })

  // U2 — user search.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/users\?.*/, (route: Route) => {
    const url = new URL(route.request().url())
    const search = url.searchParams.get('search') ?? ''
    let items = [ALICE, BOB_FED]
    if (search.length > 0) {
      const q = search.toLowerCase()
      items = items.filter(
        (u) =>
          u.username.toLowerCase().includes(q) ||
          (u.email ?? '').toLowerCase().includes(q),
      )
    }
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items }),
    })
  })

  // A1 — /rbac/assign — happy path returns "created".
  let assignCount = 0
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/rbac\/assign$/, (route: Route) => {
    if (route.request().method() !== 'POST') {
      route.continue()
      return
    }
    assignCount += 1
    route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        userAccess: { name: 'rbac-aliceacmecom-12345678', uid: 'fake-uid', namespace: '' },
        tierClusterRole: 'openova:tier-developer',
        applied: assignCount === 1 ? 'created' : 'no-op',
      }),
    })
  })

  // U3 — groups.
  let groupsState = JSON.parse(JSON.stringify(KCGROUPS)) as typeof KCGROUPS
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/groups$/, (route: Route) => {
    const m = route.request().method()
    if (m === 'GET') {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: groupsState }),
      })
      return
    }
    if (m === 'POST') {
      const body = route.request().postDataJSON()
      const newG = {
        id: 'gnew',
        name: body.name,
        path: body.parentId ? `/parent/${body.name}` : `/${body.name}`,
        attributes: body.attributes ?? {},
      }
      groupsState = [...groupsState, newG]
      route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify(newG),
      })
      return
    }
    route.continue()
  })
  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/groups\/[^/]+$/,
    (route: Route) => {
      const m = route.request().method()
      if (m === 'PUT') {
        route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
        return
      }
      if (m === 'DELETE') {
        route.fulfill({ status: 204, body: '' })
        return
      }
      route.continue()
    },
  )

  // U4 — roles + members + client roles.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/roles$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items: KCROLES }),
    })
  })
  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/roles\/[^/]+\/members$/,
    (route: Route) => {
      const url = route.request().url()
      const m = url.match(/\/roles\/([^/]+)\/members$/)
      const role = m ? decodeURIComponent(m[1]) : ''
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ role, items: [ALICE] }),
      })
    },
  )
  await page.route(
    /.*\/api\/v1\/sovereigns\/[^/]+\/keycloak\/clients\/[^/]+\/roles$/,
    (route: Route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ items: [{ id: 'cr1', name: 'wp-editor', clientRole: true }] }),
      })
    },
  )
}

// ── Tests ──────────────────────────────────────────────────────────

test.describe('RBAC management UI (slice U1+U2+U3+U4, #1098)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('U1.1: multi-grant editor renders form scaffold', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/grant`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(600)
    await expect(page.getByTestId('multi-grant-edit-page')).toBeVisible()
    await expect(page.getByTestId('multi-grant-tier-picker')).toBeVisible()
    await expect(page.getByTestId('multi-grant-scope-picker')).toBeVisible()
    await expect(page.getByTestId('multi-grant-user-fieldset')).toBeVisible()
    // Default tier action preview visible.
    await expect(page.getByTestId('multi-grant-tier-preview-viewer')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u1-multi-grant-form-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U1.2: scope chip add + remove works against canonical vocab', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/grant`)
    await page.waitForTimeout(600)
    // Pick scope key + value, click Add.
    await page
      .getByTestId('multi-grant-scope-key-input')
      .selectOption('openova.io/application')
    await page.getByTestId('multi-grant-scope-value-input').fill('wordpress')
    await page.getByTestId('multi-grant-scope-add').click()
    await expect(page.getByTestId('multi-grant-scope-chip-0')).toBeVisible()
    await expect(page.getByTestId('multi-grant-scope-chip-0')).toContainText(
      'openova.io/application=wordpress',
    )
    // Remove it again.
    await page.getByTestId('multi-grant-scope-remove-0').click()
    await expect(page.getByTestId('multi-grant-scope-empty')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u1-scope-chips-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U2: user picker search → results → click selects user', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/grant`)
    await page.waitForTimeout(600)
    await page.getByTestId('kc-user-picker-input').fill('alice')
    await page.waitForTimeout(500) // debounce + fetch
    await expect(page.getByTestId('kc-user-picker-listbox')).toBeVisible()
    await expect(page.getByTestId('kc-user-picker-result-kc-uuid-alice')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u2-user-picker-results-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
    await page.getByTestId('kc-user-picker-result-kc-uuid-alice').click()
    await expect(page.getByTestId('multi-grant-user-pinned')).toBeVisible()
  })

  test('U1.3: full submit shows "created" toast', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/grant`)
    await page.waitForTimeout(600)
    // Pick developer tier.
    await page.getByTestId('multi-grant-tier-developer').click()
    // Add a scope.
    await page
      .getByTestId('multi-grant-scope-key-input')
      .selectOption('openova.io/application')
    await page.getByTestId('multi-grant-scope-value-input').fill('wordpress')
    await page.getByTestId('multi-grant-scope-add').click()
    // Pick user.
    await page.getByTestId('kc-user-picker-input').fill('alice')
    await page.waitForTimeout(500)
    await page.getByTestId('kc-user-picker-result-kc-uuid-alice').click()
    // Apply.
    await page.getByTestId('multi-grant-apply').click()
    await page.waitForTimeout(500)
    await expect(page.getByTestId('multi-grant-toast-ok')).toBeVisible()
    await expect(page.getByTestId('multi-grant-toast-ok')).toContainText('Granted developer')
    await page.screenshot({
      path: `playwright-report/rbac-u1-toast-created-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U3: group browser tree renders', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/groups`)
    await page.waitForTimeout(600)
    await expect(page.getByTestId('group-browser-page')).toBeVisible()
    await expect(page.getByTestId('group-browser-tree')).toBeVisible()
    await expect(page.getByTestId('group-browser-node-g1')).toBeVisible()
    await expect(page.getByTestId('group-browser-node-g2')).toBeVisible()
    // Sub-group rendered nested.
    await expect(page.getByTestId('group-browser-node-g3')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u3-group-tree-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U3: add subgroup form posts to /keycloak/groups', async ({ page }) => {
    await mockRBACAPI(page)
    let lastCreate: unknown = null
    page.on('request', (req) => {
      if (req.method() === 'POST' && /\/keycloak\/groups$/.test(req.url())) {
        lastCreate = req.postDataJSON()
      }
    })
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/groups`)
    await page.waitForTimeout(600)
    await page.getByTestId('group-browser-new-name').fill('billing')
    await page.getByTestId('group-browser-new-submit').click()
    await page.waitForTimeout(500)
    expect(lastCreate).toMatchObject({ name: 'billing' })
    await page.screenshot({
      path: `playwright-report/rbac-u3-add-group-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('U4: realm-roles list + members panel', async ({ page }) => {
    await mockRBACAPI(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/rbac/roles`)
    await page.waitForTimeout(600)
    await expect(page.getByTestId('role-browser-page')).toBeVisible()
    await expect(page.getByTestId('role-browser-table')).toBeVisible()
    await expect(page.getByTestId('role-browser-row-catalyst-viewer')).toBeVisible()
    await expect(page.getByTestId('role-browser-row-catalyst-developer')).toBeVisible()
    await expect(page.getByTestId('role-browser-row-catalyst-admin')).toBeVisible()
    // Click a row → members panel renders.
    await page.getByTestId('role-browser-row-catalyst-admin').click()
    await page.waitForTimeout(500)
    await expect(page.getByTestId('role-browser-members-catalyst-admin')).toBeVisible()
    await expect(page.getByTestId('role-browser-member-kc-uuid-alice')).toBeVisible()
    await page.screenshot({
      path: `playwright-report/rbac-u4-realm-roles-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
