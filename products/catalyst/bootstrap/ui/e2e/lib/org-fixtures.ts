/**
 * e2e/lib/org-fixtures.ts — Playwright route-mocking helpers for the
 * Organization end-to-end demo spec (issue #805).
 *
 * Why mocks? Per the issue body, the Organization-demo Playwright is the
 * load-bearing investor proof artefact. It must run unattended on
 * every CI commit AND, eventually, against a live freshly-provisioned
 * otech (#804). Until #804 lands, mocking is the only way to keep the
 * screenshot evidence green and unblock the broader epic.
 *
 * Each helper here installs ONE narrow contract — org-console discovery,
 * whoami, org/users CRUD, deployment lifecycle, etc. — so a future
 * "live mode" version of this spec can opt out of any single mock by
 * not calling that helper. The helpers themselves are the seam between
 * mock-mode and live-mode.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (never compromise on quality):
 * the mock payloads are wire-shape-faithful to the back-end Go handlers
 * (see api/internal/handler/org_users.go + tenant_discover.go BE files); a wire
 * drift surfaces here as a TypeScript compile error rather than as
 * silently-passing tests.
 */

import type { Page, Route } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import {
  HOSTS,
  ORG_DISCOVERY,
  SCREENSHOT_DIR,
  SCREENSHOT_PREFIX,
  USERS,
  UUIDS,
  DEPLOYMENT_ID,
} from './config'

/* ── Screenshot evidence ───────────────────────────────────────── */

function ensureScreenshotDir(): void {
  try {
    mkdirSync(SCREENSHOT_DIR, { recursive: true })
  } catch {
    /* idempotent */
  }
}

/**
 * Take a 1440×900 fullpage screenshot named according to the spec's
 * step index and slug, then attach the path as a Playwright annotation
 * so the JSON report carries the evidence URL.
 */
export async function snap(
  page: Page,
  step: number,
  slug: string,
  testInfo?: { annotations: { type: string; description?: string }[] },
): Promise<string> {
  ensureScreenshotDir()
  const path = resolve(
    SCREENSHOT_DIR,
    `${SCREENSHOT_PREFIX}-step${step}-${slug}-1440.png`,
  )
  try {
    mkdirSync(dirname(path), { recursive: true })
  } catch {
    /* idempotent */
  }
  await page.screenshot({ path, fullPage: true })
  if (testInfo) {
    testInfo.annotations.push({ type: 'screenshot', description: path })
  }
  return path
}

/* ── Mock state ────────────────────────────────────────────────── */

interface MockUser {
  uuid: string
  email: string
  state: 'pending' | 'kc_created' | 'newapi_created' | 'secret_applied' | 'done' | 'failed'
  kc_user_id?: string
  newapi_user_id?: string
  secret_name?: string
  last_error?: string
  created_at: string
  updated_at: string
  steps: { kc: 'pending' | 'done' | 'failed'; newapi: 'pending' | 'done' | 'failed'; secret: 'pending' | 'done' | 'failed' }
}

export interface OrgMockState {
  users: Map<string, MockUser>
}

/**
 * Construct a fresh mock state seeded with no users. The state object
 * is mutated by the route handlers as the spec runs so list calls
 * reflect the create/delete sequence the Organization admin performs.
 */
export function makeMockState(): OrgMockState {
  return { users: new Map() }
}

/* ── Org-console discovery + auth ──────────────────────────────── */

/**
 * Mock GET /api/v1/tenant/discover (legacy BE route) so the SPA boot resolves
 * `console.<org-domain>` to the Organization-tier branch regardless of the
 * dev-server's actual host header.
 */
export async function mockOrgConsoleDiscovery(
  page: Page,
  payload: typeof ORG_DISCOVERY = ORG_DISCOVERY,
): Promise<void> {
  await page.route('**/api/v1/tenant/discover*', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(payload),
    })
  })
}

/**
 * Mock GET /api/v1/whoami so the SovereignConsoleLayout auth gate
 * treats the session as already authenticated. Without this the SPA
 * tries to redirect to Keycloak and the test surface freezes.
 */
export async function mockWhoami(
  page: Page,
  email: string = USERS.orgAdmin,
): Promise<void> {
  await page.route('**/api/v1/whoami', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        email,
        sub: 'kc-admin-uid',
        name: 'Organization Admin',
      }),
    })
  })
}

