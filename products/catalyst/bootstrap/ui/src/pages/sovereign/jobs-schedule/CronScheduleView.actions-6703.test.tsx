/**
 * CronScheduleView.actions-6703.test.tsx — the writes + scheduled-vs-actual
 * on the Schedule surface (epic #6703 P3/P4b).
 *
 * Asserts the operator-visible contract, with the catalyst-api mocked at the
 * fetch boundary so the exact wire payload is pinned:
 *   • "+ New schedule" is the page's primary action; it opens a MODAL (no
 *     inline form), disabled on the empty state (nothing to clone);
 *   • submitting the modal POSTs /sovereigns/{id}/schedules with the chosen
 *     source + name + cron + zone, and a server error stays in the modal;
 *   • Run now is on every row; Edit/Delete only on a console-origin row — a
 *     chart's CronJob changes in Git;
 *   • Delete asks first, then DELETEs /schedules/{ns}/{name};
 *   • the run-history drawer shows scheduled vs actual: slot, lag, the
 *     late flag and the manual badge.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor, within } from '@testing-library/react'
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

function cronjob(
  name: string,
  ns: string,
  schedule: string,
  labels: Record<string, string> = {},
  spec: Record<string, unknown> = {},
): [string, K8sObject] {
  return [
    `cronjob:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'CronJob',
      metadata: { name, namespace: ns, uid: `uid-${name}`, labels },
      spec: { schedule, ...spec },
      status: {},
    },
  ]
}

function childJob(
  name: string,
  ns: string,
  cronName: string,
  status: Record<string, unknown>,
  annotations: Record<string, string> = {},
): [string, K8sObject] {
  return [
    `job:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'Job',
      metadata: { name, namespace: ns, annotations, ownerReferences: [{ kind: 'CronJob', name: cronName }] },
      status,
    },
  ]
}

function makeSnapshot(): K8sSnapshot {
  return new Map([
    cronjob('openbao-snapshot-save', 'openbao', '*/10 * * * *', { 'catalyst.openova.io/blueprint': 'bp-openbao' }),
    cronjob(
      'openbao-snapshot-noon',
      'openbao',
      '0 12 * * 1-5',
      {
        'catalyst.openova.io/blueprint': 'bp-openbao',
        'catalyst.openova.io/schedule-origin': 'console',
        'catalyst.openova.io/schedule-source': 'openbao-snapshot-save',
      },
      { timeZone: 'Asia/Muscat' },
    ),
    // A late run: slot 18:30, started 02:33 next day (the hw307 syft shape).
    childJob(
      'openbao-snapshot-save-29894070',
      'openbao',
      'openbao-snapshot-save',
      { startTime: '2026-11-03T02:33:04Z', completionTime: '2026-11-03T02:33:27Z', succeeded: 1 },
      { 'batch.kubernetes.io/cronjob-scheduled-timestamp': '2026-11-02T18:30:00Z' },
    ),
    // A manual run (Run now).
    childJob(
      'openbao-snapshot-save-manual-1762358400',
      'openbao',
      'openbao-snapshot-save',
      { startTime: '2026-11-05T16:00:00Z', completionTime: '2026-11-05T16:00:20Z', succeeded: 1 },
      { 'cronjob.kubernetes.io/instantiate': 'manual' },
    ),
  ])
}

