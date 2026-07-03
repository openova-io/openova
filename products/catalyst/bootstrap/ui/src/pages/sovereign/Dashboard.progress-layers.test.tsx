/**
 * Dashboard.progress-layers.test.tsx — #4731 Progress + Kind as
 * first-class layers of the EXISTING editable treemap (supersedes the
 * deleted ProvisioningTreemap suite).
 *
 * Coverage:
 *   • buildJobsTreemapData — group-bucket derivation from the REAL
 *     `type=group` rows in dependency (topological) order; kind buckets
 *     nested under; individual job leaves with jobId/statusKind/
 *     statusLabel and uniform size.
 *   • Status colour mapping — the full 7-value JobStatus vocabulary
 *     incl. the HEALTH axis (healthy→green, degraded→amber,
 *     failing→red).
 *   • Sparse-group fallback — a store with only the `provisioner`
 *     group buckets by kind-derived lifecycle stages instead.
 *   • jobTileHref — JobsTable-identical JobDetail link convention
 *     (bare job name, deployment-scoped on the mothership).
 *   • Render — the converging Dashboard defaults to the job-sourced
 *     Progress→Kind stack, renders real /jobs data as treemap cells,
 *     pulses running cells, shows the categorical legend, and a job
 *     leaf CLICK navigates to that job's JobDetail with the CORRECT
 *     deployment id.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
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
  Dashboard,
} from './Dashboard'
import {
  buildJobsTreemapData,
  jobTileHref,
  PROVISIONING_DEFAULT_LAYERS,
  KIND_STAGES,
} from './dashboardJobsTreemap'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import type { Job, JobStatus } from '@/lib/jobs.types'
import type { TreemapItem } from '@/lib/treemap.types'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/* ── Fixtures ─────────────────────────────────────────────────────── */

const DEP = 'd-77'

/** Job factory — leaf install by default; override what the case needs. */
function J(partial: Partial<Job> & Pick<Job, 'id' | 'jobName'>): Job {
  return {
    type: 'install',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...partial,
  }
}

/**
 * Canonical converging store: three top-level groups chained by
 * dependsOn (provisioner → bootstrap-kit → day-2-mutations), leaves of
 * several kinds/statuses, plus one UNGROUPED cron leaf. The array is
 * deliberately SHUFFLED so bucket order can only come from the
 * dependency edges, never from input order.
 */
function threeGroupJobs(): Job[] {
  return [
    // Shuffled on purpose: day-2 group first.
    J({
      id: `${DEP}:day-2-mutations`, jobName: 'day-2-mutations',
      displayName: 'Day-2 Mutations', type: 'group', kind: 'group',
      dependsOn: [`${DEP}:bootstrap-kit`],
      childIds: [`${DEP}:mutation-add-node`, `${DEP}:reconciler-pdm`],
    }),
    J({
      id: `${DEP}:mutation-add-node`, jobName: 'mutation-add-node',
      kind: 'mutation', parentId: `${DEP}:day-2-mutations`, status: 'pending',
    }),
    J({
      id: `${DEP}:reconciler-pdm`, jobName: 'reconciler-pdm',
      kind: 'reconciler', parentId: `${DEP}:day-2-mutations`, status: 'degraded',
    }),
    J({
      id: `${DEP}:bootstrap-kit`, jobName: 'bootstrap-kit',
      displayName: 'Bootstrap', type: 'group', kind: 'group',
      dependsOn: [`${DEP}:provisioner`],
      childIds: [
        `${DEP}:install-cilium`, `${DEP}:install-keycloak`,
        `${DEP}:reconcile-flux-system`, `${DEP}:cutover-step-01-gitea`,
      ],
    }),
    J({
      id: `${DEP}:install-cilium`, jobName: 'install-cilium',
      kind: 'install', appId: 'bp-cilium',
      parentId: `${DEP}:bootstrap-kit`, status: 'succeeded',
    }),
    J({
      id: `${DEP}:install-keycloak`, jobName: 'install-keycloak',
      kind: 'install', appId: 'bp-keycloak',
      parentId: `${DEP}:bootstrap-kit`, status: 'failed',
    }),
    J({
      id: `${DEP}:reconcile-flux-system`, jobName: 'reconcile-flux-system',
      kind: 'reconcile', parentId: `${DEP}:bootstrap-kit`, status: 'running',
    }),
    J({
      id: `${DEP}:cutover-step-01-gitea`, jobName: 'cutover-step-01-gitea',
      kind: 'step', parentId: `${DEP}:bootstrap-kit`, status: 'pending',
    }),
    J({
      id: `${DEP}:provisioner`, jobName: 'provisioner',
      displayName: 'Provisioner', type: 'group', kind: 'group',
      childIds: [`${DEP}:lifecycle-tofu-apply`, `${DEP}:lifecycle-tofu-init`],
    }),
    J({
      id: `${DEP}:lifecycle-tofu-init`, jobName: 'lifecycle-tofu-init',
      kind: 'lifecycle', parentId: `${DEP}:provisioner`, status: 'succeeded',
    }),
    J({
      id: `${DEP}:lifecycle-tofu-apply`, jobName: 'lifecycle-tofu-apply',
      kind: 'lifecycle', parentId: `${DEP}:provisioner`, status: 'succeeded',
    }),
    // Ungrouped recurring leaf — must fall back to its kind stage even
    // though the group set is NOT sparse.
    J({
      id: `${DEP}:cron-trivy-scan`, jobName: 'cron-trivy-scan',
      kind: 'cron', status: 'healthy', runCount: 600,
    }),
  ]
}

