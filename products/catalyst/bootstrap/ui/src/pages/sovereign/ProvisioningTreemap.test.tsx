/**
 * ProvisioningTreemap.test.tsx — #4704 provisioning Dashboard pane.
 *
 * Coverage:
 *   • deriveProvisioningTiles — Phase-0 + per-family tile derivation from
 *     the REAL eventReducer state (grey → blue → green/red/amber as events
 *     arrive), degraded → amber (warning), never red.
 *   • aggregateKind — chip/group rollup precedence.
 *   • provisioningTileHref — JobsTable-identical link convention (bare
 *     job name, deployment-id-scoped on the mothership).
 *   • Render — header strip ("Provisioning N%" + phase chips) + ≥10 tiles
 *     from fixture data; a failed component tile carries the red semantic
 *     kind, an installing one the blue in-progress kind (visibly distinct
 *     tokens asserted via the pane's semantic CSS).
 *   • Tile click — navigates to that component's JobDetail with the
 *     CORRECT deployment id.
 *   • Ready deployment — the pane renders the final all-green grid (no
 *     empty state) when an operator opens Progress on a converged dep.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import {
  ProvisioningTreemap,
  deriveProvisioningTiles,
  aggregateKind,
  provisioningTileHref,
  appStatusKind,
  PHASE0_GROUP_ID,
} from './ProvisioningTreemap'
import {
  buildInitialState,
  reduceEvents,
  markAllReady,
  type DeploymentEvent,
  type ReducerState,
} from './eventReducer'
import { resolveApplications } from './applicationCatalog'
import type { DeploymentSnapshot } from './useDeploymentEvents'

afterEach(() => cleanup())

/* ── Fixtures ──────────────────────────────────────────────────────
 * Real catalog (bootstrap kit + mandatory components) + the REAL
 * reducer — the fixture is the event stream, not a hand-built state. */

const APPS = resolveApplications([])
const APP_IDS = APPS.map((a) => a.id)

/** Converging fixture: Phase 0 done, flux handing off, cilium installed,
 *  keycloak failed, gitea installing, openbao degraded. */
function convergingState(): ReducerState {
  const events: DeploymentEvent[] = [
    { phase: 'tofu-init', level: 'info', message: 'init', time: '2026-07-03T10:00:00Z' },
    { phase: 'tofu-apply', level: 'info', message: 'applying', time: '2026-07-03T10:01:00Z' },
    { phase: 'tofu-output', level: 'info', message: 'outputs captured', time: '2026-07-03T10:02:00Z' },
    { phase: 'flux-bootstrap', level: 'info', message: 'handing off', time: '2026-07-03T10:03:00Z' },
    { phase: 'component', component: 'cilium', state: 'installed', time: '2026-07-03T10:04:00Z' },
    { phase: 'component', component: 'keycloak', state: 'failed', level: 'error', time: '2026-07-03T10:05:00Z' },
    { phase: 'component', component: 'gitea', state: 'installing', time: '2026-07-03T10:06:00Z' },
    { phase: 'component', component: 'openbao', state: 'degraded', time: '2026-07-03T10:07:00Z' },
  ]
  return reduceEvents(buildInitialState(APP_IDS), events)
}

/** Ready fixture: every component installed via the durable
 *  componentStates map (the GET-replay path on a converged dep). */
function readyState(): ReducerState {
  const componentStates: Record<string, string> = {}
  for (const a of APPS) componentStates[a.bareId] = 'installed'
  return markAllReady(buildInitialState(APP_IDS), componentStates)
}

function tileByJobId(state: ReducerState, jobId: string) {
  for (const g of deriveProvisioningTiles(state, APPS)) {
    const t = g.tiles.find((x) => x.jobId === jobId)
    if (t) return t
  }
  return undefined
}

/* ── Unit: derivation ─────────────────────────────────────────────── */

