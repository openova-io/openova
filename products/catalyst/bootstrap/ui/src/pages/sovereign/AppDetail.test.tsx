/**
 * AppDetail.test.tsx — pixel-port lock-in for the per-Application page.
 *
 *   • Hero renders the title + status chip (NOT tabs) on first paint.
 *   • Sections render in canonical order: About → (Connection if
 *     service) → Bundled deps → Tenant → (Configuration if schema) →
 *     Jobs.
 *   • There is NO `role="tablist"` selector — the canonical surface
 *     uses sections, not tabs. This is the explicit anti-regression
 *     test against the prior invented ApplicationPage tabbed layout.
 *   • Jobs section appears for every component, with a per-component
 *     JobCard when the descriptor has a job.
 *   • Back link returns to /provision/$deploymentId.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { AppDetail } from './AppDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDetail(deploymentId: string, componentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <AppDetail disableStream />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, homeRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/app/${componentId}`],
    }),
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('AppDetail — hero', () => {
  it('renders the hero with the Application title', async () => {
    renderDetail('d-1', 'bp-cilium')
    expect(await screen.findByTestId('sov-hero')).toBeTruthy()
    // Cilium descriptor renders its name in the hero.
    expect(screen.getByText('Cilium')).toBeTruthy()
  })

  it('back link points to the apps grid', async () => {
    renderDetail('d-1', 'bp-cilium')
    const back = await screen.findByTestId('sov-back-link')
    expect(back.getAttribute('href')).toBe('/provision/d-1')
  })

  it('renders a not-found fallback for an unknown componentId', async () => {
    renderDetail('d-1', 'bp-does-not-exist')
    expect(await screen.findByTestId('sov-app-not-found')).toBeTruthy()
  })
})

describe('AppDetail — section order (NOT tabs)', () => {
  it('renders About / (Connection?) / Bundled deps / Tenant / Jobs sections in order', async () => {
    renderDetail('d-1', 'bp-cilium')
    const detail = await screen.findByTestId(/sov-app-detail-/)
    const sections = within(detail).getAllByTestId(/^sov-section-/)
    // canonical visit order: About → Tenant → Jobs (Cilium has no
    // dependencies so the deps section is omitted; Cilium isn't a
    // service-app so Connection is omitted; Cilium has no config
    // schema so Configuration is omitted). The remaining three MUST
    // render in that order.
    const ids = sections.map((s) => s.getAttribute('data-testid'))
    const aboutIdx = ids.indexOf('sov-section-about')
    const tenantIdx = ids.indexOf('sov-section-tenant')
    const jobsIdx = ids.indexOf('sov-section-jobs')
    expect(aboutIdx).toBeGreaterThanOrEqual(0)
    expect(tenantIdx).toBeGreaterThan(aboutIdx)
    expect(jobsIdx).toBeGreaterThan(tenantIdx)
  })

  it('does NOT render a role="tablist" anywhere on the page', async () => {
    renderDetail('d-1', 'bp-cilium')
    // Wait for hero so the page is mounted.
    await screen.findByTestId('sov-hero')
    expect(screen.queryByRole('tablist')).toBeNull()
  })
})

describe('AppDetail — Jobs section', () => {
  it('always renders the Jobs section', async () => {
    renderDetail('d-1', 'bp-cilium')
    expect(await screen.findByTestId('sov-section-jobs')).toBeTruthy()
  })

  it('renders one JobCard for the component', async () => {
    renderDetail('d-1', 'bp-cilium')
    const jobs = await screen.findByTestId('sov-section-jobs')
    // The job derived for bp-cilium has id "bp-cilium" — JobCard
    // testid is sov-job-card-<id>. With no events yet it renders as
    // pending.
    const card = within(jobs).queryByTestId('sov-job-card-bp-cilium')
    expect(card).toBeTruthy()
  })
})
