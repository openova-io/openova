/**
 * InstancesSection.topology-gate.test.tsx — #5609 guard.
 *
 * ASSERTS THE RENDERED PRODUCTION SURFACE, BOTH DIRECTIONS.
 *
 * `NewInstanceDialog`'s `select[data-testid=select-instance-topology]` is the
 * ONLY surface in the sovereign-admin console where an operator picks a
 * topology mode. (The app-detail Topology tab uses `PlacementEditor`, which
 * per #3969 has NO mode picker — it derives the pattern from the placement
 * targets.) So this <select> is the single point where a Blueprint's declared
 * `spec.topology.supported` becomes operator-visible.
 *
 * WHY A NEW FILE RATHER THAN TRUSTING THE OLD ONE: until #5609 the only test
 * that exercised the `active-passive` gate lived in `TopologyEditor.test.tsx`
 * and passed `supportedCanonical={[...]}` as an explicit prop to a component
 * **production never mounted**. It therefore proved a code path no operator
 * could reach, while the reachable path — this dialog resolving
 * `topology.supported` over the network — had only a NEGATIVE assertion
 * (`active-passive` disabled on a fixture that does not declare it). A gate
 * pinned in one direction only can be satisfied by disabling everything, so
 * the enable direction below is the load-bearing half. That component is
 * deleted as of #5609; these tests replace its apparent coverage with real
 * coverage.
 *
 * The catalog-data assertion at the bottom pins the claim #5609 was filed on
 * ("no operator can ever select active-passive"): it fails if the catalog ever
 * genuinely stops offering `active-passive` on any operator-visible Blueprint.
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
import { ALL_BLUEPRINTS } from '@/shared/constants/catalog.generated'

const SELF = { deploymentId: 'd-1', sovereignFQDN: 't01.omani.works' }
const ORG_ROWS = {
  items: [
    {
      org_tenant_id: 't-acme',
      subdomain: 'acme',
      company_name: 'Acme',
      tenant_namespace: 'acme',
      state: 'ready',
    },
  ],
}
const TOPOLOGY = {
  topology: {
    pattern: 'multi-region',
    regions: [
      {
        id: 'r-a',
        name: 'rgn-a',
        provider: 'huawei',
        providerRegion: 'me-east-215-a',
        clusters: [],
      },
      {
        id: 'r-b',
        name: 'rgn-b',
        provider: 'huawei',
        providerRegion: 'me-east-215-b',
        clusters: [],
      },
    ],
  },
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
 * Mount the real section against a catalog fixture whose
 * `spec.topology.supported` mirrors a REAL blueprint from the shipped
 * catalog — the same JSON shape `getCatalogItemVersion` returns live.
 */
function installFetch(bpName: string, supported: string[]) {
  captured = []
  const catalogItem = {
    name: bpName,
    version: '1.0.0',
    card: { title: bpName },
    origin: 'upstream',
    source: 'gitea',
    raw: { spec: { multiInstance: { enabled: true }, topology: { supported } } },
  }
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/catalyst/v1/apps/instances') && init?.method === 'POST') {
      captured.push(JSON.parse(String(init.body)))
      return json({ id: 'new-uid', name: 'x-1', blueprint: bpName, org: 'acme' }, 201)
    }
    if (url.includes('/instances')) return json({ items: [] })
    if (url.includes('/sovereign/self')) return json(SELF)
    if (url.includes('/v1/organizations')) return json(ORG_ROWS)
    if (url.includes('/infrastructure/topology')) return json(TOPOLOGY)
    if (url.includes('/catalog/')) return json(catalogItem)
    return json({})
  }) as typeof fetch
}

