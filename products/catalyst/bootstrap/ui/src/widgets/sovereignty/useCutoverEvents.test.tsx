/**
 * useCutoverEvents.test.tsx — coverage for the SSE consumer hook driving
 * the sovereignty cutover surface.
 *
 * What we assert:
 *   1. On mount, GET /api/v1/sovereign/cutover/status seeds the status.
 *   2. A 404 response surfaces `streamStatus='unreachable'` + an
 *      explanatory `streamError` (the catalyst-api endpoint isn't
 *      live yet on this Sovereign).
 *   3. A malformed wire shape rejects via `streamStatus='failed'`
 *      rather than silently fabricating success.
 *   4. POST /sovereign/cutover/start fires the right URL + method.
 *
 * The SSE side of the hook is exercised end-to-end by the Playwright
 * spec (e2e/sovereignty.spec.ts) where a real EventSource can be
 * intercepted; jsdom's EventSource is a no-op shim.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useCutoverEvents } from './useCutoverEvents'

// Hold the original fetch so we can restore it between tests.
const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

beforeEach(() => {
  // jsdom doesn't ship EventSource — provide a tiny shim so the hook's
  // SSE branch can be exercised without throwing. Disabled per-test
  // via opts.disableStream when not needed.
  if (typeof (globalThis as unknown as { EventSource?: unknown }).EventSource === 'undefined') {
    class StubEventSource {
      url: string
      readyState = 0
      onopen: ((this: EventSource, ev: Event) => unknown) | null = null
      onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null
      onerror: ((this: EventSource, ev: Event) => unknown) | null = null
      static readonly CLOSED = 2
      constructor(url: string) {
        this.url = url
      }
      addEventListener(): void {}
      removeEventListener(): void {}
      close(): void {}
    }
    ;(globalThis as unknown as { EventSource: typeof EventSource }).EventSource =
      StubEventSource as unknown as typeof EventSource
  }
})

function mockFetchOnce(handler: (url: string, init?: RequestInit) => Response) {
  const fn = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    const u = typeof url === 'string' ? url : (url as URL).toString()
    return handler(u, init)
  })
  globalThis.fetch = fn as unknown as typeof fetch
  return fn
}

describe('useCutoverEvents — replay GET /status on mount', () => {
  it('seeds status from a successful 200 with the parsed snapshot', async () => {
    mockFetchOnce(() =>
      new Response(
        JSON.stringify({
          state: 'tethered',
          steps: [
            { step: 'gitea-mirror', status: 'running' },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    const { result } = renderHook(() =>
      useCutoverEvents({ disableStream: true }),
    )
    await waitFor(() => {
      expect(
        result.current.status.steps.find((s) => s.step === 'gitea-mirror')
          ?.status,
      ).toBe('running')
    })
  })

  it('flips to streamStatus="completed" when the snapshot is already terminal', async () => {
    mockFetchOnce(() =>
      new Response(
        JSON.stringify({
          state: 'sovereign',
          cutoverComplete: true,
          steps: [],
          mirroredCommitSHA: 'abc123abc1234567',
          harborProjectCount: 7,
          egressTestPassed: true,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    const { result } = renderHook(() =>
      useCutoverEvents({ disableStream: true }),
    )
    await waitFor(() => {
      expect(result.current.streamStatus).toBe('completed')
      expect(result.current.status.state).toBe('sovereign')
      expect(result.current.status.mirroredCommitSHA).toBe('abc123abc1234567')
    })
  })
})

describe('useCutoverEvents — error paths', () => {
  it('surfaces streamStatus="unreachable" on 404 with helpful copy', async () => {
    mockFetchOnce(
      () =>
        new Response('not deployed', {
          status: 404,
          headers: { 'Content-Type': 'text/plain' },
        }),
    )
    const { result } = renderHook(() =>
      useCutoverEvents({ disableStream: true }),
    )
    await waitFor(() => {
      expect(result.current.streamStatus).toBe('unreachable')
      expect(result.current.streamError).toMatch(/not yet available/)
    })
  })

  it('surfaces streamStatus="failed" on a malformed payload', async () => {
    mockFetchOnce(
      () =>
        new Response(
          JSON.stringify({ state: 'pending', steps: [] }), // unknown state
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
    )
    const { result } = renderHook(() =>
      useCutoverEvents({ disableStream: true }),
    )
    await waitFor(() => {
      expect(result.current.streamStatus).toBe('failed')
      expect(result.current.streamError).toMatch(/CutoverState/)
    })
  })
})

describe('useCutoverEvents — POST /start', () => {
  it('fires the right URL with POST + JSON content-type', async () => {
    const calls: { url: string; init?: RequestInit }[] = []
    globalThis.fetch = vi.fn(
      async (url: string | URL | Request, init?: RequestInit) => {
        const u = typeof url === 'string' ? url : (url as URL).toString()
        calls.push({ url: u, init })
        // First call: GET /status seed (returns 404 → unreachable, fine).
        if (u.endsWith('/status'))
          return new Response('', { status: 404 }) as unknown as Response
        // Second call: POST /start.
        return new Response(
          JSON.stringify({
            state: 'tethered',
            steps: [{ step: 'gitea-mirror', status: 'running' }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ) as unknown as Response
      },
    ) as unknown as typeof fetch
    const { result } = renderHook(() =>
      useCutoverEvents({ disableStream: true }),
    )
    await waitFor(() => {
      // Replay GET fired first.
      expect(calls.find((c) => c.url.endsWith('/status'))).toBeTruthy()
    })
    await act(async () => {
      await result.current.startCutover()
    })
    const startCall = calls.find((c) => c.url.endsWith('/start'))
    expect(startCall).toBeTruthy()
    expect(startCall!.init?.method).toBe('POST')
    const headers = startCall!.init?.headers as Record<string, string> | undefined
    expect(headers?.['Content-Type']).toMatch(/json/i)
  })
})
