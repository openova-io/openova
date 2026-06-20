import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  _resetTenantContextForTesting,
  bootstrapTenant,
  discoverTenant,
  getTenantContext,
} from './tenantDiscover'

afterEach(() => {
  _resetTenantContextForTesting()
})

function mockFetch(status: number, body: unknown): typeof fetch {
  return vi.fn(async () => {
    return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as unknown as typeof fetch
}

describe('discoverTenant', () => {
  it('returns discovered on 200', async () => {
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.otech.example',
      tenant_id: 'tenant-acme',
      tenant_kind: 'sme',
      keycloak_realm_url: 'https://kc.otech.example/realms/org-acme',
      keycloak_client_id: 'catalyst-ui',
    })
    const got = await discoverTenant('console.acme.otech.example', fetchImpl)
    expect(got.status).toBe('discovered')
    expect(got.tenant?.tenant_kind).toBe('sme')
    expect(got.tenant?.tenant_id).toBe('tenant-acme')
  })

  it('returns unknown on 404', async () => {
    const fetchImpl = mockFetch(404, { error: 'tenant-not-registered' })
    const got = await discoverTenant('unknown.example', fetchImpl)
    expect(got.status).toBe('unknown')
  })

  it('returns unwired on 503', async () => {
    const fetchImpl = mockFetch(503, { error: 'tenant-registry-unavailable' })
    const got = await discoverTenant('console.example', fetchImpl)
    expect(got.status).toBe('unwired')
  })

  it('returns error on network failure', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error('connection refused')
    }) as unknown as typeof fetch
    const got = await discoverTenant('console.example', fetchImpl)
    expect(got.status).toBe('error')
    expect(got.error).toMatch(/connection refused/)
  })

  it('rejects empty host', async () => {
    const got = await discoverTenant('', mockFetch(200, {}))
    expect(got.status).toBe('error')
  })

  it('rejects an invalid tenant_kind in payload', async () => {
    const fetchImpl = mockFetch(200, {
      tenant_id: 'tenant-x',
      tenant_kind: 'bogus',
      keycloak_realm_url: '',
      keycloak_client_id: '',
    })
    const got = await discoverTenant('console.x.example', fetchImpl)
    expect(got.status).toBe('error')
  })
})

describe('bootstrapTenant', () => {
  it('caches the discovery result', async () => {
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.otech.example',
      tenant_id: 'tenant-acme',
      tenant_kind: 'sme',
      keycloak_realm_url: 'https://kc/realms/org',
      keycloak_client_id: 'ui',
    })
    const first = await bootstrapTenant('console.acme.otech.example', fetchImpl)
    const second = await bootstrapTenant('console.acme.otech.example', fetchImpl)
    expect(first.status).toBe('discovered')
    expect(second).toBe(first)
    // fetch only called once.
    expect((fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1)
  })

  it('exposes the result via getTenantContext()', async () => {
    expect(getTenantContext()).toBeNull()
    const fetchImpl = mockFetch(200, {
      host: 'console.acme.example',
      tenant_id: 'tenant-acme',
      tenant_kind: 'sme',
      keycloak_realm_url: '',
      keycloak_client_id: '',
    })
    await bootstrapTenant('console.acme.example', fetchImpl)
    expect(getTenantContext()?.status).toBe('discovered')
  })
})
