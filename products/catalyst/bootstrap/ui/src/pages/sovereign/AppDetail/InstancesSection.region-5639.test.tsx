/**
 * InstancesSection.region-5639.test.tsx — #5639 guard on the CREATE door.
 *
 * #5639 is titled "renders an EMPTY region selector". Two operator surfaces
 * can produce a region-less placement, and they behave DIFFERENTLY — so this
 * file pins the create dialog and PlacementEditor.region-5639.test.tsx pins
 * the edit path. Recording both is the point: a walker who checks only one
 * draws the wrong conclusion about the other.
 *
 * WHAT IS TRUE OF THIS DOOR (and was already true pre-fix — these assertions
 * are the REFUTATION, kept so a regression flips them):
 *
 *   • The selector is POPULATED from the live infrastructure topology. Live
 *     confirmation on hw292: `uat-ahs-pg` carries
 *     `spec.placement = {mode: active-hot-standby, regions: [me-east-215-a,
 *     me-east-215-b]}` — the operator picked two real regions here and they
 *     reached the CR. The empty region on that Organization's CNPG Cluster
 *     came from the CHART consuming an unset `topology.primary.region`
 *     (#5641), not from this control.
 *   • An empty region set is ALREADY unsubmittable for a multi-region mode:
 *     `placementValid` requires ≥2 regions (#3599).
 *
 * WHAT WAS WRONG HERE: when the topology reports NO regions at all, the
 * fieldset told the operator
 *
 *     "the server will place the instance in its default region"
 *
 * for EVERY mode — including active-hot-standby, where it is false twice
 * over. There is no server-side default second region, and the Create button
 * is simultaneously disabled by the ≥2 rule. The operator got a reassurance
 * and a dead button and no way to reconcile them. That copy is the same
 * false-default assumption that produced `openova.io/region: ""` downstream,
 * so it is corrected to name the real condition.
 *
 * The singleton case below is the NEGATIVE CONTROL and must stay green: a
 * mode that legitimately needs no region choice has to remain creatable, or
 * the fix is a blanket block.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { InstancesSection } from './InstancesSection'

const SELF = { deploymentId: 'd-1', sovereignFQDN: 'hw292.omani.works' }
const ORG_ROWS = {
  items: [
    {
      org_tenant_id: 't-uatco',
      subdomain: 'uatco',
      company_name: 'UAT Co',
      tenant_namespace: 'uatco',
      state: 'ready',
    },
  ],
}

/** The two regions hw292 actually reports (node label vocabulary elided —
 *  this is the catalyst region id the topology tree serves). */
const TWO_REGIONS = {
  topology: {
    pattern: 'multi-region',
    regions: [
      { id: 'r-a', name: 'me-east-215-a', provider: 'huawei', providerRegion: 'me-east-215-a', clusters: [] },
      { id: 'r-b', name: 'me-east-215-b', provider: 'huawei', providerRegion: 'me-east-215-b', clusters: [] },
    ],
  },
  cloud: [],
  storage: { pvcs: [], buckets: [], volumes: [] },
}

/** The degraded case: the topology endpoint answers, but reports no regions. */
const NO_REGIONS = {
  topology: { pattern: 'solo', regions: [] },
  cloud: [],
  storage: { pvcs: [], buckets: [], volumes: [] },
}

let captured: Array<Record<string, unknown>> = []

function json(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

/**
 * bp-postgres ships `topology.supported: [singleton, active-hot-standby]` —
 * verified cell-for-cell against the live hw292 catalog in #5708. Using the
 * real declared set keeps this test honest about what the operator can pick.
 */
function installFetch(topologyBody: unknown, supported = ['singleton', 'active-hot-standby']) {
  captured = []
  const catalogItem = {
    name: 'postgres',
    version: '0.2.17',
    card: { title: 'postgres' },
    origin: 'upstream',
    source: 'gitea',
    raw: { spec: { multiInstance: { enabled: true }, topology: { supported } } },
  }
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/catalyst/v1/apps/instances') && init?.method === 'POST') {
      captured.push(JSON.parse(String(init.body)))
      return json({ id: 'new-uid', name: 'pg-1', blueprint: 'postgres', org: 'uatco' }, 201)
    }
    if (url.includes('/instances')) return json({ items: [] })
    if (url.includes('/sovereign/self')) return json(SELF)
    if (url.includes('/v1/organizations')) return json(ORG_ROWS)
    if (url.includes('/infrastructure/topology')) return json(topologyBody)
    if (url.includes('/catalog/')) return json(catalogItem)
    return json({})
  }) as typeof fetch
}

