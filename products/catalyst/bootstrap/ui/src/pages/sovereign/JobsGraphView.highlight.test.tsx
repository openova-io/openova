/**
 * JobsGraphView.highlight.test.tsx — the graph-view highlight lens (P1b).
 *
 * The /jobs graph chip strip drives a HIGHLIGHT, not a filter: selecting a
 * kind dims every bubble that is NOT that kind (family), never removing a
 * node so dependency edges stay intact. This guard proves the wiring at the
 * JobsGraphView boundary — a `highlightKind` reaches FlowCanvasOrganic as
 * `highlightFamilyId` and stamps `data-dimmed` on the non-matching bubbles.
 *
 * (JobsGraphView / FlowCanvasOrganic / jobsToOrganic are the shipped graph
 * rework — this test only reads their rendered output, it does not change
 * them.)
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup, screen } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createMemoryHistory,
} from '@tanstack/react-router'
import type { Job } from '@/lib/jobs.types'
import { JobsGraphView } from './JobsGraphView'

afterEach(cleanup)

function job(partial: Partial<Job> & { id: string }): Job {
  return {
    jobName: partial.id,
    displayName: partial.id,
    type: 'install',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...partial,
  } as Job
}

/** Two leaves of DIFFERENT kinds → two families (install / lifecycle). */
const JOBS: Job[] = [
  job({ id: 'job-install', kind: 'install' }),
  job({ id: 'job-lifecycle', kind: 'lifecycle' }),
]

function renderGraph(highlightKind: 'install' | 'lifecycle' | null) {
  const rootRoute = createRootRoute({
    component: () => <JobsGraphView jobs={JOBS} highlightKind={highlightKind} />,
  })
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

function nodeByFamily(family: string): Element | null {
  return document.querySelector(`[data-testid^="flow-job-"][data-family="${family}"]`)
}

describe('JobsGraphView — highlightKind dims the non-matching bubbles', () => {
  it('highlightKind=install → the lifecycle bubble is dimmed, the install bubble is not', async () => {
    renderGraph('install')
    // The route renders asynchronously; once the graph is mounted the
    // FlowCanvasOrganic bubbles are present synchronously.
    await screen.findByTestId('jobs-graph-view')
    const installNode = nodeByFamily('install')
    const lifecycleNode = nodeByFamily('lifecycle')
    expect(installNode).toBeTruthy()
    expect(lifecycleNode).toBeTruthy()
    // Matching family → not dimmed; non-matching → dimmed (highlight, not
    // removal — both nodes are still present).
    expect(installNode?.getAttribute('data-dimmed')).toBe('false')
    expect(lifecycleNode?.getAttribute('data-dimmed')).toBe('true')
  })

  it('highlightKind=null → nothing is dimmed (all bubbles full opacity)', async () => {
    renderGraph(null)
    await screen.findByTestId('jobs-graph-view')
    expect(nodeByFamily('install')?.getAttribute('data-dimmed')).toBe('false')
    expect(nodeByFamily('lifecycle')?.getAttribute('data-dimmed')).toBe('false')
  })
})
