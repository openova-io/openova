/**
 * JobsGraphView.test.tsx — P2 grouped-DAG lock-in (Refs #6703).
 *
 * Asserts the Graph view:
 *   • renders ONE JobDependenciesGraph per present group row (section),
 *     titled by the group's displayName, with the member count;
 *   • renders a "Standalone tasks" section for parentless leaves;
 *   • orders sections by the fixed preference (provisioner → apps →
 *     standalone), tolerating whichever groups are present;
 *   • keeps the `data-testid="jobs-graph-view"` wrapper (existing contract);
 *   • HIGHLIGHTS a kind by dimming every NON-matching node/edge (opacity),
 *     never removing nodes — and dims nothing when highlightKind is null;
 *   • renders a graceful empty state for zero jobs;
 *   • preserves node-click navigation.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobsGraphView } from './JobsGraphView'
import type { Job, JobKind, JobStatus } from '@/lib/jobs.types'

/* ── Fixture builders ─────────────────────────────────────────────── */

function group(id: string, jobName: string, displayName: string, childIds: string[]): Job {
  return {
    id,
    jobName,
    displayName,
    type: 'group',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds,
    status: 'running',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}

function leaf(
  id: string,
  jobName: string,
  kind: JobKind,
  parentId: string,
  dependsOn: string[] = [],
  status: JobStatus = 'succeeded',
): Job {
  return {
    id,
    jobName,
    kind,
    type: 'install',
    appId: jobName,
    parentId,
    dependsOn,
    childIds: [],
    status,
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}

/**
 * Two canonical groups (out of preferred order in the array on purpose, to
 * prove the view re-sorts) + a standalone leaf.
 *   • apps      (rank 30): two `install` leaves chained i1 → i2.
 *   • provisioner (rank 10): two `step` leaves chained s1 → s2.
 *   • standalone: one `task` leaf with no parent.
 */
const APPS_GROUP = group('apps', 'apps', 'Apps', ['i1', 'i2'])
const PROV_GROUP = group('provisioner', 'provisioner', 'Provision Hetzner', ['s1', 's2'])
const FIXTURE: Job[] = [
  APPS_GROUP,
  leaf('i1', 'install-cilium', 'install', 'apps'),
  leaf('i2', 'install-cnpg', 'install', 'apps', ['i1']),
  PROV_GROUP,
  leaf('s1', 'provision-step-plan', 'step', 'provisioner'),
  leaf('s2', 'provision-step-apply', 'step', 'provisioner', ['s1']),
  leaf('t1', 'orphan-task', 'task', ''),
]

function renderGraph(jobs: Job[], props: Partial<Parameters<typeof JobsGraphView>[0]> = {}) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsGraphView jobs={jobs} {...props} />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const tree = rootRoute.addChildren([jobsRoute, detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs'] }),
  })
  return render(<RouterProvider router={router} />)
}

afterEach(() => cleanup())

describe('JobsGraphView — grouped sections', () => {
  it('keeps the jobs-graph-view wrapper', async () => {
    renderGraph(FIXTURE)
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
  })

  it('renders one section per present group + a standalone section', async () => {
    renderGraph(FIXTURE)
    await screen.findByTestId('jobs-graph-view')
    expect(screen.getByTestId('jobs-graph-section-apps')).toBeTruthy()
    expect(screen.getByTestId('jobs-graph-section-provisioner')).toBeTruthy()
    expect(screen.getByTestId('jobs-graph-section-standalone')).toBeTruthy()
  })

  it('titles each section by the group displayName + member count', async () => {
    renderGraph(FIXTURE)
    await screen.findByTestId('jobs-graph-view')
    const apps = screen.getByTestId('jobs-graph-section-apps')
    expect(apps.textContent).toContain('Apps')
    expect(screen.getByTestId('jobs-graph-section-apps-count').textContent).toBe('2')
    const prov = screen.getByTestId('jobs-graph-section-provisioner')
    expect(prov.textContent).toContain('Provision Hetzner')
    expect(screen.getByTestId('jobs-graph-section-standalone-count').textContent).toBe('1')
  })

  it('renders one JobDependenciesGraph per section (member nodes only, no group node)', async () => {
    renderGraph(FIXTURE)
    await screen.findByTestId('jobs-graph-view')
    // Member leaves render as nodes…
    expect(screen.getByTestId('jobs-deps-node-i1')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-node-s1')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-node-t1')).toBeTruthy()
    // …the group ROWS never become nodes.
    expect(screen.queryByTestId('jobs-deps-node-apps')).toBeNull()
    expect(screen.queryByTestId('jobs-deps-node-provisioner')).toBeNull()
    // Intra-section chain edges survive (i1 → i2, s1 → s2).
    expect(screen.getByTestId('jobs-deps-edge-i1-i2')).toBeTruthy()
    expect(screen.getByTestId('jobs-deps-edge-s1-s2')).toBeTruthy()
  })

  it('orders sections provisioner → apps → standalone regardless of input order', async () => {
    renderGraph(FIXTURE)
    await screen.findByTestId('jobs-graph-view')
    const testids = Array.from(
      document.querySelectorAll('[data-testid^="jobs-graph-section-"]'),
    )
      .map((el) => el.getAttribute('data-testid'))
      // Drop the per-section count nodes; keep only the section wrappers.
      .filter((id): id is string => !!id && !id.endsWith('-count'))
    expect(testids).toEqual([
      'jobs-graph-section-provisioner',
      'jobs-graph-section-apps',
      'jobs-graph-section-standalone',
    ])
  })
})