const REF = new Date(2026, 10, 5, 12, 0)

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
  const router = createRouter({
    routeTree: rootRoute.addChildren([scheduleRoute, jobsRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs/schedule'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

type FetchCall = { url: string; init?: RequestInit }
let calls: FetchCall[]
let respond: (url: string, init?: RequestInit) => { status: number; body: unknown }

beforeEach(() => {
  calls = []
  respond = () => ({ status: 404, body: {} })
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    calls.push({ url, init })
    const r = respond(url, init)
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      json: () => Promise.resolve(r.body),
      text: () => Promise.resolve(JSON.stringify(r.body)),
    } as unknown as Response)
  }) as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const scheduleCalls = () => calls.filter((c) => c.url.includes('/schedules'))

describe('CronScheduleView — New schedule (P4b)', () => {
  it('the primary action opens a modal, not an inline form; disabled on the empty state', async () => {
    renderSchedule(new Map())
    const btn = (await screen.findByTestId('sov-cron-new-schedule')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('sov-cron-schedule-empty').textContent).toMatch(/No CronJobs scheduled/)
    cleanup()

    renderSchedule(makeSnapshot())
    const enabled = (await screen.findByTestId('sov-cron-new-schedule')) as HTMLButtonElement
    expect(enabled.disabled).toBe(false)
    // No form on the page before the click.
    expect(screen.queryByTestId('sov-cron-schedule-form')).toBeNull()
    fireEvent.click(enabled)
    const dialog = await screen.findByTestId('sov-cron-schedule-dialog')
    expect(dialog.getAttribute('role')).toBe('dialog')
    expect(within(dialog).getByTestId('sov-cron-schedule-form')).toBeTruthy()
    // Both CronJobs are offered as a source.
    const select = within(dialog).getByTestId('sov-cron-schedule-form-source') as HTMLSelectElement
    expect(select.options.length).toBe(2)
  })

  it('submitting POSTs the exact create payload and closes on success', async () => {
    respond = (url, init) =>
      url.endsWith('/schedules') && init?.method === 'POST'
        ? {
            status: 201,
            body: { namespace: 'openbao', name: 'snap-noon', schedule: '0 12 * * 1-5', timeZone: 'Asia/Muscat', suspend: false, origin: 'console', sourceName: 'openbao-snapshot-save' },
          }
        : { status: 404, body: {} }
    renderSchedule(makeSnapshot())
    fireEvent.click(await screen.findByTestId('sov-cron-new-schedule'))
    const dialog = await screen.findByTestId('sov-cron-schedule-dialog')
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-source'), {
      target: { value: 'cronjob:openbao/openbao-snapshot-save' },
    })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-name'), { target: { value: 'snap-noon' } })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-schedule'), { target: { value: '0 12 * * 1-5' } })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-timezone'), { target: { value: 'Asia/Muscat' } })
    // The live preview reads the expression back.
    expect(dialog.textContent).toMatch(/Monday–Friday at 12:00/)
    fireEvent.click(within(dialog).getByTestId('sov-cron-schedule-form-submit'))

    await waitFor(() => expect(scheduleCalls().length).toBe(1))
    const [call] = scheduleCalls()
    expect(call.url).toMatch(/\/v1\/sovereigns\/d-1\/schedules$/)
    expect(call.init?.method).toBe('POST')
    expect(JSON.parse(String(call.init?.body))).toEqual({
      sourceNamespace: 'openbao',
      sourceName: 'openbao-snapshot-save',
      name: 'snap-noon',
      schedule: '0 12 * * 1-5',
      timeZone: 'Asia/Muscat',
      suspend: false,
    })
    await waitFor(() => expect(screen.queryByTestId('sov-cron-schedule-dialog')).toBeNull())
  })

  it('a client-side invalid form never reaches the API; a server error stays in the modal', async () => {
    respond = (url, init) =>
      url.endsWith('/schedules') && init?.method === 'POST'
        ? { status: 409, body: { error: 'schedule-exists', detail: 'cronjobs.batch "snap-noon" already exists' } }
        : { status: 404, body: {} }
    renderSchedule(makeSnapshot())
    fireEvent.click(await screen.findByTestId('sov-cron-new-schedule'))
    const dialog = await screen.findByTestId('sov-cron-schedule-dialog')
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-name'), { target: { value: 'Bad Name' } })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-schedule'), { target: { value: '0 25 * * *' } })
    fireEvent.click(within(dialog).getByTestId('sov-cron-schedule-form-submit'))
    expect(scheduleCalls().length).toBe(0)
    expect(dialog.textContent).toMatch(/Lowercase letters/)
    expect(dialog.textContent).toMatch(/Not a valid 5-field/)

    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-name'), { target: { value: 'snap-noon' } })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-schedule'), { target: { value: '0 12 * * *' } })
    fireEvent.click(within(dialog).getByTestId('sov-cron-schedule-form-submit'))
    const err = await within(dialog).findByTestId('sov-cron-schedule-form-error')
    expect(err.textContent).toMatch(/schedule-exists/)
    // Still open — never a green close on a failed call.
    expect(screen.getByTestId('sov-cron-schedule-dialog')).toBeTruthy()
  })
})

