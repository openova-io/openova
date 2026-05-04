/**
 * sovereignty.spec.ts — Playwright DoD coverage for the admin-console
 * sovereignty cutover surface (openova-io/openova#790 / #793).
 *
 * What we assert (issue #793 DoD checklist):
 *   ☑ Card renders with `tethered` state on a fresh handover
 *   ☑ Button click → modal → confirm → progress card visible
 *   ☑ SSE events drive 8 steps to completion
 *   ☑ Final `sovereign` state renders correctly
 *   ☑ 1440×900 screenshots of every state for evidence
 *
 * The test mocks the catalyst-api `/sovereign/cutover` endpoints
 * via Playwright's `page.route` so the spec doesn't depend on a
 * running api server. The preview page at /sovereignty/preview mounts
 * the same `SovereigntyCard` component the production /console/
 * dashboard mounts, just without the OIDC auth shell — keeping the
 * test surface 1:1 with the prod component while bypassing the
 * sovereign-hostname detection that localhost cannot satisfy.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall): tests assert the canonical contract from #793,
 *      every visible affordance is exercised in this first cut.
 *   #2 (never compromise): no test.skip; failures fail the suite.
 *   #4 (never hardcode): the mock URL paths derive from the canonical
 *      `/api/v1/sovereign/cutover/*` shape that matches API_BASE.
 *
 * Tagged @sovereignty-cutover for the CI workflow.
 */

import { test, expect, type Page, type Route } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'

/* ── Screenshot evidence ───────────────────────────────────────── */

const SCREENSHOT_DIR = resolve(
  process.cwd(),
  process.env.SOVEREIGNTY_SCREENSHOT_DIR ?? 'test-results/sovereignty',
)

function ensureScreenshotDir(): void {
  try {
    mkdirSync(SCREENSHOT_DIR, { recursive: true })
  } catch {
    /* idempotent */
  }
}

async function snapshot(page: Page, name: string): Promise<string> {
  ensureScreenshotDir()
  const path = resolve(SCREENSHOT_DIR, `${name}.png`)
  // Ensure the parent dir exists even if name contains a path separator.
  try {
    mkdirSync(dirname(path), { recursive: true })
  } catch {
    /* idempotent */
  }
  await page.screenshot({ path, fullPage: true })
  return path
}

/* ── Mock orchestration ────────────────────────────────────────── */

interface MockState {
  /** What GET /status returns. Updated as the test progresses. */
  status: Record<string, unknown>
  /** Resolves the in-flight server-sent EventSource frames. */
  pushEvent: ((eventType: string, data: unknown) => void) | null
}

/**
 * Install the cutover-API mock. Captures a `pushEvent` handle for the
 * test to drive SSE frames. Routes:
 *
 *   GET  /api/v1/sovereign/cutover/status  → state.status
 *   POST /api/v1/sovereign/cutover/start   → state.status (mutated to in-flight)
 *   GET  /api/v1/sovereign/cutover/events  → SSE stream the test writes into
 */
async function installMocks(page: Page, initialStatus: Record<string, unknown>): Promise<MockState> {
  const state: MockState = {
    status: initialStatus,
    pushEvent: null,
  }

  // GET /status
  await page.route('**/api/v1/sovereign/cutover/status', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(state.status),
    })
  })

  // POST /start — mutate to mid-flight on the first click.
  await page.route('**/api/v1/sovereign/cutover/start', async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    state.status = {
      state: 'tethered',
      cutoverComplete: false,
      startedAt: '2026-05-04T10:00:00Z',
      steps: [
        {
          step: 'gitea-mirror',
          status: 'running',
          startedAt: '2026-05-04T10:00:00Z',
        },
      ],
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(state.status),
    })
  })

  // GET /events — SSE stream. Playwright doesn't natively expose
  // streaming response bodies via `route.fulfill`, so we serve a
  // single static frame and rely on the page-side EventSource
  // override below for fine-grained event sequencing.
  await page.route('**/api/v1/sovereign/cutover/events', async (route: Route) => {
    // Minimal SSE response; the real frame-driving happens in-page
    // via the addInitScript-installed EventSource shim.
    await route.fulfill({
      status: 200,
      headers: {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
      },
      body: ': stream-open\n\n',
    })
  })

  // Override the page's EventSource constructor at script-init time so
  // the test can inject `cutover-step` / `cutover-status` frames
  // synchronously via window.__pushSSE(eventType, data).
  await page.addInitScript(() => {
    type Listener = (ev: MessageEvent) => void
    interface CutoverWindow extends Window {
      __cutoverES?: { listeners: Map<string, Listener[]>; opened: boolean }
      __pushSSE?: (eventType: string, data: unknown) => void
    }

    class FakeEventSource {
      url: string
      readyState = 0
      onopen: ((this: EventSource, ev: Event) => unknown) | null = null
      onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null
      onerror: ((this: EventSource, ev: Event) => unknown) | null = null
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSED = 2
      private listeners = new Map<string, Listener[]>()
      constructor(url: string) {
        this.url = url
        if (url.includes('/api/v1/sovereign/cutover/events')) {
          // Track the singleton so the test driver can drive frames.
          ;(window as unknown as CutoverWindow).__cutoverES = {
            listeners: this.listeners,
            opened: false,
          }
        }
        // Fire onopen on the next tick so the hook can wire its listeners.
        setTimeout(() => {
          this.readyState = 1
          if (this.onopen) this.onopen.call(this as unknown as EventSource, new Event('open'))
          const w = window as unknown as CutoverWindow
          if (w.__cutoverES) w.__cutoverES.opened = true
        }, 10)
      }
      addEventListener(type: string, listener: Listener): void {
        const existing = this.listeners.get(type) ?? []
        existing.push(listener)
        this.listeners.set(type, existing)
      }
      removeEventListener(type: string, listener: Listener): void {
        const existing = this.listeners.get(type) ?? []
        this.listeners.set(
          type,
          existing.filter((l) => l !== listener),
        )
      }
      close(): void {
        this.readyState = 2
      }
    }

    ;(window as unknown as { EventSource: typeof EventSource }).EventSource =
      FakeEventSource as unknown as typeof EventSource

    // Driver helper — the spec calls this from page.evaluate to feed
    // events to the hook as if from the catalyst-api stream.
    ;(window as unknown as CutoverWindow).__pushSSE = (
      eventType: string,
      data: unknown,
    ) => {
      const w = window as unknown as CutoverWindow
      const reg = w.__cutoverES
      if (!reg) return
      const listeners = reg.listeners.get(eventType) ?? []
      const ev = new MessageEvent(eventType, { data: JSON.stringify(data) })
      for (const l of listeners) l(ev)
    }
  })

  return state
}

