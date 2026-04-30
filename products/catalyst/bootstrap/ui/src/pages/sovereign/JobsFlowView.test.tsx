/**
 * JobsFlowView.test.tsx — component-level tests for the Flow tab on
 * the Jobs page (urgent founder spec).
 *
 * Coverage:
 *   • Renders without error on empty data.
 *   • Renders 4 stages for the canonical 5-job fan-in example.
 *   • Click batch header toggles collapsed state (job nodes
 *     disappear, supernode glyph appears).
 *   • Click job card calls navigate with the correct
 *     /provision/$id/jobs/$jobId target.
 *   • Default-collapse policy: all-succeeded batches collapsed by
 *     default; in-flight batches expanded.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobsFlowView } from './JobsFlowView'
import type { Job } from '@/lib/jobs.types'

function renderFlow(jobs: Job[], deploymentId = 'd-1') {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsFlowView jobs={jobs} deploymentId={deploymentId} />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const tree = rootRoute.addChildren([flowRoute, detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}/jobs`] }),
  })
  return { ...render(<RouterProvider router={router} />), router }
}

const mkJob = (id: string, deps: string[], batchId: string, status: Job['status'] = 'pending'): Job => ({
  id,
  jobName: id,
  appId: id,
  batchId,
  dependsOn: deps,
  status,
  startedAt: null,
  finishedAt: null,
  durationMs: 0,
})

const FIVE_JOB: Job[] = [
  mkJob('1', [], 'B'),
  mkJob('2', ['1'], 'B'),
  mkJob('3', ['1'], 'B'),
  mkJob('4', ['3'], 'B'),
  mkJob('5', ['2', '4'], 'B'),
]

beforeEach(() => {
  // Quiet unhandled console error from jsdom not implementing
  // foreignObject layout — the component renders fine, the SVG just
  // can't compute child boxes in jsdom.
})

afterEach(() => cleanup())

describe('JobsFlowView — empty state', () => {
  it('renders an empty placeholder when jobs is empty', async () => {
    renderFlow([])
    expect(await screen.findByTestId('jobs-flow-empty')).toBeTruthy()
  })

  it('does NOT render the SVG canvas when there is no data', async () => {
    renderFlow([])
    await screen.findByTestId('jobs-flow-empty')
    expect(screen.queryByTestId('jobs-flow-svg')).toBeNull()
  })
})

describe('JobsFlowView — canonical 5-job fan-in', () => {
  it('renders the SVG canvas', async () => {
    renderFlow(FIVE_JOB)
    expect(await screen.findByTestId('jobs-flow-svg')).toBeTruthy()
  })

  it('renders one batch swimlane', async () => {
    renderFlow(FIVE_JOB)
    expect(await screen.findByTestId('flow-batch-B')).toBeTruthy()
  })

  it('renders 5 job cards', async () => {
    renderFlow(FIVE_JOB)
    await screen.findByTestId('jobs-flow-svg')
    for (const id of ['1', '2', '3', '4', '5']) {
      expect(screen.getByTestId(`flow-job-${id}`)).toBeTruthy()
    }
  })

  it('renders 5 within-batch edges', async () => {
    renderFlow(FIVE_JOB)
    await screen.findByTestId('jobs-flow-svg')
    const edges = document.querySelectorAll('[data-testid^="flow-edge-"]')
    expect(edges.length).toBe(5)
  })

  it('jobs are positioned across 4 distinct stages', async () => {
    renderFlow(FIVE_JOB)
    await screen.findByTestId('jobs-flow-svg')
    const stages = new Set<string>()
    for (const id of ['1', '2', '3', '4', '5']) {
      // The job node's <rect> sits at x = stage * COLUMN_WIDTH +
      // batch padding; we read the x attribute to derive the stage.
      const node = document.querySelector(`[data-testid="flow-job-${id}"] rect`)
      const x = node?.getAttribute('x') ?? ''
      stages.add(x)
    }
    expect(stages.size).toBe(4)
  })
})

describe('JobsFlowView — batch collapse toggle', () => {
  it('default-collapses an all-succeeded batch and expands an in-flight one', async () => {
    const jobs: Job[] = [
      // all succeeded — should collapse
      mkJob('a1', [], 'A', 'succeeded'),
      mkJob('a2', ['a1'], 'A', 'succeeded'),
      // in flight — should stay expanded
      mkJob('b1', ['a2'], 'B', 'running'),
      mkJob('b2', ['b1'], 'B', 'pending'),
    ]
    renderFlow(jobs)
    await screen.findByTestId('jobs-flow-svg')
    // A's job nodes are NOT rendered (collapsed → supernode only).
    expect(screen.queryByTestId('flow-job-a1')).toBeNull()
    expect(screen.queryByTestId('flow-job-a2')).toBeNull()
    expect(screen.getByTestId('flow-batch-supernode-A')).toBeTruthy()
    // B's job nodes ARE rendered.
    expect(screen.getByTestId('flow-job-b1')).toBeTruthy()
    expect(screen.getByTestId('flow-job-b2')).toBeTruthy()
  })

  it('clicking the batch toggle flips collapsed state in place', async () => {
    const jobs: Job[] = [
      mkJob('b1', [], 'B', 'running'),
      mkJob('b2', ['b1'], 'B', 'pending'),
    ]
    renderFlow(jobs)
    await screen.findByTestId('jobs-flow-svg')
    // B starts expanded — both job cards visible.
    expect(screen.getByTestId('flow-job-b1')).toBeTruthy()
    expect(screen.getByTestId('flow-job-b2')).toBeTruthy()
    // Click toggle.
    fireEvent.click(screen.getByTestId('flow-batch-toggle-B'))
    // Now collapsed.
    expect(screen.queryByTestId('flow-job-b1')).toBeNull()
    expect(screen.queryByTestId('flow-job-b2')).toBeNull()
    expect(screen.getByTestId('flow-batch-supernode-B')).toBeTruthy()
    // Click again — back to expanded.
    fireEvent.click(screen.getByTestId('flow-batch-toggle-B'))
    expect(screen.getByTestId('flow-job-b1')).toBeTruthy()
    expect(screen.queryByTestId('flow-batch-supernode-B')).toBeNull()
  })
})

describe('JobsFlowView — click navigation', () => {
  it('clicking a job card navigates to /provision/$id/jobs/$jobId', async () => {
    const { router } = renderFlow(FIVE_JOB, 'd-42')
    await screen.findByTestId('jobs-flow-svg')
    fireEvent.click(screen.getByTestId('flow-job-3'))
    // After click, the router pathname should match the JobDetail route.
    expect(router.state.location.pathname).toBe('/provision/d-42/jobs/3')
  })
})

describe('JobsFlowView — edge classification', () => {
  it('cross-batch edge is rendered when both batches are expanded', async () => {
    const jobs: Job[] = [
      mkJob('a', [], 'A', 'running'),
      mkJob('b', ['a'], 'B', 'pending'),
    ]
    renderFlow(jobs)
    await screen.findByTestId('jobs-flow-svg')
    const edges = document.querySelectorAll('[data-testid^="flow-edge-"]')
    expect(edges.length).toBeGreaterThan(0)
    const cross = Array.from(edges).filter(
      (e) => e.getAttribute('data-kind') === 'cross-batch-job',
    )
    expect(cross.length).toBe(1)
  })

  it('meta edge is rendered when source batch is collapsed', async () => {
    const jobs: Job[] = [
      mkJob('a', [], 'A', 'succeeded'),
      mkJob('b', ['a'], 'B', 'pending'),
    ]
    renderFlow(jobs)
    await screen.findByTestId('jobs-flow-svg')
    const edges = document.querySelectorAll('[data-testid^="flow-edge-"]')
    const meta = Array.from(edges).filter(
      (e) => e.getAttribute('data-kind') === 'meta',
    )
    expect(meta.length).toBe(1)
  })

  it('meta edge from a failed batch carries data-blocked=true', async () => {
    const jobs: Job[] = [
      mkJob('a', [], 'A', 'failed'),
      mkJob('b', ['a'], 'B', 'pending'),
    ]
    renderFlow(jobs)
    await screen.findByTestId('jobs-flow-svg')
    const edges = document.querySelectorAll('[data-testid^="flow-edge-"]')
    const blocked = Array.from(edges).filter(
      (e) => e.getAttribute('data-blocked') === 'true',
    )
    expect(blocked.length).toBeGreaterThan(0)
  })
})