describe('JobsGraphView — chip highlight (dim, not filter)', () => {
  it('dims nothing when highlightKind is null', async () => {
    renderGraph(FIXTURE, { highlightKind: null })
    await screen.findByTestId('jobs-graph-view')
    expect(document.querySelectorAll('[data-dimmed="true"]').length).toBe(0)
    // With no highlight the widget emits no dim attribute at all.
    expect(document.querySelectorAll('[data-dimmed]').length).toBe(0)
  })

  it('highlighting `install` dims every non-install node but keeps the install nodes bright', async () => {
    renderGraph(FIXTURE, { highlightKind: 'install' })
    await screen.findByTestId('jobs-graph-view')
    // install leaves stay bright…
    expect(screen.getByTestId('jobs-deps-node-i1').getAttribute('data-dimmed')).toBe('false')
    expect(screen.getByTestId('jobs-deps-node-i2').getAttribute('data-dimmed')).toBe('false')
    // …step + task leaves dim.
    expect(screen.getByTestId('jobs-deps-node-s1').getAttribute('data-dimmed')).toBe('true')
    expect(screen.getByTestId('jobs-deps-node-s2').getAttribute('data-dimmed')).toBe('true')
    expect(screen.getByTestId('jobs-deps-node-t1').getAttribute('data-dimmed')).toBe('true')
    // The provisioner chain edge dims (both endpoints are non-install)…
    expect(screen.getByTestId('jobs-deps-edge-s1-s2').getAttribute('data-dimmed')).toBe('true')
    // …the install chain edge stays bright.
    expect(screen.getByTestId('jobs-deps-edge-i1-i2').getAttribute('data-dimmed')).toBe('false')
    // Node set is NEVER reduced by highlighting — dimmed nodes still render.
    expect(screen.getByTestId('jobs-deps-node-s1')).toBeTruthy()
  })
})

describe('JobsGraphView — edge cases', () => {
  it('renders a graceful empty state for zero jobs', async () => {
    renderGraph([])
    expect(await screen.findByTestId('jobs-graph-view')).toBeTruthy()
    expect(screen.getByTestId('jobs-graph-empty')).toBeTruthy()
  })

  it('a group with no children renders no section', async () => {
    const emptyGroup = group('reconcilers', 'reconcilers', 'Reconcilers', [])
    renderGraph([emptyGroup, leaf('t1', 'orphan', 'task', '')])
    await screen.findByTestId('jobs-graph-view')
    expect(screen.queryByTestId('jobs-graph-section-reconcilers')).toBeNull()
    expect(screen.getByTestId('jobs-graph-section-standalone')).toBeTruthy()
  })

  it('preserves node-click navigation to the per-job detail route', async () => {
    const nav = vi.fn()
    // Spy indirectly: clicking navigates the memory router; assert the detail
    // route mounts.
    renderGraph(FIXTURE)
    await screen.findByTestId('jobs-graph-view')
    fireEvent.click(screen.getByTestId('jobs-deps-node-i1'))
    expect(await screen.findByTestId('job-detail-target')).toBeTruthy()
    // (nav kept to document intent — the router mount is the real assertion.)
    void nav
  })
})
