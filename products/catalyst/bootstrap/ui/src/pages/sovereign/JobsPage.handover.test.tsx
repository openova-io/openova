/**
 * JobsPage.handover.test.tsx — lock-in for Gap C + Gap D fixes (see
 * memory: session_2026_05_16_t117_dod_partial.md).
 *
 * Gap C — synthetic Apps / Handover stages must render as 'succeeded'
 * once the deployment record signals handover has fired (status=ready
 * OR handoverFiredAt non-null). Before this fix the JobsPage showed
 * 8 phantom "Pending" rows per multi-region Sovereign even after the
 * deployment was fully ready, breaking DoD gates D6 (0 pending) and
 * D7 (mothership ≡ child).
 *
 * Gap D — once handover fires the operator should see a visible 3-2-1
 * countdown banner with an "Open your Sovereign console →" CTA and a
 * "Stay on mothership" Cancel button. The redirect timer auto-fires
 * window.location.assign(handoverURL) once when the countdown reaches
 * 0; clicking Cancel suppresses both the banner and the timer for the
 * rest of the page lifetime.
 *
 * The auto-redirect timer is stubbed via the JobsPage's
 * `disableHandoverAutoRedirect` prop so we can assert banner DOM +
 * Cancel button behavior without faking timers / window.location.
 * The unit-test suite handoverStageOverride.test.ts covers the
 * synthetic-stage status coercion in isolation; this file is the
 * end-to-end integration test that proves JobsPage wires both pieces
 * correctly.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { FlowNode, FlowInstance } from '@openova/flow-core'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/* ────────────────────────────────────────────────────────────────────
 * Module-level mock for useFlowStream — mirrors the pattern used in
 * JobsPage.flow-merge.test.tsx. Each test seeds mockStreamState.nodes
 * with the synthetic lifecycle-phase group nodes the catalyst-api
 * openova-flow snapshot emits in production.
 * ──────────────────────────────────────────────────────────────────── */

const mockStreamState = {
  flow: null as FlowInstance | null,
  nodes: new Map<string, FlowNode>(),
  relationships: new Map(),
  streamStatus: 'completed' as const,
  streamError: null as string | null,
}

vi.mock('@/lib/openflow-adapter-sse', () => ({
  useFlowStream: () => mockStreamState,
}))

// eslint-disable-next-line import/first
import { JobsPage } from './JobsPage'

const DEP_ID = 'f9472ed4d2b9cc2d'
const HANDOVER_URL =
  'https://console.t120.omani.works/auth/handover?token=eyJ.AAA.BBB'

/**
 * Seed mockStreamState.nodes with the 8 lifecycle-stage group nodes
 * the catalyst-api flow_snapshot_local.go emits at line ~542+ on a
 * fully-converged multi-region Sovereign (1 primary + 3 secondary
 * regions, like t120.omani.works on 2026-05-16). Status="pending"
 * because these groups have no leaf descendants and the bottom-up
 * rollup leaves them as the emitted placeholder.
 */
function seedSyntheticLifecycleStageNodes() {
  const stages = [
    // Top-level — flow_snapshot_local.go extraPhases block
    { id: `${DEP_ID}:apps`, label: 'Apps' },
    { id: `${DEP_ID}:handover`, label: 'Handover' },
    // Per-region — flow_snapshot_local.go regionsFromJobs loop
    { id: `${DEP_ID}:hel1-2:apps`, label: 'Apps (hel1-2)' },
    { id: `${DEP_ID}:hel1-2:handover`, label: 'Handover (hel1-2)' },
    { id: `${DEP_ID}:nbg1-1:apps`, label: 'Apps (nbg1-1)' },
    { id: `${DEP_ID}:nbg1-1:handover`, label: 'Handover (nbg1-1)' },
    { id: `${DEP_ID}:sin-2:apps`, label: 'Apps (sin-2)' },
    { id: `${DEP_ID}:sin-2:handover`, label: 'Handover (sin-2)' },
  ]
  for (const s of stages) {
    mockStreamState.nodes.set(s.id, {
      id: s.id,
      flowId: DEP_ID,
      label: s.label,
      status: 'pending',
    })
  }
}

