/**
 * useCatalog.test.ts — unit tests for the EPIC-2 Slice I (#1097)
 * useCatalog hook + REST client.
 *
 * Three priority cases covered (per slice L's PRIVATE > SOVEREIGN >
 * PUBLIC contract): we don't dedupe in the hook (the server does), but
 * we DO assert the wire shape carries `origin` + `source` correctly so
 * a UI rendering can show the source badge.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, waitFor, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as React from 'react'

import { useCatalog, useCatalogItemVersion } from './useCatalog'
import type { CatalogItem } from './catalog.api'

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

const PUBLIC_BP: CatalogItem = {
  name: 'bp-wordpress',
  version: '1.2.3',
  card: { title: 'WordPress', summary: 'PHP CMS' },
  origin: 1,
  source: 'public',
}
const SOVEREIGN_BP: CatalogItem = {
  name: 'bp-cortex',
  version: '0.5.0',
  card: { title: 'Cortex' },
  origin: 2,
  source: 'sovereign',
}
const PRIVATE_BP: CatalogItem = {
  name: 'bp-acme-private',
  version: '0.1.0',
  card: { title: 'ACME Private App' },
  origin: 3,
  source: 'org-private',
  org: 'acme',
}

const RAW_WITH_SCHEMA: CatalogItem = {
  ...PUBLIC_BP,
  raw: {
    spec: {
      version: '1.2.3',
      configSchema: {
        type: 'object',
        required: ['domain'],
        properties: {
          domain: { type: 'string' },
          replicas: { type: 'integer', minimum: 1, maximum: 5 },
        },
      },
    },
  },
}

describe('useCatalog', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
  })

  it('returns items from /api/v1/catalog', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ items: [PUBLIC_BP, SOVEREIGN_BP, PRIVATE_BP] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ) as typeof fetch

    const { result } = renderHook(() => useCatalog(), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toHaveLength(3)
    expect(result.current.data?.[0].source).toBe('public')
    expect(result.current.data?.[1].source).toBe('sovereign')
    expect(result.current.data?.[2].source).toBe('org-private')
  })

  it('passes the org query param when provided', async () => {
    let capturedURL = ''
    globalThis.fetch = vi.fn().mockImplementation(async (url: string) => {
      capturedURL = String(url)
      return new Response(JSON.stringify({ items: [PRIVATE_BP] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as typeof fetch

    const { result } = renderHook(() => useCatalog({ org: 'acme' }), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(capturedURL).toContain('org=acme')
  })

  it('surfaces upstream errors via the query state', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      new Response('upstream down', { status: 502 }),
    ) as typeof fetch

    const { result } = renderHook(() => useCatalog(), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect((result.current.error as Error).message).toContain('502')
  })
})

describe('useCatalogItemVersion', () => {
  let originalFetch: typeof globalThis.fetch
  beforeEach(() => {
    originalFetch = globalThis.fetch
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
  })

  it('fetches the version-pinned blueprint with raw configSchema', async () => {
    let capturedURL = ''
    globalThis.fetch = vi.fn().mockImplementation(async (url: string) => {
      capturedURL = String(url)
      return new Response(JSON.stringify(RAW_WITH_SCHEMA), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as typeof fetch

    const { result } = renderHook(
      () => useCatalogItemVersion('bp-wordpress', '1.2.3'),
      { wrapper: makeWrapper() },
    )
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(capturedURL).toContain('/catalog/bp-wordpress/versions/1.2.3')
    expect(result.current.data?.raw).toBeDefined()
    const spec = (result.current.data?.raw as Record<string, unknown>).spec as Record<string, unknown>
    expect(spec.configSchema).toBeDefined()
  })

  it('is disabled until name + version are non-empty', () => {
    globalThis.fetch = vi.fn() as typeof fetch
    const { result } = renderHook(
      () => useCatalogItemVersion('', ''),
      { wrapper: makeWrapper() },
    )
    expect(result.current.isFetching).toBe(false)
    expect(globalThis.fetch).not.toHaveBeenCalled()
  })
})
