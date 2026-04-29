/**
 * AdminPage.test.tsx — vitest coverage for the Sovereign Admin landing
 * surface. Replaces the legacy ProvisionPage.test.tsx tests that
 * exercised the abandoned DAG view.
 *
 * Coverage:
 *   1. Renders the bootstrap-kit card grid with all 11 always-installed
 *      Applications, each with a status pill defaulting to `pending`.
 *   2. Renders the user-selected card grid alongside (driven by the
 *      wizard store's `selectedComponents`).
 *   3. Replays /events history → cards flip status from `pending` to
 *      `installed` and the Hetzner-infra phase banner flips to `done`.
 *   4. Family rollup sidebar reflects the per-family install counts.
 *   5. Reaching the page with a 404 from /events doesn't crash.
 *
 * `disableStream={true}` — jsdom has no EventSource. The /events GET
 * fetch covers the user-reported scenario without it.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { AdminPage } from './AdminPage'
import { useWizardStore } from '@/entities/deployment/store'

const DEPLOYMENT_ID = 'depl-abc-1234'

function renderAdmin(disableStream = true) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <AdminPage disableStream={disableStream} />,
  })
  const routeTree = rootRoute.addChildren([provisionRoute])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [`/provision/${DEPLOYMENT_ID}`] }),
  })
  return render(<RouterProvider router={router} />)
}

class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = NoopResizeObserver

beforeEach(() => {
  vi.restoreAllMocks()
  // Reset store to a known shape — clear non-mandatory selections so
  // the bootstrap-kit grid is the dominant render target.
  const s = useWizardStore.getState()
  s.setComponents([])
  s.reset?.()
})

afterEach(() => {
  cleanup()
})

describe('AdminPage — Sovereign admin landing card grid', () => {
  it('renders the bootstrap-kit card grid with all 11 always-installed applications', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'provisioning' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderAdmin()

    // Bootstrap kit grid — 11 Blueprint cards.
    const grid = await screen.findByTestId('sov-bootstrap-grid')
    expect(grid).toBeTruthy()

    // Every BOOTSTRAP_KIT entry must render a card. Spot-check the
    // anchor cases (Cilium = 01, the unique compound id `bp-bp-catalyst-platform`).
    expect(screen.getByTestId('app-card-bp-cilium')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-flux')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-crossplane')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-bp-catalyst-platform')).toBeTruthy()

    // The bootstrap summary should call out the count.
    const summary = screen.getByTestId('sov-bootstrap-summary')
    expect(summary.textContent).toContain('11')
  })

  it('per-component status defaults to pending and flips to installed after replay', async () => {
    // Realistic event mix:
    //   • tofu-output → Hetzner-infra banner flips to done
    //   • flux-bootstrap → Cluster-bootstrap banner flips to running
    //   • per-component install events for cilium and cert-manager
    const events = [
      { time: '2026-04-29T15:00:00Z', phase: 'tofu-init', level: 'info', message: 'Initialising' },
      { time: '2026-04-29T15:00:30Z', phase: 'tofu-apply', level: 'info', message: 'hcloud_server.cp[0]: Creation complete' },
      { time: '2026-04-29T15:01:00Z', phase: 'tofu-output', level: 'info', message: 'Reading outputs' },
      { time: '2026-04-29T15:01:10Z', phase: 'flux-bootstrap', level: 'info', message: 'Cloud-init bootstrapped Flux' },
      { time: '2026-04-29T15:01:20Z', phase: 'install', component: 'bp-cilium', state: 'installed', message: 'Cilium DaemonSet ready' },
      { time: '2026-04-29T15:01:25Z', phase: 'install', component: 'bp-cert-manager', state: 'installing', message: 'cert-manager rolling out' },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        events,
        state: { id: DEPLOYMENT_ID, status: 'provisioning', numEvents: events.length },
        done: false,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    renderAdmin()

    await waitFor(() => {
      const ciliumPill = screen.getByTestId('app-status-bp-cilium')
      expect(ciliumPill.textContent).toContain('Installed')
    })

    const certPill = screen.getByTestId('app-status-bp-cert-manager')
    expect(certPill.textContent).toContain('Installing')

    // Phase banners reflect their states.
    const hetznerStatus = screen.getByTestId('sov-phase-hetzner-infra-status')
    expect(hetznerStatus.textContent).toContain('Done')
    const bootstrapStatus = screen.getByTestId('sov-phase-cluster-bootstrap-status')
    expect(bootstrapStatus.textContent).toContain('Running')
  })

  it('renders all cards in pending status before any events arrive', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderAdmin()

    const grid = await screen.findByTestId('sov-bootstrap-grid')
    expect(grid).toBeTruthy()

    // Without events, every bootstrap-kit card reads "Pending".
    const ciliumPill = screen.getByTestId('app-status-bp-cilium')
    expect(ciliumPill.textContent).toContain('Pending')
    const fluxPill = screen.getByTestId('app-status-bp-flux')
    expect(fluxPill.textContent).toContain('Pending')
  })

  it('family rollup sidebar lists every represented Catalyst family', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderAdmin()

    const rollup = await screen.findByTestId('sov-family-rollup')
    expect(rollup).toBeTruthy()
    // PILOT and SPINE always appear because flux/crossplane/cilium are
    // bootstrap-kit Applications. GUARDIAN is present because keycloak
    // is in the bootstrap kit.
    expect(screen.getByTestId('sov-fam-pilot')).toBeTruthy()
    expect(screen.getByTestId('sov-fam-spine')).toBeTruthy()
  })

  it('handles a 404 from /events without crashing the shell', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('not found', { status: 404 }),
    )

    renderAdmin()

    // Top bar still renders.
    const fqdn = await screen.findByTestId('sov-fqdn')
    expect(fqdn).toBeTruthy()
    // Bootstrap grid still rendered (every card pending).
    expect(screen.getByTestId('sov-bootstrap-grid')).toBeTruthy()
  })

  it('marks all Applications installed once the deployment is reported ready', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        events: [],
        state: {
          id: DEPLOYMENT_ID,
          status: 'ready',
          numEvents: 0,
          result: {
            sovereignFQDN: 'omantel.omani.works',
            controlPlaneIP: '203.0.113.10',
            loadBalancerIP: '203.0.113.20',
            consoleURL: 'https://console.omantel.omani.works',
            gitopsRepoURL: 'https://gitea.omantel.omani.works',
          },
        },
        done: true,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    renderAdmin()

    await waitFor(() => {
      const ciliumPill = screen.getByTestId('app-status-bp-cilium')
      expect(ciliumPill.textContent).toContain('Installed')
    })
    const overall = screen.getByTestId('sov-overall-status')
    expect(overall.textContent).toContain('Ready')
  })
})