describe('CronScheduleView — row actions (P4b)', () => {
  it('Run now on every row; Edit/Delete only on a console-origin row', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-table')
    const chartRow = screen.getByTestId('sov-cron-table-row-cronjob-openbao-openbao-snapshot-save')
    const consoleRow = screen.getByTestId('sov-cron-table-row-cronjob-openbao-openbao-snapshot-noon')
    // Run now = the canonical /jobs retry on the CronJob's own row id.
    expect(within(chartRow).getByTestId('jobs-retry-d-1:cron-openbao-snapshot-save')).toBeTruthy()
    expect(within(consoleRow).getByTestId('jobs-retry-d-1:cron-openbao-snapshot-noon')).toBeTruthy()
    expect(within(chartRow).queryByTestId('sov-cron-edit-cronjob-openbao-openbao-snapshot-save')).toBeNull()
    expect(within(chartRow).queryByTestId('sov-cron-delete-cronjob-openbao-openbao-snapshot-save')).toBeNull()
    expect(within(consoleRow).getByTestId('sov-cron-edit-cronjob-openbao-openbao-snapshot-noon')).toBeTruthy()
    expect(within(consoleRow).getByTestId('sov-cron-delete-cronjob-openbao-openbao-snapshot-noon')).toBeTruthy()
    // Origin column says which is which; the zone shows on the schedule.
    expect(screen.getByTestId('sov-cron-origin-cronjob-openbao-openbao-snapshot-save').getAttribute('data-origin')).toBe('chart')
    expect(screen.getByTestId('sov-cron-origin-cronjob-openbao-openbao-snapshot-noon').getAttribute('data-origin')).toBe('console')
    expect(screen.getByTestId('sov-cron-tz-cronjob-openbao-openbao-snapshot-noon').textContent).toMatch(/Asia\/Muscat/)
    // Clicking an action does NOT open the drawer underneath.
    fireEvent.click(within(consoleRow).getByTestId('sov-cron-edit-cronjob-openbao-openbao-snapshot-noon'))
    expect(screen.queryByTestId('sov-cron-history-drawer')).toBeNull()
    const dialog = await screen.findByTestId('sov-cron-schedule-dialog')
    expect(dialog.textContent).toMatch(/Edit schedule — openbao-snapshot-noon/)
    // Edit mode: no source picker / name, the current schedule + zone prefilled.
    expect(within(dialog).queryByTestId('sov-cron-schedule-form-name')).toBeNull()
    expect((within(dialog).getByTestId('sov-cron-schedule-form-schedule') as HTMLInputElement).value).toBe('0 12 * * 1-5')
    expect((within(dialog).getByTestId('sov-cron-schedule-form-timezone') as HTMLInputElement).value).toBe('Asia/Muscat')
  })

  it('Edit PATCHes schedule/suspend/zone; Delete asks first, then DELETEs', async () => {
    respond = (_url, init) => {
      if (init?.method === 'PATCH')
        return { status: 200, body: { namespace: 'openbao', name: 'openbao-snapshot-noon', schedule: '0 13 * * *', suspend: true, origin: 'console' } }
      if (init?.method === 'DELETE')
        return { status: 200, body: { deleted: true, namespace: 'openbao', name: 'openbao-snapshot-noon', schedule: '0 13 * * *' } }
      return { status: 404, body: {} }
    }
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-table')
    fireEvent.click(screen.getByTestId('sov-cron-edit-cronjob-openbao-openbao-snapshot-noon'))
    const dialog = await screen.findByTestId('sov-cron-schedule-dialog')
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-schedule'), { target: { value: '0 13 * * *' } })
    fireEvent.change(within(dialog).getByTestId('sov-cron-schedule-form-timezone'), { target: { value: '' } })
    fireEvent.click(within(dialog).getByTestId('sov-cron-schedule-form-suspend'))
    fireEvent.click(within(dialog).getByTestId('sov-cron-schedule-form-submit'))
    await waitFor(() => expect(scheduleCalls().length).toBe(1))
    expect(scheduleCalls()[0].url).toMatch(/\/schedules\/openbao\/openbao-snapshot-noon$/)
    expect(scheduleCalls()[0].init?.method).toBe('PATCH')
    expect(JSON.parse(String(scheduleCalls()[0].init?.body))).toEqual({ schedule: '0 13 * * *', suspend: true, clearTimeZone: true })
    await waitFor(() => expect(screen.queryByTestId('sov-cron-schedule-dialog')).toBeNull())

    fireEvent.click(screen.getByTestId('sov-cron-delete-cronjob-openbao-openbao-snapshot-noon'))
    const confirm = await screen.findByTestId('sov-cron-delete-dialog')
    expect(confirm.textContent).toMatch(/Delete schedule openbao-snapshot-noon\?/)
    // Nothing deleted until confirmed.
    expect(scheduleCalls().filter((c) => c.init?.method === 'DELETE').length).toBe(0)
    fireEvent.click(screen.getByTestId('sov-cron-delete-cancel'))
    await waitFor(() => expect(screen.queryByTestId('sov-cron-delete-dialog')).toBeNull())
    expect(scheduleCalls().filter((c) => c.init?.method === 'DELETE').length).toBe(0)

    fireEvent.click(screen.getByTestId('sov-cron-delete-cronjob-openbao-openbao-snapshot-noon'))
    fireEvent.click(await screen.findByTestId('sov-cron-delete-confirm'))
    await waitFor(() => expect(scheduleCalls().filter((c) => c.init?.method === 'DELETE').length).toBe(1))
    expect(scheduleCalls().find((c) => c.init?.method === 'DELETE')?.url).toMatch(/\/schedules\/openbao\/openbao-snapshot-noon$/)
    await waitFor(() => expect(screen.queryByTestId('sov-cron-delete-dialog')).toBeNull())
  })
})

