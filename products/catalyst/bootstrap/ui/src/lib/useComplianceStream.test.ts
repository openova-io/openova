/**
 * useComplianceStream.test.ts — unit tests for the compliance SSE hook
 * (slice U, #1096).
 *
 * Same fake-EventSource pattern as `useK8sStream.test.ts` — jsdom
 * doesn't implement EventSource, so we install a minimal global shim
 * that records the constructed instance so tests can drive onmessage /
 * onerror events directly.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useComplianceStream, type ComplianceEvent } from './useComplianceStream'

interface FakeES {
  url: string
  onopen: ((e: Event) => void) | null
  onmessage: ((e: MessageEvent) => void) | null
  onerror: ((e: Event) => void) | null
  close: () => void
}

let activeES: FakeES | null = null

class FakeEventSource implements FakeES {
  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  closed = false
  constructor(url: string) {
    this.url = url
    // eslint-disable-next-line @typescript-eslint/no-this-alias
    activeES = this
  }
  close = () => {
    this.closed = true
  }
}

beforeEach(() => {
  activeES = null
  // @ts-expect-error — install fake on the global
  globalThis.EventSource = FakeEventSource
})

afterEach(() => {
  activeES = null
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

function send(ev: ComplianceEvent) {
  if (!activeES?.onmessage) throw new Error('no onmessage handler attached')
  activeES.onmessage(new MessageEvent('message', { data: JSON.stringify(ev) }))
}

const NOW = '2026-05-09T00:00:00Z'

describe('useComplianceStream', () => {
  it('returns isLoading=true before SSE opens', () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    expect(result.current.isLoading).toBe(true)
    expect(result.current.scores).toEqual([])
  })

  it('flips isLoading=false on open', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
    })
    expect(result.current.isLoading).toBe(false)
  })

  it('adds an Application-scope score on event', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: {
          scope: 'application',
          id: 'billing',
          applicationRef: 'billing',
          total: 87,
          numerator: 87,
          denominator: 100,
          updatedAt: NOW,
        },
      })
    })
    expect(result.current.scores.length).toBe(1)
    const got = result.current.getScore('application', 'billing')
    expect(got?.total).toBe(87)
    expect(result.current.byScope.application.length).toBe(1)
  })

  it('updates an existing score keyed by scope:id', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: {
          scope: 'application',
          id: 'billing',
          total: 50,
          numerator: 50,
          denominator: 100,
          updatedAt: NOW,
        },
      })
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: {
          scope: 'application',
          id: 'billing',
          total: 90,
          numerator: 90,
          denominator: 100,
          updatedAt: NOW,
        },
      })
    })
    expect(result.current.scores.length).toBe(1)
    expect(result.current.getScore('application', 'billing')?.total).toBe(90)
  })

  it('groups by scope correctly across application + organization', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: { scope: 'application', id: 'billing', total: 80, numerator: 80, denominator: 100, updatedAt: NOW },
      })
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: { scope: 'organization', id: 'acme', total: 75, numerator: 75, denominator: 100, updatedAt: NOW },
      })
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: { scope: 'sovereign', id: 'alpha', total: 70, numerator: 70, denominator: 100, updatedAt: NOW },
      })
    })
    expect(result.current.byScope.application.length).toBe(1)
    expect(result.current.byScope.organization.length).toBe(1)
    expect(result.current.byScope.sovereign.length).toBe(1)
    expect(result.current.byScope.environment.length).toBe(0)
    expect(result.current.byScope.resource.length).toBe(0)
  })

  it('handles null total — encoded as JSON null', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: {
          scope: 'application',
          id: 'unknown',
          total: null,
          numerator: 0,
          denominator: 0,
          updatedAt: NOW,
        },
      })
    })
    const got = result.current.getScore('application', 'unknown')
    expect(got?.total).toBeNull()
  })

  it('drops malformed events without crashing', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      activeES?.onmessage?.(
        new MessageEvent('message', { data: 'not-json' }),
      )
      // Then a valid one
      send({
        type: 'score',
        cluster: 'alpha',
        at: NOW,
        score: { scope: 'application', id: 'billing', total: 50, numerator: 50, denominator: 100, updatedAt: NOW },
      })
    })
    expect(result.current.scores.length).toBe(1)
  })

  it('flips isError=true on EventSource error', async () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha' }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      activeES?.onerror?.(new Event('error'))
    })
    expect(result.current.isError).toBe(true)
  })

  it('disableStream does not open EventSource', () => {
    const { result } = renderHook(() =>
      useComplianceStream({ sovereignId: 'alpha', disableStream: true }),
    )
    expect(activeES).toBeNull()
    expect(result.current.isLoading).toBe(false)
  })

  it('sovereignId="" does not open EventSource', () => {
    const { result } = renderHook(() => useComplianceStream({ sovereignId: '' }))
    expect(activeES).toBeNull()
    expect(result.current.isLoading).toBe(false)
  })

  it('streamURL includes /compliance/stream path', async () => {
    renderHook(() => useComplianceStream({ sovereignId: 'alpha' }))
    expect(activeES?.url).toContain('/compliance/stream')
    expect(activeES?.url).toContain('alpha')
  })
})
