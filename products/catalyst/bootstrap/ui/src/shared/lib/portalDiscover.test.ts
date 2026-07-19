import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  _resetPortalContextForTesting,
  bootstrapPortal,
  discoverPortal,
  getPortalContext,
} from './portalDiscover'

afterEach(() => {
  _resetPortalContextForTesting()
})

function mockFetch(status: number, body: unknown): typeof fetch {
  return vi.fn(async () => {
    return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as unknown as typeof fetch
}

// NOTE: the `tenant_id` / `tenant_kind` keys in the mock payloads below
// are the legacy catalyst-api wire contract (tenant_discover.go) — the
// parse boundary maps them onto the clean portalId / portalKind fields.
describe('discoverPortal', () => {
  it('returns discovered on 200', async () => {
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.otech.example',
      tenant_id: 'org-acme',
      tenant_kind: 'org',
      keycloak_realm_url: 'https://kc.otech.example/realms/org-acme',
      keycloak_client_id: 'catalyst-ui',
    })
    const got = await discoverPortal('console.acme.otech.example', fetchImpl)
    expect(got.status).toBe('discovered')
    expect(got.portal?.portalKind).toBe('org')
    expect(got.portal?.portalId).toBe('org-acme')
  })

  it('returns unknown on 404', async () => {
    const fetchImpl = mockFetch(404, { error: 'host-not-registered' })
    const got = await discoverPortal('unknown.example', fetchImpl)
    expect(got.status).toBe('unknown')
  })

  it('returns unwired on 503', async () => {
    const fetchImpl = mockFetch(503, { error: 'host-registry-unavailable' })
    const got = await discoverPortal('console.example', fetchImpl)
    expect(got.status).toBe('unwired')
  })

  it('returns error on network failure', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error('connection refused')
    }) as unknown as typeof fetch
    const got = await discoverPortal('console.example', fetchImpl)
    expect(got.status).toBe('error')
    expect(got.error).toMatch(/connection refused/)
  })

  it('rejects empty host', async () => {
    const got = await discoverPortal('', mockFetch(200, {}))
    expect(got.status).toBe('error')
  })

  it('rejects an invalid kind in the payload', async () => {
    const fetchImpl = mockFetch(200, {
      tenant_id: 'org-x',
      tenant_kind: 'bogus',
      keycloak_realm_url: '',
      keycloak_client_id: '',
    })
    const got = await discoverPortal('console.x.example', fetchImpl)
    expect(got.status).toBe('error')
  })
})

describe('bootstrapPortal', () => {
  it('caches the discovery result', async () => {
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.otech.example',
      tenant_id: 'org-acme',
      tenant_kind: 'org',
      keycloak_realm_url: 'https://kc/realms/org',
      keycloak_client_id: 'ui',
    })
    const first = await bootstrapPortal('console.acme.otech.example', fetchImpl)
    const second = await bootstrapPortal('console.acme.otech.example', fetchImpl)
    expect(first.status).toBe('discovered')
    expect(second).toBe(first)
    // fetch only called once.
    expect((fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1)
  })

  it('exposes the result via getPortalContext()', async () => {
    expect(getPortalContext()).toBeNull()
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.example',
      tenant_id: 'org-acme',
      tenant_kind: 'org',
      keycloak_realm_url: '',
      keycloak_client_id: '',
    })
    await bootstrapPortal('console.acme.example', fetchImpl)
    expect(getPortalContext()?.status).toBe('discovered')
  })
})
