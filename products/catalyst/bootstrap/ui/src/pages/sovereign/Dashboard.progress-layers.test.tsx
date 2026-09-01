/**
 * Dashboard.progress-layers.test.tsx — #4731 Progress + Kind as
 * first-class layers of the EXISTING editable treemap, AMENDED per the
 * founder's four escalation complaints (2026-07-05).
 *
 * The four complaints, each with the test that proves it dies:
 *
 *   1. "where are all the other items" (full inventory) —
 *      `buildJobsTreemapData` surfaces EVERY component as a leaf,
 *      including the HelmRelease installs the finite /jobs view dropped
 *      (the treemap now reads ?inventory=full). See
 *      `renders the full platform inventory as leaves`.
 *   2. "second layer is not kind by default" — the CONVERGING default is
 *      [progress, kind]. (#6695, founder 2026-08-26: this now applies only
 *      while status != ready; a CONVERGED env flips to the real
 *      resource/health map [namespace, application] + health, because the
 *      "install, install, install…" job stack was meaningless on a
 *      converged Sovereign. See `converging … defaults to [progress, kind]`
 *      and `#6695 — converged (status=ready) flips … resource/health map`.)
 *   3. "cutover kinds still showing as pending" — the dormant cutover
 *      steps collapse into ONE `cutover (dormant)` leaf in a Dormant
 *      bucket, coloured dormant-grey, never pending. See
 *      `collapses the dormant cutover steps ...` +
 *      `pending non-cutover work stays in Pending, dormant is separate`.
 *   4. relevance on both moments — the progress layer buckets by STATE
 *      (Running/Pending/Done/Degraded/Failed/Dormant), orthogonal to
 *      kind, so both a converging and a converged env read meaningfully.
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

import { Dashboard } from './Dashboard'
import {
  buildJobsTreemapData,
  jobTileHref,
  progressStateForKind,
  PROVISIONING_DEFAULT_LAYERS,
  PROGRESS_STATES,
  CUTOVER_DORMANT_LEAF_NAME,
  KIND_STAGES,
} from './dashboardJobsTreemap'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import type { Job, JobStatus } from '@/lib/jobs.types'
import type { TreemapItem } from '@/lib/treemap.types'
import type { ApplicationDescriptor } from './applicationCatalog'

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
 * Full-inventory fixture (post ?inventory=full) in miniature: the four
 * top groups + install / reconcile / reconciler / cron / lifecycle leaves
 * + a DORMANT cutover (3 steps, all pending). Mirrors the hw224 converged
 * shape: mostly Done, one running reconcile, and the parked cutover tether.
 * Deliberately SHUFFLED so bucket order can only come from state/stage
 * order, never input order.
 */
function fullInventoryJobs(): Job[] {
  return [
    J({ id: `${DEP}:cutover`, jobName: 'cutover', displayName: 'Cutover', type: 'group', kind: 'group' }),
    J({ id: `${DEP}:cutover-step-02-harbor`, jobName: 'cutover-step-02-harbor', kind: 'step', parentId: `${DEP}:cutover`, status: 'pending' }),
    J({ id: `${DEP}:cutover-step-01-gitea`, jobName: 'cutover-step-01-gitea', kind: 'step', parentId: `${DEP}:cutover`, status: 'pending' }),
    J({ id: `${DEP}:cutover-step-03-flux`, jobName: 'cutover-step-03-flux', kind: 'step', parentId: `${DEP}:cutover`, status: 'pending' }),
    J({ id: `${DEP}:bootstrap-kit`, jobName: 'bootstrap-kit', displayName: 'Bootstrap', type: 'group', kind: 'group' }),
    J({ id: `${DEP}:install-cilium`, jobName: 'install-cilium', kind: 'install', appId: 'bp-cilium', parentId: `${DEP}:bootstrap-kit`, status: 'succeeded' }),
    J({ id: `${DEP}:install-keycloak`, jobName: 'install-keycloak', kind: 'install', appId: 'bp-keycloak', parentId: `${DEP}:bootstrap-kit`, status: 'succeeded' }),
    J({ id: `${DEP}:install-gitea`, jobName: 'install-gitea', kind: 'install', appId: 'bp-gitea', parentId: `${DEP}:bootstrap-kit`, status: 'succeeded' }),
    J({ id: `${DEP}:reconcilers`, jobName: 'reconcilers', displayName: 'Reconcilers', type: 'group', kind: 'group' }),
    J({ id: `${DEP}:reconcile-flux`, jobName: 'reconcile-flux-system', kind: 'reconcile', parentId: `${DEP}:reconcilers`, status: 'running' }),
    J({ id: `${DEP}:reconciler-pdm`, jobName: 'reconciler-pool-domain-manager', kind: 'reconciler', parentId: `${DEP}:reconcilers`, status: 'healthy' }),
    J({ id: `${DEP}:cron-trivy`, jobName: 'cron-trivy-scan', kind: 'cron', parentId: `${DEP}:reconcilers`, status: 'healthy', runCount: 600 }),
    J({ id: `${DEP}:provisioner`, jobName: 'provisioner', displayName: 'Provisioner', type: 'group', kind: 'group' }),
    J({ id: `${DEP}:tofu-apply`, jobName: 'lifecycle-tofu-apply', kind: 'lifecycle', parentId: `${DEP}:provisioner`, status: 'succeeded' }),
  ]
}