/** hw220-shape sparse store: ONLY the provisioner group exists. */
function sparseGroupJobs(): Job[] {
  return [
    J({
      id: `${DEP}:provisioner`, jobName: 'provisioner',
      displayName: 'Provisioner', type: 'group', kind: 'group',
      childIds: [
        `${DEP}:lifecycle-tofu-apply`, `${DEP}:install-cilium`,
        `${DEP}:reconcile-flux-system`,
      ],
    }),
    J({
      id: `${DEP}:lifecycle-tofu-apply`, jobName: 'lifecycle-tofu-apply',
      kind: 'lifecycle', parentId: `${DEP}:provisioner`, status: 'succeeded',
    }),
    J({
      id: `${DEP}:install-cilium`, jobName: 'install-cilium',
      kind: 'install', appId: 'bp-cilium',
      parentId: `${DEP}:provisioner`, status: 'running',
    }),
    J({
      id: `${DEP}:reconcile-flux-system`, jobName: 'reconcile-flux-system',
      kind: 'reconcile', parentId: `${DEP}:provisioner`, status: 'failing',
    }),
  ]
}

function leafByJobId(items: readonly TreemapItem[], jobId: string): TreemapItem | undefined {
  for (const it of items) {
    if (it.jobId === jobId) return it
    const hit = it.children ? leafByJobId(it.children, jobId) : undefined
    if (hit) return hit
  }
  return undefined
}

/* ── Unit: derivation ─────────────────────────────────────────────── */