describe('deriveProvisioningTiles', () => {
  it('starts every tile grey (pending) with Phase 0 first (5 infra tiles)', () => {
    const groups = deriveProvisioningTiles(buildInitialState(APP_IDS), APPS)
    expect(groups[0]?.id).toBe(PHASE0_GROUP_ID)
    expect(groups[0]?.tiles).toHaveLength(5)
    const all = groups.flatMap((g) => g.tiles)
    // 5 infra tiles + one per resolved application.
    expect(all).toHaveLength(5 + APPS.length)
    expect(all.every((t) => t.kind === 'pending')).toBe(true)
  })

  it('colours tiles in as SSE events arrive: green/blue/red/amber per statusColors', () => {
    const state = convergingState()
    // Phase 0: tofu-output done → the tofu tiles are green.
    expect(tileByJobId(state, 'infrastructure:tofu-apply')?.kind).toBe('success')
    // flux-bootstrap is mid-handoff → blue (in-progress), visibly distinct.
    expect(tileByJobId(state, 'cluster-bootstrap')?.kind).toBe('in-progress')
    // Components: installed → green, failed → red, installing → blue,
    // degraded → AMBER (warning, not red), untouched → grey.
    expect(tileByJobId(state, 'bp-cilium')?.kind).toBe('success')
    expect(tileByJobId(state, 'bp-keycloak')?.kind).toBe('failed')
    expect(tileByJobId(state, 'bp-gitea')?.kind).toBe('in-progress')
    expect(tileByJobId(state, 'bp-openbao')?.kind).toBe('warning')
    expect(tileByJobId(state, 'bp-cert-manager')?.kind).toBe('pending')
  })

  it('renders an all-green grid for a READY deployment (componentStates seed)', () => {
    const groups = deriveProvisioningTiles(readyState(), APPS)
    const all = groups.flatMap((g) => g.tiles)
    expect(all.length).toBeGreaterThanOrEqual(10)
    expect(all.every((t) => t.kind === 'success')).toBe(true)
  })
})

describe('appStatusKind / aggregateKind', () => {
  it('degraded maps to warning (amber), never failed', () => {
    expect(appStatusKind('degraded')).toBe('warning')
  })
  it('rollup precedence: failed > warning > in-progress > success > pending', () => {
    expect(aggregateKind(['success', 'failed', 'in-progress'])).toBe('failed')
    expect(aggregateKind(['success', 'warning'])).toBe('warning')
    expect(aggregateKind(['success', 'in-progress'])).toBe('in-progress')
    // Partially-done (mixed success+pending) reads as in-flight.
    expect(aggregateKind(['success', 'pending'])).toBe('in-progress')
    expect(aggregateKind(['success', 'success'])).toBe('success')
    expect(aggregateKind(['pending', 'pending'])).toBe('pending')
    expect(aggregateKind([])).toBe('pending')
  })
})

describe('provisioningTileHref', () => {
  it('builds the JobsTable-identical deployment-scoped JobDetail path', () => {
    // jsdom runs in catalyst-zero (mothership) mode.
    expect(provisioningTileHref('bp-keycloak', 'd-77')).toBe(
      '/provision/d-77/jobs/bp-keycloak',
    )
    // The colon prefix strips exactly like useJobLinkBuilder.
    expect(provisioningTileHref('infrastructure:tofu-apply', 'd-77')).toBe(
      '/provision/d-77/jobs/tofu-apply',
    )
    // Id-less mothership link falls back to the deployments list
    // (#4704 Task B — never a literal in the $deploymentId slot).
    expect(provisioningTileHref('bp-cilium', '')).toBe('/deployments')
  })
})

/* ── Render ───────────────────────────────────────────────────────── */

function renderPane(state: ReducerState, snapshot: DeploymentSnapshot | null) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => (
      <ProvisioningTreemap
        snapshot={snapshot}
        state={state}
        applications={APPS}
        deploymentId="d-77"
        fixedWidth={1200}
      />
    ),
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([dashRoute, jobDetailRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-77/dashboard'] }),
  })
  const utils = render(<RouterProvider router={router as never} />)
  return { router, ...utils }
}