async function pushEvent(page: Page, eventType: string, data: unknown): Promise<void> {
  await page.evaluate(
    ({ eventType, data }) => {
      const fn = (
        window as unknown as {
          __pushSSE?: (eventType: string, data: unknown) => void
        }
      ).__pushSSE
      if (fn) fn(eventType, data)
    },
    { eventType, data },
  )
}

/* ── Tests ─────────────────────────────────────────────────────── */

test.describe('@sovereignty-cutover SovereigntyCard — admin console DoD', () => {
  test('renders Tethered state on a fresh handover (1440×900)', async ({ page }) => {
    await installMocks(page, {
      state: 'tethered',
      cutoverComplete: false,
      steps: [],
    })
    await page.goto('sovereignty/preview')
    await expect(page.getByTestId('sovereignty-card')).toBeVisible()
    await expect(page.getByTestId('sovereignty-badge')).toContainText(/tethered/i)
    await expect(page.getByTestId('cutover-start-button')).toBeVisible()
    // Card carries the wire state attribute for downstream checks.
    await expect(page.getByTestId('sovereignty-card')).toHaveAttribute(
      'data-cutover-state',
      'tethered',
    )
    const path = await snapshot(page, '01-tethered-fresh-handover')
    test.info().annotations.push({ type: 'screenshot', description: path })
  })

  test('button click → modal → confirm → progress card mounts', async ({ page }) => {
    await installMocks(page, {
      state: 'tethered',
      cutoverComplete: false,
      steps: [],
    })
    await page.goto('sovereignty/preview')

    // 1. Click the CTA — modal opens.
    await page.getByTestId('cutover-start-button').click()
    await expect(page.getByTestId('cutover-confirm-modal')).toBeVisible()
    const modalShot = await snapshot(page, '02-confirm-modal-open')
    test.info().annotations.push({
      type: 'screenshot',
      description: modalShot,
    })

    // 2. Confirm — POST /start fires, status flips to in-flight, progress
    //    card renders.
    await page.getByTestId('cutover-confirm-button').click()
    await expect(page.getByTestId('cutover-progress-card')).toBeVisible({
      timeout: 5_000,
    })
    // All 8 step rows must always be present.
    const stepIds = [
      'gitea-mirror',
      'harbor-projects',
      'harbor-prewarm',
      'registry-pivot',
      'flux-gitrepository-patch',
      'helmrepo-patches',
      'catalyst-api-env-patch',
      'egress-block-test',
    ]
    for (const id of stepIds) {
      await expect(page.getByTestId(`cutover-step-${id}`)).toBeVisible()
    }
    const progressShot = await snapshot(page, '03-progress-card-mounted')
    test.info().annotations.push({
      type: 'screenshot',
      description: progressShot,
    })
  })

  test('SSE events drive 8 steps from running → done sequentially', async ({ page }) => {
    await installMocks(page, {
      state: 'tethered',
      cutoverComplete: false,
      steps: [],
    })
    await page.goto('sovereignty/preview')

    // Open the modal + confirm to mount the progress card.
    await page.getByTestId('cutover-start-button').click()
    await page.getByTestId('cutover-confirm-button').click()
    await expect(page.getByTestId('cutover-progress-card')).toBeVisible({
      timeout: 5_000,
    })

    const steps = [
      'gitea-mirror',
      'harbor-projects',
      'harbor-prewarm',
      'registry-pivot',
      'flux-gitrepository-patch',
      'helmrepo-patches',
      'catalyst-api-env-patch',
      'egress-block-test',
    ] as const

    // Drive each step running → done in order.
    for (let i = 0; i < steps.length; i += 1) {
      const step = steps[i]
      await pushEvent(page, 'cutover-step', {
        step,
        status: 'running',
        startedAt: '2026-05-04T10:00:00Z',
      })
      await expect(page.getByTestId(`cutover-step-${step}`)).toHaveAttribute(
        'data-step-status',
        'running',
        { timeout: 2_000 },
      )

      await pushEvent(page, 'cutover-step', {
        step,
        status: 'done',
        startedAt: '2026-05-04T10:00:00Z',
        finishedAt: `2026-05-04T10:0${(i + 1) % 10}:00Z`,
      })
      await expect(page.getByTestId(`cutover-step-${step}`)).toHaveAttribute(
        'data-step-status',
        'done',
        { timeout: 2_000 },
      )
    }

    // Mid-progress screenshot — all 8 done, awaiting terminal frame.
    const midShot = await snapshot(page, '04-all-steps-done-pre-terminal')
    test.info().annotations.push({
      type: 'screenshot',
      description: midShot,
    })

    // Percentage badge should read 100%.
    await expect(page.getByTestId('cutover-progress-pct')).toContainText('100%')
  })

  test('terminal sovereign state renders summary stats (1440×900)', async ({ page }) => {
    await installMocks(page, {
      state: 'tethered',
      cutoverComplete: false,
      steps: [],
    })
    await page.goto('sovereignty/preview')

    await page.getByTestId('cutover-start-button').click()
    await page.getByTestId('cutover-confirm-button').click()
    await expect(page.getByTestId('cutover-progress-card')).toBeVisible({
      timeout: 5_000,
    })

    // Drive every step done.
    const steps = [
      'gitea-mirror',
      'harbor-projects',
      'harbor-prewarm',
      'registry-pivot',
      'flux-gitrepository-patch',
      'helmrepo-patches',
      'catalyst-api-env-patch',
      'egress-block-test',
    ] as const
    for (const step of steps) {
      await pushEvent(page, 'cutover-step', {
        step,
        status: 'done',
        finishedAt: '2026-05-04T10:01:00Z',
      })
    }

    // Terminal frame.
    await pushEvent(page, 'cutover-status', {
      state: 'sovereign',
      cutoverComplete: true,
      steps: steps.map((s) => ({
        step: s,
        status: 'done',
        finishedAt: '2026-05-04T10:01:00Z',
      })),
      mirroredCommitSHA: 'abcdef0123456789',
      harborProjectCount: 7,
      egressTestPassed: true,
      finishedAt: '2026-05-04T10:11:00Z',
    })

    // The card should flip to the green Sovereign badge.
    await expect(page.getByTestId('sovereignty-card')).toHaveAttribute(
      'data-cutover-state',
      'sovereign',
      { timeout: 5_000 },
    )
    await expect(page.getByTestId('sovereignty-badge')).toContainText(/sovereign/i)
    // Summary stats render at the card level.
    await expect(page.getByTestId('sovereignty-stats')).toBeVisible()
    await expect(page.getByTestId('sovereignty-stat-sha')).toContainText(
      /abcdef012345/,
    )
    await expect(page.getByTestId('sovereignty-stat-harbor')).toContainText(/7/)
    await expect(page.getByTestId('sovereignty-stat-egress')).toContainText(
      /passed/,
    )
    // The achieved-summary inside the progress card also renders.
    await expect(page.getByTestId('cutover-achieved-summary')).toBeVisible()
    // CTA gone.
    await expect(page.getByTestId('cutover-start-button')).toHaveCount(0)

    const finalShot = await snapshot(page, '05-sovereign-terminal')
    test.info().annotations.push({
      type: 'screenshot',
      description: finalShot,
    })
  })

  test('renders failed-step error inline (1440×900)', async ({ page }) => {
    await installMocks(page, {
      state: 'tethered',
      cutoverComplete: false,
      steps: [],
    })
    await page.goto('sovereignty/preview')

    await page.getByTestId('cutover-start-button').click()
    await page.getByTestId('cutover-confirm-button').click()
    await expect(page.getByTestId('cutover-progress-card')).toBeVisible({
      timeout: 5_000,
    })

    // Step 1 done, step 2 fails.
    await pushEvent(page, 'cutover-step', {
      step: 'gitea-mirror',
      status: 'done',
      finishedAt: '2026-05-04T10:01:00Z',
    })
    await pushEvent(page, 'cutover-step', {
      step: 'harbor-projects',
      status: 'failed',
      finishedAt: '2026-05-04T10:02:00Z',
      message: 'harbor admin password rejected — rotate VAULT secret',
    })

    await expect(
      page.getByTestId('cutover-step-harbor-projects-error'),
    ).toContainText(/password rejected/, { timeout: 5_000 })
    await expect(page.getByTestId('cutover-step-harbor-projects')).toHaveAttribute(
      'data-step-status',
      'failed',
    )

    const failShot = await snapshot(page, '06-failed-step-inline')
    test.info().annotations.push({
      type: 'screenshot',
      description: failShot,
    })
  })
})