/** Minimal ApplicationDescriptor for the expected-catalog merge tests. */
function App(bareId: string): ApplicationDescriptor {
  return {
    id: `bp-${bareId}`,
    bareId,
    title: bareId,
    description: '',
    familyId: 'platform',
    familyName: 'Platform',
    tier: 'mandatory',
    logoUrl: null,
    dependencies: [],
    bootstrapKit: true,
  }
}

function leafByJobId(items: readonly TreemapItem[], jobId: string): TreemapItem | undefined {
  for (const it of items) {
    if (it.jobId === jobId) return it
    const hit = it.children ? leafByJobId(it.children, jobId) : undefined
    if (hit) return hit
  }
  return undefined
}

function bucketByName(items: readonly TreemapItem[], name: string): TreemapItem | undefined {
  return items.find((i) => i.name === name)
}

function allLeaves(items: readonly TreemapItem[]): TreemapItem[] {
  const out: TreemapItem[] = []
  for (const it of items) {
    if (it.children && it.children.length) out.push(...allLeaves(it.children))
    else out.push(it)
  }
  return out
}

/* ── Unit: progress = STATE, kind = KIND ──────────────────────────── */

describe('buildJobsTreemapData — progress buckets by STATE (#4731 amend)', () => {
  it('defaults are [progress, kind] and progress buckets are the STATES', () => {
    expect(PROVISIONING_DEFAULT_LAYERS).toEqual(['progress', 'kind'])
    const data = buildJobsTreemapData(fullInventoryJobs(), PROVISIONING_DEFAULT_LAYERS)
    // Only non-empty state buckets, in state order: Running < Done < Dormant.
    expect(data.items.map((i) => i.name)).toEqual(['Running', 'Done', 'Dormant'])
    // total_count = the TRUE inventory size (every underlying leaf incl. the
    // 3 collapsed cutover steps) — 10 leaves total (no applications catalog
    // passed here, so no upfront-seeded pending leaves).
    expect(data.total_count).toBe(10)
  })

  it('progressStateForKind maps every status kind onto its state bucket', () => {
    expect(progressStateForKind('in-progress')).toBe('running')
    expect(progressStateForKind('success')).toBe('done')
    expect(progressStateForKind('warning')).toBe('degraded')
    expect(progressStateForKind('failed')).toBe('failed')
    expect(progressStateForKind('pending')).toBe('pending')
    expect(progressStateForKind('dormant')).toBe('dormant')
  })

  it('complaint #1 — renders the full platform inventory as leaves (installs are NOT dropped)', () => {
    const data = buildJobsTreemapData(fullInventoryJobs(), PROVISIONING_DEFAULT_LAYERS)
    // Every HelmRelease install surfaces as a leaf — the exact rows the
    // finite /jobs view dropped and the founder said were "missing".
    for (const id of [`${DEP}:install-cilium`, `${DEP}:install-keycloak`, `${DEP}:install-gitea`]) {
      expect(leafByJobId(data.items, id), id).toBeTruthy()
    }
    // The Done bucket's kind sub-buckets carry the installs + the healthy
    // reconciler + cron + lifecycle, in stage order.
    const done = bucketByName(data.items, 'Done')
    // #6695/re-cut — kind buckets now carry REAL engine names.
    expect(done?.children?.map((c) => c.name)).toEqual(['OpenTofu', 'HelmRelease', 'CronJob', 'Deployment'])
    const installs = done?.children?.find((c) => c.name === 'HelmRelease')
    expect(installs?.children).toHaveLength(3)
  })

  it('complaint #4 — running reconcile lands in Running, colours by status', () => {
    const data = buildJobsTreemapData(fullInventoryJobs(), PROVISIONING_DEFAULT_LAYERS)
    const running = bucketByName(data.items, 'Running')
    expect(running?.statusKind).toBe('in-progress')
    expect(leafByJobId(running?.children ?? [], `${DEP}:reconcile-flux`)?.statusKind).toBe('in-progress')
    // Done bucket rolls up green.
    expect(bucketByName(data.items, 'Done')?.statusKind).toBe('success')
  })

  it('supports the kind dimension standalone (layers=[kind]) in stage order', () => {
    const data = buildJobsTreemapData(fullInventoryJobs(), ['kind'])
    // Only present kinds appear, in stage order. The 3 dormant cutover
    // steps collapse to ONE Step leaf.
    expect(data.items.map((i) => i.name)).toEqual([
      'OpenTofu', 'HelmRelease', 'Kustomization', 'Job (step)', 'CronJob', 'Deployment',
    ])
    expect(KIND_STAGES.lifecycle.order).toBeLessThan(KIND_STAGES.install.order)
  })

  it('treemap tiles lead with the COMPONENT, not the "Install" verb (#leaf-name)', () => {
    // The narrow tiles truncate; every "Install <x>" collapsed to "Instal…"
    // and the HelmReleases were indistinguishable (founder 2026-08-30). The
    // tile now shows the humanized component, acronym-aware.
    const jobs = [
      J({ id: `${DEP}:install-keycloak`, jobName: 'install-keycloak', kind: 'install', appId: 'bp-keycloak', displayName: 'Install Keycloak', status: 'succeeded' }),
      J({ id: `${DEP}:install-shared-pg`, jobName: 'install-shared-pg', kind: 'install', appId: 'bp-shared-pg', displayName: 'Install Shared Pg', status: 'succeeded' }),
      J({ id: `${DEP}:reconcile-flux`, jobName: 'reconcile-flux-system', kind: 'reconcile', appId: 'flux-system', displayName: 'Reconcile Flux System', status: 'running' }),
    ]
    const data = buildJobsTreemapData(jobs, ['kind'])
    expect(leafByJobId(data.items, `${DEP}:install-keycloak`)?.name).toBe('Keycloak')
    expect(leafByJobId(data.items, `${DEP}:install-shared-pg`)?.name).toBe('Shared PG') // acronym
    expect(leafByJobId(data.items, `${DEP}:reconcile-flux`)?.name).toBe('Flux System')
  })

  it('maps every JobStatus to its semantic colour kind, incl. the HEALTH axis', () => {
    const statuses: Array<[JobStatus, string]> = [
      ['succeeded', 'success'],
      ['running', 'in-progress'],
      ['failed', 'failed'],
      ['pending', 'pending'],
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
      expect(leaf?.statusLabel).toBe(s)
    }
  })
})