async function openDialog() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <InstancesSection blueprint="bp-postgres" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/catalog/bp-postgres'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  fireEvent.click(await screen.findByTestId('btn-new-instance'))
  await screen.findByTestId('dialog-new-instance')
  return (await screen.findByTestId('select-instance-topology')) as HTMLSelectElement
}

const submitBtn = async () =>
  (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('#5639 — the create dialog selector IS populated from the live topology', () => {
  beforeEach(() => installFetch(TWO_REGIONS))

  it('offers every region the Sovereign reports for active-hot-standby', async () => {
    const sel = await openDialog()
    fireEvent.change(sel, { target: { value: 'active-hot-standby' } })
    // The assertion #5639's title predicts would fail. It does not — on this
    // door. Kept so a real derivation regression here is caught.
    expect(await screen.findByTestId('region-checkbox-me-east-215-a')).toBeTruthy()
    expect(await screen.findByTestId('region-checkbox-me-east-215-b')).toBeTruthy()
    expect(screen.queryByTestId('instance-regions-empty')).toBeNull()
  })

  it('active-hot-standby with NO region picked cannot be submitted', async () => {
    const sel = await openDialog()
    fireEvent.change(await screen.findByTestId('input-instance-name'), { target: { value: 'pg-1' } })
    fireEvent.change(await screen.findByTestId('select-instance-org'), { target: { value: 'uatco' } })
    fireEvent.change(sel, { target: { value: 'active-hot-standby' } })

    await waitFor(async () => expect((await submitBtn()).disabled).toBe(true))
    expect(screen.getByTestId('instance-regions-validation').textContent).toContain(
      'at least 2 regions',
    )
    // One region is still not enough — the half-filled case.
    fireEvent.click(await screen.findByTestId('region-checkbox-me-east-215-a'))
    await waitFor(async () => expect((await submitBtn()).disabled).toBe(true))

    // ...and two IS enough. Without this the assertion above is satisfied by
    // a permanently-disabled button.
    fireEvent.click(await screen.findByTestId('region-checkbox-me-east-215-b'))
    await waitFor(async () => expect((await submitBtn()).disabled).toBe(false))

    fireEvent.click(await submitBtn())
    await waitFor(() => expect(captured.length).toBe(1))
    expect(captured[0].placement).toEqual({ regions: ['me-east-215-a', 'me-east-215-b'] })
  })
})

describe('#5639 — a Sovereign reporting NO regions says so honestly', () => {
  beforeEach(() => installFetch(NO_REGIONS))

  it('does NOT promise a server-side default for a multi-region mode', async () => {
    const sel = await openDialog()
    fireEvent.change(sel, { target: { value: 'active-hot-standby' } })

    const note = await screen.findByTestId('instance-regions-empty')
    const text = note.textContent ?? ''
    // Pre-fix this read "the server will place the instance in its default
    // region" for EVERY mode. For active-hot-standby there is no such
    // default — that assumption is exactly what rendered
    // `openova.io/region: ""` on hw292.
    expect(text).not.toContain('default region')
    expect(text.toLowerCase()).toContain('active-hot-standby')
    // And the operator is not left guessing why Create is dead.
    await waitFor(async () => expect((await submitBtn()).disabled).toBe(true))
  })

  it('NEGATIVE CONTROL — singleton with no regions is still creatable', async () => {
    // A mode that genuinely needs no region choice must stay submittable, or
    // the fix above is a blanket block dressed up as validation.
    const sel = await openDialog()
    expect(sel.value).toBe('singleton')
    fireEvent.change(await screen.findByTestId('input-instance-name'), { target: { value: 'pg-1' } })
    fireEvent.change(await screen.findByTestId('select-instance-org'), { target: { value: 'uatco' } })

    // The singleton copy DOES legitimately fall back to the server default.
    expect((await screen.findByTestId('instance-regions-empty')).textContent).toContain(
      'default region',
    )

    await waitFor(async () => expect((await submitBtn()).disabled).toBe(false))
    fireEvent.click(await submitBtn())
    await waitFor(() => expect(captured.length).toBe(1))
    expect(captured[0].topology).toBe('singleton')
    // No placement is sent, so the server applies the Blueprint default —
    // which is a real default, unlike the multi-region case.
    expect(captured[0].placement).toBeUndefined()
  })
})