describe('buildJobsTreemapData — progress buckets from real group rows', () => {
  it('binds the progress layer to the type=group rows in dependency order', () => {
    const data = buildJobsTreemapData(threeGroupJobs(), PROVISIONING_DEFAULT_LAYERS)
    // Group buckets first (topological over dependsOn — NOT input
    // order, which deliberately leads with day-2), then the kind-stage
    // fallback bucket for the ungrouped cron leaf.
    expect(data.items.map((i) => i.name)).toEqual([
      'Provisioner',
      'Bootstrap',
      'Day-2 Mutations',
      'Recurring',
    ])
    // total_count = leaf jobs only (groups are buckets, not items).
    expect(data.total_count).toBe(9)
    // Bucket ids are the REAL group job ids so drill state stays stable
    // across the 5s polling rebuilds.
    expect(data.items[0]?.id).toBe(`${DEP}:provisioner`)
  })

  it('nests kind buckets under progress (its types under the second layer)', () => {
    const data = buildJobsTreemapData(threeGroupJobs(), PROVISIONING_DEFAULT_LAYERS)
    const kit = data.items.find((i) => i.name === 'Bootstrap')
    // Bootstrap has install + reconcile + step leaves → 3 kind buckets
    // in stage order.
    expect(kit?.children?.map((c) => c.name)).toEqual(['Install', 'Reconcile', 'Step'])
    const installs = kit?.children?.find((c) => c.name === 'Install')
    expect(installs?.children).toHaveLength(2)
    // Leaves carry the JobDetail discriminator + uniform size.
    const keycloak = installs?.children?.find((c) => c.jobId === `${DEP}:install-keycloak`)
    expect(keycloak?.size_value).toBe(1)
    expect(keycloak?.name).toBe('install-keycloak')
  })

  it('supports the kind dimension standalone (layers=[kind]) in stage order', () => {
    const data = buildJobsTreemapData(threeGroupJobs(), ['kind'])
    const names = data.items.map((i) => i.name)
    // Stage order: Lifecycle < Install < Reconcile < Mutation < Step <
    // Cron < Reconciler (only the kinds present appear).
    expect(names).toEqual([
      'Lifecycle', 'Install', 'Reconcile', 'Mutation', 'Step', 'Cron', 'Reconciler',
    ])
    expect(KIND_STAGES.lifecycle.order).toBeLessThan(KIND_STAGES.install.order)
  })

  it('maps every JobStatus to its semantic colour kind, incl. the HEALTH axis', () => {
    const statuses: Array<[JobStatus, string]> = [
      ['succeeded', 'success'],
      ['running', 'in-progress'],
      ['failed', 'failed'],
      ['pending', 'pending'],
      // HEALTH axis (#3646 §4c): running-forever-and-correct is green,
      // degraded amber, failing red — never grey.
      ['healthy', 'success'],
      ['degraded', 'warning'],
      ['failing', 'failed'],
    ]
    const jobs = statuses.map(([s], i) =>
      J({ id: `${DEP}:install-x${i}`, jobName: `install-x${i}`, kind: 'install', status: s }),
    )
    const data = buildJobsTreemapData(jobs, ['kind'])
    for (const [i, [s, expected]] of statuses.entries()) {
      const leaf = leafByJobId(data.items, `${DEP}:install-x${i}`)
      expect(leaf?.statusKind, `${s} → ${expected}`).toBe(expected)
      // The raw status word is preserved for the tooltip/sub-label.
      expect(leaf?.statusLabel).toBe(s)
    }
  })

  it('rolls leaf kinds up onto buckets (failed > warning > in-progress > success)', () => {
    const data = buildJobsTreemapData(threeGroupJobs(), PROVISIONING_DEFAULT_LAYERS)
    const byName = new Map(data.items.map((i) => [i.name, i]))
    // Bootstrap: succeeded + failed + running + pending → failed wins.
    expect(byName.get('Bootstrap')?.statusKind).toBe('failed')
    // Provisioner: all succeeded → success.
    expect(byName.get('Provisioner')?.statusKind).toBe('success')
    // Day-2: pending + degraded → warning wins.
    expect(byName.get('Day-2 Mutations')?.statusKind).toBe('warning')
    // Recurring: healthy → success.
    expect(byName.get('Recurring')?.statusKind).toBe('success')
  })

  it('falls back to kind-derived lifecycle stages on a sparse group set (hw220 shape)', () => {
    const data = buildJobsTreemapData(sparseGroupJobs(), PROVISIONING_DEFAULT_LAYERS)
    // ONE group only → sparse → the progress buckets are the
    // kind-derived stages, in dependency order, NOT 'Provisioner'.
    expect(data.items.map((i) => i.name)).toEqual([
      'Infrastructure', 'Installs', 'Reconciles',
    ])
    expect(data.items.map((i) => i.name)).not.toContain('Provisioner')
    // The failing reconcile still surfaces red through the fallback.
    expect(data.items.find((i) => i.name === 'Reconciles')?.statusKind).toBe('failed')
  })
})

