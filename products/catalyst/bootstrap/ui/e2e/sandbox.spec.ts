/**
 * sandbox.spec.ts — Playwright e2e regression gate for the Wave 3
 * Sandbox UI scaffold (branch sandbox-wave3-ui-scaffold, PR #1621).
 *
 * Covers the four happy-path surfaces the operator hits when starting
 * an agent-coding session:
 *
 *   1. Sidebar shows a "Sandbox" entry and highlights it on /sandbox.
 *   2. /sandbox landing renders the 6-agent card grid (aider,
 *      claude-code, cursor-agent, little-coder, opencode, qwen-code)
 *      and the claude-code card exposes the "Connect Claude Max" link.
 *   3. /sandbox/settings shows the BYOS Connect / Disconnect UI.
 *   4. /sandbox/$id navigates to the session view (with createSandbox
 *      mocked so the spec doesn't need a real catalyst-api).
 *
 * Mock strategy mirrors e2e/org-demo.spec.ts — every back-end fetch
 * the page makes is stubbed via `page.route`, so the spec runs against
 * `npm run dev` (Vite on localhost:5173) without a catalyst-api up. The
 * SovereignConsoleLayout's `/api/v1/whoami` probe is mocked to a 200
 * so the auth gate falls through to the children.
 *
 * To force the SovereignSidebar (chroot Sovereign Console chrome that
 * exposes the `sov-console-nav-sandbox` test id) rather than the
 * mothership Sidebar, the dev server must be started in sovereign mode:
 *
 *   VITE_CATALYST_MODE=sovereign \
 *   VITE_SOVEREIGN_FQDN=sandbox.example.test \
 *   npm run dev
 *
 * Without those env vars `DETECTED_MODE.mode` resolves to 'catalyst-zero'
 * (mothership) and PortalShell renders the mothership Sidebar — which
 * does not surface the Sandbox nav entry. Test 1 calls this out
 * explicitly when the testid is missing so the failure points the
 * implementing operator at the right env var.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every selector
 * keys off a `data-testid` exposed by the page source — drift in the
 * testid surface fails this suite loud rather than silently passing.
 *
 * Tagged `@sandbox-wave9` so the CI workflow can grep it independently.
 */

import { expect, test, type Page, type Route } from '@playwright/test'

/* ── Canonical agent catalogue ─────────────────────────────────────
 *
 * Mirrored verbatim from src/lib/sandbox.api.ts SANDBOX_AGENTS. Test 2
 * asserts the Landing renders one card per id in this set — adding a
 * new agent is a one-line append both here and in sandbox.api.ts.
 */
const CANONICAL_AGENT_IDS = [
  'aider',
  'claude-code',
  'cursor-agent',
  'little-coder',
  'opencode',
  'qwen-code',
] as const

const FIXTURE_SANDBOX_ID = 'sandbox-fixture-test'

/* ── Mock installers ───────────────────────────────────────────────
 *
 * Each helper installs ONE narrow contract so a future live-mode run
 * can opt out by not calling that helper. The mock state object is
 * captured by closure so test 4 can introspect what the create POST
 * actually sent.
 */

interface SandboxMockState {
  /** Bodies of every POST /v1/sandbox/sessions captured by the mock. */
  createCalls: Array<{ agent: string; name?: string; repo?: string }>
}

function makeMockState(): SandboxMockState {
  return { createCalls: [] }
}

/**
 * Mock the SovereignConsoleLayout's auth probe + the
 * useResolvedDeploymentId self-discovery so the page renders past its
 * authenticating spinner without contacting catalyst-api.
 */
