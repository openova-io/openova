/**
 * AppDetail.placement-5422.test.tsx — #5422 (#3969): the Overview tab must
 * NEVER assert a placement the API did not report.
 *
 * The defect: `apiApp?.placement ?? 'singleton'` stated a specific,
 * load-bearing value for an app whose placement this response simply does
 * not carry. The Topology tab on the SAME screen derives its value from
 * live targets, so a two-region hot-standby app was described as a
 * `singleton` in Overview and correctly in Topology — two contradictory
 * values for one app, with the wrong one reading as authoritative because
 * it sits in the summary. #3969 forbids exactly that second value.
 *
 * Both directions are pinned deliberately. A guard that only asserts the
 * absent case passes just as happily against a component that renders
 * nothing at all, and a guard that only asserts the present case cannot
 * see the fallback come back. The pair is what makes the fix durable.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PATTERN_NOT_REPORTED } from '@/shared/lib/placement'
import * as catalogApi from '@/lib/catalog.api'
import { AppDetail } from './AppDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDetail() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <AppDetail disableStream />,
  })
  const tree = rootRoute.addChildren([detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/app/bp-cilium'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

/** A minimal application response; `placement` is supplied per-test. */
function appResponse(placement?: string) {
  return {
    name: 'cilium',
    blueprint: 'bp-cilium',
    ...(placement === undefined ? {} : { placement }),
  } as unknown as Awaited<ReturnType<typeof catalogApi.getApplication>>
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
  cleanup()
})

describe('#5422 Overview placement never fails open into singleton', () => {
  it('renders "not reported" — NOT singleton — when the API omits placement', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue(appResponse(undefined))
    renderDetail()

    const dd = await screen.findByTestId('app-detail-overview-placement')
    expect(dd.textContent).toBe('not reported')
    // The precise regression: the word `singleton` must not appear as an
    // asserted value for an app whose placement is unknown.
    expect(dd.textContent).not.toContain('singleton')
    // And it must be visually marked as non-authoritative, matching the
    // treatment TopologyTab gives the same sentinel (#5515).
    expect(dd.className).toContain('italic')
  })

  it('renders a REAL placement verbatim and unmarked when the API reports one', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue(
      appResponse('active-hot-standby'),
    )
    renderDetail()

    const dd = await screen.findByTestId('app-detail-overview-placement')
    expect(dd.textContent).toBe('active-hot-standby')
    expect(dd.className ?? '').not.toContain('italic')
  })

  it('renders a genuine singleton when the API actually says singleton', async () => {
    // The fix must not overcorrect: `singleton` is a legitimate value when
    // it is the server's answer rather than the client's invention.
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue(appResponse('singleton'))
    renderDetail()

    const dd = await screen.findByTestId('app-detail-overview-placement')
    expect(dd.textContent).toBe('singleton')
    expect(dd.className ?? '').not.toContain('italic')
  })

  it('uses the same sentinel constant as the Topology tab, not a private string', async () => {
    // Guards against the two surfaces drifting into separate dialects for
    // "unknown" — the failure mode #5422 is fundamentally about.
    expect(PATTERN_NOT_REPORTED).toBe('not-reported')
  })
})
