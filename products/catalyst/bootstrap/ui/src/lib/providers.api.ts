/**
 * lib/providers.api.ts — client of the cloud profile routes of catalyst-api
 * (internal/providersapi, CLD-121):
 *
 *   GET /api/v1/providers                          the clouds a Sovereign can be built on
 *   GET /api/v1/providers/{provider}?installation= the effective profile of one cloud
 *
 * The wizard renders its providers, credential inputs, regions, zones and
 * flavors from these answers and keeps no list of its own (CLD-122). An
 * answer that is not 200, or a body without the list, is thrown as a
 * ProvidersApiError, never read as an empty list. A profile is held in the
 * memory of the step that reads it, never in browser storage.
 */

import { useEffect, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'

export type CredentialSource = 'user' | 'operator' | 'either'

/** One entry of GET /api/v1/providers. */
export interface ProviderEntry {
  id: string
  provider: string
  installation: string
  displayName: string
  lifecycle: string
  credentialSource: CredentialSource
}

export interface CredentialField {
  key: string
  label: string
  secret: boolean
  required: boolean
  pattern?: string
}

export interface ProfileRegion {
  code: string
  label: string
  location: string
  zones: string[]
  defaultZone: string
}

export interface Price {
  amount: number
  currency: string
  per: string
}

export interface Flavor {
  name: string
  vcpu: number
  memoryGiB: number
  roles: string[]
  regions?: string[]
  price?: Price
}

/** The spec of an effective cloud profile, as far as the wizard reads it. */
export interface CloudProfile {
  displayName: string
  credentials: { source: CredentialSource; fields: CredentialField[] }
  regions: ProfileRegion[]
  compute: { flavors: Flavor[]; defaults?: { controlPlaneFlavor: string; workerFlavor: string } }
  storage: { defaultClass: string }
  capabilities: { multiRegion: boolean; multiCloudRegion: boolean }
}

export type FlavorRole = 'control-plane' | 'worker'

/** A refused or unreadable answer: the HTTP status and the error code of its body. */
export class ProvidersApiError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string) {
    super(code ? `${status} ${code}` : `HTTP ${status}`)
    this.name = 'ProvidersApiError'
    this.status = status
    this.code = code
  }
}

async function read(path: string): Promise<unknown> {
  const res = await fetch(`${API_BASE}${path}`, { credentials: 'include', headers: { Accept: 'application/json' } })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    throw new ProvidersApiError(res.status, 'not-json')
  }
  if (!res.ok) {
    const code = (body as { error?: unknown } | null)?.error
    throw new ProvidersApiError(res.status, typeof code === 'string' ? code : '')
  }
  return body
}

/** GET /api/v1/providers: the entries the route lists, in its order. */
export async function listProviders(): Promise<ProviderEntry[]> {
  const body = (await read('/v1/providers')) as { providers?: unknown }
  if (!Array.isArray(body?.providers)) throw new ProvidersApiError(200, 'no-providers-list')
  return body.providers as ProviderEntry[]
}

/** GET /api/v1/providers/{provider}: the profile of the pack, merged with the installation when one is named. */
export async function getProfile(provider: string, installation: string): Promise<CloudProfile> {
  const query = installation ? `?installation=${encodeURIComponent(installation)}` : ''
  const body = (await read(`/v1/providers/${encodeURIComponent(provider)}${query}`)) as CloudProfile
  if (!Array.isArray(body?.regions) || !Array.isArray(body?.compute?.flavors) || !Array.isArray(body?.credentials?.fields)) {
    throw new ProvidersApiError(200, 'profile-incomplete')
  }
  return body
}

export type Loaded<T> =
  | { state: 'loading' }
  | { state: 'error'; error: Error }
  | { state: 'ready'; value: T }

function useLoaded<T>(key: string | null, load: () => Promise<T>): Loaded<T> | null {
  const [loaded, setLoaded] = useState<{ key: string; value: Loaded<T> } | null>(null)
  useEffect(() => {
    if (key === null) return
    let live = true
    load().then(
      (value) => { if (live) setLoaded({ key, value: { state: 'ready', value } }) },
      (error: unknown) => {
        if (live) setLoaded({ key, value: { state: 'error', error: error instanceof Error ? error : new Error(String(error)) } })
      },
    )
    return () => { live = false }
    // load is a new closure on every render; key names what it reads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  if (key === null) return null
  return loaded?.key === key ? loaded.value : { state: 'loading' }
}

/** The list of the providers route; a new attempt is a new value of attempt. */
export function useProviders(attempt: number): Loaded<ProviderEntry[]> {
  return useLoaded(`list:${attempt}`, listProviders) as Loaded<ProviderEntry[]>
}

/** The profile of provider and installation; null while no provider is chosen. */
export function useProfile(provider: string | null, installation: string, attempt = 0): Loaded<CloudProfile> | null {
  return useLoaded(provider ? `${provider}/${installation}:${attempt}` : null, () => getProfile(provider as string, installation))
}

/** The flavors of the role offered in the region with the given code: a flavor without regions is offered in every region. */
export function flavorsFor(profile: CloudProfile, role: FlavorRole, region: string): Flavor[] {
  return profile.compute.flavors.filter(
    (f) => f.roles.includes(role) && (!f.regions || f.regions.length === 0 || f.regions.includes(region)),
  )
}

/** The flavor a region starts with: the profile's default for the role when it is offered there, else the first offered one. */
export function defaultFlavor(profile: CloudProfile, role: FlavorRole, region: string): string {
  const offered = flavorsFor(profile, role, region)
  const named = role === 'control-plane' ? profile.compute.defaults?.controlPlaneFlavor : profile.compute.defaults?.workerFlavor
  return (offered.find((f) => f.name === named) ?? offered[0])?.name ?? ''
}

/** The credential map a request carries: every field with a value, trimmed. */
export function credentialMap(values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(values).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v !== ''))
}

export function findFlavor(profile: CloudProfile, name: string): Flavor | undefined {
  return profile.compute.flavors.find((f) => f.name === name)
}

/** "2 vCPU · 4 GiB", and the display price of the profile when it has one. */
export function describeFlavor(f: Flavor): string {
  const spec = `${f.vcpu} vCPU · ${f.memoryGiB} GiB`
  return f.price ? `${spec} · ${f.price.amount} ${f.price.currency}/${f.price.per}` : spec
}

/** The sum of the display prices of the machines, or null when one has no price or the prices differ in currency or period. */
export function totalPrice(machines: Array<{ flavor: Flavor | undefined; count: number }>): Price | null {
  const priced: Array<{ price: Price | undefined; count: number }> = machines
    .filter((m) => m.count > 0)
    .map((m) => ({ price: m.flavor?.price, count: m.count }))
  const first = priced[0]?.price
  if (!first || priced.some((m) => !m.price || m.price.currency !== first.currency || m.price.per !== first.per)) return null
  return {
    amount: priced.reduce((sum, m) => sum + (m.price as Price).amount * m.count, 0),
    currency: first.currency,
    per: first.per,
  }
}