async function mockAuth(page: Page): Promise<void> {
  await page.route('**/api/v1/whoami', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        email: 'operator@example.test',
        sub: 'kc-operator-uid',
        verified: true,
      }),
    })
  })

  await page.route('**/api/v1/sovereign/self', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        deploymentId: 'fixture-deployment-id',
        sovereignFQDN: 'sandbox.example.test',
      }),
    })
  })

  // #6723 — the SovereignSidebar renders Agenity from the MERGED
  // /console-ui/sidebar-entries view (bp-agenity's Blueprint consoleUI ⊕
  // the sovereign-admin's Settings → Menu overrides), not from a static
  // FLAT_NAV row. Stub the merged view with the Blueprint default so test 1
  // exercises the mapped entry the way a live Sovereign renders it.
  await page.route('**/api/v1/sovereigns/*/console-ui/sidebar-entries', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        entries: [
          {
            id: 'bp-agenity',
            label: 'Agenity',
            route: '/apps/bp-agenity/dashboard',
            order: 40,
            icon: 'M3 4a1 1 0 011-1h16a1 1 0 011 1v12a1 1 0 01-1 1H4a1 1 0 01-1-1V4zm5 5l3 3-3 3m5 0h4M8 21h8',
            source: 'blueprint',
            enabled: true,
            defaultLabel: 'Agenity',
            defaultRoute: '/apps/bp-agenity/dashboard',
            defaultOrder: 40,
            defaultEnabled: true,
          },
        ],
        parents: ['dashboard', 'cloud', 'apps', 'catalog', 'jobs', 'compliance', 'users', 'organizations', 'billing'],
      }),
    })
  })

  // useDeploymentEvents fires GET + SSE against /v1/deployments/{id}/...
  // We stub them to keep the network log quiet and avoid the page
  // logging EventSource errors which would surface as console warnings.
  await page.route('**/api/v1/deployments/*/events', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ events: [], snapshot: null }),
    })
  })
  await page.route('**/api/v1/deployments/*/logs', async (route: Route) => {
    // SSE EventSource — return an empty 200 text/event-stream. The
    // page tolerates an empty stream (events is the source of truth).
    await route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: '',
    })
  })
}

/**
 * Mock the Sandbox API surface — GET sessions, POST sessions, GET BYOS
 * status, POST connect, DELETE disconnect.
 */
async function mockSandboxApi(page: Page, state: SandboxMockState): Promise<void> {
  await page.route('**/api/v1/sandbox/sessions', async (route: Route) => {
    const method = route.request().method()
    if (method === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ sandboxes: [] }),
      })
      return
    }
    if (method === 'POST') {
      const body = JSON.parse(route.request().postData() ?? '{}') as {
        agent?: string
        name?: string
        repo?: string
      }
      state.createCalls.push({
        agent: body.agent ?? '',
        name: body.name,
        repo: body.repo,
      })
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          id: FIXTURE_SANDBOX_ID,
          name: body.name || FIXTURE_SANDBOX_ID,
          agent: body.agent ?? 'claude-code',
          status: 'pending',
          createdAt: new Date().toISOString(),
          repo: body.repo ?? '',
        }),
      })
      return
    }
    await route.continue()
  })

  await page.route('**/api/v1/sandbox/byos/claude-code/status', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'disconnected',
        accountLabel: '',
        connectedAt: '',
      }),
    })
  })

  await page.route('**/api/v1/sandbox/byos/claude-code/connect', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        authorizeUrl: 'https://auth.anthropic.com/authorize?fixture=1',
      }),
    })
  })

  await page.route('**/api/v1/sandbox/byos/claude-code/disconnect', async (route: Route) => {
    await route.fulfill({ status: 204, body: '' })
  })
}

async function installAllMocks(page: Page): Promise<SandboxMockState> {
  const state = makeMockState()
  // Pre-seed the rootBeforeLoad auth-fast-path marker (#1090 cluster A2)
  // so the global route guard does NOT also fire its own whoami probe
  // on every page.goto — keeps the network log smaller and stabilises
  // the cold-cache navigation timing.
  await page.addInitScript(() => {
    try {
      window.sessionStorage.setItem('catalyst:authed', '1')
    } catch {
      /* private browsing */
    }
  })
  await mockAuth(page)
  await mockSandboxApi(page, state)
  return state
}

/* ── Tests ─────────────────────────────────────────────────────────
 *
 * One describe block, four happy-path tests. Workers=1 in
 * playwright.config.ts so the mock state never crosses tests; each
 * `beforeEach` installs a fresh state object captured by closure into
 * `mockState` for assertions inside the tests.
 *
 * Generous per-assertion timeouts (15-25 s) absorb Vite dev's cold-
 * cache module load on first hit — xterm.js + react-query + tanstack
 * router are all lazy-imported and the first navigation has to fetch
 * them across the wire. Warm runs (CI second pass / local re-run) all
 * finish well under 5 s.
 */

