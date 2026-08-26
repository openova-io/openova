/**
 * ConvergenceWizard.test.tsx — #3925 surface-A state-aware Dashboard.
 *
 * Coverage:
 *   • deriveWizardPhase — status → phase mapping (the 5 phases)
 *   • hrRatio — multi-region HR census + componentStates fallback
 *   • wizard render — highlights the live phase + shows the Reconcile ratio
 *   • deep-links — phase ③ → /cloud (unified graph, #3958), phase ② → /jobs
 *   • Dashboard state-aware DEFAULTS (#4731) — the one treemap defaults to
 *     the job-sourced Progress→Kind stack (status colour) while converging,
 *     morphs to the resource defaults on ready, and re-stacks back to
 *     Progress on operator demand. No component toggle.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import {
  ConvergenceWizard,
  deriveWizardPhase,
  hrRatio,
} from './ConvergenceWizard'
import { Dashboard } from './Dashboard'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { TreemapData } from '@/lib/treemap.types'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('deriveWizardPhase', () => {
  const cases: Array<[string | undefined, ReturnType<typeof deriveWizardPhase>]> = [
    ['pending', 'infrastructure'],
    ['tofu-applying', 'infrastructure'],
    ['flux-bootstrapping', 'bootstrap'],
    ['phase1-watching', 'reconciliation'],
    ['ready', 'ready'],
    ['failed', 'health'],
    ['partial-failure', 'health'],
    [undefined, 'infrastructure'],
  ]
  for (const [status, expected] of cases) {
    it(`${status ?? '(none)'} → ${expected}`, () => {
      expect(deriveWizardPhase(status ? ({ status } as DeploymentSnapshot) : null)).toBe(expected)
    })
  }
})

describe('hrRatio', () => {
  it('sums the multi-region HR census', () => {
    const snap = {
      regions: [
        { region: 'a', primary: true, hrReady: 60, hrTotal: 63, degraded: false },
        { region: 'b', primary: false, hrReady: 48, hrTotal: 63, degraded: true },
      ],
    } as DeploymentSnapshot
    expect(hrRatio(snap)).toEqual({ ready: 108, total: 126 })
  })
  it('falls back to componentStates when no region census', () => {
    const snap = {
      componentStates: { cilium: 'installed', flux: 'installed', keycloak: 'installing' },
    } as unknown as DeploymentSnapshot
    expect(hrRatio(snap)).toEqual({ ready: 2, total: 3 })
  })
  it('returns 0/0 when nothing is known', () => {
    expect(hrRatio(null)).toEqual({ ready: 0, total: 0 })
  })
})

function renderWizard(snapshot: DeploymentSnapshot | null) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => <ConvergenceWizard snapshot={snapshot} deploymentId="d-1" />,
  })
  // #3958 — the wizard's reconciliation deep-link now lands on the
  // unified Cloud graph (/cloud), not the deleted /reconciliation page.
  const reconRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div data-testid="recon-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route, reconRoute, jobsRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/dashboard'] }),
  })
  return render(<RouterProvider router={router as never} />)
}

describe('ConvergenceWizard render', () => {
  it('highlights the live phase + shows the Reconcile ratio', async () => {
    renderWizard({
      status: 'phase1-watching',
      regions: [{ region: 'a', primary: true, hrReady: 52, hrTotal: 65, degraded: false }],
    } as DeploymentSnapshot)
    const recon = await screen.findByTestId('wizard-phase-reconciliation')
    expect(recon.getAttribute('data-active')).toBe('true')
    expect(screen.getByTestId('wizard-reconcile-ratio').textContent).toContain('52/65')
  })

  it('deep-links phase ③ to the unified Cloud graph reconciliation LENS and ② to Jobs', async () => {
    renderWizard({ status: 'phase1-watching' } as DeploymentSnapshot)
    const reconLink = await screen.findByTestId('wizard-link-reconciliation')
    const href = reconLink.getAttribute('href') ?? ''
    expect(href).toContain('/cloud')
    // #3996 follow-up — the link must select the Reconciliation lens, not
    // the default Cloud lens. The cloud route's validateSearch carries the
    // `lens` param through, so the rendered href must include it (a dropped
    // param would mean the deep-link lands on the default lens).
    expect(href).toContain('view=graph')
    expect(href).toContain('lens=reconciliation')
    const jobsLink = screen.getByTestId('wizard-link-jobs')
    expect(jobsLink.getAttribute('href')).toContain('/jobs')
    // #4704 Task B — never a bare /jobs: the link must carry the
    // deployment id on the mothership (jsdom runs in catalyst-zero mode).
    expect(jobsLink.getAttribute('href')).toContain('/provision/d-1/jobs')
  })

  it('#4704 — every phase drills down to a log-bearing surface with the deployment id', async () => {
    renderWizard({ status: 'tofu-applying' } as DeploymentSnapshot)
    // Infrastructure → finite Jobs table (rows open JobDetail + LogPane).
    const infraLink = await screen.findByTestId('wizard-link-infrastructure')
    expect(infraLink.getAttribute('href')).toContain('/provision/d-1/jobs')
    // Health → reconciliation graph (recon objects + logs).
    const healthLink = screen.getByTestId('wizard-link-health')
    expect(healthLink.getAttribute('href')).toContain('/cloud')
    expect(healthLink.getAttribute('href')).toContain('lens=reconciliation')
  })

  it('#4704 — a failed deployment renders its pinned phase RED, not in-progress blue', async () => {
    renderWizard({ status: 'failed' } as DeploymentSnapshot)
    // failed pins the Health phase (deriveWizardPhase) — it must carry
    // the failed marker + class so the semantic red styling applies.
    const health = await screen.findByTestId('wizard-phase-health')
    expect(health.getAttribute('data-failed')).toBe('true')
    expect(health.className).toContain('wizard-phase-failed')
    expect(health.className).not.toContain('wizard-phase-active')
  })

  it('#4704 — a converging deployment keeps the in-progress (blue) phase styling', async () => {
    renderWizard({ status: 'phase1-watching' } as DeploymentSnapshot)
    const recon = await screen.findByTestId('wizard-phase-reconciliation')
    expect(recon.getAttribute('data-failed')).toBe('false')
    expect(recon.className).toContain('wizard-phase-active')
  })
})

// ── Dashboard view toggle + auto-flip ─────────────────────────────────────
//
// The Dashboard derives its view from the deployment snapshot (fetched via
// useDeploymentEvents → GET /events). We stub fetch to return a chosen
// status so we can assert the auto-flip.

function stubEventsFetch(state: DeploymentSnapshot | undefined) {
  globalThis.fetch = ((url: string) => {
    const u = String(url)
    if (u.includes('/events')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ events: [], state, done: state !== undefined }),
      } as unknown as Response)
    }
    // deployment poll / anything else
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve(state ?? {}),
    } as unknown as Response)
  }) as typeof fetch
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class FakeRO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof FakeRO }).ResizeObserver = FakeRO
  }
}

const EMPTY_TREEMAP: TreemapData = { total_count: 0, items: [] } as unknown as TreemapData

function renderDashboard() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    // disableStream skips the EventSource (jsdom has none); the snapshot is
    // resolved via the GET /events replay path ({state, done:true}) — the
    // same seam the existing Dashboard suite uses.
    component: () => <Dashboard initialDataOverride={EMPTY_TREEMAP} disableStream />,
  })
  const reconRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div data-testid="recon-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const decomRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <div data-testid="decom-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([dashRoute, reconRoute, jobsRoute, decomRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/dashboard'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('Dashboard state-aware view', () => {
  beforeEach(() => {
    // default: not-ready (still converging)
    stubEventsFetch({ status: 'phase1-watching' } as DeploymentSnapshot)
  })

  // #4731 — the Dashboard is ONE treemap in both lifecycle states: no
  // Progress ⇄ Treemap component toggle, no ProvisioningTreemap pane.
  // While converging the DEFAULTS are the job-sourced Progress→Kind
  // stack coloured by status; on ready they morph to the resource
  // defaults. Same pane, same component.
  it('defaults to the Progress→Kind job stack (status colour) while converging', async () => {
    renderDashboard()
    // The one treemap frame + controller are ALWAYS present.
    expect(await screen.findByTestId('dashboard-treemap-frame')).toBeTruthy()
    expect(screen.queryByTestId('provisioning-treemap')).toBeNull()
    expect(screen.queryByTestId('dashboard-view-toggle')).toBeNull()
    expect(screen.queryByTestId('convergence-wizard')).toBeNull()
    const layer0 = screen.getByTestId('treemap-layer-0-select') as HTMLSelectElement
    const layer1 = screen.getByTestId('treemap-layer-1-select') as HTMLSelectElement
    const colour = screen.getByTestId('treemap-color-select') as HTMLSelectElement
    const size = screen.getByTestId('treemap-size-select') as HTMLSelectElement
    expect(layer0.value).toBe('progress')
    expect(layer1.value).toBe('kind')
    expect(colour.value).toBe('status')
    expect(size.value).toBe('uniform')
  })

  // #6695 (founder 2026-08-26): the ready default FLIPS to the real
  // resource/health map. The #4731 amendment had kept [progress, kind]
  // unconditionally, but on a converged Sovereign that job stack was
  // meaningless ("install, install, install… nothing distinguishable"), so
  // ready now shows [namespace, application] coloured by health.
  it('#6695 — flips to the resource/health map when status is ready', async () => {
    stubEventsFetch({ status: 'ready' } as DeploymentSnapshot)
    renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    // Converged ⇒ the default stack is [namespace, application] (real k8s
    // objects), coloured by HEALTH so down components pop red, sized by
    // cpu_request so a fully down app keeps a visible tile.
    await waitFor(() => {
      expect(
        (screen.getByTestId('treemap-layer-0-select') as HTMLSelectElement).value,
      ).toBe('namespace')
    })
    const layer1 = screen.getByTestId('treemap-layer-1-select') as HTMLSelectElement
    const colour = screen.getByTestId('treemap-color-select') as HTMLSelectElement
    const size = screen.getByTestId('treemap-size-select') as HTMLSelectElement
    expect(layer1.value).toBe('application')
    expect(colour.value).toBe('health')
    expect(size.value).toBe('cpu_request')
    expect(screen.queryByTestId('dashboard-view-toggle')).toBeNull()
  })

  it('the job stack AND utilisation colour both stay selectable on ready', async () => {
    // MUST-PRESERVE: neither vocabulary is removed — the operator can pivot
    // back to the job-sourced [progress, kind] view, and utilisation colour
    // is still pickable on a resource stack (it is just no longer the ready
    // default, which is now health).
    stubEventsFetch({ status: 'ready' } as DeploymentSnapshot)
    renderDashboard()
    // Ready default is the resource stack.
    await waitFor(() => {
      expect(
        (screen.getByTestId('treemap-layer-0-select') as HTMLSelectElement).value,
      ).toBe('namespace')
    })
    // utilisation colour is still selectable on the resource stack.
    fireEvent.change(screen.getByTestId('treemap-color-select'), {
      target: { value: 'utilization' },
    })
    await waitFor(() => {
      expect(
        (screen.getByTestId('treemap-color-select') as HTMLSelectElement).value,
      ).toBe('utilization')
    })
    // The job-sourced [progress, kind] view is still reachable — picking
    // progress for layer 0 flips the whole stack back to the job vocabulary
    // (colour re-derives to status, size to uniform).
    fireEvent.change(screen.getByTestId('treemap-layer-0-select'), {
      target: { value: 'progress' },
    })
    await waitFor(() => {
      expect(
        (screen.getByTestId('treemap-color-select') as HTMLSelectElement).value,
      ).toBe('status')
    })
    expect((screen.getByTestId('treemap-size-select') as HTMLSelectElement).value).toBe('uniform')
  })
})