async function openDialogFor(bpName: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <InstancesSection blueprint={`bp-${bpName}`} />,
  })
  const tree = rootRoute.addChildren([route])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/d-1/catalog/bp-${bpName}`],
    }),
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

function optionFor(sel: HTMLSelectElement, value: string): HTMLOptionElement {
  const opt = Array.from(sel.querySelectorAll('option')).find(
    (o) => o.getAttribute('value') === value,
  )
  expect(opt, `no <option value="${value}"> in the topology select`).toBeTruthy()
  return opt as HTMLOptionElement
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('#5609 — active-passive IS selectable when the Blueprint declares it', () => {
  // bp-ferretdb ships supported: ['active-passive', 'singleton'] (verified
  // against products/catalyst/bootstrap/api/internal/catalog/blueprints.json).
  beforeEach(() => installFetch('ferretdb', ['active-passive', 'singleton']))

  it('renders active-passive ENABLED (the option is not greyed out)', async () => {
    const sel = await openDialogFor('ferretdb')
    await waitFor(() => {
      expect(optionFor(sel, 'active-passive').disabled).toBe(false)
    })
    // Declared → enabled. Not declared → still rendered, but disabled. Both
    // halves matter: a gate that enabled everything would pass the first
    // assertion and fail the second.
    expect(optionFor(sel, 'singleton').disabled).toBe(false)
    expect(optionFor(sel, 'active-active').disabled).toBe(true)
    expect(optionFor(sel, 'active-hot-standby').disabled).toBe(true)
  })

  it('the operator can actually SELECT active-passive and the value sticks', async () => {
    const sel = await openDialogFor('ferretdb')
    await waitFor(() => expect(optionFor(sel, 'active-passive').disabled).toBe(false))
    fireEvent.change(sel, { target: { value: 'active-passive' } })
    expect(sel.value).toBe('active-passive')
  })

  it('a create request POSTs topology=active-passive end-to-end', async () => {
    const sel = await openDialogFor('ferretdb')
    await waitFor(() => expect(optionFor(sel, 'active-passive').disabled).toBe(false))

    fireEvent.change(await screen.findByTestId('input-instance-name'), {
      target: { value: 'fdb-1' },
    })
    fireEvent.change(await screen.findByTestId('select-instance-org'), {
      target: { value: 'acme' },
    })
    fireEvent.change(sel, { target: { value: 'active-passive' } })
    // active-passive is a multi-region class → ≥2 regions required (#3599).
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-a'))
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-b'))

    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    fireEvent.click(submit)

    await waitFor(() => expect(captured.length).toBe(1))
    // The canonical token reaches the wire — this is what "selectable"
    // means operationally, not merely "an <option> was painted".
    expect(captured[0].topology).toBe('active-passive')
    expect(captured[0].placement).toEqual({ regions: ['rgn-a', 'rgn-b'] })
  })
})

describe('#5609 — active-passive is NOT offered when the Blueprint omits it', () => {
  // bp-nemo-guardrails ships supported: ['active-active', 'singleton'] — the
  // exact blueprint walked live on hw292 in the #5609 report.
  beforeEach(() => installFetch('nemo-guardrails', ['active-active', 'singleton']))

  it('renders active-passive DISABLED with the not-supported reason', async () => {
    const sel = await openDialogFor('nemo-guardrails')
    await waitFor(() => {
      expect(optionFor(sel, 'active-passive').disabled).toBe(true)
    })
    expect(optionFor(sel, 'active-passive').textContent).toContain('not supported by this blueprint')
    // Non-vacuity: the same control leaves the declared modes enabled, so it
    // is not blanket-disabling every multi-region class.
    expect(optionFor(sel, 'active-active').disabled).toBe(false)
    expect(optionFor(sel, 'singleton').disabled).toBe(false)
    expect(optionFor(sel, 'active-hot-standby').disabled).toBe(true)
  })
})

describe('#5609 — the shipped catalog actually offers active-passive to operators', () => {
  it('at least one operator-VISIBLE (listed) Blueprint declares active-passive', () => {
    const listedWithAP = ALL_BLUEPRINTS.filter(
      (b) => b.visibility === 'listed' && (b.topology?.supported ?? []).includes('active-passive'),
    ).map((b) => b.id)

    // #5609 was filed asserting the intersection of "declares active-passive"
    // and "an operator can reach it" is EMPTY. It is not: bp-matrix,
    // bp-newapi, bp-openmeter, bp-stalwart-tenant, bp-temporal and
    // bp-wordpress-tenant are all visibility=listed and declare it. If a
    // future catalog edit really did empty this set, THAT would make the mode
    // unreachable — and this assertion is what would catch it.
    expect(listedWithAP.length).toBeGreaterThan(0)
  })

  it('every Blueprint declaring active-passive also ships its perTopology contract', () => {
    const missing = ALL_BLUEPRINTS.filter(
      (b) =>
        (b.topology?.supported ?? []).includes('active-passive') &&
        !b.topology?.perTopology?.['active-passive'],
    ).map((b) => b.id)
    // A declared class with no DR contract would render an empty helper
    // string in the picker — offered but undescribed.
    expect(missing).toEqual([])
  })
})
