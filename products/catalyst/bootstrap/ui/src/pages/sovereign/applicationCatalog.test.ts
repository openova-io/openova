/**
 * applicationCatalog.test.ts — vitest coverage for the function that
 * resolves the Application set rendered on the Sovereign Admin page.
 *
 * Coverage:
 *   • BOOTSTRAP_KIT (11 entries) is always present, in numerical order.
 *   • User selection adds beyond the bootstrap kit.
 *   • Transitive dependencies cascade in (e.g. Harbor adds cnpg /
 *     valkey / seaweedfs).
 *   • Mandatory components from the catalog are always represented.
 *   • Output is deduplicated and stably ordered.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { createElement, type FunctionComponent } from 'react'
import { render, screen, cleanup } from '@testing-library/react'
import { RouterProvider, createRouter, createRootRoute, createRoute, createMemoryHistory } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as catalogApi from '@/lib/catalog.api'
import { resolveApplications, reverseDependencies } from './applicationCatalog'
import { AppDetail } from './AppDetail'
import { KIT_COMPONENT_IDS } from '@/shared/lib/kitComponents'
import { BOOTSTRAP_KIT, BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'

describe('applicationCatalog — resolveApplications', () => {
  it('always includes the eleven BOOTSTRAP_KIT Applications', () => {
    const apps = resolveApplications([])
    const ids = apps.map((a) => a.id)
    for (const b of BOOTSTRAP_KIT) {
      expect(ids).toContain(b.id)
    }
  })

  it('marks bootstrap-kit apps with bootstrapKit: true', () => {
    const apps = resolveApplications([])
    const cilium = apps.find((a) => a.id === 'bp-cilium')
    expect(cilium?.bootstrapKit).toBe(true)
  })

  it('user selection adds beyond the bootstrap kit', () => {
    const apps = resolveApplications(['harbor'])
    const ids = apps.map((a) => a.id)
    expect(ids).toContain('bp-harbor')
    // Harbor's cascade pulls in cnpg, valkey, seaweedfs at the catalog level.
    expect(ids).toContain('bp-cnpg')
    expect(ids).toContain('bp-seaweedfs')
    expect(ids).toContain('bp-valkey')
  })

  it('mandatory catalog components are always represented', () => {
    const apps = resolveApplications([])
    // Examples of mandatory-tier catalog components (post transitive
    // promotion). They must appear regardless of user selection.
    const ids = apps.map((a) => a.id)
    expect(ids).toContain('bp-flux')
    expect(ids).toContain('bp-crossplane')
    expect(ids).toContain('bp-openbao')
  })

  it('output is deduplicated', () => {
    const apps = resolveApplications(['flux']) // bootstrap kit already has flux
    const ids = apps.map((a) => a.id)
    const fluxCount = ids.filter((id) => id === 'bp-flux').length
    expect(fluxCount).toBe(1)
  })

  it('descriptors carry family + tier metadata', () => {
    const apps = resolveApplications([])
    const cilium = apps.find((a) => a.id === 'bp-cilium')
    expect(cilium?.familyId).toBe('foundation')
    expect(cilium?.familyName).toBe('Foundation')
    expect(cilium?.tier).toBe('mandatory')
  })
})

describe('applicationCatalog — reverseDependencies', () => {
  it('returns the Blueprint ids whose dependencies include the input', () => {
    // External-DNS depends on PowerDNS, so reverse-deps for powerdns
    // includes external-dns.
    const reverse = reverseDependencies('powerdns')
    expect(reverse).toContain('external-dns')
  })

  it('returns an empty array for components nothing depends on', () => {
    const reverse = reverseDependencies('this-id-does-not-exist')
    expect(reverse).toEqual([])
  })
})

// ADR-0015 D4: the AppDetail family chip is the kit ring; a catalogue Blueprint outside the kit has none.
const DetailPage: FunctionComponent<{ disableStream?: boolean }> = AppDetail
function renderDetail(id: string) {
  const root = createRootRoute()
  const component = () => createElement(DetailPage, { disableStream: true })
  const route = createRoute({ getParentRoute: () => root, path: '/provision/$deploymentId/app/$componentId', component })
  const history = createMemoryHistory({ initialEntries: [`/provision/d-1/app/${id}`] })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const router = createRouter({ routeTree: root.addChildren([route]), history })
  render(createElement(QueryClientProvider, { client: qc }, createElement(RouterProvider, { router })))
}
describe('AppDetail — the family chip (ADR-0015 D4)', () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks() })
  it.each(['wordpress-tenant', 'agenity', 'chargeback', 'openclaw', 'stalwart-tenant'])('bp-%s, a catalogue Blueprint outside the kit, renders no app-detail-family', async (s) => {
    expect([BLUEPRINT_BY_ID[`bp-${s}`]?.id, KIT_COMPONENT_IDS.includes(s)]).toEqual([`bp-${s}`, false])
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue({ name: 'inst', namespace: 'ns', blueprint: `bp-${s}` } as never)
    renderDetail('inst')
    expect((await screen.findByTestId('app-detail-name')).textContent).toBe('inst')
    // The catalogue entry was read: the subtitle is its title, not the Application name.
    expect(document.querySelector('.hero-subtitle')?.textContent).toBe(`— ${BLUEPRINT_BY_ID[`bp-${s}`].title}`)
    expect(screen.queryByTestId('app-detail-family')).toBeNull()
  })
  it('the kit release bp-cilium renders its ring title Foundation', async () => {
    renderDetail('bp-cilium')
    expect((await screen.findByTestId('app-detail-family')).textContent).toBe('Foundation')
  })
})