/* ── Unit: dormant cutover collapse (complaint #3) ────────────────── */

describe('buildJobsTreemapData — dormant cutover collapse (#4731 complaint #3)', () => {
  it('collapses the dormant cutover steps into ONE dormant leaf, never pending', () => {
    const data = buildJobsTreemapData(fullInventoryJobs(), PROVISIONING_DEFAULT_LAYERS)
    // There is NO Pending bucket — the 3 pending cutover steps did NOT
    // pollute it (the founder's exact complaint).
    expect(bucketByName(data.items, 'Pending')).toBeUndefined()
    // The Dormant bucket holds exactly ONE aggregate leaf, coloured dormant.
    const dormant = bucketByName(data.items, 'Dormant')
    expect(dormant?.statusKind).toBe('dormant')
    const dormantLeaves = allLeaves(dormant?.children ?? [])
    expect(dormantLeaves).toHaveLength(1)
    expect(dormantLeaves[0]?.name).toBe(CUTOVER_DORMANT_LEAF_NAME)
    expect(dormantLeaves[0]?.statusKind).toBe('dormant')
    // The aggregate carries the collapsed step count (3) + deep-links to
    // the Cutover group so a click opens the per-step detail.
    expect(dormantLeaves[0]?.count).toBe(3)
    expect(dormantLeaves[0]?.jobId).toBe(`${DEP}:cutover`)
    // No individual cutover-step leaf leaked anywhere.
    expect(leafByJobId(data.items, `${DEP}:cutover-step-01-gitea`)).toBeUndefined()
  })

  it('pending NON-cutover work stays in Pending; dormant cutover is separate', () => {
    const jobs = [
      ...fullInventoryJobs(),
      // A genuinely queued (pending) install — belongs in Pending.
      J({ id: `${DEP}:install-openbao`, jobName: 'install-openbao', kind: 'install', appId: 'bp-openbao', parentId: `${DEP}:bootstrap-kit`, status: 'pending' }),
    ]
    const data = buildJobsTreemapData(jobs, PROVISIONING_DEFAULT_LAYERS)
    // Pending bucket now exists (the openbao install) and holds it.
    const pending = bucketByName(data.items, 'Pending')
    expect(pending?.statusKind).toBe('pending')
    expect(leafByJobId(pending?.children ?? [], `${DEP}:install-openbao`)).toBeTruthy()
    // Dormant is still its own separate bucket — pending and dormant never
    // share a bucket.
    expect(bucketByName(data.items, 'Dormant')?.statusKind).toBe('dormant')
    expect(leafByJobId(pending?.children ?? [], `${DEP}:cutover`)).toBeUndefined()
  })

  it('once cutover FIRES (any step non-pending) the steps expand as live leaves', () => {
    const jobs = fullInventoryJobs().map((j) =>
      j.jobName === 'cutover-step-01-gitea' ? { ...j, status: 'succeeded' as JobStatus } : j,
    )
    const data = buildJobsTreemapData(jobs, PROVISIONING_DEFAULT_LAYERS)
    // The dormant aggregate is gone; individual steps render again.
    expect(leafByJobId(data.items, `${DEP}:cutover`)).toBeUndefined()
    expect(allLeaves(data.items).some((l) => l.name === CUTOVER_DORMANT_LEAF_NAME)).toBe(false)
    expect(leafByJobId(data.items, `${DEP}:cutover-step-01-gitea`)?.statusKind).toBe('success')
    // The two still-pending steps land in Pending (real queued work now).
    expect(leafByJobId(bucketByName(data.items, 'Pending')?.children ?? [], `${DEP}:cutover-step-02-harbor`)).toBeTruthy()
  })

  it('PROGRESS_STATES colours dormant grey and orders it last', () => {
    expect(PROGRESS_STATES.dormant.statusKind).toBe('dormant')
    expect(PROGRESS_STATES.dormant.order).toBeGreaterThan(PROGRESS_STATES.done.order)
    expect(PROGRESS_STATES.running.order).toBeLessThan(PROGRESS_STATES.pending.order)
  })
})

