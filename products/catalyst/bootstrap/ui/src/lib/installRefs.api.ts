/**
 * installRefs.api.ts — ADR-0015 D4: the install dialog's option lists.
 *
 * Every value the create-instance dialog sends (org, environment, regions)
 * is a value from one of these lists, verbatim. The lists are read from the
 * objects the API validates against, so a submit can only name an existing
 * Organization, one of its Environments, and a region in the Sovereign's
 * region registry — never a derived `<org>-prod`, a `sovereign` placeholder
 * or a re-spelled region.
 *
 *   GET /api/v1/refs/organizations                    → Organization CRs
 *   GET /api/v1/refs/organizations/{org}/environments → that Org's Environments
 *   GET /api/v1/refs/regions                          → the region registry
 */
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

export interface RefOrganization {
  /** Organization CR name — the value the create request carries. */
  name: string
  displayName: string
  namespace: string
  ready: boolean
  kind: string
  /** The Sovereign's own control-plane Organization. */
  platform: boolean
}

export interface RefEnvironment {
  /** Environment CR name — the value the create request carries. */
  name: string
  envType: string
  organizationRef: string
}

export interface RefRegion {
  /** Canonical region id — the value the create request carries. */
  id: string
  label: string
  primary: boolean
}

async function getJSON<T>(path: string, what: string): Promise<T> {
  const res = await authedFetch(`${API_BASE}${path}`, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let message = `${what}: HTTP ${res.status}`
    try {
      const d = (await res.json()) as { message?: string }
      if (d?.message) message = d.message
    } catch {
      /* keep the status message */
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

export async function listRefOrganizations(): Promise<RefOrganization[]> {
  const d = await getJSON<{ organizations?: RefOrganization[] }>('/v1/refs/organizations', 'organizations')
  return d.organizations ?? []
}

export async function listRefEnvironments(org: string): Promise<RefEnvironment[]> {
  const d = await getJSON<{ environments?: RefEnvironment[] }>(
    `/v1/refs/organizations/${encodeURIComponent(org)}/environments`,
    'environments',
  )
  return d.environments ?? []
}

export async function listRefRegions(): Promise<RefRegion[]> {
  const d = await getJSON<{ regions?: RefRegion[] }>('/v1/refs/regions', 'regions')
  return d.regions ?? []
}