function renderJobs(opts: { disableHandoverAutoRedirect?: boolean } = {}) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => (
      <JobsPage
        disableStream
        disableJobsBackfill
        disableHandoverAutoRedirect={opts.disableHandoverAutoRedirect ?? true}
      />
    ),
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard',
    component: () => <div data-testid="dashboard-target" />,
  })
  const tree = rootRoute.addChildren([
    jobsRoute,
    homeRoute,
    jobDetailRoute,
    flowRoute,
    wizardRoute,
    dashboardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${DEP_ID}/jobs`],
    }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  mockStreamState.flow = null
  mockStreamState.nodes = new Map()
  mockStreamState.relationships = new Map()
  mockStreamState.streamStatus = 'completed'
  mockStreamState.streamError = null

  // Stub the deployment events fetch — returns a 'ready' snapshot with
  // handoverURL + handoverFiredAt populated. Mirrors the wire shape
  // catalyst-api emits after fireHandover persists the record.
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () =>
        Promise.resolve({
          events: [],
          state: {
            id: DEP_ID,
            status: 'ready',
            sovereignFQDN: 't120.omani.works',
            handoverURL: HANDOVER_URL,
            handoverFiredAt: '2026-05-16T09:55:06Z',
            phase1Outcome: 'ready',
          },
          done: true,
        }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('JobsPage — Gap C: synthetic stage status (handover fired)', () => {
  it('renders Apps / Handover synthetic stages as succeeded (not pending) when handoverFiredAt is set', async () => {
    seedSyntheticLifecycleStageNodes()
    renderJobs()

    // Each row's status pill carries a status data-attribute via
    // JobsTable; the cell text contains "Succeeded"/"Pending"/etc per
    // statusBadge(). We assert by reading the row's status cell text.
    const appsRow = await screen.findByTestId(`jobs-table-row-${DEP_ID}:apps`)
    expect(appsRow.textContent).toContain('Succeeded')
    expect(appsRow.textContent).not.toContain('Pending')

    const handoverRow = await screen.findByTestId(`jobs-table-row-${DEP_ID}:handover`)
    expect(handoverRow.textContent).toContain('Succeeded')
    expect(handoverRow.textContent).not.toContain('Pending')
  })

  it('coerces per-region Apps / Handover stages to succeeded as well', async () => {
    seedSyntheticLifecycleStageNodes()
    renderJobs()

    for (const region of ['hel1-2', 'nbg1-1', 'sin-2']) {
      const appsRow = await screen.findByTestId(
        `jobs-table-row-${DEP_ID}:${region}:apps`,
      )
      expect(appsRow.textContent).toContain('Succeeded')

      const handoverRow = await screen.findByTestId(
        `jobs-table-row-${DEP_ID}:${region}:handover`,
      )
      expect(handoverRow.textContent).toContain('Succeeded')
    }
  })
})

describe('JobsPage — Gap D: handover redirect banner', () => {
  it('renders the 3-2-1 countdown banner with the canonical handoverURL when handover has fired', async () => {
    seedSyntheticLifecycleStageNodes()
    renderJobs({ disableHandoverAutoRedirect: true })

    const banner = await screen.findByTestId('sov-jobs-handover-redirect-banner')
    expect(banner).toBeTruthy()
    expect(banner.textContent).toContain('Sovereign is ready')
    expect(banner.textContent).toContain('t120.omani.works')

    const cta = await screen.findByTestId('sov-jobs-handover-redirect-cta')
    expect(cta.getAttribute('href')).toBe(HANDOVER_URL)
    expect(cta.textContent).toContain('Open your Sovereign console')

    // Initial countdown value is rendered (3 seconds).
    const countdown = await screen.findByTestId(
      'sov-jobs-handover-redirect-countdown',
    )
    expect(countdown.textContent).toBe('3')
  })

  it('Cancel button suppresses the banner for the rest of the page lifetime', async () => {
    seedSyntheticLifecycleStageNodes()
    renderJobs({ disableHandoverAutoRedirect: true })

    const cancel = await screen.findByTestId('sov-jobs-handover-redirect-cancel')
    fireEvent.click(cancel)

    // Banner is removed from the DOM after cancel.
    await waitFor(() => {
      expect(
        screen.queryByTestId('sov-jobs-handover-redirect-banner'),
      ).toBeNull()
    })
  })

  it('auto-redirect fires window.location.assign(handoverURL) once when the countdown reaches 0', async () => {
    seedSyntheticLifecycleStageNodes()

    // Stub window.location.assign — production code performs a real
    // top-level navigation, which jsdom can't follow. Replace
    // window.location with a writeable proxy whose `assign` is a spy.
    const original = window.location
    const assignSpy = vi.fn()
    delete (window as { location?: Location }).location
    ;(window as unknown as { location: Partial<Location> }).location = {
      ...original,
      assign: assignSpy as unknown as Location['assign'],
    } as Location

    try {
      // Render with the auto-redirect ENABLED so the interval timer
      // ticks down to 0. The countdown is 3 seconds; with the 1s
      // setInterval that's a 3-4 second real-time wait.
      renderJobs({ disableHandoverAutoRedirect: false })

      const banner = await screen.findByTestId(
        'sov-jobs-handover-redirect-banner',
      )
      expect(banner).toBeTruthy()

      // Poll up to 6 seconds for the timer to drain. We use real timers
      // because the JobsPage's effect chain (useDeploymentEvents fetch
      // + state update + banner mount + interval scheduling) is async-
      // sensitive; mixing vi.useFakeTimers() with @testing-library
      // waitFor produced a hang in the parallel AppsPage test.
      await waitFor(
        () => {
          expect(assignSpy).toHaveBeenCalledWith(HANDOVER_URL)
        },
        { timeout: 6000, interval: 200 },
      )
      // The redirect MUST fire exactly once across the countdown
      // lifecycle — the redirectFiredRef guards a second navigate
      // even if a stray tick fires after 0.
      expect(assignSpy).toHaveBeenCalledTimes(1)
    } finally {
      ;(window as unknown as { location: Location }).location = original
    }
  }, 10000)
})

describe('JobsPage — Gap C: still-installing snapshot leaves stages pending', () => {
  it('leaves Apps / Handover stages as pending when handover has NOT fired', async () => {
    // Override the fetch stub: snapshot has no handoverFiredAt + status
    // is still installing.
    globalThis.fetch = (() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            events: [],
            state: {
              id: DEP_ID,
              status: 'phase1-installing',
              sovereignFQDN: 't120.omani.works',
            },
            done: false,
          }),
      } as unknown as Response)) as typeof fetch

    seedSyntheticLifecycleStageNodes()
    renderJobs()

    const appsRow = await screen.findByTestId(`jobs-table-row-${DEP_ID}:apps`)
    expect(appsRow.textContent).toContain('Pending')

    const handoverRow = await screen.findByTestId(
      `jobs-table-row-${DEP_ID}:handover`,
    )
    expect(handoverRow.textContent).toContain('Pending')

    // And the banner is NOT rendered.
    expect(
      screen.queryByTestId('sov-jobs-handover-redirect-banner'),
    ).toBeNull()
  })
})