/* ── Unit: a SUSPENDED (dormant) HelmRelease leaf, not just cutover ── */

describe('buildJobsTreemapData — suspended HelmRelease renders Dormant, not Pending', () => {
  // The backend bridges stamp a suspended (spec.suspend=true) HelmRelease
  // install leaf with status `dormant` (it reports installed, which would
  // otherwise map to Succeeded/green). This is DISTINCT from the cutover-step
  // collapse: a lone suspended HR leaf carries status `dormant` directly and
  // must land in the Dormant bucket — never Pending (the old mislabel) and
  // never Done.
  it('a suspended install HR leaf lands in Dormant, never Pending or Done', () => {
    const jobs = [
      ...fullInventoryJobs(),
      // A suspended (parked) HelmRelease, e.g. bp-self-sovereign-cutover
      // installed-but-dormant, or an operator-suspended app.
      J({
        id: `${DEP}:install-self-sovereign-cutover`,
        jobName: 'install-self-sovereign-cutover',
        kind: 'install',
        appId: 'bp-self-sovereign-cutover',
        parentId: `${DEP}:bootstrap-kit`,
        status: 'dormant' as JobStatus,
      }),
    ]
    const data = buildJobsTreemapData(jobs, PROVISIONING_DEFAULT_LAYERS)

    // The leaf is in the Dormant bucket…
    const dormant = bucketByName(data.items, 'Dormant')
    expect(dormant?.statusKind).toBe('dormant')
    const leaf = leafByJobId(dormant?.children ?? [], `${DEP}:install-self-sovereign-cutover`)
    expect(leaf).toBeTruthy()
    expect(leaf?.statusKind).toBe('dormant')

    // …and NOT in Pending (the old mislabel) nor Done.
    expect(leafByJobId(bucketByName(data.items, 'Pending')?.children ?? [], `${DEP}:install-self-sovereign-cutover`)).toBeUndefined()
    expect(leafByJobId(bucketByName(data.items, 'Done')?.children ?? [], `${DEP}:install-self-sovereign-cutover`)).toBeUndefined()
  })
})

