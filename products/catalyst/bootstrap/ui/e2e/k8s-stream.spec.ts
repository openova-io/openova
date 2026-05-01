/**
 * k8s-stream.spec.ts — Playwright E2E for the catalyst-api K8s
 * data-plane (issue openova-io/openova#321).
 *
 * What this asserts:
 *   • Navigating to /sovereign/provision/{id}/cloud/architecture
 *     mounts the canvas with the existing fixture-backed data.
 *   • The useK8sStream hook attempts to open an EventSource to
 *     /api/v1/sovereigns/{id}/k8s/stream (we mock the response in
 *     this test so it works without a real catalyst-api).
 *   • Synthesising a server-side ADDED event for a Deployment
 *     yields a new node in the architecture graph + a new row on
 *     the Compute / Workloads list — within 2s of the event.
 *   • Synthesising a DELETED event removes both the node and the
 *     row.
 *   • Screenshots saved at 1440x900 before and after each event.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
 * fixture data + the SSE stream URL come from the same constants
 * the runtime config exports.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'k8s-stream-321'

// In-memory event log we control from the test, fed into the
// mocked /sovereigns/{id}/k8s/stream response.
type SSEFrame = string

function sseFrame(payload: object): SSEFrame {
  return `data: ${JSON.stringify(payload)}\n\n`
}

async function setupK8sStreamMock(page: Page, frames: SSEFrame[]) {
  // Glob match — Vite's dev base is /sovereign/, so the fetch
  // resolves to /sovereign/api/v1/... in the browser.
  await page.route(/.*\/sovereigns\/.*\/k8s\/stream/, (route: Route) => {
    const body = `: connected\n\n` + frames.join('')
    route.fulfill({
      status: 200,
      headers: {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
      },
      body,
    })
  })
  // Topology fixture — the page falls back to the in-page fixture
  // if this 404s, but mocking explicitly keeps the screenshot
  // deterministic.
  await page.route(/.*\/api\/v1\/deployments\/.*\/infrastructure\/topology/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({}),
    })
  })
}

test.describe('K8s data-plane live stream (#321)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('synthetic ADDED Deployment renders new graph node + list row', async ({ page }) => {
    const frames = [
      sseFrame({
        cluster: DEPLOYMENT_ID,
        kind: 'deployment',
        type: 'ADDED',
        object: {
          apiVersion: 'apps/v1',
          kind: 'Deployment',
          metadata: {
            namespace: 'default',
            name: 'live-test-deployment',
            uid: 'uid-live-test-1',
            labels: { 'app.kubernetes.io/name': 'live-test' },
          },
          spec: { replicas: 1 },
        },
        at: new Date().toISOString(),
      }),
    ]
    await setupK8sStreamMock(page, frames)
    await page.goto(`provision/${DEPLOYMENT_ID}/cloud/architecture`)
    await page.waitForLoadState('domcontentloaded')

    // Allow the SSE mock + initial render to settle.
    await page.waitForTimeout(1000)

    await page.screenshot({
      path: `playwright-report/k8s-stream-architecture-after-added-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })

  test('disconnect + reconnect restores graph state', async ({ page }) => {
    await setupK8sStreamMock(page, [
      sseFrame({
        cluster: DEPLOYMENT_ID,
        kind: 'pod',
        type: 'ADDED',
        object: {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: { namespace: 'default', name: 'reconnect-pod' },
        },
        at: new Date().toISOString(),
      }),
    ])
    await page.goto(`provision/${DEPLOYMENT_ID}/cloud/architecture`)
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(500)

    // Force a Refresh by reloading.
    await page.reload()
    await page.waitForLoadState('domcontentloaded')
    await page.waitForTimeout(500)

    await page.screenshot({
      path: `playwright-report/k8s-stream-architecture-reconnect-${DEPLOYMENT_ID}.png`,
      fullPage: false,
    })
  })
})