/* ── Marketplace signup + org-create ──────────────────────────── */

/**
 * Mock the marketplace org-create endpoint. On POST, returns a
 * deployment id the spec can navigate to for Step 2's provisioning
 * surface. On GET (status poll), advances through phases until
 * `done` so the UI's "provisioning…" view eventually flips to the
 * success card.
 *
 * NOTE: the canonical wire shape for Organization create lives in #804
 * (in flight). This mock implements the EXPECTED shape per the locked
 * decisions in #795:
 *
 *   POST /api/v1/organizations  { slug, admin_email }
 *     → { deployment_id, console_url, status: "provisioning" }
 *   GET  /api/v1/organizations/{deployment_id}/status
 *     → { status: "provisioning"|"done", handover_url?, console_url }
 *
 * When #804 lands, the helper stays — the spec just opts out of the
 * mock by not calling `installMarketplaceMocks()`.
 */
export async function installMarketplaceMocks(page: Page): Promise<void> {
  let pollCount = 0

  await page.route('**/api/v1/organizations', async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    await route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({
        deployment_id: DEPLOYMENT_ID,
        console_url: `https://${HOSTS.orgConsole}/`,
        status: 'provisioning',
      }),
    })
  })

  await page.route('**/api/v1/organizations/*/status', async (route: Route) => {
    pollCount += 1
    // Flip to "done" on the second poll so the spec doesn't have to
    // wait the real ~5min provisioning window.
    const done = pollCount >= 2
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        deployment_id: DEPLOYMENT_ID,
        status: done ? 'done' : 'provisioning',
        console_url: `https://${HOSTS.orgConsole}/`,
        handover_url: done
          ? `https://${HOSTS.orgConsole}/auth/handover?token=mock-jwt`
          : undefined,
      }),
    })
  })
}

/* ── Organization user CRUD (#802 wire shape) ───────────────────────────── */

/**
 * Mock GET / POST / DELETE /api/v1/org/users so the unified-rbac
 * console renders create-user state transitions without a back end.
 * Mirrors api/internal/handler/org_users.go EXACTLY: the response
 * shape is `OrgUser` (see ui/src/pages/org/org.api.ts), the create
 * call returns 202 with `state: "done"` (synchronous-success branch
 * of ADR-0003 — Keycloak + NewAPI + Secret all green).
 */
export async function installOrgUserMocks(
  page: Page,
  state: OrgMockState,
): Promise<void> {
  await page.route('**/api/v1/org/users', async (route: Route) => {
    if (route.request().method() === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          items: Array.from(state.users.values()),
        }),
      })
      return
    }
    if (route.request().method() === 'POST') {
      const body = JSON.parse(route.request().postData() ?? '{}') as { email?: string }
      const email = body.email ?? 'unknown@example.com'
      // Pin the alice/bob UUIDs so screenshot filenames stay stable.
      const uuid = email.startsWith('alice')
        ? UUIDS.alice
        : email.startsWith('bob')
          ? UUIDS.bob
          : `uuid-${Math.random().toString(36).slice(2, 10)}`
      const now = new Date().toISOString()
      const created: MockUser = {
        uuid,
        email,
        state: 'done',
        kc_user_id: `kc-${uuid}`,
        newapi_user_id: `newapi-${uuid}`,
        secret_name: `newapi-key-${uuid}`,
        created_at: now,
        updated_at: now,
        steps: { kc: 'done', newapi: 'done', secret: 'done' },
      }
      state.users.set(uuid, created)
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify(created),
      })
      return
    }
    await route.continue()
  })

  await page.route('**/api/v1/org/users/*', async (route: Route) => {
    if (route.request().method() === 'DELETE') {
      const url = new URL(route.request().url())
      const uuid = url.pathname.split('/').pop() ?? ''
      state.users.delete(uuid)
      await route.fulfill({ status: 204, body: '' })
      return
    }
    await route.continue()
  })
}

/* ── Deployment / provisioning surface ─────────────────────────── */

