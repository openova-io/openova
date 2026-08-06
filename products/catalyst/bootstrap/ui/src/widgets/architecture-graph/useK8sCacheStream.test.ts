/**
 * useK8sCacheStream.test.ts — unit tests for the architecture-graph
 * SSE consumer.
 *
 * Regression context (qa-loop iter-5):
 *   The catalyst-api `/api/v1/sovereigns/{id}/k8s/stream` SSE encoder
 *   multiplexes TWO event shapes onto the same channel:
 *     1. `{type:"ready", cluster, kinds, at}` — first frame on connect,
 *        added by the immediate-snapshot path (Fix #6 / PR #1189).
 *     2. `{type:"ADDED"|"MODIFIED"|"DELETED", cluster, kind,
 *         object:{metadata,...}, at}` — actual k8s deltas.
 *
 *   A naive consumer that dereferences `payload.object.metadata` blows
 *   up on the first frame ("Cannot read properties of undefined
 *   (reading 'metadata')") and tears down the entire /cloud route.
 *   This test pins the guard against regression — it covers all 12
 *   failing rows in the iter-5 cluster (TC-015/016/017/018/025/026/
 *   027/077/142/168/193/221).
 *
 * jsdom does not implement EventSource, so we install a minimal global
 * shim and drive onmessage manually.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useK8sCacheStream, objectKey } from './useK8sCacheStream'

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

function sendRaw(data: unknown) {
  if (!activeES?.onmessage) throw new Error('no onmessage handler attached')
  activeES.onmessage(new MessageEvent('message', { data: JSON.stringify(data) }))
}

describe('useK8sCacheStream', () => {
  it('survives the {type:"ready"} first-frame and processes a subsequent ADDED', async () => {
    const { result } = renderHook(() => useK8sCacheStream('sovereign-omantel.biz'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      // First server frame: `ready`. No `object`, no `kind`.
      sendRaw({
        type: 'ready',
        cluster: 'sovereign-omantel.biz',
        kinds: ['pod', 'deployment'],
        at: new Date().toISOString(),
      })
    })
    // No throw. Snapshot is still empty; status is open (the hook
    // accepts the server's readiness signal).
    expect(result.current.snapshot.size).toBe(0)
    expect(result.current.status).toBe('open')
    // A subsequent ADDED frame lands cleanly.
    await act(async () => {
      sendRaw({
        cluster: 'sovereign-omantel.biz',
        kind: 'pod',
        type: 'ADDED',
        object: {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: { namespace: 'default', name: 'p1', uid: 'u1' },
        },
        at: new Date().toISOString(),
      })
    })
    expect(result.current.snapshot.size).toBe(1)
    // #5571: the key carries the region as an `@{cluster}` suffix.
    expect(
      result.current.snapshot.get('pod:default/p1@sovereign-omantel.biz'),
    ).toBeDefined()
  })

  it('drops a frame whose object is missing entirely', async () => {
    const { result } = renderHook(() => useK8sCacheStream('alpha'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      // Garbled MODIFIED with no object — must not throw.
      sendRaw({ cluster: 'alpha', kind: 'pod', type: 'MODIFIED', at: 'x' })
    })
    expect(result.current.snapshot.size).toBe(0)
    expect(result.current.status).toBe('open')
  })

  it('drops a frame whose object has no metadata', async () => {
    const { result } = renderHook(() => useK8sCacheStream('alpha'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      sendRaw({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: { apiVersion: 'v1' },
        at: 'x',
      })
    })
    expect(result.current.snapshot.size).toBe(0)
  })

  it('processes ADDED → MODIFIED → DELETED on the same key', async () => {
    const { result } = renderHook(() => useK8sCacheStream('alpha'))
    await act(async () => {
      activeES?.onopen?.(new Event('open'))
      sendRaw({
        cluster: 'alpha',
        kind: 'pod',
        type: 'ADDED',
        object: { metadata: { namespace: 'default', name: 'p1' }, status: { phase: 'Pending' } },
        at: 'x',
      })
    })
    expect(result.current.snapshot.size).toBe(1)
    await act(async () => {
      sendRaw({
        cluster: 'alpha',
        kind: 'pod',
        type: 'MODIFIED',
        object: { metadata: { namespace: 'default', name: 'p1' }, status: { phase: 'Running' } },
        at: 'x',
      })
    })
    const obj = result.current.snapshot.get('pod:default/p1@alpha') as
      | { status?: { phase?: string } }
      | undefined
    expect(obj?.status?.phase).toBe('Running')
    await act(async () => {
      sendRaw({
        cluster: 'alpha',
        kind: 'pod',
        type: 'DELETED',
        object: { metadata: { namespace: 'default', name: 'p1' } },
        at: 'x',
      })
    })
    expect(result.current.snapshot.size).toBe(0)
  })

  it('disabled mode never opens an EventSource', () => {
    const { result } = renderHook(() => useK8sCacheStream('alpha', { enabled: false }))
    expect(activeES).toBeNull()
    expect(result.current.snapshot.size).toBe(0)
  })

  it('objectKey composes `kind:ns/name@cluster` and `kind:name@cluster`', () => {
    expect(
      objectKey('pod', { metadata: { namespace: 'default', name: 'p1' } }, 'r-a'),
    ).toBe('pod:default/p1@r-a')
    expect(objectKey('node', { metadata: { name: 'n1' } }, 'r-a')).toBe(
      'node:n1@r-a',
    )
    // No region context → legacy unsuffixed form (mothership/tests).
    expect(objectKey('node', { metadata: { name: 'n1' } }, '')).toBe('node:n1')
  })
})
