/**
 * JobDetail.test.tsx — lock-in for the v3 JobDetail surface (this PR).
 *
 * Coverage:
 *   • Tab strip has EXACTLY two tabs labeled "Flow" and "Exec Log".
 *   • Default-active tab = Flow.
 *   • Dependencies + Apps tabs are GONE (removed in v3).
 *   • Flow tab renders the embedded FlowPage (data-testid='flow-page-embedded').
 *   • Exec Log tab renders the ExecutionLogs viewer.
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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { JobDetail } from './JobDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDetail(deploymentId: string, jobId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <JobDetail disableStream />,
  })
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, flowRoute, jobsRoute, homeRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs/${jobId}`],
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
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('JobDetail — v3 tab strip', () => {
  it('renders exactly 2 tabs labeled Flow + Exec Log', async () => {
    // Use a known fixture job id — `bp-cilium` lives in the bootstrap-kit
    // batch and is part of the default-application catalog.
    renderDetail('d-1', 'bp-cilium')
    const tablist = await screen.findByTestId('job-detail-tablist')
    const tabs = tablist.querySelectorAll('[role="tab"]')
    expect(tabs.length).toBe(2)
    const labels = Array.from(tabs).map((t) => (t.textContent ?? '').trim())
    expect(labels).toEqual(['Flow', 'Exec Log'])
  })

  it('Flow tab is active by default', async () => {
    renderDetail('d-1', 'bp-cilium')
    const flowTab = await screen.findByTestId('job-detail-tab-flow')
    expect(flowTab.getAttribute('aria-selected')).toBe('true')
  })

  it('does NOT render Dependencies or Apps tabs (v2 retired)', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    expect(screen.queryByTestId('job-detail-tab-dependencies')).toBeNull()
    expect(screen.queryByTestId('job-detail-tab-apps')).toBeNull()
    expect(screen.queryByTestId('job-detail-deps-panel')).toBeNull()
    expect(screen.queryByTestId('job-detail-apps-panel')).toBeNull()
  })

  it('Flow tab panel mounts the embedded FlowPage canvas', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    expect(screen.queryByTestId('job-detail-flow-panel')).toBeTruthy()
    expect(screen.queryByTestId('flow-page-embedded')).toBeTruthy()
  })

  it('clicking Exec Log tab swaps the active panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('job-detail-tablist')
    const logTab = screen.getByTestId('job-detail-tab-logs')
    fireEvent.click(logTab)
    expect(logTab.getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByTestId('job-detail-logs-panel')).toBeTruthy()
    expect(screen.queryByTestId('job-detail-flow-panel')).toBeNull()
  })
})
