/**
 * JobsTable.test.tsx — coverage for the pure helpers + the table
 * surface (issue #204 founder spec).
 *
 * Pure helpers:
 *   • compareJobs — status-priority sort with startedAt-DESC tiebreak
 *     and pending-jumps-to-top semantics (item #10).
 *   • matchJob — search predicate spans jobName / appId / dependsOn /
 *     status / parentId.
 *   • formatDuration — "12s" / "1m 24s" / "2h 5m" rendering.
 *
 * Component:
 *   • Renders the canonical column set.
 *   • Search input filters the visible row count.
 *   • Filter dropdowns narrow the visible row count.
 *   • appIdFilter prop short-circuits to a single appId (used by
 *     AppDetail's Jobs tab — item #8b).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import {
  JobsTable,
  STATUS_PRIORITY,
  compareJobs,
  formatDuration,
  matchJob,
  regionFromJob,
  regionOf,
  regionUnionOfGroup,
} from './JobsTable'
import { FIXTURE_JOBS } from '@/test/fixtures/jobs.fixture'
import type { Job } from '@/lib/jobs.types'

afterEach(() => cleanup())

function renderTable(props: Parameters<typeof JobsTable>[0]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsTable {...props} />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const tree = rootRoute.addChildren([homeRoute, detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs'] }),
  })
  return render(<RouterProvider router={router} />)
}

describe('JobsTable — STATUS_PRIORITY', () => {
  it('orders running > pending > succeeded > failed', () => {
    expect(STATUS_PRIORITY.running).toBeLessThan(STATUS_PRIORITY.pending)
    expect(STATUS_PRIORITY.pending).toBeLessThan(STATUS_PRIORITY.succeeded)
    expect(STATUS_PRIORITY.succeeded).toBeLessThan(STATUS_PRIORITY.failed)
  })
})

describe('JobsTable — compareJobs', () => {
  function makeJob(partial: Partial<Job>): Job {
    return {
      id: partial.id ?? 'j',
      jobName: partial.jobName ?? 'Job',
      appId: partial.appId ?? 'bp-x',
      type: partial.type ?? 'install',
      parentId: partial.parentId ?? 'applications',
      childIds: partial.childIds ?? [],
      dependsOn: partial.dependsOn ?? [],
      status: partial.status ?? 'pending',
      startedAt: partial.startedAt ?? null,
      finishedAt: partial.finishedAt ?? null,
      durationMs: partial.durationMs ?? 0,
    }
  }

  it('running sorts before pending', () => {
    const r = makeJob({ id: 'r', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    const p = makeJob({ id: 'p', status: 'pending' })
    expect(compareJobs(r, p)).toBeLessThan(0)
    expect(compareJobs(p, r)).toBeGreaterThan(0)
  })

  it('pending sorts before succeeded', () => {
    const p = makeJob({ id: 'p', status: 'pending' })
    const s = makeJob({ id: 's', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(p, s)).toBeLessThan(0)
  })

  it('succeeded sorts before failed', () => {
    const s = makeJob({ id: 's', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    const f = makeJob({ id: 'f', status: 'failed', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(s, f)).toBeLessThan(0)
  })

  it('within same status: startedAt DESC (newer first)', () => {
    const newer = makeJob({ id: 'newer', status: 'running', startedAt: '2026-04-29T10:05:00Z' })
    const older = makeJob({ id: 'older', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(newer, older)).toBeLessThan(0)
    expect(compareJobs(older, newer)).toBeGreaterThan(0)
  })

  it('null startedAt sorts after a real startedAt within same status', () => {
    const real = makeJob({ id: 'real', status: 'pending', startedAt: '2026-04-29T10:00:00Z' })
    const empty = makeJob({ id: 'empty', status: 'pending', startedAt: null })
    expect(compareJobs(real, empty)).toBeLessThan(0)
  })

  it('pending jumps to top when its status transitions to running', () => {
    // Founder spec item #10: "pending jobs jump to top when they
    // transition to running". This is the consequence of the
    // status-priority ordering — a pending job that starts running
    // immediately outranks every other non-running job in the table.
    const ranOnce = makeJob({ id: 'ran', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    const wasPending = makeJob({ id: 'pend', status: 'pending', startedAt: null })
    // Simulate the transition.
    const startedRunning = { ...wasPending, status: 'running' as const, startedAt: '2026-04-29T10:10:00Z' }
    // Pre-transition: pending sits BELOW the succeeded-with-realtime job? No —
    // pending (1) outranks succeeded (2), so pending is already higher.
    expect(compareJobs(wasPending, ranOnce)).toBeLessThan(0)
    // Post-transition: running (0) is even higher than pending (1).
    expect(compareJobs(startedRunning, wasPending)).toBeLessThan(0)
    // And running (0) is above succeeded (2) regardless of startedAt.
    expect(compareJobs(startedRunning, ranOnce)).toBeLessThan(0)
  })

  it('id ASC tiebreak when status + startedAt are equal', () => {
    const a = makeJob({ id: 'a', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    const b = makeJob({ id: 'b', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(a, b)).toBeLessThan(0)
    expect(compareJobs(b, a)).toBeGreaterThan(0)
  })
})

describe('JobsTable — matchJob (search filter)', () => {
  const job: Job = {
    id: 'job-1',
    jobName: 'Install Cilium',
    type: 'install',
    appId: 'bp-cilium',
    parentId: 'applications',
    childIds: [],
    dependsOn: ['job-flux-bootstrap'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:45Z',
    durationMs: 45_000,
  }

  it('returns true for empty / whitespace queries', () => {
    expect(matchJob(job, '')).toBe(true)
    expect(matchJob(job, '   ')).toBe(true)
  })

  it('matches case-insensitively across jobName', () => {
    expect(matchJob(job, 'cilium')).toBe(true)
    expect(matchJob(job, 'CILIUM')).toBe(true)
    expect(matchJob(job, 'Install')).toBe(true)
  })

  it('matches across appId', () => {
    expect(matchJob(job, 'bp-cilium')).toBe(true)
  })

  it('matches across parentId', () => {
    expect(matchJob(job, 'applications')).toBe(true)
  })

  it('matches across status', () => {
    expect(matchJob(job, 'succeeded')).toBe(true)
  })

  it('matches across dependsOn entries', () => {
    expect(matchJob(job, 'job-flux-bootstrap')).toBe(true)
    expect(matchJob(job, 'flux')).toBe(true)
  })

  it('returns false when no field matches', () => {
    expect(matchJob(job, 'nothing-matches-this')).toBe(false)
  })

  it('never throws on handover-imported rows that omit wire fields (#3367)', () => {
    // Shape observed live on hw130 after the #3364 jobs import: the Job
    // type promises strings, but imported rows carry only what the
    // export stamped. Cast mirrors the runtime wire shape.
    const imported = {
      id: 'imp-1',
      jobName: 'bp-grafana',
      type: 'install',
      status: 'succeeded',
    } as unknown as Job
    expect(() => matchJob(imported, 'graf')).not.toThrow()
    expect(matchJob(imported, 'graf')).toBe(true)
    expect(matchJob(imported, 'nothing-matches-this')).toBe(false)
    const empty = { id: 'imp-2' } as unknown as Job
    expect(() => matchJob(empty, 'x')).not.toThrow()
    expect(matchJob(empty, 'x')).toBe(false)
  })
})

describe('JobsTable — formatDuration', () => {
  it('renders short durations as Ns', () => {
    expect(formatDuration(12_000)).toBe('12s')
  })

  it('renders mid durations as Mm Ss', () => {
    expect(formatDuration(84_000)).toBe('1m 24s')
  })

  it('renders long durations as Hh Mm', () => {
    expect(formatDuration(7_500_000)).toBe('2h 5m')
  })

  it('renders 0 / negative as em-dash', () => {
    expect(formatDuration(0)).toBe('—')
    expect(formatDuration(-100)).toBe('—')
    expect(formatDuration(NaN)).toBe('—')
  })
})

describe('JobsTable — render', () => {
  it('renders all leaf fixture rows by default (group rows hidden)', async () => {
    renderTable({ jobs: FIXTURE_JOBS })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    const expectedLeafCount = FIXTURE_JOBS.filter((j) => j.type !== 'group').length
    expect(rows.length).toBe(expectedLeafCount)
  })

  it('search input filters the visible row count', async () => {
    renderTable({ jobs: FIXTURE_JOBS })
    await screen.findByTestId('jobs-table')
    const search = screen.getByTestId('jobs-search') as HTMLInputElement
    // Search for a query that exists in exactly one fixture job's
    // jobName/appId/dependsOn — "cert-manager" only appears on the
    // `job-install-cert-manager` row (jobName + appId).
    fireEvent.change(search, { target: { value: 'cert-manager' } })
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-testid')).toContain('cert-manager')
  })

  it('status filter narrows to a single status', async () => {
    renderTable({ jobs: FIXTURE_JOBS })
    await screen.findByTestId('jobs-table')
    const statusFilter = screen.getByTestId('jobs-filter-status') as HTMLSelectElement
    fireEvent.change(statusFilter, { target: { value: 'failed' } })
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-status')).toBe('failed')
  })

  it('appIdFilter prop short-circuits to one appId (AppDetail Jobs tab — item #8b)', async () => {
    renderTable({ jobs: FIXTURE_JOBS, appIdFilter: 'bp-cilium' })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    // Only `job-install-cilium` carries appId='bp-cilium' in the fixture.
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-testid')).toBe('jobs-table-row-job-install-cilium')
    expect(screen.queryByTestId('jobs-filter-app')).toBeNull()
  })

  it('renders all seven canonical columns', async () => {
    renderTable({ jobs: FIXTURE_JOBS })
    await screen.findByTestId('jobs-table')
    const headers = screen
      .getAllByRole('columnheader')
      .map((h) => (h.textContent ?? '').toLowerCase().trim())
    expect(headers).toEqual(['name', 'app', 'deps', 'parent', 'status', 'started', 'duration'])
  })

  it('row link stays scoped under /provision/$deploymentId on the mother surface (prov #59)', async () => {
    renderTable({ jobs: FIXTURE_JOBS })
    await screen.findByTestId('jobs-table')
    const link = screen.getByTestId('jobs-row-link-job-install-cilium') as HTMLAnchorElement
    expect(link.tagName.toLowerCase()).toBe('a')
    // useJobLinkBuilder contract: on the mother's monitoring surface
    // (non-sovereign hostname + $deploymentId in the route — exactly
    // this harness, mounted at /provision/d-1/jobs) every link MUST
    // stay scoped under /provision/$deploymentId; the clean
    // /jobs/$jobId form is reserved for the Sovereign chroot console
    // where the deployment is implicit from the hostname. The previous
    // expectation here (clean root URLs, post-#976) predated the
    // prov #59 chroot-scoping fix (2026-05-13) and failed ever since.
    expect(link.getAttribute('href')).toBe('/provision/d-1/jobs/job-install-cilium')
  })

  // Issue #232 verbatim: "simulates 0 reducer-derived jobs + 5
  // backend-API jobs, expects 5 rows rendered with backend data".
  // The JobsPage merges reducer-derived + live-backfill via mergeJobs()
  // before passing the array to JobsTable; this test exercises the
  // table's render path with the merged input directly.
  it('renders all rows when fed exclusively from a backend-jobs API list (issue #232)', async () => {
    const baseLeaf = {
      type: 'install' as const,
      parentId: 'applications',
      childIds: [],
      dependsOn: [],
    }
    const liveOnly: Job[] = [
      {
        ...baseLeaf,
        id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium',
        status: 'succeeded',
        startedAt: '2026-04-29T10:00:00Z', finishedAt: '2026-04-29T10:01:00Z',
        durationMs: 60_000,
      },
      {
        ...baseLeaf,
        id: 'bp-cert-manager', jobName: 'Install cert-manager', appId: 'bp-cert-manager',
        status: 'succeeded',
        startedAt: '2026-04-29T10:01:00Z', finishedAt: '2026-04-29T10:02:00Z',
        durationMs: 60_000,
      },
      {
        ...baseLeaf,
        id: 'bp-flux', jobName: 'Install Flux', appId: 'bp-flux',
        status: 'succeeded',
        startedAt: '2026-04-29T10:02:00Z', finishedAt: '2026-04-29T10:03:00Z',
        durationMs: 60_000,
      },
      {
        ...baseLeaf,
        id: 'bp-crossplane', jobName: 'Install Crossplane', appId: 'bp-crossplane',
        status: 'running',
        startedAt: '2026-04-29T10:03:00Z', finishedAt: null, durationMs: 0,
      },
      {
        ...baseLeaf,
        id: 'bp-vault', jobName: 'Install Vault', appId: 'bp-vault',
        status: 'pending',
        startedAt: null, finishedAt: null, durationMs: 0,
      },
    ]
    renderTable({ jobs: liveOnly })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(5)
    // Statuses surface verbatim from the backend list (no demotion to pending).
    expect(screen.getByTestId('jobs-cell-status-bp-cilium').textContent?.toLowerCase()).toContain('succeeded')
    expect(screen.getByTestId('jobs-cell-status-bp-crossplane').textContent?.toLowerCase()).toContain('running')
    expect(screen.getByTestId('jobs-cell-status-bp-vault').textContent?.toLowerCase()).toContain('pending')
  })
})

// ── C8-005 (2026-05-17 t143): region filter helpers + dropdown ───────
describe('regionFromJob (C8-005)', () => {
  it('returns empty for primary-region rows (no `:` in appId)', () => {
    expect(regionFromJob({ jobName: 'Install cilium', appId: 'bp-cilium' })).toBe('')
  })

  it('extracts region from a `<region>:<chart>` appId', () => {
    expect(regionFromJob({ jobName: 'Install cilium', appId: 'fsn1:bp-cilium' })).toBe('fsn1')
  })

  it('handles hyphenated region keys', () => {
    expect(regionFromJob({ jobName: 'Install cilium', appId: 'hel1-2:bp-cilium' })).toBe('hel1-2')
  })

  it('falls back to parsing `install-<region>:<chart>` jobName when appId is empty', () => {
    expect(regionFromJob({ jobName: 'install-nbg1-1:bp-flux', appId: '' })).toBe('nbg1-1')
  })

  it('returns empty for group/day-2 rows with no parseable region', () => {
    expect(regionFromJob({ jobName: 'applications', appId: '' })).toBe('')
  })

  it('prefers the first-class region field over the appId prefix (#3276)', () => {
    // region wins even when the appId prefix would parse to something
    // else — the backend-stamped field is the source of truth.
    expect(
      regionFromJob({ jobName: 'install-cilium', appId: 'bp-cilium', region: 'me-east-215-b-1' }),
    ).toBe('me-east-215-b-1')
  })

  it('falls back to the appId prefix when region is absent (#3276)', () => {
    expect(
      regionFromJob({ jobName: 'install-fsn1:bp-cilium', appId: 'fsn1:bp-cilium', region: undefined }),
    ).toBe('fsn1')
  })
})

// ── founder hw126 report: region must be derivable from jobName alone ─
describe('regionOf — jobName-only region derivation (founder hw126)', () => {
  it('extracts the region from a secondary-region install row', () => {
    // The exact shape the founder saw missing a region label on hw126.
    expect(regionOf('install-me-east-215-b-1:harbor')).toBe('me-east-215-b-1')
  })

  it('returns "" (primary) for a plain install row', () => {
    expect(regionOf('install-harbor')).toBe('')
  })

  it('returns "" (primary) for lifecycle rows (provision-*)', () => {
    expect(regionOf('provision-tofu-apply')).toBe('')
    expect(regionOf('provision-network')).toBe('')
  })

  it('never misreads a ":" qualifier in a lifecycle row as a region', () => {
    expect(regionOf('provision-tofu:apply')).toBe('')
  })

  it('handles the bare componentID form <region>:<chart>', () => {
    expect(regionOf('hel1-2:cilium')).toBe('hel1-2')
  })

  it('returns "" for group slugs and empty names', () => {
    expect(regionOf('applications')).toBe('')
    expect(regionOf('day-2-mutations')).toBe('')
    expect(regionOf('')).toBe('')
  })
})

describe('regionUnionOfGroup — parent/group rows surface the child-region union', () => {
  const child = (over: Partial<Job>): Pick<Job, 'jobName' | 'appId' | 'region' | 'parentId' | 'type'> => ({
    jobName: over.jobName ?? 'install-cilium',
    appId: over.appId ?? '',
    region: over.region,
    parentId: over.parentId ?? 'applications',
    type: over.type ?? 'install',
  })

  it('unions distinct child regions, lexically sorted', () => {
    const jobs = [
      child({ jobName: 'install-hel1-2:cilium' }),
      child({ jobName: 'install-fsn1:cilium' }),
      child({ jobName: 'install-fsn1:flux' }), // duplicate region — deduped
      child({ jobName: 'install-cert-manager' }), // primary — excluded from the union
    ]
    expect(regionUnionOfGroup(jobs, 'applications')).toEqual(['fsn1', 'hel1-2'])
  })

  it('returns [] when every child is primary (cell renders "—")', () => {
    const jobs = [
      child({ jobName: 'install-cilium' }),
      child({ jobName: 'install-flux' }),
    ]
    expect(regionUnionOfGroup(jobs, 'applications')).toEqual([])
  })

  it('only counts direct children of the requested group', () => {
    const jobs = [
      child({ jobName: 'install-fsn1:cilium', parentId: 'applications' }),
      child({ jobName: 'install-hel1-2:cilium', parentId: 'bootstrap-kit' }),
      // Nested group rows never contribute themselves.
      child({ jobName: 'nbg1-1:subgroup', parentId: 'applications', type: 'group' }),
    ]
    expect(regionUnionOfGroup(jobs, 'applications')).toEqual(['fsn1'])
  })

  it('prefers the first-class region field on children', () => {
    const jobs = [
      child({ jobName: 'install-harbor', region: 'me-east-215-b-1' }),
    ]
    expect(regionUnionOfGroup(jobs, 'applications')).toEqual(['me-east-215-b-1'])
  })
})

describe('Parent chip hover title carries the group region union (multi-region)', () => {
  const baseLeaf = {
    type: 'install' as const,
    parentId: 'applications',
    childIds: [],
    dependsOn: [],
    status: 'succeeded' as const,
    startedAt: '2026-06-11T10:00:00Z',
    finishedAt: '2026-06-11T10:01:00Z',
    durationMs: 60_000,
  }
  const group: Job = {
    id: 'applications',
    jobName: 'applications',
    displayName: 'Applications',
    type: 'group',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: ['bp-cilium', 'me-east-215-b-1:bp-harbor'],
    status: 'succeeded',
    startedAt: '2026-06-11T10:00:00Z',
    finishedAt: '2026-06-11T10:01:00Z',
    durationMs: 60_000,
  }

  it('appends the union to the Parent chip title on a multi-region set', async () => {
    const jobs: Job[] = [
      group,
      { ...baseLeaf, id: 'bp-cilium', jobName: 'install-cilium', appId: 'bp-cilium' },
      {
        ...baseLeaf,
        id: 'me-east-215-b-1:bp-harbor',
        jobName: 'install-me-east-215-b-1:harbor',
        appId: 'me-east-215-b-1:bp-harbor',
        region: 'me-east-215-b-1',
      },
    ]
    renderTable({ jobs })
    await screen.findByTestId('jobs-table')
    const chip = screen.getByTestId('jobs-cell-parent-bp-cilium')
    expect(chip.getAttribute('title')).toBe('Applications — regions: me-east-215-b-1')
    // The visible chip label stays the plain parent label.
    expect(chip.textContent).toBe('Applications')
  })

  it('keeps the plain parent label as title on a single-region set', async () => {
    const jobs: Job[] = [
      group,
      { ...baseLeaf, id: 'bp-cilium', jobName: 'install-cilium', appId: 'bp-cilium' },
      { ...baseLeaf, id: 'bp-flux', jobName: 'install-flux', appId: 'bp-flux' },
    ]
    renderTable({ jobs })
    await screen.findByTestId('jobs-table')
    const chip = screen.getByTestId('jobs-cell-parent-bp-cilium')
    expect(chip.getAttribute('title')).toBe('Applications')
  })
})

describe('JobsTable region filter (C8-005)', () => {
  const baseLeaf = {
    type: 'install' as const,
    parentId: 'applications',
    childIds: [],
    dependsOn: [],
    status: 'succeeded' as const,
    startedAt: '2026-05-17T10:00:00Z',
    finishedAt: '2026-05-17T10:01:00Z',
    durationMs: 60_000,
  }

  it('hides the region dropdown on single-region deployments', async () => {
    const singleRegion: Job[] = [
      { ...baseLeaf, id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium' },
      { ...baseLeaf, id: 'bp-flux', jobName: 'Install Flux', appId: 'bp-flux' },
    ]
    renderTable({ jobs: singleRegion })
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('jobs-filter-region')).toBeNull()
  })

  it('shows the region dropdown when 2+ regions appear', async () => {
    const multiRegion: Job[] = [
      { ...baseLeaf, id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium' },
      { ...baseLeaf, id: 'fsn1:bp-cilium', jobName: 'install-fsn1:bp-cilium', appId: 'fsn1:bp-cilium' },
      { ...baseLeaf, id: 'hel1-2:bp-cilium', jobName: 'install-hel1-2:bp-cilium', appId: 'hel1-2:bp-cilium' },
    ]
    renderTable({ jobs: multiRegion })
    await screen.findByTestId('jobs-table')
    const sel = screen.getByTestId('jobs-filter-region') as HTMLSelectElement
    expect(sel).toBeTruthy()
    // Options: All + 2 regions (sorted lexically: fsn1, hel1-2)
    const opts = Array.from(sel.querySelectorAll('option')).map((o) => o.textContent)
    expect(opts).toEqual(['All', 'fsn1', 'hel1-2'])
  })

  it('filters rows to the selected region', async () => {
    const multiRegion: Job[] = [
      { ...baseLeaf, id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium' },
      { ...baseLeaf, id: 'fsn1:bp-cilium', jobName: 'install-fsn1:bp-cilium', appId: 'fsn1:bp-cilium' },
      { ...baseLeaf, id: 'hel1-2:bp-cilium', jobName: 'install-hel1-2:bp-cilium', appId: 'hel1-2:bp-cilium' },
    ]
    renderTable({ jobs: multiRegion })
    await screen.findByTestId('jobs-table')
    fireEvent.change(screen.getByTestId('jobs-filter-region'), { target: { value: 'fsn1' } })
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(screen.queryByTestId('jobs-table-row-bp-cilium')).toBeNull()
    expect(screen.queryByTestId('jobs-table-row-hel1-2:bp-cilium')).toBeNull()
  })

  // #3276 — the per-row Region column appears only on a multi-region
  // Sovereign and labels each row with its region (primary rows read
  // "primary"). Uses the first-class region field the backend now stamps.
  it('hides the Region column on a single-region Sovereign', async () => {
    const singleRegion: Job[] = [
      { ...baseLeaf, id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium' },
      { ...baseLeaf, id: 'bp-flux', jobName: 'Install Flux', appId: 'bp-flux' },
    ]
    renderTable({ jobs: singleRegion })
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('jobs-cell-region-bp-cilium')).toBeNull()
    expect(screen.queryByTestId('jobs-cell-region-primary-bp-cilium')).toBeNull()
  })

  it('renders a region-labeled Region column on a multi-region Sovereign (#3276)', async () => {
    const multiRegion: Job[] = [
      { ...baseLeaf, id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium' },
      {
        ...baseLeaf,
        id: 'me-east-215-b-1:bp-cilium',
        jobName: 'install-me-east-215-b-1:bp-cilium',
        appId: 'me-east-215-b-1:bp-cilium',
        region: 'me-east-215-b-1',
      },
    ]
    renderTable({ jobs: multiRegion })
    await screen.findByTestId('jobs-table')
    // Primary row → "primary" label; secondary row → region chip.
    expect(screen.getByTestId('jobs-cell-region-primary-bp-cilium')).toBeTruthy()
    const regionCell = screen.getByTestId('jobs-cell-region-me-east-215-b-1:bp-cilium')
    expect(regionCell.textContent).toContain('me-east-215-b-1')
  })
})
