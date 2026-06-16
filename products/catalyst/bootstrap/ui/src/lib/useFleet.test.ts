/**
 * useFleet.test.ts — unit tests for the EPIC-6 Slice U-Fleet (#1101)
 * useFleet / useFleetSovereignSummary / useFleetApplications hooks.
 *
 * Mirrors useCatalog.test.ts: stub `globalThis.fetch`, render the hook
 * inside a fresh QueryClient per test, await `result.current.data`.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as React from 'react'

import {
  useFleet,
  useFleetSovereignSummary,
  useFleetApplications,
} from './useFleet'
import type {
  SovereignsResponse,
  SovereignDetail,
  ApplicationsResponse,
} from './fleet.api'
import { canonicalizeTopologyMode } from './fleet.api'

function makeWrapper() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  return ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client: qc }, children)
}

const SAMPLE_SOVS: SovereignsResponse = {
  sovereigns: [
    { id: 'sov-a', fqdn: 'a.example.com', region: 'fsn1', health: 'green', providerType: 'hetzner' },
    { id: 'sov-b', fqdn: 'b.example.com', region: 'hel1', health: 'yellow', providerType: 'hetzner' },
  ],
  total: 2,
  page: 1,
  pageSize: 25,
}

const SAMPLE_DETAIL: SovereignDetail = {
  sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
  orgs: 3,
  applications: { total: 7, active: 5, failing: 1 },
  regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
  alerts: 0,
  lastActivity: '2026-05-01T10:00:00Z',
}

const SAMPLE_APPS: ApplicationsResponse = {
  applications: [
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'wp', blueprint: 'bp-wordpress', version: '1.0' },
      regions: ['hz-fsn-rtz-prod'],
      topology: 'single-region',
      drPosture: '—',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
    {
      sovereign: { id: 'sov-a', fqdn: 'a.example.com', health: 'green' },
      app: { name: 'api', blueprint: 'bp-django', version: '0.9' },
      regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
      topology: 'active-hotstandby',
      drPosture: 'DR active',
      status: 'Ready',
      org: 'acme',
      namespace: 'acme',
    },
  ],
  total: 2,
}

function jsonResponse(payload: unknown): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

describe('useFleet', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
  })

  it('returns Sovereigns from /api/v1/fleet/sovereigns', async () => {
    const fetchSpy = vi.fn(async (url: RequestInfo | URL) => {
      expect(String(url)).toContain('/api/v1/fleet/sovereigns')
      return jsonResponse(SAMPLE_SOVS)
    })
    globalThis.fetch = fetchSpy as never

    const { result } = renderHook(() => useFleet({ pageSize: 25 }), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.total).toBe(2)
    expect(result.current.data?.sovereigns[0].id).toBe('sov-a')
  })

  it('passes page and pageSize as query params', async () => {
    let lastURL = ''
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      lastURL = String(url)
      return jsonResponse(SAMPLE_SOVS)
    }) as never
    const { result } = renderHook(() => useFleet({ page: 2, pageSize: 10 }), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(lastURL).toContain('page=2')
    expect(lastURL).toContain('pageSize=10')
  })

  it('surfaces errors on non-2xx response', async () => {
    globalThis.fetch = (async () =>
      new Response('boom', { status: 500 })) as never

    const { result } = renderHook(() => useFleet(), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(result.current.error).toBeInstanceOf(Error)
  })
})

describe('useFleetSovereignSummary', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
  })

  it('returns the per-Sov rollup', async () => {
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      expect(String(url)).toContain('/api/v1/fleet/sovereigns/sov-a/summary')
      return jsonResponse(SAMPLE_DETAIL)
    }) as never

    const { result } = renderHook(() => useFleetSovereignSummary('sov-a'), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.applications.total).toBe(7)
    expect(result.current.data?.regions).toContain('hz-fsn-rtz-prod')
  })

  it('disables when id is empty', () => {
    globalThis.fetch = vi.fn() as never
    const { result } = renderHook(() => useFleetSovereignSummary(''), {
      wrapper: makeWrapper(),
    })
    // No call should fire.
    expect(result.current.data).toBeUndefined()
    expect(result.current.fetchStatus).toBe('idle')
  })
})

describe('useFleetApplications', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
  })

  it('returns applications without filters', async () => {
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      const u = String(url)
      expect(u).toContain('/api/v1/fleet/applications')
      // No filter query string.
      expect(u).not.toContain('topology=')
      return jsonResponse(SAMPLE_APPS)
    }) as never
    const { result } = renderHook(() => useFleetApplications(), {
      wrapper: makeWrapper(),
    })
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(result.current.data?.total).toBe(2)
  })

  it('passes filters as query params', async () => {
    let lastURL = ''
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      lastURL = String(url)
      return jsonResponse({ ...SAMPLE_APPS, total: 1, applications: [SAMPLE_APPS.applications[1]] })
    }) as never
    const { result } = renderHook(
      () =>
        useFleetApplications({
          org: 'acme',
          // #3375 §3(f) — a legacy-dialect input is canonicalised on the
          // wire so it matches the backend's canonical vocabulary instead
          // of silently filtering nothing.
          topology: 'active-hotstandby',
          drPosture: 'DR active',
        }),
      { wrapper: makeWrapper() },
    )
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(lastURL).toContain('org=acme')
    // The legacy `active-hotstandby` dialect is posted as the canonical
    // `active-hot-standby` (URL-encoded), never raw.
    expect(lastURL).toContain('topology=active-hot-standby')
    expect(lastURL).not.toContain('topology=active-hotstandby')
    // 'DR active' contains a space — encoded as DR+active or DR%20active.
    expect(/(DR\+active|DR%20active)/.test(lastURL)).toBe(true)
    expect(result.current.data?.total).toBe(1)
  })

  // #3375 §3(f) — an already-canonical topology filter passes through
  // unchanged (no double-mangling).
  it('passes a canonical topology filter through unchanged', async () => {
    let lastURL = ''
    globalThis.fetch = (async (url: RequestInfo | URL) => {
      lastURL = String(url)
      return jsonResponse({ ...SAMPLE_APPS, total: 1, applications: [SAMPLE_APPS.applications[1]] })
    }) as never
    const { result } = renderHook(
      () => useFleetApplications({ topology: 'active-hot-standby' }),
      { wrapper: makeWrapper() },
    )
    await waitFor(() => expect(result.current.data).toBeDefined())
    expect(lastURL).toContain('topology=active-hot-standby')
  })
})

// #3375 §3(f) — the one-vocabulary canonicaliser. BOTH the legacy editor
// dialect and the canonical vocabulary map onto the single canonical
// token the backend validates.
describe('canonicalizeTopologyMode', () => {
  it('maps the legacy editor dialect to canonical', () => {
    expect(canonicalizeTopologyMode('single-region')).toBe('singleton')
    expect(canonicalizeTopologyMode('active-hotstandby')).toBe('active-hot-standby')
  })
  it('passes already-canonical values through unchanged', () => {
    expect(canonicalizeTopologyMode('singleton')).toBe('singleton')
    expect(canonicalizeTopologyMode('active-hot-standby')).toBe('active-hot-standby')
    expect(canonicalizeTopologyMode('active-active')).toBe('active-active')
    expect(canonicalizeTopologyMode('active-passive')).toBe('active-passive')
  })
  it('is case- and whitespace-insensitive', () => {
    expect(canonicalizeTopologyMode('  Active-HotStandby ')).toBe('active-hot-standby')
  })
})