/* ── Upfront full expected inventory (founder: "see all at once") ──── */

describe('buildJobsTreemapData — upfront expected inventory from t=0', () => {
  // The full planned catalog for a tiny prov: 5 bootstrap-kit slots.
  const catalog = [
    App('cilium'),
    App('cert-manager'),
    App('flux'),
    App('keycloak'),
    App('gitea'),
  ]

  it('seeds EVERY expected component as a pending leaf at provisioning start', () => {
    // t=0: only the provisioner lifecycle rows exist as jobs — no HR has
    // reconciled yet. The map must STILL show all 5 planned installs.
    const t0Jobs = [
      J({ id: `${DEP}:provisioner`, jobName: 'provisioner', displayName: 'Provisioner', type: 'group', kind: 'group' }),
      J({ id: `${DEP}:tofu-apply`, jobName: 'lifecycle-tofu-apply', kind: 'lifecycle', parentId: `${DEP}:provisioner`, status: 'running' }),
    ]
    const data = buildJobsTreemapData(t0Jobs, PROVISIONING_DEFAULT_LAYERS, catalog)
    // Full expected inventory = 5 planned installs (pending) + 1 lifecycle
    // (running). total_count counts every planned component from the start.
    expect(data.total_count).toBe(6)
    const pendingInstalls = bucketByName(data.items, 'Pending')?.children?.find((c) => c.name === 'HelmRelease')
    expect(pendingInstalls?.children).toHaveLength(5)
    // Every planned install leaf is pending (queued), NOT missing.
    for (const c of pendingInstalls?.children ?? []) {
      expect(c.statusKind).toBe('pending')
    }
  })

  it('the live job WINS over the planned pending leaf as convergence progresses', () => {
    // cilium install has started + succeeded; the other 4 are still planned.
    const jobs = [
      J({ id: `${DEP}:bootstrap-kit`, jobName: 'bootstrap-kit', displayName: 'Bootstrap', type: 'group', kind: 'group' }),
      J({ id: `${DEP}:install-cilium`, jobName: 'install-cilium', kind: 'install', appId: 'cilium', parentId: `${DEP}:bootstrap-kit`, status: 'succeeded' }),
    ]
    const data = buildJobsTreemapData(jobs, PROVISIONING_DEFAULT_LAYERS, catalog)
    // No duplicate cilium: it appears ONCE, in Done (the live job wins).
    expect(leafByJobId(data.items, `${DEP}:install-cilium`)?.statusKind).toBe('success')
    const doneInstalls = bucketByName(data.items, 'Done')?.children?.find((c) => c.name === 'HelmRelease')
    expect(allLeaves(doneInstalls?.children ?? [])).toHaveLength(1)
    // The remaining 4 planned installs are still pending.
    const pendingInstalls = bucketByName(data.items, 'Pending')?.children?.find((c) => c.name === 'HelmRelease')
    expect(pendingInstalls?.children).toHaveLength(4)
    // Total still equals the full expected inventory (5 installs) — never
    // fewer, never doubled.
    expect(data.total_count).toBe(5)
  })
})

/* ── jobTileHref (unchanged convention) ───────────────────────────── */

