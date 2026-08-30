/**
 * CronScheduleView.test.tsx — the consolidated CronJob Schedule surface
 * (P3-frontend, Refs #6703). Asserts the operator-visible contract:
 *   • one row per CronJob (SVG + detail table);
 *   • a fire mark at the correct x for a known schedule (0 12 * * * → 12:00);
 *   • a collision overlay + summary where ≥2 crons fire on the same minute;
 *   • the run-history drill-in lists the selected CronJob's child Jobs with
 *     their statuses.
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
import { CronScheduleView } from './CronScheduleView'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'

function cronjob(name: string, ns: string, schedule: string): [string, K8sObject] {
  return [
    `cronjob:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'CronJob',
      metadata: { name, namespace: ns },
      spec: { schedule },
      status: {},
    },
  ]
}

function childJob(
  name: string,
  ns: string,
  cronName: string,
  status: Record<string, unknown>,
): [string, K8sObject] {
  return [
    `job:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'Job',
      metadata: {
        name,
        namespace: ns,
        ownerReferences: [{ kind: 'CronJob', name: cronName }],
      },
      status,
    },
  ]
}

function makeSnapshot(): K8sSnapshot {
  return new Map([
    cronjob('noon-report', 'analytics', '0 12 * * *'),
    cronjob('backup-a', 'db', '0 0 * * *'),
    cronjob('backup-b', 'cache', '0 0 * * *'), // collides with backup-a at 00:00
    childJob('noon-report-1', 'analytics', 'noon-report', {
      startTime: '2026-08-23T12:00:00Z',
      completionTime: '2026-08-23T12:00:30Z',
      succeeded: 1,
    }),
    childJob('noon-report-2', 'analytics', 'noon-report', {
      startTime: '2026-08-24T12:00:00Z',
      completionTime: '2026-08-24T12:01:00Z',
      failed: 1,
    }),
  ])
}

const REF = new Date(2026, 7, 24, 6, 0)

function renderSchedule(snapshot: K8sSnapshot) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const scheduleRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/schedule',
    component: () => <CronScheduleView snapshotOverride={snapshot} nowOverride={REF} />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
    validateSearch: (raw: Record<string, unknown>) => raw,
  })
  const tree = rootRoute.addChildren([scheduleRoute, jobsRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs/schedule'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: false,
      status: 404,
      json: () => Promise.resolve({}),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CronScheduleView', () => {
  it('renders one detail-table row per CronJob', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-timeline')
    expect(screen.getByTestId('sov-cron-table-row-cronjob-analytics-noon-report')).toBeTruthy()
    expect(screen.getByTestId('sov-cron-table-row-cronjob-db-backup-a')).toBeTruthy()
    expect(screen.getByTestId('sov-cron-table-row-cronjob-cache-backup-b')).toBeTruthy()
    // Human-readable schedule surfaces.
    expect(screen.getByText('Every day at 12:00')).toBeTruthy()
  })

  it('places a fire mark at the 12:00 x-position for `0 12 * * *`', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-timeline')
    const marks = document.querySelectorAll('[data-testid^="sov-cron-mark-"][data-minute="720"]')
    expect(marks.length).toBe(1)
    // x = LABEL_W(220) + PAD_X(20) + (720/1440) * innerW(700) = 590.
    expect(marks[0].getAttribute('data-x')).toBe('590')
  })

  it('groups a collision where two CronJobs fire on the same minute', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-timeline')
    // backup-a + backup-b both fire at 00:00 → collision at minute 0, count 2.
    const collision = screen.getByTestId('sov-cron-collision-0')
    expect(collision.getAttribute('data-count')).toBe('2')
    // The summary banner reports it.
    expect(screen.getByTestId('sov-cron-collision-summary')).toBeTruthy()
    // Noon fires alone — no collision marker there.
    expect(screen.queryByTestId('sov-cron-collision-720')).toBeNull()
  })

  it('opens the run-history drawer listing the CronJob child Jobs, newest first', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-timeline')
    fireEvent.click(screen.getByTestId('sov-cron-table-row-cronjob-analytics-noon-report'))
    const drawer = await screen.findByTestId('sov-cron-history-drawer')
    expect(drawer).toBeTruthy()
    // Both child runs are listed.
    expect(screen.getByTestId('sov-cron-run-noon-report-1')).toBeTruthy()
    expect(screen.getByTestId('sov-cron-run-noon-report-2')).toBeTruthy()
    // Newest run (noon-report-2, failed) is the first row.
    const rows = screen.getByTestId('sov-cron-history-table').querySelectorAll('tbody tr')
    expect(rows[0].getAttribute('data-testid')).toBe('sov-cron-run-noon-report-2')
    // Closing the drawer removes it.
    fireEvent.click(screen.getByTestId('sov-cron-history-close'))
    expect(screen.queryByTestId('sov-cron-history-drawer')).toBeNull()
  })

  it('renders the empty state when no CronJobs exist', async () => {
    renderSchedule(new Map())
    expect(await screen.findByTestId('sov-cron-schedule-empty')).toBeTruthy()
  })

  it('links back to the jobs cron list', async () => {
    renderSchedule(makeSnapshot())
    const back = await screen.findByTestId('sov-cron-schedule-back')
    expect(back.getAttribute('href')).toContain('/jobs')
  })
})
