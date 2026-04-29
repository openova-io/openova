/**
 * AdminPage.test.tsx — vitest coverage for the Sovereign Admin landing
 * surface (pixel-port of admin/nova/catalog).
 *
 * The legacy two-grid (bootstrap-kit + user-selected) split + phase-
 * banner row was DROPPED in the admin/nova/catalog port — that layout
 * does not exist on the canonical surface. This file's coverage now
 * mirrors the new shape:
 *
 *   1. Sidebar chrome — five-item nav, Catalog active.
 *   2. Header — "Catalog Management" h1 + "+ Add App" button.
 *   3. Single auto-fit card grid — every Application (bootstrap-kit
 *      ∪ selected ∪ transitive deps) renders as a card with a status
 *      pill defaulting to `pending`.
 *   4. /events history replay flips per-component status pills.
 *   5. Tabs row (Apps / Plans / Industries / Add-ons) — Apps active
 *      by default; the others reveal admin-parity placeholder grids.
 *   6. /events 404 doesn't crash the shell.
 *
 * `disableStream={true}` — jsdom has no EventSource. The /events GET
 * fetch covers the user-reported scenario without it.
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
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div>wizard</div>,
  })
  const routeTree = rootRoute.addChildren([provisionRoute, wizardRoute])
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
  // the bootstrap-kit set is the dominant render target.
  const s = useWizardStore.getState()
  s.setComponents([])
  s.reset?.()
})

afterEach(() => {
  cleanup()
})

describe('AdminPage — Sovereign admin/nova/catalog port', () => {
  it('renders the admin shell (sidebar + main column)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    renderAdmin()

    expect(await screen.findByTestId('sov-admin-shell')).toBeTruthy()
    expect(screen.getByTestId('sov-sidebar')).toBeTruthy()
    expect(screen.getByTestId('sov-nav-catalog')).toBeTruthy()
    expect(screen.getByTestId('sov-nav-catalog').dataset.active).toBe('true')
  })

  it('renders the canonical "Catalog Management" header + Add button', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    renderAdmin()

    const title = await screen.findByTestId('sov-page-title')
    expect(title.textContent).toBe('Catalog Management')
    expect(screen.getByTestId('sov-add-button').textContent).toBe('+ Add App')
  })

  it('renders the single apps grid with all bootstrap-kit applications', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    renderAdmin()

    const grid = await screen.findByTestId('sov-apps-grid')
    expect(grid).toBeTruthy()

    expect(screen.getByTestId('app-card-bp-cilium')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-flux')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-crossplane')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-bp-catalyst-platform')).toBeTruthy()
  })

  it('per-component status defaults to pending and flips to installed after replay', async () => {
    const events = [
      { time: '2026-04-29T15:01:20Z', phase: 'install', component: 'bp-cilium', state: 'installed', message: 'Cilium DaemonSet ready' },
      { time: '2026-04-29T15:01:25Z', phase: 'install', component: 'bp-cert-manager', state: 'installing', message: 'cert-manager rolling out' },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          events,
          state: { id: DEPLOYMENT_ID, status: 'provisioning', numEvents: events.length },
          done: false,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    renderAdmin()

    await waitFor(() => {
      const ciliumPill = screen.getByTestId('app-status-bp-cilium')
      expect(ciliumPill.textContent).toContain('Installed')
    })

    const certPill = screen.getByTestId('app-status-bp-cert-manager')
    expect(certPill.textContent).toContain('Installing')

    const fluxPill = screen.getByTestId('app-status-bp-flux')
    expect(fluxPill.textContent).toContain('Pending')
  })

  it('renders all cards in pending status before any events arrive', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    renderAdmin()

    const grid = await screen.findByTestId('sov-apps-grid')
    expect(grid).toBeTruthy()

    const ciliumPill = screen.getByTestId('app-status-bp-cilium')
    expect(ciliumPill.textContent).toContain('Pending')
    const fluxPill = screen.getByTestId('app-status-bp-flux')
    expect(fluxPill.textContent).toContain('Pending')
  })

  it('switches between admin tabs (Apps / Plans / Industries / Add-ons)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ events: [], state: { id: DEPLOYMENT_ID, status: 'pending' }, done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    renderAdmin()

    expect(await screen.findByTestId('sov-apps-grid')).toBeTruthy()

    fireEvent.click(screen.getByTestId('sov-tab-plans'))
    expect(screen.queryByTestId('sov-apps-grid')).toBeNull()
    expect(screen.getByTestId('sov-plans-grid')).toBeTruthy()

    fireEvent.click(screen.getByTestId('sov-tab-industries'))
    expect(screen.getByTestId('sov-industries-grid')).toBeTruthy()

    fireEvent.click(screen.getByTestId('sov-tab-addons'))
    expect(screen.getByTestId('sov-addons-grid')).toBeTruthy()

    fireEvent.click(screen.getByTestId('sov-tab-apps'))
    expect(screen.getByTestId('sov-apps-grid')).toBeTruthy()
  })

  it('handles a 404 from /events without crashing the shell', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('not found', { status: 404 }))

    renderAdmin()

    expect(await screen.findByTestId('sov-admin-shell')).toBeTruthy()
    expect(screen.getByTestId('sov-apps-grid')).toBeTruthy()
  })
})
