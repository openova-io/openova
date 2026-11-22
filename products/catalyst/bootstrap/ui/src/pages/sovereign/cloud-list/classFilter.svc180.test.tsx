/**
 * classFilter.svc180.test.tsx — SVC-180: Resources is the unified view, and
 * the separation by class is a grouping and a filter inside it.
 *
 *   1. The HelmReleases list (one row per installed Blueprint) groups its
 *      rows by class, in the class order, with a Class column; the rows of
 *      class platform-component are listed here.
 *   2. The class filter lives in the address: choosing a class writes
 *      `?class=<class>`, a reload of that address returns the same view, and
 *      "All classes" removes it.
 *   3. A class in the address without a kind opens the HelmReleases list and
 *      the address keeps the class (a class view of the left menu is that
 *      address).
 *
 * Snapshot keys are built with the production `objectKey`, so the fixture
 * cannot drift from the key contract the list filters on.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import { objectKey } from '@/widgets/architecture-graph/useK8sCacheStream'
import { CLASS_LABELS, CLASS_ORDER, classOfBlueprint } from '@/shared/constants/classes'

const cloudState: {
  k8sSnapshot: K8sSnapshot
  k8sStatus: string
  k8sRevision: number
  deploymentId: string
  data: null
  isLoading: boolean
} = {
  k8sSnapshot: new Map(),
  k8sStatus: 'open',
  k8sRevision: 1,
  deploymentId: '',
  data: null,
  isLoading: false,
}

vi.mock('../CloudPage', () => ({ useCloud: () => cloudState }))

// Imported AFTER the mock so the pages pick up the stubbed useCloud.
const { HelmReleasesListPage } = await import('./kindsPages')
const { CloudListView } = await import('./CloudListView')

function hr(name: string, chart: string): [string, K8sObject] {
  const obj: K8sObject = {
    apiVersion: 'helm.toolkit.fluxcd.io/v2',
    kind: 'HelmRelease',
    metadata: { namespace: 'flux-system', name, uid: `uid-${name}` },
    spec: { chart: { spec: { chart } } },
  }
  return [objectKey('helmrelease', obj, ''), obj]
}

// Five classes are present, platform-component among them, and one release
// whose chart is no Blueprint (it falls to the default class).
const RELEASES: Array<[string, K8sObject]> = [
  hr('bp-cilium', 'bp-cilium'),
  hr('bp-cert-manager', 'bp-cert-manager'),
  hr('shared-pg', 'bp-postgres'),
  hr('bp-valkey', 'bp-valkey'),
  hr('bp-agenity', 'bp-agenity'),
  hr('acme-site', 'bp-wordpress-tenant'),
  hr('plain-release', 'some-chart'),
]

afterEach(cleanup)

function renderAt(href: string, page: 'helmreleases' | 'list-view') {
  cloudState.k8sSnapshot = new Map(RELEASES)
  cloudState.k8sRevision += 1
  const rootRoute = createRootRoute()
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/cloud',
    component: () => (page === 'helmreleases' ? <HelmReleasesListPage /> : <CloudListView />),
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([cloudRoute]),
    history: createMemoryHistory({ initialEntries: [href] }),
  })
  render(<RouterProvider router={router as never} />)
  return router
}

/** The body rows in order: a group row gives its class, a data row its name. */
function bodyOrder(table: HTMLElement): string[] {
  return [...table.querySelectorAll('tbody tr')].map((tr) => {
    const id = tr.getAttribute('data-testid') ?? ''
    return id.replace('cloud-helmrelease-class-group-', 'group:').replace('cloud-helmrelease-row-', '')
  })
}

describe('Resources — class grouping and the class filter (SVC-180)', () => {
  it('groups the HelmReleases by class, in the class order, and lists the platform components', async () => {
    renderAt('/cloud?view=list&kind=helmreleases', 'helmreleases')
    const table = await screen.findByTestId('cloud-helmrelease-table')
    const order = bodyOrder(table)
    const groups = order.filter((r) => r.startsWith('group:')).map((r) => r.slice('group:'.length))
    expect(groups).toEqual(['application', 'data-service', 'ai-service', 'platform-component'])
    expect(groups).toEqual(CLASS_ORDER.filter((c) => groups.includes(c)))
    // Every row sits under the group of its own class.
    let current = ''
    for (const r of order) {
      if (r.startsWith('group:')) {
        current = r.slice('group:'.length)
        continue
      }
      const [, obj] = RELEASES.find(([, o]) => o.metadata?.name === r)!
      const chart = ((obj.spec as { chart: { spec: { chart: string } } }).chart.spec.chart)
      expect(classOfBlueprint(chart.startsWith('bp-') ? chart : null), r).toBe(current)
    }
    expect(order).toContain('bp-cilium')
    expect(order).toContain('bp-cert-manager')
    // The Class column names the class of the row.
    const cilium = screen.getByTestId('cloud-helmrelease-row-bp-cilium')
    expect(within(cilium).getByText(CLASS_LABELS['platform-component'].singular)).toBeTruthy()
    expect(screen.getByTestId('cloud-helmrelease-class-group-data-service').textContent).toContain('Data services')
  })

  it('the class filter is in the address, and a reload returns the same view', async () => {
    const router = renderAt('/cloud?view=list&kind=helmreleases', 'helmreleases')
    const select = (await screen.findByTestId('cloud-helmrelease-class-filter')) as HTMLSelectElement
    expect(select.value).toBe('')
    await act(async () => {
      fireEvent.change(select, { target: { value: 'platform-component' } })
    })
    expect(router.state.location.search).toMatchObject({ view: 'list', kind: 'helmreleases', class: 'platform-component' })
    const href = router.state.location.href
    expect(href).toContain('class=platform-component')
    const filtered = bodyOrder(screen.getByTestId('cloud-helmrelease-table'))
    expect(filtered).toEqual(['group:platform-component', 'bp-cert-manager', 'bp-cilium'])

    // Reload: a fresh router on the same address shows the same view.
    cleanup()
    const reloaded = renderAt(href, 'helmreleases')
    const again = (await screen.findByTestId('cloud-helmrelease-class-filter')) as HTMLSelectElement
    expect(again.value).toBe('platform-component')
    expect(bodyOrder(screen.getByTestId('cloud-helmrelease-table'))).toEqual(filtered)

    // All classes removes the parameter and every row is back.
    await act(async () => {
      fireEvent.change(again, { target: { value: '' } })
    })
    expect(reloaded.state.location.search).not.toHaveProperty('class')
    expect(bodyOrder(screen.getByTestId('cloud-helmrelease-table')).filter((r) => !r.startsWith('group:')).length).toBe(RELEASES.length)
  })

  it('a class with no release says so instead of an empty table', async () => {
    renderAt('/cloud?view=list&kind=helmreleases&class=infrastructure', 'helmreleases')
    const empty = await screen.findByTestId('cloud-helmrelease-class-empty')
    expect(empty.textContent).toContain('Infrastructure')
    expect(screen.queryByTestId('cloud-helmrelease-table')).toBeNull()
  })

  it('a class in the address without a kind opens the HelmReleases list and keeps the class', async () => {
    const router = renderAt('/cloud?class=data-service', 'list-view')
    const view = await screen.findByTestId('cloud-list-view')
    expect(view.getAttribute('data-kind')).toBe('helmreleases')
    await screen.findByTestId('cloud-helmrelease-row-shared-pg')
    expect(router.state.location.search).toMatchObject({ view: 'list', kind: 'helmreleases', class: 'data-service' })
    expect(screen.queryByTestId('cloud-helmrelease-row-bp-cilium')).toBeNull()
  })
})