test.describe('@sandbox-wave9 Sandbox UI happy path (PR #1621)', () => {
  // Vite dev cold-cache navigation pulls SandboxLanding +
  // SandboxSettings + SandboxSession + xterm + tanstack-router across
  // the wire on the first hit. Bump per-test timeout to 90 s so the
  // first cold-cache test in the run doesn't fail teardown; warm
  // re-runs finish well under 30 s.
  test.describe.configure({ timeout: 90_000 })

  let mockState: SandboxMockState

  test.beforeEach(async ({ page }) => {
    mockState = await installAllMocks(page)
  })

  /* ── 1. Sidebar Agenity entry (mapped, #6723) on /sandbox ───── */

  test('sidebar exposes the mapped Agenity nav entry on /sandbox', async ({
    page,
  }) => {
    await page.goto('/sandbox')

    // #6723: Agenity is a Blueprint-sourced MAPPED entry (bp-agenity
    // consoleUI, stubbed by mockConsoleShell above), rendered with the
    // Wave 5.69c `sov-console-nav-bp-<id>` test id — the former static
    // `sov-console-nav-sandbox` row no longer exists.
    const navLink = page.locator('[data-testid="sov-console-nav-bp-bp-agenity"]')
    await expect(
      navLink,
      'SovereignSidebar is missing [data-testid=sov-console-nav-bp-bp-agenity]. Either the merged /console-ui/sidebar-entries stub drifted (see mockConsoleShell in this spec + DynamicNavLink in SovereignSidebar.tsx) or the dev server is not in sovereign mode (set VITE_CATALYST_MODE=sovereign VITE_SOVEREIGN_FQDN=sandbox.example.test before `npm run dev`).',
    ).toBeVisible({ timeout: 25_000 })

    // Label mirrors bp-agenity's consoleUI.sidebarLabel verbatim —
    // case-sensitive so a drift fails this guard.
    await expect(navLink).toContainText('Agenity')
    await expect(navLink).toHaveAttribute('data-nav-source', 'blueprint')
    await expect(navLink).toHaveAttribute('href', /\/apps\/bp-agenity\/dashboard$/)

    // The static row is gone for good.
    await expect(page.locator('[data-testid="sov-console-nav-sandbox"]')).toHaveCount(0)
  })

  /* ── 2. /sandbox renders 6 agent cards + Connect Claude Max ── */

  test('landing renders 6 agent cards with the Connect Claude Max CTA on claude-code', async ({
    page,
  }) => {
    await page.goto('/sandbox')

    // Wait for landing page mount — the data-testid is on the page
    // root in SandboxLanding.tsx.
    const landing = page.locator('[data-testid="sandbox-landing-page"]')
    await expect(landing).toBeVisible({ timeout: 25_000 })

    // Agent grid — must render exactly one card per canonical agent id.
    const cards = page.locator('[data-testid^="sandbox-agent-card-"]')
    await expect(
      cards,
      `Sandbox landing renders ${CANONICAL_AGENT_IDS.length} agent cards (one per SANDBOX_AGENTS row in src/lib/sandbox.api.ts).`,
    ).toHaveCount(CANONICAL_AGENT_IDS.length)

    for (const agentId of CANONICAL_AGENT_IDS) {
      const card = page.locator(`[data-testid="sandbox-agent-card-${agentId}"]`)
      await expect(
        card,
        `Sandbox landing is missing the ${agentId} card — add it to SANDBOX_AGENTS in src/lib/sandbox.api.ts.`,
      ).toBeVisible()
    }

    // Only the claude-code card carries the "Connect Claude Max" link
    // (see AgentCard's `isClaude` branch in SandboxLanding.tsx).
    const connectClaude = page.locator(
      '[data-testid="sandbox-agent-claude-connect"]',
    )
    await expect(
      connectClaude,
      'claude-code card is missing the Connect Claude Max link — see the `isClaude` branch in AgentCard in SandboxLanding.tsx.',
    ).toBeVisible()
    await expect(connectClaude).toContainText(/Connect Claude Max/i)
  })

  /* ── 3. /sandbox/settings BYOS Connect/Disconnect UI ────────── */

  test('settings renders BYOS Claude Code card with Connect button when disconnected', async ({
    page,
  }) => {
    await page.goto('/sandbox/settings')

    const settingsPage = page.locator('[data-testid="sandbox-settings-page"]')
    await expect(settingsPage).toBeVisible({ timeout: 25_000 })

    const byosCard = page.locator(
      '[data-testid="sandbox-byos-claude-code-card"]',
    )
    await expect(
      byosCard,
      'Sandbox settings is missing the [data-testid=sandbox-byos-claude-code-card] section — see ClaudeCodeByosCard in SandboxSettings.tsx.',
    ).toBeVisible()

    // Status pill — mock returns `disconnected` so the Connect button
    // is the active branch (Disconnect is hidden by `isConnected` gate).
    const status = page.locator('[data-testid="sandbox-byos-claude-code-status"]')
    await expect(status).toContainText(/Not connected/i)

    const connect = page.locator(
      '[data-testid="sandbox-byos-claude-code-connect"]',
    )
    await expect(
      connect,
      'BYOS card is missing the Connect button when status=disconnected — see the isConnected branch in ClaudeCodeByosCard.',
    ).toBeVisible()
    await expect(connect).toContainText(/Connect Claude Max/i)

    // Disconnect button must NOT render in the disconnected branch.
    const disconnect = page.locator(
      '[data-testid="sandbox-byos-claude-code-disconnect"]',
    )
    await expect(disconnect).toHaveCount(0)
  })

  /* ── 4. /sandbox/$id route + create POST contract ───────────── */

  test('start-session modal POSTs createSandbox and /sandbox/$id route resolves', async ({
    page,
  }) => {
    // The Landing modal flow exercises createSandbox end-to-end. We
    // validate the FE↔BE contract by capturing the POST request — proves
    // the StartSandboxModal submit handler invokes the createSandbox
    // mutation defined in src/lib/sandbox.api.ts.

    await page.goto('/sandbox')
    await expect(
      page.locator('[data-testid="sandbox-landing-page"]'),
    ).toBeVisible({ timeout: 25_000 })

    // Open the create modal for the aider agent.
    await page.locator('[data-testid="sandbox-agent-start-aider"]').click()
    const modal = page.locator('[data-testid="sandbox-start-modal"]')
    await expect(modal).toBeVisible()

    // Submit with empty fields — the server-side default fills in the
    // canonical sandbox-<slug> name (mock returns FIXTURE_SANDBOX_ID).
    // Use Promise.all so we capture the request even if the mutation's
    // onSuccess fires + invalidates the query before the assertion lands.
    const [createReq] = await Promise.all([
      page.waitForRequest(
        (req) =>
          req.url().includes('/api/v1/sandbox/sessions') &&
          req.method() === 'POST',
        { timeout: 15_000 },
      ),
      page.locator('[data-testid="sandbox-start-submit"]').click(),
    ])

    // Verify the createSandbox POST shape — proves the FE↔BE contract
    // in src/lib/sandbox.api.ts createSandbox().
    const postBody = JSON.parse(createReq.postData() ?? '{}') as {
      agent?: string
    }
    expect(
      postBody.agent,
      'createSandbox POST is missing agent=aider — verify the StartSandboxModal submit handler maps the agent prop to the create payload.',
    ).toBe('aider')

    // Belt-and-braces — the mock state must reflect exactly one capture
    // so a future refactor that double-submits surfaces here.
    expect(mockState.createCalls).toHaveLength(1)
    expect(mockState.createCalls[0]?.agent).toBe('aider')

    // Direct navigation to /sandbox/$id — proves the route is
    // registered against consoleLayoutRoute (PR #1621 wiring). We
    // assert the back-to-landing link is present (SandboxSession.tsx
    // headerSlotLeft); xterm.js terminal mount is a separate test seam.
    await page.goto(`/sandbox/${FIXTURE_SANDBOX_ID}`)
    const backLink = page.locator('[data-testid="sov-sandbox-back-to-landing"]')
    await expect(
      backLink,
      'SandboxSession is missing the back-to-landing link — see SandboxSession.tsx headerSlotLeft.',
    ).toBeVisible({ timeout: 25_000 })
  })
})