describe('ProvisioningTreemap render', () => {
  it('renders the header strip + ≥10 tiles from the fixture stream', async () => {
    renderPane(convergingState(), { status: 'phase1-watching' } as DeploymentSnapshot)
    const header = await screen.findByTestId('provisioning-treemap-header')
    expect(header.textContent).toContain('Provisioning')
    expect(header.textContent).toMatch(/\d+%/)
    // Phase chips, coloured by aggregate state.
    expect(screen.getByTestId('prov-chip-phase0').textContent).toContain(
      'Phase 0 · Cloud infrastructure',
    )
    const kitChip = screen.getByTestId('prov-chip-bootstrap-kit')
    expect(kitChip.textContent).toContain('Phase 1 · Bootstrap kit')
    expect(kitChip.textContent).toMatch(/\d+\/\d+/)
    // The tile grid — the treemap skeleton.
    const tiles = screen.getAllByTestId(/^prov-tile-/)
    expect(tiles.length).toBeGreaterThanOrEqual(10)
  })

  it('colours a failed component red and an installing one blue (distinct kinds + tokens)', async () => {
    const { container } = renderPane(convergingState(), {
      status: 'phase1-watching',
    } as DeploymentSnapshot)
    const failed = await screen.findByTestId('prov-tile-bp-keycloak')
    const installing = screen.getByTestId('prov-tile-bp-gitea')
    const installed = screen.getByTestId('prov-tile-bp-cilium')
    const pending = screen.getByTestId('prov-tile-bp-cert-manager')
    expect(failed.getAttribute('data-kind')).toBe('failed')
    expect(installing.getAttribute('data-kind')).toBe('in-progress')
    expect(installed.getAttribute('data-kind')).toBe('success')
    expect(pending.getAttribute('data-kind')).toBe('pending')
    // The pane's semantic CSS binds each kind to a DISTINCT theme token
    // (the statusColors contract): failed→danger red, in-progress→accent
    // blue (never grey/green), success→green, pending→dim grey.
    const css = container.querySelector('style')?.textContent ?? ''
    expect(css).toContain('.prov-kind-failed .prov-tile-rect')
    expect(css).toMatch(/\.prov-kind-failed[^}]*--color-danger/)
    expect(css).toMatch(/\.prov-kind-in-progress[^}]*--color-accent/)
    expect(css).toMatch(/\.prov-kind-success[^}]*--color-success/)
    expect(css).toMatch(/\.prov-kind-pending[^}]*--color-text-dim/)
  })

  it('tile click navigates to that component JobDetail with the CORRECT deployment id', async () => {
    const { router } = renderPane(convergingState(), {
      status: 'phase1-watching',
    } as DeploymentSnapshot)
    const tile = await screen.findByTestId('prov-tile-bp-keycloak')
    fireEvent.click(tile)
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/provision/d-77/jobs/bp-keycloak')
    })
    expect(await screen.findByTestId('job-detail-target')).toBeTruthy()
  })

  it('renders the final all-green grid (not an empty state) on a READY deployment', async () => {
    renderPane(readyState(), { status: 'ready' } as DeploymentSnapshot)
    const pane = await screen.findByTestId('provisioning-treemap')
    expect(pane.getAttribute('data-ready')).toBe('true')
    expect(screen.getByTestId('provisioning-progress-label').textContent).toContain('Ready')
    const tiles = screen.getAllByTestId(/^prov-tile-/)
    expect(tiles.length).toBeGreaterThanOrEqual(10)
    expect(screen.getByTestId('prov-tile-bp-cilium').getAttribute('data-kind')).toBe('success')
    // The morph hint flips to the converged copy.
    expect(screen.getByTestId('provisioning-treemap-hint').textContent).toContain('Converged')
  })
})
