/**
 * InstancesSection.test.tsx — #3599 + #3600 (EPIC #3597) lock-in for the
 * create-from-catalog wizard (NewInstanceDialog).
 *
 *   • Every fixed-set input is a dropdown / multiselect from LIVE data;
 *     only the instance name is free-text (#3600):
 *       - Organization → <select> populated from listOrganizations()
 *       - Topology mode → <select> from the editor ALL_MODES set
 *       - Region(s) → checkboxes from the live infrastructure topology
 *       - vCluster → <select> of REAL placement targets only (#5616):
 *         "host" + live vClusters keyed by their real host NAMESPACE
 *         (never the display name, never fabricated tier options)
 *   • Placement is written onto the create request (#3599): the POST body
 *     carries placement {vcluster, regions} + topology.
 *   • active-active / active-hotstandby require ≥2 regions; Create stays
 *     disabled until satisfied (#3599 validation).
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

// One blueprint with two supported topologies + no shareable backing deps.
const WP_CATALOG = {
  name: 'wordpress',
  version: '2.0.0',
  card: { title: 'WordPress' },
  origin: 'upstream',
  source: 'gitea',
  raw: {
    spec: {
      multiInstance: { enabled: true },
      topology: {
        supported: ['single-region', 'active-hot-standby'],
        defaults: { 'single-region': 'single-region', 'multi-region': 'active-hot-standby' },
      },
    },
  },
}

// /sovereign/self parent-org payload (the directory's first row).
const SELF = { deploymentId: 'd-1', sovereignFQDN: 't01.omani.works' }
// /v1/organizations sub-org feed — OrgRecordWire shape (snake_case; the
// org_tenant_id / tenant_namespace keys are the legacy BE wire contract). The
// dialog's Org dropdown value is the subdomain slug.
const ORG_ROWS = {
  items: [
    { org_tenant_id: 't-acme', subdomain: 'acme', company_name: 'Acme', tenant_namespace: 'acme', state: 'ready' },
  ],
}

// #5616 — two live vClusters, both with a display NAME that differs from
// their real host NAMESPACE:
//   v1 "acme-rtz" runs in ns "rtz"  → tier-resolvable, MUST be offered
//     with value = "rtz" (the real namespace), never "acme-rtz".
//   v2 "vcluster" runs in ns "acme" → the per-Org boundary vCluster; no
//     placement.vcluster spelling resolves it today, so it MUST NOT be
//     offered (on hw292 its display name became a namespace that does
//     not exist and the install Degraded).
const TOPOLOGY = {
  topology: {
    pattern: 'multi-region',
    regions: [
      { id: 'r-a', name: 'rgn-a', provider: 'huawei', providerRegion: 'me-east-215-a', clusters: [
        { id: 'c-a', name: 'cl-a', vclusters: [
          { id: 'v1', name: 'acme-rtz', namespace: 'rtz', isolationMode: 'rtz', status: 'healthy' },
          { id: 'v2', name: 'vcluster', namespace: 'acme', isolationMode: 'rtz', status: 'healthy' },
        ] },
      ] },
      { id: 'r-b', name: 'rgn-b', provider: 'huawei', providerRegion: 'me-east-215-b', clusters: [] },
    ],
  },
  cloud: [],
  storage: { pvcs: [], buckets: [], volumes: [] },
}

// Captured create-instance POST bodies.
let captured: Array<Record<string, unknown>> = []

function installFetch() {
  captured = []
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    // Create-instance POST.
    if (url.includes('/catalyst/v1/apps/instances') && init?.method === 'POST') {
      captured.push(JSON.parse(String(init.body)))
      return json({ id: 'new-uid', name: 'wp-1', blueprint: 'wordpress', org: 'acme', topology: 'single-region', status: 'Pending' }, 201)
    }
    // Instances list (the section body).
    if (url.includes('/instances')) return json({ items: [] })
    // Org sources: /sovereign/self (parent) + /v1/organizations (sub-orgs).
    if (url.includes('/sovereign/self')) return json(SELF)
    if (url.includes('/v1/organizations')) return json(ORG_ROWS)
    // Infra topology (regions + vclusters).
    if (url.includes('/infrastructure/topology')) return json(TOPOLOGY)
    // Catalog item + version (topology supported list).
    if (url.includes('/catalog/')) return json(WP_CATALOG)
    return json({})
  }) as typeof fetch
}

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response)
}

function renderSection() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  // Mount under a deploymentId-bearing route so useResolvedDeploymentId
  // resolves from the URL (no /sovereign/self round-trip needed for the id).
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <InstancesSection blueprint="bp-wordpress" />,
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const tree = rootRoute.addChildren([route, appRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/catalog/bp-wordpress'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

async function openDialog() {
  renderSection()
  fireEvent.click(await screen.findByTestId('btn-new-instance'))
  return screen.findByTestId('dialog-new-instance')
}

beforeEach(() => installFetch())
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('NewInstanceDialog — dropdowns (#3600)', () => {
  it('Organization is a <select> populated from live orgs', async () => {
    await openDialog()
    const sel = (await screen.findByTestId('select-instance-org')) as HTMLSelectElement
    expect(sel.tagName).toBe('SELECT')
    // The Acme sub-org option resolves once listOrganizations settles.
    await waitFor(() => {
      expect(sel.querySelector('option[value="acme"]')).toBeTruthy()
    })
  })

  it('Topology mode is a <select> from the canonical editor mode set', async () => {
    await openDialog()
    const sel = (await screen.findByTestId('select-instance-topology')) as HTMLSelectElement
    expect(sel.tagName).toBe('SELECT')
    // One vocabulary (#3375 DoD-1): the blueprint declares the legacy
    // spelling (supported: ['single-region', 'active-hot-standby']) but
    // the select offers the CANONICAL tokens it folds onto.
    const values = Array.from(sel.querySelectorAll('option')).map((o) => o.getAttribute('value'))
    expect(values).toContain('singleton')
    expect(values).toContain('active-hot-standby')
    expect(values).not.toContain('single-region')
    expect(values).not.toContain('active-hotstandby')
  })

  it('#3922 — the create <select> enumerates ALL FOUR canonical modes (parity with the app-detail radio)', async () => {
    await openDialog()
    const sel = (await screen.findByTestId('select-instance-topology')) as HTMLSelectElement
    const options = Array.from(sel.querySelectorAll('option'))
    const values = options.map((o) => o.getAttribute('value'))
    // The full #3856 single vocabulary is present — the create dialog no
    // longer DROPS active-passive / active-active (the truncation bug).
    expect(values).toEqual(['singleton', 'active-active', 'active-hot-standby', 'active-passive'])
    // Modes the blueprint does NOT support (this fixture: only singleton +
    // active-hot-standby) are present-but-disabled — never silently removed.
    // #5609 — the ENABLE direction (a blueprint that DOES declare
    // active-passive gets it selectable) is pinned in
    // InstancesSection.topology-gate.test.tsx; this assertion alone could be
    // satisfied by disabling everything.
    const byValue = (v: string) => options.find((o) => o.getAttribute('value') === v)!
    expect((byValue('singleton') as HTMLOptionElement).disabled).toBe(false)
    expect((byValue('active-hot-standby') as HTMLOptionElement).disabled).toBe(false)
    expect((byValue('active-passive') as HTMLOptionElement).disabled).toBe(true)
    expect((byValue('active-active') as HTMLOptionElement).disabled).toBe(true)
  })

  it('vCluster is a <select>', async () => {
    await openDialog()
    expect(((await screen.findByTestId('select-instance-vcluster')) as HTMLSelectElement).tagName).toBe('SELECT')
  })

  // #5616 — every offered option must be a REAL placement target. The
  // hw292 walk proved the old dropdown was 5-for-5 wrong: fixed tier
  // aliases (mgmt/dmz/rtz) were offered with no live vCluster behind
  // them, and live vClusters were keyed by display NAME (the per-Org
  // vCluster is named "vcluster" but runs in the Org namespace), so
  // every explicit choice was consumed downstream as a namespace that
  // does not exist and the Application landed Degraded.
  it('#5616 — the vCluster select offers only real placement targets, keyed by real namespace', async () => {
    await openDialog()
    // Wait for the live topology to resolve (regions come from the same query).
    await screen.findByTestId('region-checkbox-rgn-a')
    const sel = (await screen.findByTestId('select-instance-vcluster')) as HTMLSelectElement
    const options = Array.from(sel.querySelectorAll('option'))
    const values = options.map((o) => o.getAttribute('value'))
    // Exactly: blank default, host, and the ONE tier-resolvable live
    // vCluster — by its real namespace.
    expect(values).toEqual(['', 'host', 'rtz'])
    // The live vCluster's display name is label-only, never the value.
    const rtzOption = options.find((o) => o.getAttribute('value') === 'rtz')!
    expect(rtzOption.textContent).toContain('acme-rtz')
    expect(values).not.toContain('acme-rtz')
    // Fabricated tier aliases with no live vCluster behind them are gone.
    expect(values).not.toContain('mgmt')
    expect(values).not.toContain('dmz')
    // The per-Org vCluster resolves through the blank default, not an
    // explicit option — neither its display name nor its Org namespace
    // is offered as a value.
    expect(values).not.toContain('vcluster')
    expect(values).not.toContain('acme')
  })

  it('Region(s) render as checkboxes from the live topology', async () => {
    await openDialog()
    expect(await screen.findByTestId('region-checkbox-rgn-a')).toBeTruthy()
    expect(await screen.findByTestId('region-checkbox-rgn-b')).toBeTruthy()
  })

  it('the only free-text input is the instance name', async () => {
    await openDialog()
    const dialog = await screen.findByTestId('dialog-new-instance')
    const textInputs = Array.from(dialog.querySelectorAll('input[type="text"]'))
    expect(textInputs).toHaveLength(1)
    expect((textInputs[0] as HTMLInputElement).getAttribute('data-testid')).toBe('input-instance-name')
  })
})

describe('NewInstanceDialog — placement (#3599)', () => {
  it('multi-region modes require ≥2 regions before Create enables', async () => {
    await openDialog()
    const name = await screen.findByTestId('input-instance-name')
    fireEvent.change(name, { target: { value: 'wp-1' } })
    fireEvent.change(await screen.findByTestId('select-instance-org'), { target: { value: 'acme' } })
    fireEvent.change(await screen.findByTestId('select-instance-topology'), { target: { value: 'active-hot-standby' } })

    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    // No regions yet → disabled + validation hint visible.
    expect(submit.disabled).toBe(true)
    expect(await screen.findByTestId('instance-regions-validation')).toBeTruthy()

    // One region — still short of 2.
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-a'))
    expect(submit.disabled).toBe(true)

    // Second region — now valid.
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-b'))
    await waitFor(() => expect(submit.disabled).toBe(false))
  })

  it('submit writes placement {vcluster, regions} + topology onto the request', async () => {
    await openDialog()
    fireEvent.change(await screen.findByTestId('input-instance-name'), { target: { value: 'wp-1' } })
    fireEvent.change(await screen.findByTestId('select-instance-org'), { target: { value: 'acme' } })
    fireEvent.change(await screen.findByTestId('select-instance-topology'), { target: { value: 'active-hot-standby' } })
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-a'))
    fireEvent.click(await screen.findByTestId('region-checkbox-rgn-b'))
    fireEvent.change(await screen.findByTestId('select-instance-vcluster'), { target: { value: 'rtz' } })

    fireEvent.click(await screen.findByTestId('btn-submit-instance'))

    await waitFor(() => expect(captured.length).toBe(1))
    const body = captured[0]
    expect(body.blueprint).toBe('wordpress')
    expect(body.org).toBe('acme')
    expect(body.name).toBe('wp-1')
    // One vocabulary (#3375 DoD-1): the canonical topology is POSTed.
    expect(body.topology).toBe('active-hot-standby')
    expect(body.placement).toEqual({ vcluster: 'rtz', regions: ['rgn-a', 'rgn-b'] })
  })

  // #5616 both directions — selecting the live vCluster submits its REAL
  // host namespace ("rtz"), and the display name ("acme-rtz") appears
  // nowhere in the payload.
  it('#5616 — the submitted payload carries the real namespace of the selected vCluster, never its display name', async () => {
    await openDialog()
    fireEvent.change(await screen.findByTestId('input-instance-name'), { target: { value: 'wp-2' } })
    fireEvent.change(await screen.findByTestId('select-instance-org'), { target: { value: 'acme' } })
    // Wait for the topology-fed options, then select the live vCluster
    // (displayed with its name "acme-rtz", valued by its namespace).
    await screen.findByTestId('region-checkbox-rgn-a')
    const sel = (await screen.findByTestId('select-instance-vcluster')) as HTMLSelectElement
    await waitFor(() => expect(sel.querySelector('option[value="rtz"]')).toBeTruthy())
    fireEvent.change(sel, { target: { value: 'rtz' } })

    fireEvent.click(await screen.findByTestId('btn-submit-instance'))

    await waitFor(() => expect(captured.length).toBe(1))
    const body = captured[0]
    const placement = body.placement as { vcluster?: string }
    // Right value present: the real namespace.
    expect(placement.vcluster).toBe('rtz')
    // Wrong value absent: the display name never reaches the wire.
    expect(placement.vcluster).not.toBe('acme-rtz')
    expect(JSON.stringify(body)).not.toContain('acme-rtz')
  })
})

// #3648 (founder #6) — the "+ New instance" button must never flash before the
// instances list resolves, and must not offer a second instance for a
// singleton-per-Org Blueprint that already has its one instance.
describe('"+ New instance" gating (#3648, founder #6)', () => {
  function renderWith(multiInstance: boolean, items: Array<Record<string, unknown>>) {
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/instances')) return json({ items })
      if (url.includes('/catalog/')) return json(WP_CATALOG)
      return json({})
    }) as typeof fetch
    const rootRoute = createRootRoute({ component: () => <Outlet /> })
    const route = createRoute({
      getParentRoute: () => rootRoute,
      path: '/provision/$deploymentId/catalog/$blueprintName',
      component: () => <InstancesSection blueprint="bp-wordpress" multiInstance={multiInstance} />,
    })
    const tree = rootRoute.addChildren([route])
    const router = createRouter({
      routeTree: tree,
      history: createMemoryHistory({ initialEntries: ['/provision/d-1/catalog/bp-wordpress'] }),
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    return render(
      <QueryClientProvider client={qc}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )
  }

  it('hides the button for a singleton that already has an instance', async () => {
    renderWith(false, [{ id: 'i-1', name: 'wp-only', org: 'acme', topology: 'single-region', status: 'Ready' }])
    await screen.findByTestId('sov-instance-row-wp-only')
    expect(screen.queryByTestId('btn-new-instance')).toBeNull()
  })

  it('shows the button for a multi-instance Blueprint once the list resolves', async () => {
    renderWith(true, [])
    expect(await screen.findByTestId('btn-new-instance')).toBeTruthy()
  })

  it('keeps the button for a singleton with no instance yet (the first is creatable)', async () => {
    renderWith(false, [])
    expect(await screen.findByTestId('btn-new-instance')).toBeTruthy()
  })
})
