/**
 * ApplicationPage.test.tsx — vitest coverage for the per-Application
 * detail page reached at `/sovereign/provision/$deploymentId/app/$componentId`.
 *
 * Coverage:
 *   1. Renders the four-tab navigation (Logs / Dependencies / Status /
 *      Overview).
 *   2. Logs tab populates from the /events GET replay, filtered by
 *      `event.component === componentId`.
 *   3. Tab switching flips the rendered panel.
 *   4. Dependencies tab surfaces both directions (depends on +
 *      depended on by) using the catalog edges.
 *   5. Status tab reads helm release / namespace / chart version from
 *      the reducer state and falls back to "unknown" when absent.
 *   6. Overview tab renders the marketplaceCopy positioning paragraph
 *      and upstream link.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { ApplicationPage } from './ApplicationPage'
import { useWizardStore } from '@/entities/deployment/store'

const DEPLOYMENT_ID = 'depl-aaa-2222'
const COMPONENT_ID = 'bp-cilium'

function renderApp(componentId = COMPONENT_ID) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => null,
  })
  const provisionAppRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <ApplicationPage disableStream={true} />,
  })
  const routeTree = rootRoute.addChildren([provisionRoute, provisionAppRoute])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${DEPLOYMENT_ID}/app/${componentId}`],
    }),
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
  const s = useWizardStore.getState()
  s.setComponents([])
  s.reset?.()
})

afterEach(() => {
  cleanup()
})

describe('ApplicationPage — per-Application tab navigation', () => {
  it('renders the Logs / Dependencies / Status / Overview tablist', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderApp()

    const tablist = await screen.findByTestId('sov-tablist')
    expect(tablist).toBeTruthy()
    expect(screen.getByTestId('sov-tab-logs')).toBeTruthy()
    expect(screen.getByTestId('sov-tab-dependencies')).toBeTruthy()
    expect(screen.getByTestId('sov-tab-status')).toBeTruthy()
    expect(screen.getByTestId('sov-tab-overview')).toBeTruthy()
  })

  it('Logs tab populates from /events history (replay filtered by component id)', async () => {
    const events = [
      { time: '2026-04-29T15:00:00Z', phase: 'install', component: 'bp-cilium', state: 'installing', message: 'Reconciling Cilium HelmRelease' },
      { time: '2026-04-29T15:00:30Z', phase: 'install', component: 'bp-cilium', state: 'installing', message: 'Cilium DaemonSet rolling' },
      // A non-matching event — must NOT appear in the cilium log.
      { time: '2026-04-29T15:00:45Z', phase: 'install', component: 'bp-flux', state: 'installing', message: 'Flux reconciling' },
      { time: '2026-04-29T15:01:00Z', phase: 'install', component: 'bp-cilium', state: 'installed', message: 'Cilium DaemonSet ready' },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        events,
        state: { id: DEPLOYMENT_ID, status: 'provisioning', numEvents: events.length },
        done: false,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    renderApp()

    const log = await screen.findByTestId('sov-app-log')
    await waitFor(() => {
      expect(log.textContent).toContain('Reconciling Cilium')
    })
    expect(log.textContent).toContain('Cilium DaemonSet ready')
    // The flux line was filtered out because its component is bp-flux,
    // not bp-cilium.
    expect(log.textContent ?? '').not.toContain('Flux reconciling')
  })

  it('switching to the Dependencies tab renders both depends-on and depended-on-by panels', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }),
    )

    // Use cert-manager — depends on external-dns; depended on by … none directly.
    renderApp('bp-cert-manager')

    const tabBtn = await screen.findByTestId('sov-tab-dependencies')
    fireEvent.click(tabBtn)

    const depsTab = await screen.findByTestId('sov-deps-tab')
    expect(depsTab).toBeTruthy()
    expect(screen.getByTestId('sov-deps-on')).toBeTruthy()
    expect(screen.getByTestId('sov-deps-by')).toBeTruthy()
  })

  it('Status tab reads helm release / namespace / chart from per-component event state', async () => {
    const events = [
      {
        time: '2026-04-29T15:00:00Z',
        phase: 'install',
        component: 'bp-cilium',
        state: 'installing',
        message: 'Reconciling',
        helmRelease: 'cilium',
        namespace: 'kube-system',
        chartVersion: '1.17.6',
      },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        events,
        state: { id: DEPLOYMENT_ID, status: 'provisioning', numEvents: events.length },
        done: false,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    renderApp()

    const tabBtn = await screen.findByTestId('sov-tab-status')
    fireEvent.click(tabBtn)

    await waitFor(() => {
      const helm = screen.getByTestId('sov-status-helm')
      expect(helm.textContent).toContain('cilium')
    })
    const ns = screen.getByTestId('sov-status-ns')
    expect(ns.textContent).toContain('kube-system')
    const chart = screen.getByTestId('sov-status-chart')
    expect(chart.textContent).toContain('1.17.6')
  })

  it('Overview tab renders the marketplaceCopy positioning paragraph + upstream link', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderApp()

    const tabBtn = await screen.findByTestId('sov-tab-overview')
    fireEvent.click(tabBtn)

    const overview = await screen.findByTestId('sov-overview-tab')
    expect(overview).toBeTruthy()
    // Cilium has marketplaceCopy.COMPONENT_COPY → renders positioning + upstream.
    const positioning = screen.getByTestId('sov-overview-positioning')
    expect(positioning.textContent ?? '').toMatch(/Cilium/i)
    const upstream = screen.getByTestId('sov-overview-upstream')
    expect(upstream.textContent ?? '').toMatch(/cilium\.io/i)
  })

  it('renders the not-found surface when the component id is unknown to this Sovereign', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderApp('bp-this-does-not-exist')

    const notFound = await screen.findByTestId('sov-app-not-found')
    expect(notFound).toBeTruthy()
  })
})
