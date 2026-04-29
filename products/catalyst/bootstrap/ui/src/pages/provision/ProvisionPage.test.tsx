/**
 * ProvisionPage.test.tsx — vitest coverage for the buffered-events history
 * replay introduced by issue #180.
 *
 * The user reported: "this is empty are you sure this is progressing?" —
 * navigating to `/sovereign/provision/<completed-id>` rendered an empty
 * `0 events · done` shell because all live SSE events had been lost when
 * the channel closed. The catalyst-api now buffers events on a durable
 * slice, exposes GET /api/v1/deployments/{id}/events, and replays on SSE
 * connect — and ProvisionPage on mount fetches that slice FIRST so the
 * bubbles + log buckets populate even for a deployment that already
 * finished.
 *
 * This test renders ProvisionPage with a fetch mock that returns a
 * realistic completed-deployment payload, asserts the page (a) calls the
 * /events endpoint, (b) renders bubble states from the replayed events
 * (Hetzner-infra flips to done because tofu-output landed), and (c)
 * renders the historical events in the live-log panel for the auto-
 * selected Hetzner-infra bubble.
 *
 * `disableStream={true}` prevents the EventSource side-effect from firing
 * — jsdom doesn't have an EventSource and we don't need it: the GET
 * /events fetch alone covers the user-reported regression's surface.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { ProvisionPage } from './ProvisionPage'

// Stable deployment id reused in URL + fetch URL assertion.
const DEPLOYMENT_ID = 'abc123'

function renderProvision(disableStream = false) {
  // Root route renders an Outlet so child routes mount; mirrors the
  // production routing tree (router.tsx). Using a route component that
  // returned `<div />` as we did first hid the Outlet, so ProvisionPage
  // never rendered into the DOM.
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <ProvisionPage disableStream={disableStream} />,
  })
  const routeTree = rootRoute.addChildren([provisionRoute])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [`/provision/${DEPLOYMENT_ID}`] }),
  })
  return render(<RouterProvider router={router} />)
}

// jsdom doesn't ship ResizeObserver — ProvisionPage uses it to resize the
// SVG layout on mount. The page only reads contentRect, so a no-op
// observer is enough to let the component mount cleanly under vitest.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = NoopResizeObserver

beforeEach(() => {
  vi.restoreAllMocks()
})

afterEach(() => {
  cleanup()
})

describe('ProvisionPage — history replay (#180)', () => {
  it('fetches /api/v1/deployments/<id>/events on mount', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'provisioning' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderProvision(true)

    await waitFor(() => {
      expect(fetchSpy).toHaveBeenCalled()
    })
    const calledURL = String(fetchSpy.mock.calls[0]?.[0] ?? '')
    expect(calledURL).toContain(`/api/v1/deployments/${DEPLOYMENT_ID}/events`)
  })

  it('renders bubble states + log entries from a completed deployment', async () => {
    // Realistic happy-path event sequence the catalyst-api emits during a
    // successful Hetzner provisioning run. tofu-output flips Hetzner-infra
    // to done; flux-bootstrap flips that supernode to running.
    const events = [
      { time: '2026-04-29T15:00:00Z', phase: 'tofu-init', level: 'info', message: 'Initialising OpenTofu working directory' },
      { time: '2026-04-29T15:00:05Z', phase: 'tofu-plan', level: 'info', message: 'Planning Hetzner resources (network, firewall, server, LB, DNS)' },
      { time: '2026-04-29T15:00:30Z', phase: 'tofu-apply', level: 'info', message: 'hcloud_network.sovereign: Creation complete after 2s' },
      { time: '2026-04-29T15:00:32Z', phase: 'tofu-apply', level: 'info', message: 'hcloud_firewall.sovereign: Creation complete after 1s' },
      { time: '2026-04-29T15:01:10Z', phase: 'tofu-apply', level: 'info', message: 'hcloud_server.cp[0]: Creation complete after 18s' },
      { time: '2026-04-29T15:01:15Z', phase: 'tofu-apply', level: 'info', message: 'hcloud_load_balancer.api: Creation complete after 4s' },
      { time: '2026-04-29T15:01:20Z', phase: 'tofu-output', level: 'info', message: 'Reading OpenTofu outputs' },
      { time: '2026-04-29T15:01:21Z', phase: 'flux-bootstrap', level: 'info', message: 'Cloud-init has bootstrapped Flux + Crossplane in the new cluster' },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          events,
          state: {
            id: DEPLOYMENT_ID,
            status: 'ready',
            startedAt: '2026-04-29T15:00:00Z',
            finishedAt: '2026-04-29T15:01:21Z',
            sovereignFQDN: 'omantel.omani.works',
            region: 'fsn1',
            numEvents: events.length,
            result: {
              sovereignFQDN: 'omantel.omani.works',
              controlPlaneIP: '203.0.113.10',
              loadBalancerIP: '203.0.113.20',
              consoleURL: 'https://console.omantel.omani.works',
              gitopsRepoURL: 'https://gitea.omantel.omani.works',
            },
          },
          done: true,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    renderProvision(true)

    // Hetzner-infra bubble exists (assert the bubble is in the DAG).
    const hetzner = await screen.findByTestId('dag-node-hetzner-infra')
    expect(hetzner).toBeTruthy()

    // Wait for the GET /events fetch + reducer pass.
    await waitFor(() => {
      // The pill should be `Ready` once the fetch's `done: true` propagates
      // — that's the user-visible "page rendered the full history" signal.
      const pill = screen.getByTestId('status-pill')
      expect(pill.textContent).toContain('Ready')
    })

    // Phase 0 section in sidebar contains BOTH the Hetzner supernode and
    // the Flux-bootstrap supernode — they're both `kind: 'super'`. After
    // replaying the 8 events both flip to done (status: ready triggers
    // the "force pending+running to done" sweep), so the section must
    // read "2/2 ready".
    const sb = screen.getByTestId('sidebar-section-phase0')
    expect(sb.textContent ?? '').toMatch(/2\/2 ready/)

    // Log panel — selectedNodeId auto-sets to hetzner-infra on first event,
    // so the log stream should have data lines from the replayed events.
    const stream = screen.getByTestId('log-stream')
    // At least one of the replayed Hetzner messages must appear in the
    // log panel — proves the events flowed through applyEventToContext
    // and landed in detailLines[hetzner-infra].
    expect(stream.textContent ?? '').toMatch(/hcloud_server\.cp\[0\]/)
  })

  it('handles 404 from /events without crashing (older catalyst-api or unknown id)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('not found', { status: 404 }),
    )

    renderProvision(true)

    // Page should render the chrome — the failure UX is gated on the SSE
    // stream (which we disabled here), so a 404 from the GET alone leaves
    // the page in `connecting` state. We assert the topbar exists so the
    // 404 didn't take down the component tree.
    const fqdn = await screen.findByTestId('topbar-fqdn')
    expect(fqdn).toBeTruthy()
  })

  it('uses the first event time to anchor the elapsed clock for completed deployments', async () => {
    // First event is one minute before the test's "now". After the GET
    // resolves, the page should render an elapsed reading of at least
    // one minute (the SSE onopen would normally start the clock at
    // Date.now(), but for replays we anchor on the first event time so
    // a deep-link doesn't show "0m 00s" for a 30-minute deployment).
    const oneMinuteAgo = new Date(Date.now() - 60_000).toISOString()
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          events: [
            { time: oneMinuteAgo, phase: 'tofu-init', level: 'info', message: 'starting' },
          ],
          state: { id: DEPLOYMENT_ID, status: 'ready', numEvents: 1 },
          done: true,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    renderProvision(true)

    await waitFor(() => {
      const pill = screen.getByTestId('status-pill')
      expect(pill.textContent).toContain('Ready')
    })

    // The elapsed reading lives next to the progress percentage. We don't
    // assert exact seconds (timing-sensitive), only that it reads at least
    // 1 minute — proves we used the event timestamp, not `Date.now()`.
    const progPct = screen.getByTestId('progress-pct')
    const topbar = progPct.closest('.tb-r') as HTMLElement
    expect(topbar).toBeTruthy()
    const elapsed = within(topbar).getByText(/\d+m \d{2}s/)
    const match = /(\d+)m (\d{2})s/.exec(elapsed.textContent ?? '')
    expect(match).not.toBeNull()
    if (match) {
      const minutes = parseInt(match[1] ?? '0', 10)
      expect(minutes).toBeGreaterThanOrEqual(1)
    }
  })
})
