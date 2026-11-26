/**
 * providers-routes.fixture.ts — the routes of catalyst-api that the wizard
 * steps call, answered for their tests: a pack "pack-a" with the
 * installation "site-1", in the shapes of internal/providersapi
 * (GET /api/v1/providers, GET /api/v1/providers/{provider}) and
 * handler/credentials.go. Used by the tests of pages/wizard/steps.
 */

import type { CloudProfile, ProviderEntry } from '@/lib/providers.api'

export const ENTRY: ProviderEntry = {
  id: 'pack-a/site-1', provider: 'pack-a', installation: 'site-1',
  displayName: 'Pack A at Site 1', lifecycle: 'proven', credentialSource: 'user',
}

export const OTHER_ENTRY: ProviderEntry = {
  id: 'pack-b', provider: 'pack-b', installation: '',
  displayName: 'Pack B', lifecycle: 'proven', credentialSource: 'operator',
}

/** The effective profile of pack-a merged with site-1; over replaces whole sections. */
export function profileOf(over: Partial<CloudProfile> = {}): CloudProfile {
  return {
    displayName: 'Pack A at Site 1',
    credentials: {
      source: 'user',
      fields: [
        { key: 'api_token', label: 'API token', secret: true, required: true },
        { key: 'project', label: 'Project', secret: false, required: false },
      ],
    },
    regions: [
      { code: 'east-1', label: 'East', location: 'East hall', zones: ['east-1a', 'east-1b'], defaultZone: 'east-1b' },
      { code: 'west-1', label: 'West', location: 'West hall', zones: ['west-1a'], defaultZone: 'west-1a' },
    ],
    compute: {
      flavors: [
        { name: 'm.small', vcpu: 2, memoryGiB: 4, roles: ['control-plane', 'worker'], price: { amount: 10, currency: 'EUR', per: 'month' } },
        { name: 'm.large', vcpu: 8, memoryGiB: 32, roles: ['control-plane', 'worker'], regions: ['east-1'], price: { amount: 40, currency: 'EUR', per: 'month' } },
        { name: 'w.medium', vcpu: 4, memoryGiB: 16, roles: ['worker'], price: { amount: 20, currency: 'EUR', per: 'month' } },
      ],
      defaults: { controlPlaneFlavor: 'm.large', workerFlavor: 'w.medium' },
    },
    storage: { defaultClass: 'block-ssd' },
    capabilities: { multiRegion: true, multiCloudRegion: false },
    ...over,
  }
}

export interface Answer { status: number; body: unknown }
export type Routes = Record<string, Answer | ((init?: RequestInit) => Answer)>

/** The key of a call: its method and its path from /v1/ on, with the query. */
export function routeKey(url: string | URL, init?: RequestInit): string {
  const u = new URL(String(url), 'http://wizard.test')
  const path = u.pathname.slice(u.pathname.indexOf('/v1/')) + u.search
  return `${init?.method ?? 'GET'} ${path}`
}

/** A fetch that answers the named routes and refuses every other call, so an unexpected call fails the test. */
export function answering(routes: Routes) {
  return async (url: string | URL, init?: RequestInit): Promise<Response> => {
    const route = routes[routeKey(url, init)]
    if (!route) throw new Error(`no answer for ${routeKey(url, init)}`)
    const a = typeof route === 'function' ? route(init) : route
    return { ok: a.status >= 200 && a.status < 300, status: a.status, json: async () => a.body } as Response
  }
}

/** The two read routes for one listed cloud and its profile. */
export function cloudRoutes(entries: ProviderEntry[] = [ENTRY], profile: CloudProfile = profileOf()): Routes {
  return {
    'GET /v1/providers': { status: 200, body: { providers: entries } },
    'GET /v1/providers/pack-a?installation=site-1': { status: 200, body: profile },
  }
}
