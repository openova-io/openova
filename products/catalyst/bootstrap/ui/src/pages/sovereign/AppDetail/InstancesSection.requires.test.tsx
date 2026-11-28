/**
 * InstancesSection.requires.test.tsx — SVC-280: the create dialog renders one
 * block per requires[] entry of the Blueprint. The block reads the capability
 * from GET /api/v1/catalog/capabilities/{name}, offers its offered
 * implementations (the default chosen), lists for reuse only the
 * Applications of the chosen implementation, and posts the element
 * {capability, alias, implementation, mode, instance}.
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

type Requirement = { capability: string; contract: string; alias: string; optional?: boolean }

function probeCatalog(requires: Requirement[] | undefined) {
  return {
    name: 'capability-probe',
    version: '0.1.1',
    card: { title: 'Capability probe' },
    origin: 'upstream',
    source: 'gitea',
    raw: {
      spec: {
        ...(requires ? { requires } : {}),
        topology: { supported: ['singleton'], defaults: { 'single-region': 'singleton', 'multi-region': 'singleton' } },
      },
    },
  }
}

const implementation = (blueprint: string, title: string, kind: string, offered: boolean) => ({
  blueprint,
  title,
  implementation: kind,
  lifecycle: 'experimental',
  offered,
  available: true,
  unavailableReason: '',
})

// Two offered implementations and one the Sovereign does not offer.
function objectStorage(over: Record<string, unknown> = {}) {
  return {
    name: 'object-storage',
    title: 'Object storage',
    contract: 'v1',
    default: 'bp-object-bucket',
    organizationChoice: 'allowed',
    implementations: [
      implementation('bp-object-bucket', 'Object storage (in-cluster)', 'in-cluster', true),
      implementation('bp-object-bucket-managed', 'Object storage (cloud)', 'cloud-native', true),
      implementation('bp-object-bucket-old', 'Object storage (old)', 'in-cluster', false),
    ],
    ...over,
  }
}

// Existing Applications per Blueprint, by the bare name of the instances route.
const APPLICATIONS: Record<string, unknown[]> = {
  'object-bucket': [{ id: 'a-1', name: 'cluster-objects', org: 'acme', topology: 'singleton', contexts: [] }],
  'object-bucket-managed': [{ id: 'm-1', name: 'shared-objects', org: 'acme', topology: 'singleton', contexts: [{}] }],
}

const REF_ORGS = {
  organizations: [{ name: 'acme', displayName: 'Acme', namespace: 'acme', ready: true, kind: 'customer', platform: false }],
}
const REF_ENVS = { environments: [{ name: 'acme-prod', envType: 'prod', organizationRef: 'acme' }] }
const REF_REGIONS = { regions: [{ id: 'rgn-a', label: 'rgn-a', primary: true }] }

let captured: Array<Record<string, unknown>> = []
let capabilityCalls: string[] = []

function installFetch(requires: Requirement[] | undefined, capability: Record<string, unknown>, catalogStatus = 200) {
  captured = []
  capabilityCalls = []
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/catalyst/v1/apps/instances') && init?.method === 'POST') {
      captured.push(JSON.parse(String(init.body)))
      return json({ id: 'new-uid', name: 'probe-1', blueprint: 'bp-capability-probe', org: 'acme', status: 'Pending' }, 201)
    }
    const cap = url.match(/\/api\/v1\/catalog\/capabilities\/([^/?]+)/)
    if (cap) {
      capabilityCalls.push(decodeURIComponent(cap[1]))
      return json(capability)
    }
    const inst = url.match(/\/catalyst\/v1\/catalog\/([^/]+)\/instances/)
    if (inst) return json({ items: APPLICATIONS[decodeURIComponent(inst[1])] ?? [] })
    if (url.match(/\/v1\/refs\/organizations\/[^/]+\/environments/)) return json(REF_ENVS)
    if (url.includes('/v1/refs/organizations')) return json(REF_ORGS)
    if (url.includes('/v1/refs/regions')) return json(REF_REGIONS)
    if (url.includes('/catalog/')) return catalogStatus === 200 ? json(probeCatalog(requires)) : json({}, catalogStatus)
    return json({})
  }) as typeof fetch
}

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response)
}

async function openDialog() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <InstancesSection blueprint="bp-capability-probe" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/catalog/bp-capability-probe'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  fireEvent.click(await screen.findByTestId('btn-new-instance'))
  await screen.findByTestId('dialog-new-instance')
}

async function fillRequired() {
  fireEvent.change(await screen.findByTestId('input-instance-name'), { target: { value: 'probe-1' } })
  const orgSel = (await screen.findByTestId('select-instance-org')) as HTMLSelectElement
  await waitFor(() => expect(orgSel.querySelector('option[value="acme"]')).toBeTruthy())
  fireEvent.change(orgSel, { target: { value: 'acme' } })
}

const OBJECTS: Requirement = { capability: 'object-storage', contract: 'v1', alias: 'objects', optional: true }

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('NewInstanceDialog — one block per requires[] entry (SVC-280)', () => {
  beforeEach(() => installFetch([OBJECTS], objectStorage()))

  it('renders the two offered implementations and posts the chosen one with the Application to reuse', async () => {
    await openDialog()
    const picker = (await screen.findByTestId('backing-implementation-objects')) as HTMLSelectElement
    const values = Array.from(picker.querySelectorAll('option')).map((o) => o.getAttribute('value'))
    expect(values).toEqual(['bp-object-bucket', 'bp-object-bucket-managed'])
    expect(picker.value).toBe('bp-object-bucket')
    expect(capabilityCalls).toEqual(['object-storage'])

    fireEvent.change(picker, { target: { value: 'bp-object-bucket-managed' } })
    // The chosen implementation is named by the title the capability route gave it.
    await waitFor(() => expect(screen.getByTestId('backing-selector-objects').textContent).toContain('Object storage (cloud)'))
    const reuse = (await screen.findByTestId('backing-reuse-objects')) as HTMLInputElement
    await waitFor(() => expect(reuse.disabled).toBe(false))
    fireEvent.click(reuse)
    const existing = (await screen.findByTestId('backing-instance-objects')) as HTMLSelectElement
    // Only the Applications of the chosen implementation are offered for reuse.
    expect(Array.from(existing.querySelectorAll('option')).map((o) => o.getAttribute('value'))).toEqual(['shared-objects'])

    await fillRequired()
    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    fireEvent.click(submit)
    await waitFor(() => expect(captured.length).toBe(1))
    expect(captured[0].backing).toEqual([
      { capability: 'object-storage', alias: 'objects', implementation: 'bp-object-bucket-managed', mode: 'reuse', instance: 'shared-objects' },
    ])
  })

  it('posts the default implementation with Create new when nothing is changed', async () => {
    await openDialog()
    await screen.findByTestId('backing-create-objects')
    await fillRequired()
    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    fireEvent.click(submit)
    await waitFor(() => expect(captured.length).toBe(1))
    expect(captured[0].backing).toEqual([
      { capability: 'object-storage', alias: 'objects', implementation: 'bp-object-bucket', mode: 'create' },
    ])
  })
})

describe('NewInstanceDialog — the implementation selector is hidden when there is no choice (SVC-280)', () => {
  it('hides it when the Sovereign fixes the choice, and posts the default', async () => {
    installFetch([OBJECTS], objectStorage({ organizationChoice: 'fixed' }))
    await openDialog()
    await screen.findByTestId('backing-create-objects')
    expect(screen.queryByTestId('backing-implementation-objects')).toBeNull()
    await fillRequired()
    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    fireEvent.click(submit)
    await waitFor(() => expect(captured.length).toBe(1))
    expect(captured[0].backing).toEqual([
      { capability: 'object-storage', alias: 'objects', implementation: 'bp-object-bucket', mode: 'create' },
    ])
  })

  it('hides it when one implementation is offered', async () => {
    installFetch([OBJECTS], objectStorage({
      implementations: [implementation('bp-object-bucket', 'Object storage (in-cluster)', 'in-cluster', true)],
    }))
    await openDialog()
    await screen.findByTestId('backing-create-objects')
    expect(screen.queryByTestId('backing-implementation-objects')).toBeNull()
  })
})

describe('NewInstanceDialog — nothing offered (SVC-280)', () => {
  const noneOffered = () =>
    objectStorage({
      default: '',
      implementations: [implementation('bp-object-bucket-old', 'Object storage (old)', 'in-cluster', false)],
    })

  it('a required capability keeps Create disabled and says why', async () => {
    installFetch([{ ...OBJECTS, optional: false }], noneOffered())
    await openDialog()
    const none = await screen.findByTestId('backing-capability-none-objects')
    expect(none.textContent).toBe('No implementation of Object storage is offered on this Sovereign.')
    await fillRequired()
    // The name and the Organization alone would enable Create: wait for the
    // Environment the Organization brings, then the button must stay disabled.
    await waitFor(() => expect((screen.getByTestId('select-instance-environment') as HTMLSelectElement).value).toBe('acme-prod'))
    const submit = screen.getByTestId('btn-submit-instance') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.click(submit)
    expect(captured).toEqual([])
  })

  it('an optional capability installs without an element', async () => {
    installFetch([OBJECTS], noneOffered())
    await openDialog()
    const none = await screen.findByTestId('backing-capability-none-objects')
    expect(none.textContent).toBe(
      'No implementation of Object storage is offered on this Sovereign. The Application is installed without it.',
    )
    await fillRequired()
    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    fireEvent.click(submit)
    await waitFor(() => expect(captured.length).toBe(1))
    expect('backing' in captured[0]).toBe(false)
  })
})

describe('NewInstanceDialog — a Blueprint that cannot be read (SVC-280)', () => {
  it('keeps Create disabled and says why: a failed read is not a Blueprint without requires', async () => {
    installFetch([{ ...OBJECTS, optional: false }], objectStorage(), 500)
    await openDialog()
    // The Blueprint read retries once before it fails.
    const failed = await screen.findByTestId('blueprint-read-error', {}, { timeout: 4000 })
    expect(failed.textContent).toBe(
      'The Blueprint bp-capability-probe could not be read, so the capabilities it requires are not known: catalog get: HTTP 500',
    )
    await fillRequired()
    // The name, the Organization and its Environment alone would enable Create.
    await waitFor(() => expect((screen.getByTestId('select-instance-environment') as HTMLSelectElement).value).toBe('acme-prod'))
    const submit = screen.getByTestId('btn-submit-instance') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.click(submit)
    expect(captured).toEqual([])
    expect(capabilityCalls).toEqual([])
  })
})

describe('NewInstanceDialog — a Blueprint without requires (SVC-280)', () => {
  it('asks for no capability, renders no block and posts no backing', async () => {
    installFetch(undefined, objectStorage())
    await openDialog()
    await fillRequired()
    const submit = (await screen.findByTestId('btn-submit-instance')) as HTMLButtonElement
    await waitFor(() => expect(submit.disabled).toBe(false))
    expect(screen.queryByTestId('backing-services')).toBeNull()
    fireEvent.click(submit)
    await waitFor(() => expect(captured.length).toBe(1))
    expect('backing' in captured[0]).toBe(false)
    expect(capabilityCalls).toEqual([])
  })
})