describe('CronScheduleView — scheduled vs actual (P3)', () => {
  it('the drawer shows slot, lag, the late flag and the manual badge; the table flags a late latest run', async () => {
    renderSchedule(makeSnapshot())
    await screen.findByTestId('sov-cron-schedule-table')
    // The latest run of openbao-snapshot-save is the manual one (newest) —
    // no late flag on the row for a manual run.
    expect(screen.queryByTestId('sov-cron-late-cronjob-openbao-openbao-snapshot-save')).toBeNull()
    fireEvent.click(screen.getByTestId('sov-cron-table-row-cronjob-openbao-openbao-snapshot-save'))
    const drawer = await screen.findByTestId('sov-cron-history-drawer')
    const adherence = within(drawer).getByTestId('sov-cron-adherence')
    expect(adherence.getAttribute('data-late')).toBe('1')
    expect(adherence.getAttribute('data-manual')).toBe('1')
    expect(adherence.textContent).toMatch(/1 of 1 scheduled runs started late/)
    expect(adherence.textContent).toMatch(/\+8h 03m/)
    expect(within(drawer).getByTestId('sov-cron-run-lag-openbao-snapshot-save-29894070').textContent).toBe('+8h 03m')
    expect(within(drawer).getByTestId('sov-cron-run-manual-openbao-snapshot-save-manual-1762358400')).toBeTruthy()
    expect(within(drawer).getByTestId('sov-cron-run-lag-openbao-snapshot-save-manual-1762358400').textContent).toBe('—')
  })

  it('a CronJob whose LATEST run fired late carries the late flag on its row', async () => {
    const snap: K8sSnapshot = new Map([
      cronjob('syft', 'syft-grype', '30 2 * * *'),
      childJob(
        'syft-1',
        'syft-grype',
        'syft',
        { startTime: '2026-11-03T02:33:04Z', succeeded: 1 },
        { 'batch.kubernetes.io/cronjob-scheduled-timestamp': '2026-11-02T18:30:00Z' },
      ),
    ])
    renderSchedule(snap)
    await screen.findByTestId('sov-cron-schedule-table')
    expect(screen.getByTestId('sov-cron-late-cronjob-syft-grype-syft').textContent).toMatch(/late \+8h 03m/)
  })
})