describe('jobTileHref — JobsTable-identical JobDetail link convention', () => {
  // jsdom runs in catalyst-zero (mothership) mode.
  it('builds the deployment-scoped path with the bare job name', () => {
    expect(jobTileHref(`${DEP}:install-keycloak`, DEP)).toBe(
      `/provision/${DEP}/jobs/install-keycloak`,
    )
  })
  it('keeps the multi-region "<region>:" part of the bare name (encoded)', () => {
    expect(jobTileHref(`${DEP}:nbg1-1:install-x`, DEP)).toBe(
      `/provision/${DEP}/jobs/nbg1-1%3Ainstall-x`,
    )
  })
  it('falls back to /deployments when the deployment id is missing (#4704 Task B)', () => {
    expect(jobTileHref('install-cilium', '')).toBe('/deployments')
  })
})

/* ── Render ───────────────────────────────────────────────────────── */

function stubFetch(jobs: Job[], status: string) {
  globalThis.fetch = ((url: string) => {
    const u = String(url)
    if (u.includes('/jobs')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ jobs }),
      } as unknown as Response)
    }
    if (u.includes('/events')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ events: [], state: { status }, done: true }),
      } as unknown as Response)
    }
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({}),
    } as unknown as Response)
  }) as typeof fetch
}

function renderDashboard() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => <Dashboard disableStream />,
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const decomRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <div data-testid="decom-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([dashRoute, jobDetailRoute, decomRoute]),
    history: createMemoryHistory({
      initialEntries: [`/provision/${DEP}/dashboard`],
    }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const utils = render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
  return { ...utils, router }
}

describe('Dashboard — job-sourced Progress treemap (converging defaults)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    stubFetch(threeGroupJobs(), 'phase1-watching')
    // jsdom lays out nothing: force a measurable surface width so
    // SquarifiedSurface clears its `width > 0` gate and mounts cells
    // (same harness as the Dashboard drill suite).
    Object.defineProperty(HTMLDivElement.prototype, 'clientWidth', {
      configurable: true,
      get() {
        return 800
      },
    })
    class SyncResizeObserver {
      private cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
      }
      observe() {
        this.cb([], this as unknown as ResizeObserver)
      }
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof SyncResizeObserver }).ResizeObserver =
      SyncResizeObserver
  })

  it('renders the /jobs tree as treemap cells with status colours + pulse + legend', async () => {
    const { container } = renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    // Job-sourced cells mounted from the REAL fetch (no override seam).
    await waitFor(() => {
      expect(
        container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`),
      ).toBeTruthy()
    })
    const failed = container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`)!
    const running = container.querySelector(`g[data-job-id="${DEP}:reconcile-flux-system"]`)!
    const healthy = container.querySelector(`g[data-job-id="${DEP}:cron-trivy-scan"]`)!
    expect(failed.getAttribute('data-status-kind')).toBe('failed')
    expect(running.getAttribute('data-status-kind')).toBe('in-progress')
    // HEALTH axis: healthy renders green (success), never grey.
    expect(healthy.getAttribute('data-status-kind')).toBe('success')
    // Running cells pulse — visibly distinct from pending/success.
    expect(running.querySelector('rect.dash-cell-pulse')).toBeTruthy()
    expect(failed.querySelector('rect.dash-cell-pulse')).toBeNull()
    // Categorical legend replaces the gradient bar in status mode.
    expect(screen.getByTestId('dashboard-legend-status-success')).toBeTruthy()
    expect(screen.getByTestId('dashboard-legend-status-failed')).toBeTruthy()
    expect(screen.getByTestId('dashboard-legend-status-in-progress')).toBeTruthy()
  })

  it('job-leaf click navigates to that job\'s JobDetail with the CORRECT deployment id', async () => {
    const { container, router } = renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    await waitFor(() => {
      expect(
        container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`),
      ).toBeTruthy()
    })
    const leafG = container.querySelector(
      `g[data-job-id="${DEP}:install-keycloak"]`,
    ) as SVGGElement
    expect((leafG.getAttribute('style') ?? '').includes('pointer')).toBe(true)
    leafG.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-target')).toBeTruthy()
    })
    expect(router.state.location.pathname).toBe(
      `/provision/${DEP}/jobs/install-keycloak`,
    )
  })
})
