/**
 * useK8sStream.test.ts — unit tests for the K8s data-plane hook.
 *
 * jsdom does not implement EventSource. We install a minimal global
 * shim that records the constructed instance so tests can drive
 * onmessage / onerror events directly.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useK8sStream, getMeta, type K8sEvent } from './useK8sStream'

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

function send(ev: K8sEvent) {
  if (!activeES?.onmessage) throw new Error('no onmessage handler attached')
  activeES.onmessage(new MessageEvent('message', { data: JSON.stringify(ev) }))
}

describe('useK8sStream', () => {
  it('returns isLoading=true before SSE opens', () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    expect(result.current.isLoading).toBe(true)
    expect(result.current.items).toEqual([])
  })

  it('flips isLoading=false on open', async () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
    })
    expect(result.current.isLoading).toBe(false)
  })

  it('adds an item on ADDED event', async () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: { namespace: 'default', name: 'x', uid: 'u1' },
        },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.items).toHaveLength(1)
    const meta = getMeta(result.current.items[0])
    expect(meta.name).toBe('x')
    expect(result.current.byKind['pod']).toHaveLength(1)
  })

  it('removes an item on DELETED event', async () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: {
          metadata: { namespace: 'default', name: 'x' },
        },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.items).toHaveLength(1)
    await act(async () => {
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'DELETED',
        object: { metadata: { namespace: 'default', name: 'x' } },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.items).toHaveLength(0)
  })

  it('updates an item on MODIFIED event (same key)', async () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: { metadata: { namespace: 'default', name: 'x' }, spec: { phase: 'Pending' } },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.items).toHaveLength(1)
    await act(async () => {
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'MODIFIED',
        object: { metadata: { namespace: 'default', name: 'x' }, spec: { phase: 'Running' } },
        at: new Date().toISOString(),
      })
    })
    // Same key — count stays 1, but the inner object updated.
    expect(result.current.items).toHaveLength(1)
    const obj = result.current.items[0] as { spec: { phase: string } }
    expect(obj.spec.phase).toBe('Running')
  })

  it('does not crash on malformed event payload', async () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'] }),
    )
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      activeES?.onmessage?.(new MessageEvent('message', { data: 'not-json' }))
    })
    expect(result.current.items).toHaveLength(0)
    // Hook still functional — a follow-up valid event lands.
    await act(async () => {
      send({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: { metadata: { namespace: 'default', name: 'x' } },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.items).toHaveLength(1)
  })

  it('disableStream skips connection and reports !isLoading', () => {
    const { result } = renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod'], disableStream: true }),
    )
    expect(activeES).toBeNull()
    expect(result.current.isLoading).toBe(false)
  })

  it('builds the stream URL with kinds query parameter', () => {
    renderHook(() =>
      useK8sStream({ sovereignId: 'alpha', kinds: ['pod', 'deployment'] }),
    )
    expect(activeES?.url).toContain('/v1/sovereigns/alpha/k8s/stream')
    expect(activeES?.url).toContain('kinds=pod%2Cdeployment')
    expect(activeES?.url).toContain('initialState=1')
  })

  it('omits kinds query when watching all', () => {
    renderHook(() => useK8sStream({ sovereignId: 'alpha', kinds: [] }))
    expect(activeES?.url).not.toContain('kinds=')
  })
})