describe('jobTileHref — JobsTable-identical JobDetail link convention', () => {
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
    // #6695 — a CONVERGED (ready) env defaults to the real resource/health
    // stack, which fetches this endpoint instead of the job tree. Answer it
    // with an empty-but-valid TreemapData so the ready-flip render path does
    // not error (the ready test asserts the CONTROLS, not cell data).
    if (u.includes('/dashboard/treemap')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ items: [], total_count: 0 }),
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

describe('Dashboard — job-sourced Progress treemap render', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    // #6695 — the CONVERGING moment: while status != ready the map defaults
    // to the job-sourced [progress, kind] stack (what is installing). The
    // ready-flip to the real resource/health stack has its own test below.
    stubFetch(fullInventoryJobs(), 'provisioning')
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

  it('converging (status != ready) defaults to the job-sourced [progress, kind] stack', async () => {
    // beforeEach stubs status='provisioning' → the converging moment.
    renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    const layer0 = (await screen.findByTestId('treemap-layer-0-select')) as HTMLSelectElement
    const layer1 = (await screen.findByTestId('treemap-layer-1-select')) as HTMLSelectElement
    expect(layer0.value).toBe('progress')
    expect(layer1.value).toBe('kind')
    // Job-sourced ⇒ Color=Status, Size=Uniform (auto-locked to the job vocab).
    expect((screen.getByTestId('treemap-color-select') as HTMLSelectElement).value).toBe('status')
    expect((screen.getByTestId('treemap-size-select') as HTMLSelectElement).value).toBe('uniform')
  })

  it('#6695 — converged (status=ready) flips the default to the real resource/health map', async () => {
    // The founder reversed the #4731 always-[progress,kind] default: on a
    // CONVERGED Sovereign the "install, install, install…" job stack was
    // meaningless. It now flips to the real resource/health map so down
    // components surface as red tiles.
    stubFetch(fullInventoryJobs(), 'ready')
    renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    const layer0 = (await screen.findByTestId('treemap-layer-0-select')) as HTMLSelectElement
    const layer1 = (await screen.findByTestId('treemap-layer-1-select')) as HTMLSelectElement
    // RESOURCE_DEFAULT_LAYERS = [namespace, application] — real k8s objects,
    // NOT the synthetic [progress, kind] job tree.
    expect(layer0.value).toBe('namespace')
    expect(layer1.value).toBe('application')
    // Resource-sourced on ready ⇒ Color=Health (down components pop red),
    // Size=cpu_request (a down app keeps a visible tile, never area 0).
    expect((screen.getByTestId('treemap-color-select') as HTMLSelectElement).value).toBe('health')
    expect((screen.getByTestId('treemap-size-select') as HTMLSelectElement).value).toBe('cpu_request')
  })

  it('renders the /jobs inventory as status-coloured cells + dormant + legend', async () => {
    const { container } = renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    await waitFor(() => {
      expect(container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`)).toBeTruthy()
    })
    const done = container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`)!
    const running = container.querySelector(`g[data-job-id="${DEP}:reconcile-flux"]`)!
    expect(done.getAttribute('data-status-kind')).toBe('success')
    expect(running.getAttribute('data-status-kind')).toBe('in-progress')
    // The dormant cutover aggregate renders as a dormant cell (dashed).
    const dormant = container.querySelector(`g[data-job-id="${DEP}:cutover"]`)!
    expect(dormant.getAttribute('data-status-kind')).toBe('dormant')
    expect(dormant.querySelector('rect[style*="dasharray"]')).toBeTruthy()
    // Running cells pulse; the categorical legend carries the Dormant swatch.
    expect(running.querySelector('rect.dash-cell-pulse')).toBeTruthy()
    expect(screen.getByTestId('dashboard-legend-status-dormant')).toBeTruthy()
    expect(screen.getByTestId('dashboard-legend-status-success')).toBeTruthy()
  })

  it('job-leaf click navigates to that job\'s JobDetail with the CORRECT deployment id', async () => {
    const { container, router } = renderDashboard()
    await screen.findByTestId('dashboard-treemap-frame')
    await waitFor(() => {
      expect(container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`)).toBeTruthy()
    })
    const leafG = container.querySelector(`g[data-job-id="${DEP}:install-keycloak"]`) as SVGGElement
    expect((leafG.getAttribute('style') ?? '').includes('pointer')).toBe(true)
    leafG.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await waitFor(() => {
      expect(screen.queryByTestId('job-detail-target')).toBeTruthy()
    })
    expect(router.state.location.pathname).toBe(`/provision/${DEP}/jobs/install-keycloak`)
  })
})