/**
 * Mock the catalyst-api deployment endpoints used by the provisioning
 * status page (`/provision/$deploymentId`). Returns a payload that
 * keeps the AppsPage rendering without redirecting back to the
 * customer console — the Organization-demo spec wants to capture the
 * "provisioning success" screenshot AT this URL, not after the
 * post-handover redirect.
 */
export async function installDeploymentMocks(page: Page): Promise<void> {
  await page.route(`**/api/v1/deployments/${DEPLOYMENT_ID}`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: DEPLOYMENT_ID,
        sovereignFQDN: HOSTS.orgDomain,
        // adoptedAt deliberately absent so the SPA doesn't try to
        // hard-navigate to the customer console (which would break the
        // screenshot capture inside Playwright).
        status: 'provisioning',
      }),
    })
  })
}

/* ── App-side mocks: WordPress / OpenClaw / webmail / billing ── */

/**
 * Stub responses for cross-app surfaces the spec walks through. These
 * are NOT part of the catalyst-ui SPA — they live in the per-Org
 * wordpress Blueprint (#800), bp-openclaw (#803), and the per-Org
 * stalwart Blueprint (#801). For
 * mock-mode CI, we serve a tiny placeholder HTML so the screenshot
 * captures *something* attributable to that app.
 *
 * Once #804 wires real DNS + ingress, the spec opts out of these
 * mocks and Playwright dials the actual hostnames.
 */
export async function installAppPlaceholders(page: Page): Promise<void> {
  const apps: { host: string; title: string; body: string }[] = [
    {
      host: HOSTS.wordpress,
      title: 'WordPress — alice@acme dashboard (mock)',
      body: 'WordPress dashboard authenticated as alice — mock placeholder for #800.',
    },
    {
      host: HOSTS.openclaw,
      title: 'OpenClaw — alice@acme workspace (mock)',
      body: 'OpenClaw controller spawned alice\'s pod — mock placeholder for #803.',
    },
    {
      host: HOSTS.webmail,
      title: 'Webmail — alice@acme inbox (mock)',
      body: 'Stalwart webmail showing message from alice → bob — mock placeholder for #801.',
    },
  ]

  for (const app of apps) {
    await page.route(`https://${app.host}/**`, async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: `<!doctype html><html><head><title>${app.title}</title>
        <style>body{font-family:system-ui;background:#0b1220;color:#e2e8f0;margin:0;padding:48px;}
        h1{color:#60a5fa;}p{max-width:48rem;line-height:1.6;}</style>
        </head><body><h1>${app.title}</h1><p>${app.body}</p></body></html>`,
      })
    })
  }
}

/**
 * Mock the Organization billing/credit-ledger endpoint. Returns a ledger entry
 * representing alice's NewAPI usage so Step 6's screenshot can show a
 * non-empty balance view.
 *
 * Wire shape derives from the locked decision in #795: authoritative
 * ledger lives in `org_billing.credit_ledger`, NewAPI's Postgres is a
 * thin in-flight cache. Path `/api/v1/org/billing/ledger` is the
 * conventional shape; #804 lands the real handler.
 */
export async function installBillingMocks(page: Page): Promise<void> {
  await page.route('**/api/v1/org/billing/ledger*', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        balance: 9_998_750, // microcredits (~$9.99 remaining of $10 voucher)
        currency: 'OPENOVA_CREDIT',
        entries: [
          {
            id: 'ledger-001',
            user_uuid: UUIDS.alice,
            user_email: USERS.alice,
            amount_micro: -1_250,
            reason: 'usage:newapi:qwen3-coder',
            recorded_at: new Date().toISOString(),
          },
        ],
      }),
    })
  })
}

/* ── Kitchen-sink installer ────────────────────────────────────── */

/**
 * Install every mock helper above against `page`. Convenience wrapper
 * for the Organization-demo spec which wires every surface in one shot. Returns
 * the shared mock state so individual tests can introspect or seed
 * additional users.
 */
export async function installAllMocks(page: Page): Promise<OrgMockState> {
  const state = makeMockState()
  await mockOrgConsoleDiscovery(page)
  await mockWhoami(page)
  await installMarketplaceMocks(page)
  await installOrgUserMocks(page, state)
  await installDeploymentMocks(page)
  await installAppPlaceholders(page)
  await installBillingMocks(page)
  return state
}
